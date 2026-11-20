// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// rebuildFixture is a one-tree pool on a checkout holding one source file, and
// a command that passes when the file still says OK.
func rebuildFixture(t *testing.T, src string) (pool *adequacy.WorkspacePool, root, codeKey string, cmd []string) {
	t.Helper()
	root = t.TempDir()
	codeKey = "Sources/A/A.swift"
	if err := os.MkdirAll(filepath.Join(root, "Sources", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, codeKey), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pool, _, err := adequacy.NewWorkspacePool(context.Background(), root, 1, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return pool, root, codeKey, []string{"sh", "-c", "grep -q OK " + codeKey}
}

func mustPlugin(t *testing.T, name string) lang.Plugin {
	t.Helper()
	p, ok := lang.ByName(name)
	if !ok {
		t.Fatalf("no %s plugin", name)
	}
	return p
}

// countRebuildRuns counts the rebuild runs through the seam, and lets a test
// stand in an outcome the real run cannot cheaply produce.
func countRebuildRuns(t *testing.T, stub func() (time.Duration, error)) *int {
	t.Helper()
	real := measureRebuild
	t.Cleanup(func() { measureRebuild = real })
	n := new(int)
	measureRebuild = func(p *adequacy.WorkspacePool, ctx context.Context, base map[string]string, codePath, probed string, cmd []string) (time.Duration, error) {
		*n++
		if stub != nil {
			return stub()
		}
		return real(p, ctx, base, codePath, probed, cmd)
	}
	return n
}

// A language whose mutant pays a relink its baseline does not gets one timed
// run on the probe, its duration becomes the pool's floor, the file is put
// back, and nothing is printed: a floor that was measured needs no excuse.
func TestRebuildFloorIsMeasuredForAPerRunBuilder(t *testing.T) {
	pool, root, codeKey, cmd := rebuildFixture(t, "let ok = \"OK\"\n")
	runs := countRebuildRuns(t, nil)
	var out bytes.Buffer
	took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("let ok = \"OK\"\n"), cmd, 0, &out)
	if err != nil {
		t.Fatalf("rebuildFloor: %v", err)
	}
	if record != capFloorOf(pool) || !strings.HasPrefix(record, "rebuild run, ") {
		t.Errorf("record = %q, want the measured rebuild run named, as capFloorOf(pool) = %q", record, capFloorOf(pool))
	}
	if *runs != 1 || took <= 0 || pool.WarmDuration() != took {
		t.Fatalf("runs = %d, took = %v, WarmDuration = %v: want one run whose duration is the floor", *runs, took, pool.WarmDuration())
	}
	if b, _ := os.ReadFile(filepath.Join(root, codeKey)); string(b) != "let ok = \"OK\"\n" {
		t.Fatalf("%s left as %q", codeKey, b)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q for a floor that was measured", out.String())
	}
}

// No rebuild run for a language that does not declare one, and none when the
// operator named --test-timeout: that cap is theirs and no floor reaches it.
func TestRebuildFloorRunsNothingItDoesNotNeed(t *testing.T) {
	pool, _, codeKey, cmd := rebuildFixture(t, "OK\n")
	runs := countRebuildRuns(t, nil)
	for _, c := range []struct {
		lang    string
		timeout time.Duration
	}{{"go", 0}, {"python", 0}, {"rust", 0}, {"swift", time.Minute}} {
		took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, c.lang), codeKey, []byte("OK\n"), cmd, c.timeout, &bytes.Buffer{})
		if err != nil || took != 0 || record != "" {
			t.Errorf("%s (test-timeout %v): took %v, record %q, err %v; want nothing run and nothing recorded", c.lang, c.timeout, took, record, err)
		}
	}
	if *runs != 0 || pool.WarmDuration() != 0 {
		t.Fatalf("runs = %d, WarmDuration = %v; want none", *runs, pool.WarmDuration())
	}
}

// A rebuild run that fails costs the file its floor and nothing else: the
// audit goes on, and the line says which file and why, with no number that
// could read as a measured floor.
func TestAFailedRebuildRunIsDisclosedAndYieldsNoFloor(t *testing.T) {
	pool, root, codeKey, _ := rebuildFixture(t, "OK\n")
	var out bytes.Buffer
	took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("OK\n"), []string{"false"}, 0, &out)
	if err != nil {
		t.Fatalf("a failed rebuild run must not fail the audit: %v", err)
	}
	if !strings.HasPrefix(record, "none: ") || !strings.Contains(record, "did not pass") {
		t.Errorf("record = %q, want \"none: <why>\"", record)
	}
	if took <= 0 {
		t.Errorf("took = %v: the run's time must still be accounted", took)
	}
	if pool.WarmDuration() != 0 {
		t.Errorf("WarmDuration = %v, want 0", pool.WarmDuration())
	}
	line := out.String()
	if !strings.Contains(line, codeKey) || !strings.Contains(line, "did not pass") {
		t.Errorf("disclosure = %q, want the file and the reason", line)
	}
	if b, _ := os.ReadFile(filepath.Join(root, codeKey)); string(b) != "OK\n" {
		t.Fatalf("%s left as %q", codeKey, b)
	}
}

// A file the probe cannot change (a shebang must stay first) is disclosed and
// run without a floor; nothing is run for it.
func TestAnUnprobeableFileIsDisclosedAndNotRun(t *testing.T) {
	pool, _, codeKey, cmd := rebuildFixture(t, "#!/usr/bin/swift\nOK\n")
	runs := countRebuildRuns(t, nil)
	var out bytes.Buffer
	_, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("#!/usr/bin/swift\nOK\n"), cmd, 0, &out)
	if err != nil {
		t.Fatalf("rebuildFloor: %v", err)
	}
	if !strings.HasPrefix(record, "none: ") {
		t.Errorf("record = %q, want \"none: <why>\"", record)
	}
	if *runs != 0 {
		t.Fatalf("ran %d rebuild run(s) for a file the probe refused", *runs)
	}
	if !strings.Contains(out.String(), codeKey) {
		t.Errorf("disclosure = %q, want the file named", out.String())
	}
}

// The two outcomes that stop the file: a floor over the ceiling (the file is
// not graded, and says why) and a checkout that was not put back.
func TestRebuildFloorStopsTheFileOnTheCeilingOrAFailedRestore(t *testing.T) {
	pool, _, codeKey, cmd := rebuildFixture(t, "OK\n")
	over := fmt.Errorf("%w: it took 2m0s", adequacy.ErrRebuildOverCeiling)
	restore := errors.Join(errors.New("ran"), &adequacy.RestoreError{Err: errors.New("adequacy: restoring A.swift: is a directory")})
	for _, want := range []error{over, restore} {
		countRebuildRuns(t, func() (time.Duration, error) { return 2 * time.Minute, want })
		_, _, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("OK\n"), cmd, 0, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("no error for %v; the file must not be graded", want)
		}
		if !strings.Contains(err.Error(), codeKey) {
			t.Errorf("error %q does not name the file", err)
		}
	}
}

// The rebuild run is part of what the workspace substrate spends before it can
// score, so it is in the pool phase the timing records.
func TestPoolDurationIncludesTheRebuildRun(t *testing.T) {
	d := &adequacy.Disclosure{Trees: 1, RebuildDuration: 17 * time.Second}
	if got := poolDuration(d); got != 17*time.Second {
		t.Fatalf("poolDuration = %v, want the 17s rebuild run", got)
	}
}
