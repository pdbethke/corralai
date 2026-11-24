// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// The audited Rust source, whose tests are inline, and the landed block
// (what ParseTest hands every door: lang.LandBody at depth 0).
const (
	inlineSrc   = "pub fn double(x: i32) -> i32 { x * 2 }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    #[test]\n    fn own() { assert_eq!(double(2), 4); }\n}\n"
	inlineBlock = "\n#[cfg(test)]\nmod corral_tests {\nuse super::*;\n#[test]\nfn kills() { assert_eq!(double(3), 6); }\n}\n"
)

// inlineLanding is the landing whose Path IS the audited source — below
// lang.CheckTestLanding, which still refuses it; the doors are built first.
func inlineLanding() AuthoredLanding {
	return AuthoredLanding{
		Landing:  pairing.Landing{Path: "src/lib.rs", Shape: pairing.Inline, Label: "inline", Into: true, Depth: 0, DeclarationsRead: true},
		Existing: inlineSrc,
	}
}

func TestAuthoredLandingInline(t *testing.T) {
	if !inlineLanding().Inline("src/lib.rs") {
		t.Error("a landing at the source itself is not inline")
	}
	if inlineLanding().Inline("src/other.rs") {
		t.Error("a landing at another file is inline")
	}
	if declaredLanding("").Inline("src/foo.rs") {
		t.Error("a Declared landing is inline")
	}
	if (AuthoredLanding{}).Inline("src/lib.rs") {
		t.Error("the zero landing is inline")
	}
}

// doorJail records, for every run any door hands it, the command and every
// file in the workspace. A run fails when src/lib.rs holds the canary or the
// token KILL (a compiler refusing invalid source, a test catching a mutant).
// With runsBlock it is a command that also RUNS the appended block's tests,
// so it fails on the reach canary (the always-failing landed test); without
// it, it is a command that compiles the file but whose filter never runs the
// block — the case the inline positive control must call unreached.
type doorJail struct {
	mu        sync.Mutex
	runs      []doorRun
	runsBlock bool
}

type doorRun struct {
	cmd   []string
	files map[string]string
}

func (j *doorJail) run(files map[string]string, cmd []string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runs = append(j.runs, doorRun{cmd: slices.Clone(cmd), files: files})
	src := files["src/lib.rs"]
	if j.runsBlock && strings.Contains(src, "fn corral_reach_canary()") {
		return false
	}
	return !strings.Contains(src, adequacy.CanaryCode) && !strings.Contains(src, "KILL")
}

func (j *doorJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	return j.run(files, cmd), nil
}

func (j *doorJail) RunTestVerbose(_ context.Context, files map[string]string, cmd []string) (bool, string, error) {
	return j.run(files, cmd), "", nil
}

func (j *doorJail) take() []doorRun {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.runs
	j.runs = nil
	return r
}

// TestInlineAuthoredDoorsComposeTheSameFile: every door that runs the
// authored test — the compile check, the clean-code failure run, the authored
// pass and its positive control, and the collection preflight — writes the
// block INTO the source, appended to the code in place, and writes nothing at
// any other path. The compile check, the failure run and the authored pass's
// baseline write identical bytes for the original code, so the file compiled
// is the file graded; the positive control writes the canary in the block's
// place: the TestLander's always-failing test, landed as a block (there is
// no separate file to plant a canary in, and invalid source would ask only
// what Score's own canary already asked).
func TestInlineAuthoredDoorsComposeTheSameFile(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{"Cargo.toml": "[package]\n", "src/lib.rs": inlineSrc}
	want := inlineSrc + inlineBlock
	wantCanary := inlineSrc + reachBlock(t)
	j := &doorJail{runsBlock: true}
	l := inlineLanding()
	v := JailValidator{Jail: j, BaseFiles: base, DevTestPath: "src/lib.rs", Landing: l}
	s := JailScorer{Jail: j, BaseFiles: base, DevTestPath: "src/lib.rs", Landing: l}

	onlyBaseKeys := func(who string, runs []doorRun) {
		t.Helper()
		for _, r := range runs {
			for k := range r.files {
				if _, ok := base[k]; !ok {
					t.Errorf("%s wrote %s, a file the base does not hold", who, k)
				}
			}
		}
	}

	if err := v.CompileTest(ctx, "src/lib.rs", inlineSrc, inlineBlock); err != nil {
		t.Fatal(err)
	}
	runs := j.take()
	onlyBaseKeys("CompileTest", runs)
	if len(runs) == 0 || runs[0].files["src/lib.rs"] != want {
		t.Fatalf("CompileTest did not compile the source with the block appended: %+v", runs)
	}

	_ = s.CompliantFailure(ctx, "src/lib.rs", inlineSrc, inlineBlock, "cargo test")
	runs = j.take()
	onlyBaseKeys("CompliantFailure", runs)
	if len(runs) != 1 || runs[0].files["src/lib.rs"] != want {
		t.Fatalf("CompliantFailure did not run the source with the block appended: %+v", runs)
	}

	mutants := []adequacy.Mutant{{ID: "m1", Search: "x * 2", Replace: "x * 2 /* KILL */"}}
	rep, err := s.ScoreAuthoredReport(ctx, "src/lib.rs", inlineSrc, inlineBlock, mutants, "cargo test")
	if err != nil {
		t.Fatal(err)
	}
	runs = j.take()
	onlyBaseKeys("ScoreAuthoredReport", runs)
	if !rep.CompliantPass || !rep.CanaryKilled || rep.AuthoredTestUnreached || !slices.Contains(rep.Killed, "m1") {
		t.Fatalf("the inline authored pass did not grade: %+v", rep)
	}
	if got := runs[0].files["src/lib.rs"]; got != want {
		t.Errorf("the authored pass's baseline wrote\n%s\nwant\n%s", got, want)
	}
	if got := runs[len(runs)-1].files["src/lib.rs"]; got != wantCanary {
		t.Errorf("the positive control wrote\n%s\nwant the source with the reach canary appended", got)
	}
	for i, r := range runs {
		src := r.files["src/lib.rs"]
		if !strings.HasSuffix(src, inlineBlock) && src != wantCanary && !strings.HasSuffix(src, adequacy.CanaryCode+"\n"+inlineBlock) {
			t.Errorf("authored-pass run %d dropped the block:\n%s", i, src)
		}
	}

	ok, err := s.AuthoredTestWouldBeCollected(ctx, "src/lib.rs", inlineSrc, []string{"cargo", "test"})
	if err != nil || !ok {
		t.Fatalf("preflight: %v %v", ok, err)
	}
	runs = j.take()
	onlyBaseKeys("AuthoredTestWouldBeCollected", runs)
	if len(runs) != 1 || runs[0].files["src/lib.rs"] != wantCanary {
		t.Fatalf("the preflight did not plant the reach canary in the source: %+v", runs)
	}
}

