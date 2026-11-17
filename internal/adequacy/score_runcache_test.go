// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// rcCall is one run a rcRecJail saw: which jail method, the code under
// audit and the argv it was handed.
type rcCall struct {
	method string
	code   string
	argv   string
}

// rcRecJail records every run. It answers from the same script as
// scriptedJail but implements Jail and DetailedJail only; runCacheJail adds
// the RunCacheJail seam.
type rcRecJail struct {
	*scriptedJail
	mu2   sync.Mutex
	calls []rcCall
}

func (j *rcRecJail) record(method string, files map[string]string, cmd []string) {
	j.mu2.Lock()
	defer j.mu2.Unlock()
	j.calls = append(j.calls, rcCall{method, files["a.py"], strings.Join(cmd, " ")})
}

func (j *rcRecJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	j.record("RunTest", files, cmd)
	return j.answer(ctx, files, cmd)
}

func (j *rcRecJail) RunTestDetailed(ctx context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	j.record("RunTestDetailed", files, cmd)
	ok, err := j.answer(ctx, files, cmd)
	if err == nil && !ok {
		return false, []byte("FAILED a.py::test_x - boom\n"), nil
	}
	return ok, nil, err
}

type runCacheJail struct {
	*rcRecJail
	cache sandbox.SharedCache
}

func (j runCacheJail) WarmRun(ctx context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	j.record("WarmRun", files, cmd)
	ok, err := j.answer(ctx, files, cmd)
	if err == nil && !ok {
		return false, []byte("baseline failed loudly\n"), nil
	}
	return ok, nil, err
}
func (j runCacheJail) RunCache() sandbox.SharedCache { return j.cache }
func (j runCacheJail) Close() error                  { return nil }

func newRC(t *testing.T, script map[string][]scriptedResult) runCacheJail {
	return runCacheJail{
		rcRecJail: &rcRecJail{scriptedJail: newScriptedJail(t, script)},
		cache:     sandbox.SharedCache{Target: "/cache"},
	}
}

func (j runCacheJail) seen() []rcCall {
	j.mu2.Lock()
	defer j.mu2.Unlock()
	return append([]rcCall(nil), j.calls...)
}

func TestScoreWarmsTheRunCacheWithTheBaselineOnly(t *testing.T) {
	j := newRC(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail},
		scriptKey(confirmCode, confirmK):           {rPass},
	}))
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer), WithWarmBaseline())
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	var warm, other int
	for i, c := range j.seen() {
		if c.method == "WarmRun" {
			warm++
			if i != 0 || c.code != confirmCode || c.argv != "suite" {
				t.Errorf("WarmRun was call %d on %q %q, want the first, on the compliant code", i, c.code, c.argv)
			}
		} else {
			other++
		}
	}
	if warm != 1 || other < 3 {
		t.Errorf("WarmRun calls = %d, other = %d, want 1 and at least 3 (canary, grading, confirmations): %v", warm, other, j.seen())
	}
	if want := (sandbox.SharedCache{Target: "/cache"}); rep.RunCache != want {
		t.Errorf("Report.RunCache = %+v, want %+v", rep.RunCache, want)
	}
}

// Review Focus 1: a failing baseline is ONE WarmRun and no RunTest, and the
// report is the one a plain jail gives, with the run's output kept.
func TestScoreWithAFailingBaselineStillReportsAsBefore(t *testing.T) {
	script := map[string][]scriptedResult{scriptKey(confirmCode, confirmSuite): {rFail}}
	j := newRC(t, script)
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite, WithWarmBaseline())
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	calls := j.seen()
	if len(calls) != 1 || calls[0].method != "WarmRun" {
		t.Fatalf("calls = %v, want exactly one WarmRun", calls)
	}
	if rep.CompliantPass || len(rep.Killed)+len(rep.Survived) != 0 {
		t.Errorf("report = %+v, want a COULD-NOT-GRADE report", rep)
	}
	if rep.BaselineOutput != "baseline failed loudly\n" {
		t.Errorf("BaselineOutput = %q, want the warm run's output", rep.BaselineOutput)
	}
	plain := newScriptedJail(t, map[string][]scriptedResult{scriptKey(confirmCode, confirmSuite): {rFail}})
	prep, err := Score(context.Background(), plain, nil, "a.py", confirmCode, oneMutant(), confirmSuite)
	if err != nil {
		t.Fatalf("Score plain: %v", err)
	}
	if prep.CompliantPass != rep.CompliantPass || prep.Total != rep.Total || len(prep.Killed) != len(rep.Killed) {
		t.Errorf("plain report %+v differs from run-cache report %+v", prep, rep)
	}
}

