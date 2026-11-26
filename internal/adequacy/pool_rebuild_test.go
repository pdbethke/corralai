// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The rebuild run is a grading run like any other: the probed file is on disk
// while the command runs, the original bytes are back when it returns, and the
// measured duration is what the pool reports as its WarmDuration.
func TestMeasureRebuildSetsTheFloorAndRestoresTheFile(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	p := onePool(t, root, 30*time.Second)

	// Sleeps only when the probe is on disk, so the duration proves it ran
	// against the probed file.
	cmd := []string{"sh", "-c", `head -n 1 a.txt | grep -q PROBE && sleep 0.3; exit 0`}
	took, err := p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL\n", "PROBE\nORIGINAL\n", cmd)
	if err != nil {
		t.Fatalf("MeasureRebuild: %v", err)
	}
	if took < 300*time.Millisecond {
		t.Fatalf("took %v: the command did not see the probe on disk", took)
	}
	if got := p.WarmDuration(); got != took {
		t.Fatalf("WarmDuration = %v, want the measured %v", got, took)
	}
	// The probe is reported with the file and the code it was built from, so
	// Score can confirm a timed-out mutant's kill on a run that pays the
	// same rebuild (RebuildProbeJail).
	if path, compliant, probed := p.RebuildProbe(); path != "a.txt" || compliant != "ORIGINAL\n" || probed != "PROBE\nORIGINAL\n" {
		t.Fatalf("RebuildProbe() = %q, %q, %q; want the file, its code and the probe", path, compliant, probed)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Fatalf("a.txt left as %q after the rebuild run", got)
	}
}

// THE RESTORE CASES. On this substrate the probe is written into the
// developer's real checkout, so a probe left behind is a corrupted source file.
// Every way the run can end puts the original bytes back, through the one
// journaled restore every grading run takes, and none of them yields a floor.
func TestMeasureRebuildRestoresWhenTheRunFails(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	p := onePool(t, root, 30*time.Second)

	_, err := p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL\n", "PROBE\nORIGINAL\n", []string{"false"})
	if err == nil {
		t.Fatal("a rebuild run whose suite failed returned no error")
	}
	if errors.Is(err, ErrRebuildOverCeiling) || isRestoreError(err) {
		t.Fatalf("a failing suite is neither over the ceiling nor a failed restore: %v", err)
	}
	if got := p.WarmDuration(); got != 0 {
		t.Fatalf("WarmDuration = %v after a failed run, want 0 (no floor)", got)
	}
	if _, _, probed := p.RebuildProbe(); probed != "" {
		t.Fatalf("RebuildProbe() reported %q after a failed run, want none", probed)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Fatalf("a.txt left as %q after a failed rebuild run", got)
	}
}

func TestMeasureRebuildRestoresWhenTheRunTimesOut(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	p := onePool(t, root, 100*time.Millisecond)

	_, err := p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL\n", "PROBE\nORIGINAL\n", []string{"sleep", "30"})
	if !errors.Is(err, ErrTestTimeout) {
		t.Fatalf("err = %v, want ErrTestTimeout", err)
	}
	// A rebuild run cut off at the runner's own timeout is the slowest rebuild
	// there is, not a run that merely gave no floor: every mutant's relink
	// would be cut off the same way and read as a kill. It is over the
	// ceiling, so the file is not graded.
	if !errors.Is(err, ErrRebuildOverCeiling) {
		t.Fatalf("err = %v, want ErrRebuildOverCeiling: a timed-out rebuild run must stop the file", err)
	}
	if got := p.WarmDuration(); got != 0 {
		t.Fatalf("WarmDuration = %v after a timed-out run, want 0 (no floor)", got)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Fatalf("a.txt left as %q after a timed-out rebuild run", got)
	}
}

func TestMeasureRebuildRestoresWhenTheRunPanics(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	// The per-run env is called after the probe is written and before the
	// command starts: a panic there unwinds through the restore.
	p, _, err := NewWorkspacePool(context.Background(), root, 1, 30*time.Second,
		WithPerRunEnv(func() ([]string, func()) { panic("per-run env exploded") }))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not propagate")
			}
		}()
		_, _ = p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL\n", "PROBE\nORIGINAL\n", []string{"true"})
	}()
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Fatalf("a.txt left as %q after a panicking rebuild run", got)
	}
	if got := p.WarmDuration(); got != 0 {
		t.Fatalf("WarmDuration = %v after a panic, want 0", got)
	}
}

