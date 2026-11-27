// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
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
	measureRebuild = func(p *adequacy.WorkspacePool, ctx context.Context, base map[string]string, codePath, compliant, probed string, cmd []string) (time.Duration, error) {
		*n++
		if stub != nil {
			return stub()
		}
		return real(p, ctx, base, codePath, compliant, probed, cmd)
	}
	return n
}

// Every language gets one timed run on the probe, its duration becomes the
// pool's floor, the probe is what confirms a timed-out mutant's kill, the
// file is put back, and the scan says nothing: a floor that was measured
// needs no excuse. The fixture's file is named for Swift and holds no code
// any language would read; the command only greps it.
func TestRebuildFloorIsMeasuredForEveryLanguage(t *testing.T) {
	for _, name := range lang.Names() {
		src := "OK\n"
		if name == "php" {
			src = "<?php\nOK\n"
		}
		pool, root, codeKey, cmd := rebuildFixture(t, src)
		runs := countRebuildRuns(t, nil)
		took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, name), codeKey, []byte(src), cmd, 0)
		if err != nil {
			t.Fatalf("%s: rebuildFloor: %v", name, err)
		}
		if record != capFloorOf(pool) || !strings.HasPrefix(record, "rebuild run, ") {
			t.Errorf("%s: record = %q, want the measured rebuild run named, as capFloorOf(pool) = %q", name, record, capFloorOf(pool))
		}
		if *runs != 1 || took <= 0 || pool.WarmDuration() != took {
			t.Fatalf("%s: runs = %d, took = %v, WarmDuration = %v: want one run whose duration is the floor", name, *runs, took, pool.WarmDuration())
		}
		if path, compliant, probed := pool.RebuildProbe(); path != codeKey || compliant != src || !strings.Contains(probed, "corral rebuild probe") {
			t.Errorf("%s: RebuildProbe() = %q, %q, %q; want the file, its code and the probe", name, path, compliant, probed)
		}
		if b, _ := os.ReadFile(filepath.Join(root, codeKey)); string(b) != src {
			t.Fatalf("%s: %s left as %q", name, codeKey, b)
		}
		if line := capFloorNoneLine(codeKey, record); line != "" {
			t.Errorf("%s: the scan would print %q for a floor that was measured", name, line)
		}
	}
}

// No rebuild run when the operator named --test-timeout: that cap is theirs
// and no floor reaches it.
func TestRebuildFloorRunsNothingUnderAnExplicitTimeout(t *testing.T) {
	pool, _, codeKey, cmd := rebuildFixture(t, "OK\n")
	runs := countRebuildRuns(t, nil)
	for _, name := range lang.Names() {
		took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, name), codeKey, []byte("OK\n"), cmd, time.Minute)
		if err != nil || took != 0 || record != "" {
			t.Errorf("%s (test-timeout 1m): took %v, record %q, err %v; want nothing run and nothing recorded", name, took, record, err)
		}
	}
	if *runs != 0 || pool.WarmDuration() != 0 {
		t.Fatalf("runs = %d, WarmDuration = %v; want none", *runs, pool.WarmDuration())
	}
}

// A rebuild run that fails costs the file its floor and nothing else: the
// audit goes on, and the scan's line says which file and why, with no number
// that could read as a measured floor.
func TestAFailedRebuildRunIsDisclosedAndYieldsNoFloor(t *testing.T) {
	pool, root, codeKey, _ := rebuildFixture(t, "OK\n")
	took, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("OK\n"), []string{"false"}, 0)
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
	line := capFloorNoneLine(codeKey, record)
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
	_, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("#!/usr/bin/swift\nOK\n"), cmd, 0)
	if err != nil {
		t.Fatalf("rebuildFloor: %v", err)
	}
	if !strings.HasPrefix(record, "none: ") {
		t.Errorf("record = %q, want \"none: <why>\"", record)
	}
	if *runs != 0 {
		t.Fatalf("ran %d rebuild run(s) for a file the probe refused", *runs)
	}
	if line := capFloorNoneLine(codeKey, record); !strings.Contains(line, codeKey) || !strings.Contains(line, "shebang") {
		t.Errorf("disclosure = %q, want the file and the reason", line)
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
		_, _, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("OK\n"), cmd, 0)
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

