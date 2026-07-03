// SPDX-License-Identifier: Elastic-2.0

// Package admission is host-side spawn governance: before the reference launcher
// starts a child agent process, it must Acquire a lease. The Local impl caps both
// concurrency (a semaphore) and host load (a 1-min loadavg gate), refusing rather
// than blocking so a coordinator can never conscript a host beyond its capacity.
// Controller is an interface (like sandbox.Isolator) so cgroups/container impls can
// be dropped in without touching the launcher.
package admission

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Lease is a held admission slot; Release returns it. Release is idempotent (safe to call multiple times).
type Lease interface{ Release() }

// Controller grants (or refuses) permission to launch a child on this host.
type Controller interface {
	// Acquire is NON-BLOCKING: it returns a lease, or an error explaining why the
	// host won't run another child right now (at capacity, or load too high).
	Acquire(role string) (Lease, error)
	Name() string
}

// Local is the reference Controller: a concurrency semaphore + a host-load gate.
type Local struct {
	mu         sync.Mutex
	inUse      int
	max        int
	loadFactor float64
	load       func() float64
}

// NewLocal builds a Local. maxConcurrent<=0 falls back to 2*NumCPU; loadFactor<=0
// falls back to 2.0. load returns the current 1-minute load average.
func NewLocal(maxConcurrent int, loadFactor float64, load func() float64) *Local {
	if maxConcurrent <= 0 {
		maxConcurrent = 2 * runtime.NumCPU()
	}
	if loadFactor <= 0 {
		loadFactor = 2.0
	}
	if load == nil {
		load = readLoadAvg
	}
	return &Local{max: maxConcurrent, loadFactor: loadFactor, load: load}
}

// FromEnv builds a Local from CORRAL_MAX_CONCURRENT_CHILDREN and CORRAL_LOAD_FACTOR.
func FromEnv() *Local {
	max := 0
	if v, err := strconv.Atoi(os.Getenv("CORRAL_MAX_CONCURRENT_CHILDREN")); err == nil {
		max = v
	}
	lf := 0.0
	if v, err := strconv.ParseFloat(os.Getenv("CORRAL_LOAD_FACTOR"), 64); err == nil {
		lf = v
	}
	return NewLocal(max, lf, nil)
}

func (l *Local) Name() string { return "local" }

type localLease struct {
	l    *Local
	once sync.Once
}

func (ll *localLease) Release() {
	ll.once.Do(func() {
		ll.l.mu.Lock()
		if ll.l.inUse > 0 {
			ll.l.inUse--
		}
		ll.l.mu.Unlock()
	})
}

// Acquire grants a slot unless the host is at the concurrency cap or its load
// exceeds loadFactor*NumCPU.
func (l *Local) Acquire(role string) (Lease, error) {
	if cur := l.load(); cur > l.loadFactor*float64(runtime.NumCPU()) {
		return nil, fmt.Errorf("admission refused: host load %.2f exceeds %.2f", cur, l.loadFactor*float64(runtime.NumCPU()))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse >= l.max {
		return nil, fmt.Errorf("admission refused: host at capacity (%d concurrent children)", l.max)
	}
	l.inUse++
	return &localLease{l: l}, nil
}

// readLoadAvg returns the 1-minute load average from /proc/loadavg, or 0 if it
// can't be read (non-Linux) — the load gate is then a no-op and the semaphore alone
// governs. Pure Go: no cgo, no build tags.
func readLoadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return v
}
