// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// rewriteDoorJail records every command a door hands it, through either of
// the two run methods a door uses, and passes every one.
type rewriteDoorJail struct {
	mu   sync.Mutex
	cmds [][]string
}

func (j *rewriteDoorJail) record(cmd []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cmds = append(j.cmds, slices.Clone(cmd))
}

func (j *rewriteDoorJail) RunTest(_ context.Context, _ map[string]string, cmd []string) (bool, error) {
	j.record(cmd)
	return false, nil // a failing run: the positive control reads that as "reached"
}

func (j *rewriteDoorJail) RunTestVerbose(_ context.Context, _ map[string]string, cmd []string) (bool, string, error) {
	j.record(cmd)
	return true, "", nil
}

func markRewrite(cmd []string) []string { return append(slices.Clone(cmd), "--rewritten") }

// EVERY DOOR. A cache shared among runs holds test RESULTS, so a command that
// reaches the jail without the grading rewrite could replay one. The doors
// outside adequacy.Score each take the rewrite from the one helper: the
// authored command's failure run, the positive control that plants invalid
// source, and every command of the compile check.
func TestEveryDoorThatRunsACommandTakesTheRewrite(t *testing.T) {
	jail := &rewriteDoorJail{}
	scorer := JailScorer{Jail: jail, BaseFiles: map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}, CommandRewrite: markRewrite}
	validator := JailValidator{Jail: jail, BaseFiles: map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}, CommandRewrite: markRewrite}
	ctx := context.Background()

	if _, _, err := scorer.CompliantRun(ctx, "p.go", "package p\n", "package p\n", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if _, err := scorer.AuthoredTestWouldBeCollected(ctx, "p.go", []string{"go", "test", "./..."}); err != nil {
		t.Fatal(err)
	}
	if err := validator.CompileTest(ctx, "p.go", "package p\n", "package p\n"); err != nil {
		t.Fatal(err)
	}

	if len(jail.cmds) < 3 {
		t.Fatalf("only %d command(s) reached the jail: %v", len(jail.cmds), jail.cmds)
	}
	for _, c := range jail.cmds {
		if !slices.Contains(c, "--rewritten") {
			t.Errorf("a command reached the jail without the grading rewrite: %v", c)
		}
	}
}

// Without a rewrite (every JailScorer{} literal, the daemon's included) the
// doors hand the jail the command byte for byte.
func TestNoRewriteLeavesEveryDoorsCommandUntouched(t *testing.T) {
	jail := &rewriteDoorJail{}
	scorer := JailScorer{Jail: jail, BaseFiles: map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}}
	if _, _, err := scorer.CompliantRun(context.Background(), "p.go", "package p\n", "package p\n", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if _, err := scorer.AuthoredTestWouldBeCollected(context.Background(), "p.go", []string{"go", "test", "./..."}); err != nil {
		t.Fatal(err)
	}
	for _, c := range jail.cmds {
		if slices.Contains(c, "--rewritten") {
			t.Errorf("a command was rewritten with no rewrite set: %v", c)
		}
	}
}

// Score's own commands (baseline, mutants, confirmation, compile gate) are
// rewritten through baseScoreOpts, the one option list every Score call shares.
func TestBaseScoreOptsCarriesTheRewrite(t *testing.T) {
	jail := &rewriteDoorJail{}
	scorer := JailScorer{Jail: jail, BaseFiles: map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}, CommandRewrite: markRewrite}
	m := adequacy.Mutant{ID: "m1", Search: "package p", Replace: "package p // changed"}
	_, _ = scorer.ScoreReport(context.Background(), "p.go", "package p\n", "", []adequacy.Mutant{m}, "go test ./...")
	if len(jail.cmds) == 0 {
		t.Fatal("Score ran nothing")
	}
	for _, c := range jail.cmds {
		if !slices.Contains(c, "--rewritten") {
			t.Errorf("Score handed the jail an unrewritten command: %v", c)
		}
	}
}
