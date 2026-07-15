// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/fleet"
)

// TestRetentionConfigDisabled verifies that CORRALAI_FLEET_RETENTION_DISABLE=1
// causes RetentionConfigFromEnv to return Disabled=true, and that Compact is a
// true no-op in that state — no remote attach attempt, no error.
func TestRetentionConfigDisabled(t *testing.T) {
	t.Setenv("CORRALAI_FLEET_RETENTION_DISABLE", "1")
	cfg := fleet.RetentionConfigFromEnv()
	if !cfg.Disabled {
		t.Fatal("expected Disabled=true when CORRALAI_FLEET_RETENTION_DISABLE=1")
	}
	// Compact must be a true no-op: passes an unreachable path — if the guard
	// fires it would fail to open the file, but Disabled short-circuits first.
	del, err := fleet.Compact(cfg, "/nonexistent/remote.duckdb", "test-brain", time.Now())
	if err != nil {
		t.Fatalf("Compact with Disabled=true: unexpected error: %v", err)
	}
	if del != nil {
		t.Fatalf("Compact with Disabled=true: expected nil map, got %v", del)
	}
}

// TestRetentionConfigDefaults verifies that RetentionConfigFromEnv returns the
// documented defaults (Disabled=false, TTLDays=90) when no env vars are set.
func TestRetentionConfigDefaults(t *testing.T) {
	t.Setenv("CORRALAI_FLEET_RETENTION_DISABLE", "") // t.Setenv auto-restores on cleanup (parallel-safe)
	t.Setenv("CORRALAI_FLEET_RETENTION_DAYS", "")
	cfg := fleet.RetentionConfigFromEnv()
	if cfg.Disabled {
		t.Fatal("expected Disabled=false by default")
	}
	if cfg.TTLDays != 90 {
		t.Fatalf("expected TTLDays=90 by default, got %d", cfg.TTLDays)
	}
}

// TestRetentionConfigTTLOverride verifies that CORRALAI_FLEET_RETENTION_DAYS
// overrides the default TTL.
func TestRetentionConfigTTLOverride(t *testing.T) {
	t.Setenv("CORRALAI_FLEET_RETENTION_DISABLE", "")
	t.Setenv("CORRALAI_FLEET_RETENTION_DAYS", "30")
	cfg := fleet.RetentionConfigFromEnv()
	if cfg.Disabled {
		t.Fatal("expected Disabled=false")
	}
	if cfg.TTLDays != 30 {
		t.Fatalf("expected TTLDays=30, got %d", cfg.TTLDays)
	}
}

// TestRetentionCadenceCalc verifies the retentionEvery calculation: given a
// retention interval and sync interval, every = max(1, retentionSec/syncSec).
// This is an inline replication of the logic in startFleetSync so we can verify
// boundary cases without running a full goroutine.
func TestRetentionCadenceCalc(t *testing.T) {
	cases := []struct {
		retSec  int
		syncSec int
		want    int
	}{
		{3600, 30, 120}, // 1 hour / 30s = 120 ticks
		{3600, 60, 60},  // 1 hour / 60s = 60 ticks
		{10, 30, 1},     // retSec < syncSec → clamp to 1
		{0, 30, 1},      // 0/30 → clamp to 1
		{3600, 1, 3600}, // 1s sync → 3600 ticks
	}
	for _, c := range cases {
		syncSec := c.syncSec
		if syncSec < 1 {
			syncSec = 1
		}
		got := c.retSec / syncSec
		if got < 1 {
			got = 1
		}
		if got != c.want {
			t.Errorf("retSec=%d syncSec=%d: want %d, got %d", c.retSec, c.syncSec, c.want, got)
		}
	}
}

// TestFleetFailureThrottle verifies syncThrottle logs the first failure, then
// throttles to every Nth, and signals recovery exactly once when a success
// ends a failure streak.
func TestFleetFailureThrottle(t *testing.T) {
	th := &syncThrottle{logEvery: 20}
	// 1st failure logs; next 19 don't; 21st (every 20th) does; a success resets + signals recovery.
	if !th.shouldLogFailure() {
		t.Fatal("first failure must log")
	}
	for i := 0; i < 19; i++ {
		if th.shouldLogFailure() {
			t.Fatalf("failure %d must be throttled", i+2)
		}
	}
	if !th.shouldLogFailure() {
		t.Fatal("the 20th-later failure must log")
	}
	if rec := th.recordSuccess(); !rec {
		t.Fatal("first success after failures must signal recovery")
	}
	if rec := th.recordSuccess(); rec {
		t.Fatal("a success with no prior failure must not signal recovery")
	}
}
