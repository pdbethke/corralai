// SPDX-License-Identifier: Elastic-2.0

package hoststat

import (
	"syscall"
	"testing"
	"time"
)

// spinCPU burns at least want of this process's CPU time, measured by
// getrusage, not by the wall clock. A 50ms wall-clock spin measured 18ms and
// 6ms of CPU on loaded CI runners (2026-11-01), because a descheduled thread
// spends wall time without spending CPU; the assertion below is about the
// sampler, so the spin must deliver the CPU it claims. Bounded so a host that
// never schedules the test fails it rather than hanging.
func spinCPU(t *testing.T, want time.Duration) {
	t.Helper()
	cpu := func() time.Duration {
		var ru syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
			t.Fatal(err)
		}
		return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}
	start, deadline := cpu(), time.Now().Add(30*time.Second)
	for cpu()-start < want {
		if time.Now().After(deadline) {
			t.Fatalf("spent %v of CPU in 30s of wall time, wanted %v", cpu()-start, want)
		}
	}
}