// TestWorkspaceRestoresTheSourceAfterAnInlineAuthoredRun: on the workspace
// substrate the inline block is written into the operator's own source file,
// in the code each run writes there, so the substrate's journaled restore of
// that one path must bring it back byte for byte, and no other file is left
// behind. The command is a real process over a real checkout: it fails on
// the canary or a KILL mutant, and passes only when the block is present, so
// a pass proves the block was on disk for the run.
func TestWorkspaceRestoresTheSourceAfterAnInlineAuthoredRun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"Cargo.toml": "[package]\n", "src/lib.rs": inlineSrc}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(root, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listing := func() []string {
		var out []string
		_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, _ error) error {
			out = append(out, p)
			return nil
		})
		return out
	}
	before := listing()

	s := JailScorer{Jail: adequacy.NewWorkspaceRunner(root, 30*time.Second), BaseFiles: map[string]string{}, DevTestPath: "src/lib.rs", Landing: inlineLanding()}
	// "Runs" the block: fails on its always-failing reach canary, as a
	// runner that executes corral_tests would.
	cmd := `sh -c 'if grep -q "corral canary" src/lib.rs || grep -q KILL src/lib.rs || grep -q "fn corral_reach_canary" src/lib.rs; then exit 1; fi; grep -q corral_tests src/lib.rs'`
	mutants := []adequacy.Mutant{
		{ID: "m1", Search: "x * 2", Replace: "x * 2 /* KILL */"},
		{ID: "m2", Search: "x * 2", Replace: "x + x"},
	}
	rep, err := s.ScoreAuthoredReport(context.Background(), "src/lib.rs", inlineSrc, inlineBlock, mutants, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || !rep.CanaryKilled || rep.AuthoredTestUnreached {
		t.Fatalf("the inline run did not grade on the workspace substrate: %+v", rep)
	}
	if !slices.Contains(rep.Killed, "m1") || !slices.Contains(rep.Survived, "m2") {
		t.Fatalf("killed %v survived %v", rep.Killed, rep.Survived)
	}

	got, err := os.ReadFile(filepath.Join(root, "src/lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != inlineSrc {
		t.Fatalf("the source was not restored:\n%s", got)
	}
	if after := listing(); !slices.Equal(before, after) {
		t.Fatalf("the checkout's files changed:\nbefore %v\nafter  %v", before, after)
	}
}

// reachBlock is the inline positive control's block for inlineSrc.
func reachBlock(t *testing.T) string {
	t.Helper()
	b, err := golang.ReachCanaryBlock(inlineLanding().Landing, inlineSrc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestInlinePositiveControlSeesWhetherTheBlockRuns: for an inline landing the
// control appends a test that always fails, so it can tell a command that
// RUNS the authored block (it fails: reached) from one that compiles the
// source but never runs the block (it passes: unreached). Planting invalid
// source instead would fail both, and a dropped block would pass both.
func TestInlinePositiveControlSeesWhetherTheBlockRuns(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{"Cargo.toml": "[package]\n", "src/lib.rs": inlineSrc}
	mutants := []adequacy.Mutant{{ID: "m1", Search: "x * 2", Replace: "x * 2 /* KILL */"}}
	for _, tc := range []struct {
		name      string
		runsBlock bool
	}{{"never runs the block", false}, {"runs the block", true}} {
		t.Run(tc.name, func(t *testing.T) {
			j := &doorJail{runsBlock: tc.runsBlock}
			s := JailScorer{Jail: j, BaseFiles: base, DevTestPath: "src/lib.rs", Landing: inlineLanding()}

			reached, err := s.AuthoredTestWouldBeCollected(ctx, "src/lib.rs", inlineSrc, []string{"cargo", "test"})
			if err != nil {
				t.Fatal(err)
			}
			if reached != tc.runsBlock {
				t.Errorf("preflight reached=%v for a command that runsBlock=%v", reached, tc.runsBlock)
			}
			if got := j.take()[0].files["src/lib.rs"]; got != inlineSrc+reachBlock(t) {
				t.Errorf("the control did not write the source with the reach canary appended:\n%s", got)
			}

			rep, err := s.ScoreAuthoredReport(ctx, "src/lib.rs", inlineSrc, inlineBlock, mutants, "cargo test")
			if err != nil {
				t.Fatal(err)
			}
			if rep.AuthoredTestUnreached == tc.runsBlock || rep.CanaryKilled != tc.runsBlock {
				t.Errorf("runsBlock=%v: AuthoredTestUnreached=%v CanaryKilled=%v", tc.runsBlock, rep.AuthoredTestUnreached, rep.CanaryKilled)
			}
		})
	}
}
