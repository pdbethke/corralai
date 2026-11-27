// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// The false kill the workspace's go -count=1 rewrite exists to stop, with a
// real go on a real pool. The pool's runs use the operator's GOCACHE, and a
// file's baseline-stability runs have already run the unmutated code by the
// time Score does, so (warmed here by one run) Score's baseline replays the
// stored result and takes no time, its per-mutant cap is the floor, a mutant
// that changes something no test checks runs its tests, is cut off at the
// cap, and the re-probe on the unmutated code replays again and passes:
// a kill the suite never made. Through the rewrite every run runs, the cap
// is three times a real baseline, and the mutant survives.
func TestAWorkspaceGoBaselineThatReplaysCreditsAFalseKill(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("SKIP: go not on PATH")
	}
	t.Setenv("GOCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "")
	code := "package a\n\nfunc Unchecked() int { return 1 }\n"
	files := map[string]string{
		"go.mod":    "module example.com/a\n\ngo 1.21\n",
		"a.go":      code,
		"a_test.go": "package a\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) { t.Log(Unchecked()); time.Sleep(600 * time.Millisecond) }\n",
	}
	cmd := []string{"go", "test", "./..."}
	mutants := []Mutant{{ID: "m", Search: "return 1", Replace: "return 2"}}
	lowFloor := func(c *scoreConfig) { c.capFloor = 200 * time.Millisecond }
	goPlug, ok := lang.ByName("go")
	if !ok {
		t.Fatal("no go plugin")
	}

	for _, rewritten := range []bool{false, true} {
		root := wsTree(t, files)
		p := onePool(t, root, 2*time.Minute)
		opts := []ScoreOption{lowFloor}
		if rewritten {
			opts = append(opts, WithCommandRewrite(lang.NoReplayArgv(goPlug, lang.Cmd(cmd))))
		}
		// The stability runs' stand-in: the unmutated code, run as Score
		// will run it.
		warm := cmd
		if rewritten {
			warm = lang.NoReplayArgv(goPlug, lang.Cmd(cmd))(cmd)
		}
		if pass, err := p.RunTest(context.Background(), map[string]string{}, warm); err != nil || !pass {
			t.Fatalf("warming run: pass=%v err=%v", pass, err)
		}
		rep, err := Score(context.Background(), p, map[string]string{}, "a.go", code, mutants, cmd, opts...)
		if err != nil {
			t.Fatalf("rewritten=%v: Score: %v", rewritten, err)
		}
		pm := rep.PerMutant["m"]
		if !rewritten && pm.KillConfirmation != KillConfirmedReprobed {
			t.Errorf("as typed: m = %+v, want the timeout kill a replayed baseline credits (the control)", pm)
		}
		if rewritten && (!hasID(rep.Survived, "m") || hasID(rep.Killed, "m")) {
			t.Errorf("with -count=1: Killed = %v, Survived = %v (m = %+v), want m a survivor", rep.Killed, rep.Survived, pm)
		}
		if got := read(t, root, "a.go"); got != code {
			t.Fatalf("a.go left as %q", got)
		}
	}
}
