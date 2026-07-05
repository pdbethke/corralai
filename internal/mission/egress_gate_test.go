// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// fakeEgress is an EgressScanner spy used in tests.
type fakeEgress struct {
	findings  []EgressFinding
	calls     int
	lastDir   string
	lastFiles []string
}

func (f *fakeEgress) Scan(_ context.Context, dir string, files []string) []EgressFinding {
	f.calls++
	f.lastDir = dir
	f.lastFiles = files
	return f.findings
}

func setupEgressMission(t *testing.T) (*Store, *queue.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ms.Close() })

	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "add a wishlist feature", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	return ms, q, mid
}

// TestEgressGate_PlantedSecretBlocksPushAndPR verifies the priority case: a
// blocking egress finding (the mirror of a planted secret in the changed
// files) must withhold Push/OpenPR entirely, must not set PRURL, and must be
// recorded as a critical finding.
func TestEgressGate_PlantedSecretBlocksPushAndPR(t *testing.T) {
	ms, q, mid := setupEgressMission(t)

	fake := &fakeRepo{}
	egressFake := &fakeEgress{findings: []EgressFinding{
		{Path: "secrets.go", Line: 3, Rule: "AWS Access Key ID", Sample: "AKIA...MNOP (redacted)", Severity: "block"},
	}}
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Egress = egressFake
	e.Workspace = t.TempDir()

	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if egressFake.calls == 0 {
		t.Fatal("expected the egress scanner to be invoked")
	}
	if fake.pushCalls != 0 {
		t.Fatalf("expected Push to be withheld on a blocking egress finding, got %d calls", fake.pushCalls)
	}
	if fake.prCalls != 0 {
		t.Fatalf("expected OpenPR to be withheld on a blocking egress finding, got %d calls", fake.prCalls)
	}
	mi, err := ms.Mission(mid)
	if err != nil || mi == nil {
		t.Fatalf("mission lookup: %v", err)
	}
	if mi.PRURL != "" {
		t.Fatalf("expected no PRURL to be set, got %q", mi.PRURL)
	}

	findings, err := q.Findings(mid, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Reporter == "egress-scan" && f.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a critical egress-scan finding to be recorded, got: %+v", findings)
	}

	// A retried Tick must not re-attempt push/PR (egressBlocked is sticky, like
	// prGaveUp) — the reconcile loop stops hammering a permanently-blocked mission.
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if fake.pushCalls != 0 {
		t.Fatalf("expected Push to remain withheld after a further tick, got %d calls", fake.pushCalls)
	}
}

// TestEgressGate_CleanSetProceedsAsBefore verifies a clean change set (no
// findings from the scanner) is not blocked: push+PR fire exactly as they did
// before the egress gate existed.
func TestEgressGate_CleanSetProceedsAsBefore(t *testing.T) {
	ms, q, mid := setupEgressMission(t)

	fake := &fakeRepo{}
	egressFake := &fakeEgress{} // no findings — clean
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Egress = egressFake
	e.Workspace = t.TempDir()

	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if egressFake.calls == 0 {
		t.Fatal("expected the egress scanner to be invoked even on a clean set")
	}
	if fake.pushCalls != 1 {
		t.Fatalf("expected Push to fire once on a clean egress scan, got %d calls", fake.pushCalls)
	}
	if fake.prCalls != 1 {
		t.Fatalf("expected OpenPR to fire once on a clean egress scan, got %d calls", fake.prCalls)
	}
	mi, err := ms.Mission(mid)
	if err != nil || mi == nil {
		t.Fatalf("mission lookup: %v", err)
	}
	if mi.PRURL == "" {
		t.Fatal("expected PRURL to be set on a clean egress scan")
	}
}

// TestEgressGate_ScansCumulativeChangedFiles verifies the gate asks for the
// mission's cumulative changed-file set (ChangedFilesRange against base), not
// just the most recent commit — a secret from an earlier phase must still be
// visible to the scanner at push time.
func TestEgressGate_ScansCumulativeChangedFiles(t *testing.T) {
	ms, q, _ := setupEgressMission(t)

	fake := &fakeRepo{rangeFiles: []string{"phase1.go", "phase2.go"}}
	egressFake := &fakeEgress{}
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Egress = egressFake
	e.Workspace = t.TempDir()

	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if len(fake.rangeCalls) == 0 || fake.rangeCalls[0] != "main" {
		t.Fatalf("expected ChangedFilesRange to be called with base %q, got calls: %v", "main", fake.rangeCalls)
	}
	if len(egressFake.lastFiles) != 2 || egressFake.lastFiles[0] != "phase1.go" || egressFake.lastFiles[1] != "phase2.go" {
		t.Fatalf("expected the scanner to receive the cumulative file set, got: %v", egressFake.lastFiles)
	}
}

// TestEgressGate_NilEgressLeavesFlowUnchanged verifies that a nil Engine.Egress
// (the default, matching every pre-existing test) disables the gate entirely —
// the RepoOps.ChangedFilesRange addition must not change behavior for callers
// that never configure egress scanning.
func TestEgressGate_NilEgressLeavesFlowUnchanged(t *testing.T) {
	ms, q, _ := setupEgressMission(t)

	fake := &fakeRepo{}
	e := NewEngine(ms, q)
	e.Repo = fake
	// e.Egress intentionally left nil.
	e.Workspace = t.TempDir()

	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	if fake.pushCalls != 1 || fake.prCalls != 1 {
		t.Fatalf("expected push+PR to proceed with Egress unset, got push=%d pr=%d", fake.pushCalls, fake.prCalls)
	}
	if len(fake.rangeCalls) != 0 {
		t.Fatalf("expected ChangedFilesRange to be untouched when Egress is nil, got: %v", fake.rangeCalls)
	}
}
