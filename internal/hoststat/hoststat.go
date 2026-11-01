// SPDX-License-Identifier: Elastic-2.0

// Package hoststat measures the conditions a run executed under: the host's
// load before, during and after, the memory it had free at the start, and
// what the run and its children consumed. REA's runs at load 38 and load 13
// looked identical on the record although the load decided which test units
// the evidence bound cut; this is the measurement that tells them apart.
//
// Every figure is a pointer and nil means NOT MEASURED. A host that cannot
// say (no /proc, a platform with no reader here) yields nil fields and a
// NotMeasured sentence naming why: a 0 for "could not read the load" would be
// a load of zero, which is a measurement.
package hoststat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"sync"
	"time"
)

// procRoot is where the Linux readers look. It is a constant: only tests
// reach startAt with another root.
const procRoot = "/proc"

// Conditions is what a Sampler observed between Start and Stop.
type Conditions struct {
	// LoadStart, LoadEnd and LoadPeak are the 1-minute load average at Start,
	// at Stop and the largest of every sample taken between (the two ends
	// included), so the peak is never below either end.
	LoadStart, LoadEnd, LoadPeak *float64
	// MemAvailableStartBytes is /proc/meminfo's MemAvailable at Start.
	MemAvailableStartBytes *int64
	// PeakRSSBytes is the largest resident set of any child of this process
	// that has been waited for (getrusage RUSAGE_CHILDREN ru_maxrss). It is a
	// process-lifetime maximum, not a delta: the kernel offers no other.
	PeakRSSBytes *int64
	// CPUMillis is user plus system CPU time of this process and its waited
	// children consumed between Start and Stop.
	CPUMillis *int64
	// WallMillis is Stop minus Start. It needs no platform support.
	WallMillis int64
	// NotMeasured says, in a sentence, what could not be read and why; ""
	// when everything was.
	NotMeasured string
}

// Sampler samples the 1-minute load average on an interval from Start until
// Stop or until its context is cancelled.
type Sampler struct {
	root  string
	begin time.Time
	cpu0  int64
	cpuOK bool

	mu    sync.Mutex
	start Conditions
	peak  *float64
	notes []string

	quit chan struct{}
	done chan struct{}
	once sync.Once
	out  Conditions
}

// Start begins sampling every interval and returns at once. The goroutine it
// starts ends on Stop; use StartContext to also end it with a context.
func Start(interval time.Duration) *Sampler {
	return StartContext(context.Background(), interval)
}

// StartContext is Start whose sampling goroutine also ends when ctx is done.
// Stop is still needed for the figures.
func StartContext(ctx context.Context, interval time.Duration) *Sampler {
	return startAt(ctx, procRoot, interval)
}

func startAt(ctx context.Context, root string, interval time.Duration) *Sampler {
	s := &Sampler{root: root, begin: time.Now(), quit: make(chan struct{}), done: make(chan struct{})}
	if !supported {
		s.notes = append(s.notes, "not measured on "+runtime.GOOS)
		close(s.done)
		return s
	}
	if v, err := readLoad(root); err != nil {
		s.note(err)
	} else {
		s.start.LoadStart = &v
		p := v
		s.peak = &p
	}
	if v, err := readMemAvailable(root); err != nil {
		s.note(err)
	} else {
		s.start.MemAvailableStartBytes = &v
	}
	if cpu, _, err := usage(); err != nil {
		s.note(err)
	} else {
		s.cpu0, s.cpuOK = cpu, true
	}
	go s.loop(ctx, interval)
	return s
}

func (s *Sampler) note(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteLocked(err)
}

// noteLocked records a reason once: a read that fails on every tick must not
// grow the sentence by one copy per tick.
func (s *Sampler) noteLocked(err error) {
	m := err.Error()
	for _, n := range s.notes {
		if n == m {
			return
		}
	}
	s.notes = append(s.notes, m)
}

func (s *Sampler) loop(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	if interval <= 0 {
		<-firstOf(ctx, s.quit)
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sample()
		case <-s.quit:
			return
		case <-ctx.Done():
			return
		}
	}
}

// firstOf closes its result when ctx is done or quit is closed.
func firstOf(ctx context.Context, quit <-chan struct{}) <-chan struct{} {
	c := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-quit:
		}
		close(c)
	}()
	return c
}

// sample reads the load once and raises the peak.
func (s *Sampler) sample() {
	v, err := readLoad(s.root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.noteLocked(err)
		return
	}
	if s.peak == nil || v > *s.peak {
		p := v
		s.peak = &p
	}
}

// Stop ends the sampler, takes the closing readings and returns the
// conditions. It is safe to call more than once: later calls return the first
// answer.
func (s *Sampler) Stop() Conditions {
	s.once.Do(func() {
		close(s.quit)
		<-s.done
		s.out = s.finish()
	})
	return s.out
}

func (s *Sampler) finish() Conditions {
	out := Conditions{WallMillis: time.Since(s.begin).Milliseconds()}
	if !supported {
		out.NotMeasured = strings.Join(s.notes, "; ")
		return out
	}
	out.LoadStart = s.start.LoadStart
	out.MemAvailableStartBytes = s.start.MemAvailableStartBytes
	if v, err := readLoad(s.root); err != nil {
		s.note(err)
	} else {
		out.LoadEnd = &v
		s.mu.Lock()
		if s.peak == nil || v > *s.peak {
			p := v
			s.peak = &p
		}
		s.mu.Unlock()
	}
	if cpu, rss, err := usage(); err != nil {
		s.note(err)
	} else {
		if s.cpuOK {
			d := cpu - s.cpu0
			out.CPUMillis = &d
		}
		out.PeakRSSBytes = &rss
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if out.LoadStart != nil || out.LoadEnd != nil {
		out.LoadPeak = s.peak
	}
	out.NotMeasured = strings.Join(s.notes, "; ")
	return out
}

// unreadable is the error every reader returns, naming the path it tried.
func unreadable(path string, err error) error {
	// An *fs.PathError already prints the path; say it once.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s unreadable: %v", path, err)
}
