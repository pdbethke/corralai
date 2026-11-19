// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"slices"
	"testing"
)

// endsJail is a Jail that offers every output-returning run: the head
// through RunTestVerbose, the tail through RunTestDetailed, both ends through
// RunTestEnds. It records which was asked for and the command it was handed.
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

func (j *endsJail) RunTestEnds(_ context.Context, _ map[string]string, cmd []string) (bool, string, error) {
	j.asked, j.cmds = append(j.asked, "ends"), append(j.cmds, slices.Clone(cmd))
	return false, "BOTH ENDS", nil
}

// headOnlyJail offers no run that keeps the end of its output.
type headOnlyJail struct{ asked int }

func (j *headOnlyJail) RunTest(context.Context, map[string]string, []string) (bool, error) {
	return false, nil
}

func (j *headOnlyJail) RunTestVerbose(context.Context, map[string]string, []string) (bool, string, error) {
	j.asked++
	return false, "HEAD", nil
}

// The clean-code run of an authored test hands back BOTH ends of its output
// wherever the jail can keep them: a runner names some failures last (which
// salvage reads) and some first (a hung Go test, which the writer needs).
// One run serves every reader; the command still passes through the
// rewrite. A jail with no such run keeps handing back what it always did.
func TestCompliantRunReadsBothEndsOfTheRunWhereTheJailKeepsThem(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{"go.mod": "module m\n", "p.go": "package p\n"}

	both := &endsJail{}
	s := JailScorer{Jail: both, BaseFiles: base, CommandRewrite: markRewrite}
	pass, out, err := s.CompliantRun(ctx, "p.go", "package p\n", "package p\n", "go test ./...")
	if err != nil || pass || out != "BOTH ENDS" {
		t.Errorf("CompliantRun = pass %v, %q, %v; want the failing run's two ends", pass, out, err)
	}
	if !slices.Equal(both.asked, []string{"ends"}) {
		t.Errorf("the jail was asked %v, want one two-ends run and no second run", both.asked)
	}
	if len(both.cmds) != 1 || both.cmds[0][len(both.cmds[0])-1] != "--rewritten" {
		t.Errorf("the command reached the jail unrewritten: %v", both.cmds)
	}
	if got := s.CompliantFailure(ctx, "p.go", "package p\n", "package p\n", "go test ./..."); got != "BOTH ENDS" {
		t.Errorf("CompliantFailure = %q, want the same capture the salvage reader gets", got)
	}

	head := &headOnlyJail{}
	pass, out, err = JailScorer{Jail: head, BaseFiles: base}.CompliantRun(ctx, "p.go", "package p\n", "package p\n", "go test ./...")
	if err != nil || pass || out != "HEAD" || head.asked != 1 {
		t.Errorf("a jail with no two-ends run: pass %v, %q, %v after %d runs; want its own output, once", pass, out, err, head.asked)
	}
}
