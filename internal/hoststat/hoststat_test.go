// SPDX-License-Identifier: Elastic-2.0

package hoststat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHostConditionsAreMeasuredOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the readers are Linux's; hoststat_other.go is tested below")
	}
	s := Start(10 * time.Millisecond)
	spinCPU(t, 50*time.Millisecond)
	if err := exec.Command("true").Run(); err != nil {
		t.Fatal(err)
	}
	c := s.Stop()
	if c.LoadStart == nil || c.LoadEnd == nil || c.LoadPeak == nil || c.CPUMillis == nil || c.MemAvailableStartBytes == nil || c.PeakRSSBytes == nil {
		t.Fatalf("a field is nil on Linux: %+v (NotMeasured %q)", c, c.NotMeasured)
	}
	if *c.LoadPeak < *c.LoadStart || *c.LoadPeak < *c.LoadEnd {
		t.Errorf("peak %v below start %v or end %v", *c.LoadPeak, *c.LoadStart, *c.LoadEnd)
	}
	if *c.CPUMillis < 40 {
		t.Errorf("CPUMillis = %d after spending 50ms of CPU", *c.CPUMillis)
	}
	if c.WallMillis < 50 {
		t.Errorf("WallMillis = %d", c.WallMillis)
	}
	if c.NotMeasured != "" {
		t.Errorf("NotMeasured = %q on a host that measured everything", c.NotMeasured)
	}
}

// Review Focus 2: a host with no /proc records nothing for load or memory and
// says why; it never records zeros.
func TestHostConditionsWithoutProcAreNotMeasured(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the /proc readers exist only on Linux")
	}
	missing := filepath.Join(t.TempDir(), "no-proc")
	s := startAt(context.Background(), missing, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	c := s.Stop()
	if c.LoadStart != nil || c.LoadEnd != nil || c.LoadPeak != nil || c.MemAvailableStartBytes != nil {
		t.Errorf("load or memory recorded with no /proc: %+v", c)
	}
	if !strings.Contains(c.NotMeasured, filepath.Join(missing, "loadavg")) || !strings.Contains(c.NotMeasured, filepath.Join(missing, "meminfo")) {
		t.Errorf("NotMeasured = %q, want both paths named", c.NotMeasured)
	}
	if strings.Count(c.NotMeasured, "loadavg") != 1 {
		t.Errorf("NotMeasured = %q repeats a reason once per tick", c.NotMeasured)
	}
}

func TestProcFilesAreParsed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux readers")
	}
	root := t.TempDir()
	write := func(n, s string) {
		if err := os.WriteFile(filepath.Join(root, n), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("loadavg", "38.25 20.10 9.00 3/900 1234\n")
	write("meminfo", "MemTotal: 100 kB\nMemAvailable:    2048 kB\n")
	s := startAt(context.Background(), root, time.Hour)
	c := s.Stop()
	if c.LoadStart == nil || *c.LoadStart != 38.25 || *c.MemAvailableStartBytes != 2048*1024 {
		t.Errorf("parsed = %+v", c)
	}
}

func TestSamplerGoroutineEndsOnStopAndOnCancel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("no sampler goroutine off Linux")
	}
	s := Start(time.Millisecond)
	s.Stop()
	select {
	case <-s.done:
	default:
		t.Fatal("Stop returned with the sampler goroutine still running")
	}
	if again := s.Stop(); again.WallMillis != s.out.WallMillis {
		t.Error("a second Stop must return the first answer")
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := StartContext(ctx, time.Millisecond)
	cancel()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the context did not end the sampler goroutine")
	}
	if got := c.Stop(); got.LoadEnd == nil {
		t.Errorf("Stop after cancel still takes the closing reading: %+v", got)
	}
}

func TestNoGoroutinesLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		Start(time.Millisecond).Stop()
		ctx, cancel := context.WithCancel(context.Background())
		s := StartContext(ctx, 0)
		cancel()
		s.Stop()
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d -> %d: a sampler leaked", before, after)
	}
}

func TestOtherPlatformsSayNotMeasured(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("covered by the Linux tests")
	}
	c := Start(time.Millisecond).Stop()
	if c.LoadStart != nil || c.CPUMillis != nil || c.PeakRSSBytes != nil || c.NotMeasured != "not measured on "+runtime.GOOS {
		t.Errorf("%+v", c)
	}
}