// Final review, finding 3: a rebuild run cut off at the runner's timeout is
// slower than any rebuild that finished, so it stops the file as a floor over
// the ceiling does (not graded, with the reason), where it used to record
// "none: ..." and grade every mutant under a cap its relink could never fit.
func TestATimedOutRebuildRunStopsTheFile(t *testing.T) {
	root := t.TempDir()
	codeKey := "Sources/A/A.swift"
	mustWrite(t, filepath.Join(root, codeKey), "OK\n")
	pool, _, err := adequacy.NewWorkspacePool(context.Background(), root, 1, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := rebuildFloor(context.Background(), pool, mustPlugin(t, "swift"), codeKey, []byte("OK\n"), []string{"sleep", "30"}, 0)
	if err == nil {
		t.Fatalf("a timed-out rebuild run graded the file: record %q", record)
	}
	if !errors.Is(err, adequacy.ErrRebuildOverCeiling) || !strings.Contains(err.Error(), codeKey) || !strings.Contains(err.Error(), "cannot be graded") {
		t.Errorf("err = %v, want the file named as not gradable, over the ceiling", err)
	}
	if record != "" || pool.WarmDuration() != 0 {
		t.Errorf("record %q, WarmDuration %v: a run that stops the file sets no floor", record, pool.WarmDuration())
	}
}

// Final review, finding 2: on a one-tree pool nothing runs the suite before
// the pool is built (the probe skips one tree, and the evidence pass builds
// elsewhere), so a rebuild run made where the pool is built is the first
// ordinary run of the job and times a cold build as the rebuild. The rebuild
// run is now made by the audit's wiring, once Execute has armed it after the
// baseline-stability runs, which warm the tree, so no run is added. Here the "suite" is slow on
// its first invocation only, as a cold build is.
func TestTheRebuildRunFollowsTheBaselineStabilityRuns(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Package.swift"), "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"A\")\n")
	mustWrite(t, filepath.Join(root, "Sources", "A", "A.swift"), "let ok = \"OK\"\n")
	mustWrite(t, filepath.Join(root, "Tests", "ATests", "ATests.swift"), "import XCTest\n")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	// A stand-in swift on PATH, for the host-side toolchain check: the suite
	// below is the command that runs, and it needs no Swift.
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "swift"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "swift"), 0o700); err != nil { // #nosec G302 -- test stand-in must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mark := filepath.Join(t.TempDir(), "built")
	cold := 1500 * time.Millisecond
	cmd := []string{"sh", "-c", fmt.Sprintf(`[ -e %q ] || { touch %q; sleep %g; }; grep -q OK Sources/A/A.swift`, mark, mark, cold.Seconds())}
	in := localAuditInput{
		repoDir: root, codePath: "Sources/A/A.swift", testPath: "Tests/ATests/ATests.swift",
		goal: "the file keeps saying OK", lang: "swift", substrate: substrateWorkspace,
		mutantConcurrency: 1, checkArgv: cmd, timeout: time.Minute,
		concurrency: new(adequacy.Disclosure), capFloor: new(string), pool: &workspacePool{},
		stdout: io.Discard, stderr: io.Discard,
	}
	defer in.pool.close()

	runner, cleanup, err := baselineRunnerFor(context.Background(), in)
	if err != nil {
		t.Fatalf("baseline wiring: %v", err)
	}
	if stable, err := reposcan.CheckBaselineStable(runner, 2); err != nil || !stable {
		t.Fatalf("baseline stable = %v, err = %v", stable, err)
	}
	cleanup()
	// What Execute does once the baseline is stably green; the audit's wiring
	// then comes through the same door and reuses the job's pool.
	if in.pool.pool.WarmDuration() != 0 || *in.capFloor != "" {
		t.Fatalf("a rebuild run was made before the baseline-stability runs: floor %v, CapFloor %q", in.pool.pool.WarmDuration(), *in.capFloor)
	}
	in.pool.armRebuild()
	_, cleanup2, err := baselineRunnerFor(context.Background(), in)
	if err != nil {
		t.Fatalf("audit wiring: %v", err)
	}
	cleanup2()

	warm := in.pool.pool.WarmDuration()
	if warm <= 0 || warm >= cold {
		t.Fatalf("the recorded floor is %v: want the warm rebuild, under the %v cold first run", warm, cold)
	}
	if !strings.HasPrefix(*in.capFloor, "rebuild run, ") {
		t.Errorf("CapFloor = %q, want the measured rebuild run", *in.capFloor)
	}
	if in.concurrency.RebuildDuration != warm {
		t.Errorf("the pool phase records a rebuild of %v, want the %v the floor was set from", in.concurrency.RebuildDuration, warm)
	}
}

// Execute arms the rebuild run only after the baseline-stability runs pass,
// so the wiring the baseline builds makes none and the audit's makes it; a
// file whose baseline is not stably green pays for no rebuild run at all.
func TestExecuteArmsTheRebuildRunAfterAStableGreenBaseline(t *testing.T) {
	for _, green := range []bool{true, false} {
		var atBaseline, atAudit bool
		audited := false
		ex := localExecutor{
			baselineRuns: 2,
			substrate:    substrateWorkspace,
			progress:     io.Discard,
			newBaseline: func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
				atBaseline = atBaseline || in.pool.rebuildDue
				return fixedBaseline(green), func() {}, nil
			},
			audit: func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
				audited, atAudit = true, in.pool.rebuildDue
				return advpool.Verdict{}, nil
			},
		}
		if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "Sources/B/B.swift", Lang: "swift", Goal: reposcan.Goal{Text: "g"}}); err != nil {
			t.Fatal(err)
		}
		if atBaseline {
			t.Errorf("green=%v: the rebuild run was armed before the baseline-stability runs", green)
		}
		if green && (!audited || !atAudit) {
			t.Errorf("a stably green baseline reached the audit with the rebuild run not armed (audited %v)", audited)
		}
		if !green && audited {
			t.Errorf("a red baseline reached the audit")
		}
	}
}

type fixedBaseline bool

func (b fixedBaseline) RunBaseline() (bool, error) { return bool(b), nil }