// A restore that could not complete is told apart from a failed run, because
// the two need opposite answers: a failed run costs the file its floor and the
// audit goes on, while a tree that was not put back is not the tree under
// audit, and the caller must stop.
func TestMeasureRebuildReportsARestoreItCouldNotComplete(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	p := onePool(t, root, 30*time.Second)

	_, err := p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL\n", "PROBE\nORIGINAL\n",
		[]string{"sh", "-c", "rm a.txt && mkdir a.txt"})
	if !isRestoreError(err) {
		t.Fatalf("err = %v, want a *RestoreError", err)
	}
	if got := p.WarmDuration(); got != 0 {
		t.Fatalf("WarmDuration = %v, want 0", got)
	}
}

// The ceiling: a rebuild run slower than maxMutantTimeout cannot be graded
// under it, because every mutant pays that rebuild and would be cut off.
// Refused before any mutant is spent on. A run under the ceiling whose three
// times is over it is NOT refused: the cap sits at the ceiling, as it does for
// a slow baseline, and a mutant cut off there is confirmed as a kill only by
// the rebuild probe finishing in the same budget (RebuildProbeJail), so the
// thinner margin can leave a mutant unmeasured but cannot credit a kill.
// (Until 2026-11-26 the refusal was at three times the run, which, once every
// language made a rebuild run, would have stopped grading every suite slower
// than 100 s that grades today.)
func TestRebuildFloorOverTheCeilingIsRefused(t *testing.T) {
	if err := rebuildFloorRefusal(maxMutantTimeout / mutantTimeoutMultiple * 2); err != nil {
		t.Fatalf("a run whose three times is over the ceiling was refused: %v", err)
	}
	if err := rebuildFloorRefusal(maxMutantTimeout); err != nil {
		t.Fatalf("a run exactly at the ceiling was refused: %v", err)
	}
	err := rebuildFloorRefusal(maxMutantTimeout + time.Second)
	if !errors.Is(err, ErrRebuildOverCeiling) {
		t.Fatalf("err = %v, want ErrRebuildOverCeiling", err)
	}
}

// End to end on a real pool: a "build" that costs nothing on unchanged bytes
// and 0.5 s on changed ones, and a mutant that takes 0.6 s and passes. Without
// the rebuild run the cap is the (test-lowered) floor, the mutant times out,
// and the re-probe credits a kill the suite never made. With it, the cap is
// 3 × the rebuild run and the mutant is graded a survivor by its own run.
func TestMeasuredRebuildRunGradesASlowMutantByItsResult(t *testing.T) {
	cmd := []string{"sh", "-c", `case "$(cat a.txt)" in
ORIGINAL) exit 0 ;;
PROBE*) sleep 0.5; exit 0 ;;
*corral*) exit 1 ;;
*) sleep 0.6; exit 0 ;;
esac`}
	mutants := []Mutant{{ID: "m", Search: "ORIGINAL", Replace: "MUTANT"}}
	lowFloor := func(c *scoreConfig) { c.capFloor = 200 * time.Millisecond }

	for _, measured := range []bool{false, true} {
		root := wsTree(t, map[string]string{"a.txt": "ORIGINAL"})
		p := onePool(t, root, 30*time.Second)
		if measured {
			if _, err := p.MeasureRebuild(context.Background(), nil, "a.txt", "ORIGINAL", "PROBE\nORIGINAL", cmd); err != nil {
				t.Fatalf("MeasureRebuild: %v", err)
			}
		}
		rep, err := Score(context.Background(), p, map[string]string{}, "a.txt", "ORIGINAL", mutants, cmd, lowFloor)
		if err != nil {
			t.Fatalf("measured=%v: Score: %v", measured, err)
		}
		if measured && (!hasID(rep.Survived, "m") || hasID(rep.Killed, "m")) {
			t.Errorf("with the rebuild run: Killed = %v, Survived = %v, want m a survivor", rep.Killed, rep.Survived)
		}
		if !measured && rep.PerMutant["m"].KillConfirmation != KillConfirmedReprobed {
			t.Errorf("without the rebuild run: m = %+v, want the timeout kill this exists to stop", rep.PerMutant["m"])
		}
		if got := read(t, root, "a.txt"); got != "ORIGINAL" {
			t.Fatalf("a.txt left as %q", got)
		}
	}
}

func onePool(t *testing.T, root string, timeout time.Duration) *WorkspacePool {
	t.Helper()
	p, _, err := NewWorkspacePool(context.Background(), root, 1, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func isRestoreError(err error) bool {
	var re *RestoreError
	return errors.As(err, &re)
}
