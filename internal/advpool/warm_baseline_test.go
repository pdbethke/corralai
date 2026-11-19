// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// warmRecordingJail is a RunCacheJail that records which run was the warming
// one (the only run that may write the shared cache) and passes every run.
type warmRecordingJail struct {
	mu   sync.Mutex
	runs []warmRun
}

type warmRun struct {
	warm bool
	test string // the authored test's content, if the tree holds one
	cmd  string
}

func (j *warmRecordingJail) note(warm bool, files map[string]string, cmd []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	test := ""
	for k, v := range files {
		if strings.HasSuffix(k, "_corral_test.go") || strings.Contains(k, "corral_authored") {
			test = v
		}
	}
	j.runs = append(j.runs, warmRun{warm: warm, test: test, cmd: strings.Join(cmd, " ")})
}
func (j *warmRecordingJail) RunTest(_ context.Context, f map[string]string, cmd []string) (bool, error) {
	j.note(false, f, cmd)
	return true, nil
}
func (j *warmRecordingJail) RunTestDetailed(_ context.Context, f map[string]string, cmd []string) (bool, []byte, error) {
	j.note(false, f, cmd)
	return true, nil, nil
}
func (j *warmRecordingJail) WarmRun(_ context.Context, f map[string]string, cmd []string) (bool, []byte, error) {
	j.note(true, f, cmd)
	return true, nil, nil
}
func (j *warmRecordingJail) RunCache() sandbox.SharedCache { return sandbox.SharedCache{Target: "/c"} }
func (j *warmRecordingJail) Close() error                  { return nil }
func (j *warmRecordingJail) Warmed() bool                  { return j.warms() > 0 }

// WarmDuration is zero: these tests read which run warmed, not caps.
func (j *warmRecordingJail) WarmDuration() time.Duration { return 0 }

func (j *warmRecordingJail) warms() (n int) {
	for _, r := range j.runs {
		if r.warm {
			n++
		}
	}
	return
}

func warmScorer(j adequacy.Jail, check func([]string) bool) JailScorer {
	return JailScorer{
		Jail:      j,
		BaseFiles: map[string]string{"go.mod": "module m\n", "p/p.go": "package p\nfunc One() int { return 1 }\n", "p/p_test.go": "package p\n"},
		Lang:      "go", DevTestPath: "p/p_test.go",
		CommandRewrite: markRewrite, WarmCheck: check,
	}
}

var warmMutant = []adequacy.Mutant{{ID: "m1", Search: "return 1", Replace: "return 2"}}

// Exactly one path may warm the shared cache: the dev pass, through the
// driver's scoreDevReport, and only when the check on that pass's own baseline
// command says the command forbids a cached result. Every other Score call on
// the jail (authored, salvage, the critic's checks, the matrix) runs the cache
// frozen, because a test a model wrote may be in its tree.
func TestOnlyTheDevPassWarmsTheRunCache(t *testing.T) {
	j := &warmRecordingJail{}
	s := warmScorer(j, func(cmd []string) bool { return slices.Contains(cmd, "go") })
	ctx := context.Background()
	rs := RunSpec{CodePath: "p/p.go", Code: "package p\nfunc One() int { return 1 }\n", DevTestCode: "package p\n", TestCmd: "go test ./...", Lang: "go", DevTestPath: "p/p_test.go"}

	if _, _, err := scoreDevReport(ctx, s, rs, warmMutant); err != nil {
		t.Fatal(err)
	}
	if j.warms() != 1 || !j.runs[0].warm {
		t.Fatalf("the dev pass must warm exactly once, with its baseline: %+v", j.runs)
	}
	before := len(j.runs)
	// Every other door that scores on the same jail.
	_, _ = s.ScoreAuthoredReport(ctx, "p/p.go", rs.Code, "package p // model wrote this\n", warmMutant, "go test ./...")
	_, _ = s.ScoreAuthoredDeselected(ctx, "p/p.go", rs.Code, "package p // model wrote this\n", warmMutant, "go test ./...", "go test ./...", []string{"TestX"})
	_, _ = s.ScoreReport(ctx, "p/p.go", rs.Code, "", warmMutant, "go test ./...")
	_, _ = s.ScoreReportFor(ctx, "p/p.go", rs.Code, "", warmMutant, "go test ./...", func(adequacy.Mutant) adequacy.MutantCommand {
		return adequacy.MutantCommand{Cmd: []string{"go", "test", "./..."}}
	})
	_, _, _ = s.Score(ctx, "p/p.go", rs.Code, "", warmMutant, "go test ./...")
	if len(j.runs) == before {
		t.Fatal("the other doors ran nothing")
	}
	if j.warms() != 1 {
		t.Errorf("a pass other than the dev pass warmed the cache: %+v", j.runs)
	}
}

// A dev-pass baseline command the check cannot clear (RunCacheUnreadable at
// that point) warms nothing at all.
func TestADevPassWhoseCommandCannotBeClearedWarmsNothing(t *testing.T) {
	j := &warmRecordingJail{}
	s := warmScorer(j, func([]string) bool { return false })
	rs := RunSpec{CodePath: "p/p.go", Code: "package p\nfunc One() int { return 1 }\n", DevTestCode: "package p\n", TestCmd: "go test -no-such-flag ./...", Lang: "go", DevTestPath: "p/p_test.go"}
	if _, _, err := scoreDevReport(context.Background(), s, rs, warmMutant); err != nil {
		t.Fatal(err)
	}
	if j.warms() != 0 || len(j.runs) == 0 {
		t.Errorf("want runs and no warming run, got %+v", j.runs)
	}
}

// The check gets the dev pass's exact baseline command, not the file's.
func TestTheWarmCheckSeesTheDevPassBaselineCommand(t *testing.T) {
	j := &warmRecordingJail{}
	var seen [][]string
	s := warmScorer(j, func(cmd []string) bool { seen = append(seen, slices.Clone(cmd)); return true })
	rs := RunSpec{CodePath: "p/p.go", Code: "package p\nfunc One() int { return 1 }\n", DevTestCode: "package p\n", TestCmd: "go test ./p/...", Lang: "go", DevTestPath: "p/p_test.go"}
	if _, _, err := scoreDevReport(context.Background(), s, rs, warmMutant); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || strings.Join(seen[0], " ") != "go test ./p/..." {
		t.Errorf("the check saw %v, want [go test ./p/...]", seen)
	}
}
