// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"slices"
	"testing"
)

// endsJail is a Jail that can hand back either end of a run's output: the
// head through RunTestVerbose, the tail through RunTestDetailed. It records
// which was asked for and the command it was handed.
type endsJail struct {
	asked []string
	cmds  [][]string
}

func (j *endsJail) RunTest(context.Context, map[string]string, []string) (bool, error) {
	return false, nil
}

func (j *endsJail) RunTestVerbose(_ context.Context, _ map[string]string, cmd []string) (bool, string, error) {
	j.asked, j.cmds = append(j.asked, "verbose"), append(j.cmds, slices.Clone(cmd))
	return false, "HEAD", nil
}

func (j *endsJail) RunTestDetailed(_ context.Context, _ map[string]string, cmd []string) (bool, []byte, error) {
	j.asked, j.cmds = append(j.asked, "detailed"), append(j.cmds, slices.Clone(cmd))
	return false, []byte("TAIL"), nil
}

// headOnlyJail offers no tail-keeping run.
type headOnlyJail struct{ asked int }

func (j *headOnlyJail) RunTest(context.Context, map[string]string, []string) (bool, error) {
	return false, nil
}

func (j *headOnlyJail) RunTestVerbose(context.Context, map[string]string, []string) (bool, string, error) {
	j.asked++
	return false, "HEAD", nil
}

// The clean-code run of an authored test hands back the END of its output
// wherever the jail can keep one: that is where a runner names what failed,
// which salvage reads and the test writer is shown. One run serves both; the
// command still passes through the rewrite. A jail with no tail-keeping run
// keeps handing back what it always did.
func TestCompliantRunReadsTheEndOfTheRunWhereTheJailKeepsOne(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}

	both := &endsJail{}
	s := JailScorer{Jail: both, BaseFiles: base, CommandRewrite: markRewrite}
	pass, out, err := s.CompliantRun(ctx, "p.go", "package p\n", "package p\n", "go test ./...")
	if err != nil || pass || out != "TAIL" {
		t.Errorf("CompliantRun = pass %v, %q, %v; want the failing run's tail", pass, out, err)
	}
	if !slices.Equal(both.asked, []string{"detailed"}) {
		t.Errorf("the jail was asked %v, want one tail-keeping run and no second run", both.asked)
	}
	if len(both.cmds) != 1 || both.cmds[0][len(both.cmds[0])-1] != "--rewritten" {
		t.Errorf("the command reached the jail unrewritten: %v", both.cmds)
	}
	if got := s.CompliantFailure(ctx, "p.go", "package p\n", "package p\n", "go test ./..."); got != "TAIL" {
		t.Errorf("CompliantFailure = %q, want the same tail the salvage reader gets", got)
	}

	head := &headOnlyJail{}
	pass, out, err = JailScorer{Jail: head, BaseFiles: base}.CompliantRun(ctx, "p.go", "package p\n", "package p\n", "go test ./...")
	if err != nil || pass || out != "HEAD" || head.asked != 1 {
		t.Errorf("a jail with no tail-keeping run: pass %v, %q, %v after %d runs; want its own output, once", pass, out, err, head.asked)
	}
}