func TestScoreRewritesEveryCommandItRuns(t *testing.T) {
	// Fail-fast, a narrowed killer and a repeat must all be rewritten: the
	// rewritten argv is the raw one plus X, with fail-fast args inside it.
	script := map[string][]scriptedResult{
		scriptKey(confirmCode, []string{"suite", "X"}):                    {rPass},
		scriptKey(CanaryCode, []string{"suite", "X"}):                     {rFail},
		scriptKey(confirmCode, []string{"suite", "--ff", "X"}):            {rPass},
		scriptKey(confirmMutantCode, []string{"suite", "--ff", "X"}):      {rFail},
		scriptKey(confirmMutantCode, []string{"k", confirmKilledBy, "X"}): {rFail},
		scriptKey(confirmCode, []string{"k", confirmKilledBy, "X"}):       {rPass},
	}
	var seenArgs [][]string
	var mu sync.Mutex
	caller := []string{"suite"}
	j := newRC(t, script)
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), caller,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer),
		WithMutantFailFast(func(cmd []string) ([]string, bool) { return []string{"--ff"}, true }),
		WithCommandRewrite(func(cmd []string) []string {
			mu.Lock()
			seenArgs = append(seenArgs, cmd)
			mu.Unlock()
			return append(cmd, "X")
		}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want m", rep.Killed)
	}
	calls := j.seen()
	// baseline, fail-fast probe, canary, grading, confirm alone (x2).
	if len(calls) != 6 {
		t.Fatalf("jail saw %d runs, want 6: %v", len(calls), calls)
	}
	for _, c := range calls {
		if !strings.HasSuffix(c.argv, " X") {
			t.Errorf("run %v reached the jail unrewritten", c)
		}
	}
	if len(seenArgs) != len(calls) {
		t.Errorf("rewrite called %d times for %d runs", len(seenArgs), len(calls))
	}
	if !reflect.DeepEqual(caller, []string{"suite"}) || len(caller[:cap(caller)]) != 1 {
		t.Errorf("the caller's command was changed: %v", caller)
	}
	// Never the same backing array twice.
	for i := range seenArgs {
		for k := i + 1; k < len(seenArgs); k++ {
			if len(seenArgs[i]) > 0 && len(seenArgs[k]) > 0 && &seenArgs[i][0] == &seenArgs[k][0] {
				t.Errorf("rewrite saw one slice twice (calls %d and %d)", i, k)
			}
		}
	}
}

// The repeated confirmation (no confirmer) is rewritten too.
func TestScoreRewritesARepeatedConfirmation(t *testing.T) {
	script := map[string][]scriptedResult{
		scriptKey(confirmCode, []string{"suite", "X"}):       {rPass},
		scriptKey(CanaryCode, []string{"suite", "X"}):        {rFail},
		scriptKey(confirmMutantCode, []string{"suite", "X"}): {rFail},
	}
	j := newRC(t, script)
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithCommandRewrite(func(cmd []string) []string { return append(cmd, "X") }))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v", rep.Killed)
	}
	n := 0
	for _, c := range j.seen() {
		n++
		if !strings.HasSuffix(c.argv, " X") {
			t.Errorf("run %v reached the jail unrewritten", c)
		}
	}
	if n < 4 {
		t.Errorf("only %d runs, want baseline, canary, grading and a repeat", n)
	}
}

func TestScoreIsUnchangedForAJailWithoutARunCache(t *testing.T) {
	mk := func() *rcRecJail {
		return &rcRecJail{scriptedJail: newScriptedJail(t, baseScript(map[string][]scriptedResult{
			scriptKey(confirmMutantCode, confirmSuite): {rFail},
			scriptKey(confirmCode, confirmK):           {rPass},
		}))}
	}
	a, b := mk(), mk()
	opts := []ScoreOption{WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer)}
	ra, err := Score(context.Background(), a, nil, "a.py", confirmCode, oneMutant(), confirmSuite, opts...)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Score(context.Background(), b, nil, "a.py", confirmCode, oneMutant(), confirmSuite, append(opts, WithCommandRewrite(nil))...)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.calls, b.calls) {
		t.Errorf("calls differ:\n%v\n%v", a.calls, b.calls)
	}
	for _, c := range a.calls {
		if c.method == "WarmRun" {
			t.Errorf("a plain jail saw %v", c)
		}
	}
	if ra.RunCache != (sandbox.SharedCache{}) || rb.RunCache != (sandbox.SharedCache{}) {
		t.Errorf("RunCache = %+v / %+v, want zero", ra.RunCache, rb.RunCache)
	}
	// The baseline stays on the detailed run it used before this task.
	if a.calls[0].method != "RunTestDetailed" {
		t.Errorf("first call = %v, want RunTestDetailed", a.calls[0])
	}
}

// Score never warms on its own: a RunCacheJail asked for no warm baseline
// gets zero WarmRun calls, and its baseline is a frozen detailed run, as for
// a jail with no run cache. The warming run writes what every later run
// reads, so only a caller that knows the tree is the project's own asks.
func TestScoreWithoutWarmBaselineNeverWarms(t *testing.T) {
	j := newRC(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail},
		scriptKey(confirmCode, confirmK):           {rPass},
	}))
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range j.seen() {
		if c.method == "WarmRun" {
			t.Errorf("a Score call that did not ask to warm made a WarmRun: %v", j.seen())
		}
	}
	if first := j.seen()[0]; first.method != "RunTestDetailed" {
		t.Errorf("baseline was %v, want a frozen RunTestDetailed", first)
	}
	if want := (sandbox.SharedCache{Target: "/cache"}); rep.RunCache != want {
		t.Errorf("Report.RunCache = %+v, want %+v (still read after the baseline)", rep.RunCache, want)
	}
}
