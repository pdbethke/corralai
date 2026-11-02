// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// confirmCode is the compliant file every confirmation test grades, and
// confirmMutantCode the one mutant edit; confirmSuite is the shared grading
// command and confirmK the narrowed command the test confirmer builds for the
// test the failing run blamed.
const (
	confirmCode       = "ORIGINAL\n"
	confirmMutantCode = "MUTANT\n"
	confirmKilledBy   = "a.py::test_x"
)

var (
	confirmSuite = []string{"suite"}
	confirmK     = []string{"k", confirmKilledBy}
)

// testConfirmer stands in for the runner row's KillerCmd: it narrows any base
// command to the one test named, and is distinguishable in the log by its
// first word.
func testConfirmer(base []string, killedBy string) ([]string, bool) {
	if killedBy == "" {
		return nil, false
	}
	return []string{"k", killedBy}, true
}

// scriptedResult is what one scripted run answers.
type scriptedResult string

const (
	rPass    scriptedResult = "pass"
	rFail    scriptedResult = "fail"
	rTimeout scriptedResult = "timeout"
)

type scriptedCall struct {
	code string
	cmd  string
}

// scriptedJail answers each run from a script keyed by the code under audit
// plus the joined command. A key holds a SEQUENCE: each run consumes the next
// answer and the last one repeats, which is how a first run can fail and its
// repeat pass. Every call is logged. An unscripted run fails the test, so a
// confirmation run nobody expected cannot pass silently.
//
// It implements only Jail; detailedScriptedJail adds the DetailedJail seam.
type scriptedJail struct {
	t      *testing.T
	mu     sync.Mutex
	script map[string][]scriptedResult
	log    []scriptedCall
	// onCall, when set, runs before the answer is read, outside the lock.
	onCall func(code string, cmd []string)
}

func scriptKey(code string, cmd []string) string { return code + "|" + strings.Join(cmd, " ") }

func newScriptedJail(t *testing.T, script map[string][]scriptedResult) *scriptedJail {
	return &scriptedJail{t: t, script: script}
}

func (j *scriptedJail) answer(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	if j.onCall != nil {
		j.onCall(code, cmd)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, scriptedCall{code: code, cmd: strings.Join(cmd, " ")})
	key := scriptKey(code, cmd)
	seq, ok := j.script[key]
	if !ok || len(seq) == 0 {
		j.t.Errorf("unscripted run: %q", key)
		return false, errors.New("unscripted run")
	}
	r := seq[0]
	if len(seq) > 1 {
		j.script[key] = seq[1:]
	}
	switch r {
	case rPass:
		return true, nil
	case rFail:
		return false, nil
	default:
		return false, ErrTestTimeout
	}
}

func (j *scriptedJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return j.answer(ctx, files, cmd)
}

func (j *scriptedJail) calls() []scriptedCall {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]scriptedCall(nil), j.log...)
}

// detailedScriptedJail is scriptedJail with the DetailedJail seam: a failing
// run prints pytest's own short-summary line, so the failure parser names
// a.py::test_x exactly as it would from a real run.
type detailedScriptedJail struct{ *scriptedJail }

func (j detailedScriptedJail) RunTestDetailed(ctx context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	ok, err := j.answer(ctx, files, cmd)
	if err == nil && !ok {
		return false, []byte("FAILED a.py::test_x - boom\n"), nil
	}
	return ok, nil, err
}

// baseScript is the baseline passing and the canary failing: the two runs
// every Score with mutants makes before any mutant is graded.
func baseScript(extra map[string][]scriptedResult) map[string][]scriptedResult {
	s := map[string][]scriptedResult{
		scriptKey(confirmCode, confirmSuite):   {rPass},
		scriptKey(CanaryCode, confirmSuite):    {rFail},
		scriptKey(confirmMutantCode, confirmK): {rFail},
	}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

func oneMutant() []Mutant { return []Mutant{{ID: "m", Replace: confirmMutantCode}} }

func hasID(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// A real kill, confirmed by the blamed test alone: it fails on the mutant
// and passes on the compliant code, in exactly two runs after the grading run.
func TestConfirmAloneConfirmsARealKill(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail},
		scriptKey(confirmCode, confirmK):           {rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want m", rep.Killed)
	}
	g := rep.PerMutant["m"]
	if g.KillConfirmation != KillConfirmedAlone {
		t.Errorf("KillConfirmation = %q, want %q", g.KillConfirmation, KillConfirmedAlone)
	}
	if g.KilledBy != confirmKilledBy {
		t.Errorf("KilledBy = %q, want %q", g.KilledBy, confirmKilledBy)
	}
	calls := j.calls()
	want := []scriptedCall{
		{confirmCode, "suite"}, {CanaryCode, "suite"}, {confirmMutantCode, "suite"},
		{confirmMutantCode, "k a.py::test_x"}, {confirmCode, "k a.py::test_x"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %v, want %v", i, calls[i], want[i])
		}
	}
}

// THE t14 SHAPE: the suite fails once on the mutant, the blamed test passes
// alone on it, and the repeated suite run passes. The first failure was not
// the mutant's, so it is a survivor that remembers whom the first run blamed.
func TestConfirmRefutesAFalseKill(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rPass},
		scriptKey(confirmMutantCode, confirmK):     {rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Survived, "m") || len(rep.Killed) != 0 {
		t.Fatalf("Killed=%v Survived=%v, want m survived", rep.Killed, rep.Survived)
	}
	g := rep.PerMutant["m"]
	if g.UnrepeatedKilledBy != confirmKilledBy {
		t.Errorf("UnrepeatedKilledBy = %q, want %q", g.UnrepeatedKilledBy, confirmKilledBy)
	}
	if g.KilledBy != "" || g.KillConfirmation != "" {
		t.Errorf("KilledBy=%q KillConfirmation=%q, want both empty on a survivor", g.KilledBy, g.KillConfirmation)
	}
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1 — a refuted kill is an ordinary survivor", rep.Total)
	}
}

// The blamed test passes alone (it leans on a sibling), but the suite fails
// on the mutant twice and passes on compliant code: killed, by repeat.
func TestConfirmRepeatedKill(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmMutantCode, confirmK):     {rPass},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want m", rep.Killed)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != KillConfirmedRepeated || g.KilledBy != confirmKilledBy {
		t.Errorf("KillConfirmation=%q KilledBy=%q, want %q / %q", g.KillConfirmation, g.KilledBy, KillConfirmedRepeated, confirmKilledBy)
	}
}

// Step A did not confirm, the repeat fails, and the compliant code fails the
// same command on its re-run: the suite is not stable, so the mutant is
// unmeasured — out of Total, never a kill and never a survivor.
func TestConfirmUnstableSuiteIsUnmeasured(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmCode, confirmK):           {rFail},
		scriptKey(confirmCode, confirmSuite):       {rPass, rFail},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Unmeasured, "m") || len(rep.Killed)+len(rep.Survived) != 0 {
		t.Fatalf("Killed=%v Survived=%v Unmeasured=%v, want m unmeasured", rep.Killed, rep.Survived, rep.Unmeasured)
	}
	want := "kill not confirmed: the grading command also failed on the unmutated code when re-run alone, so the suite is not stable enough for this mutant's failure to be evidence (command: 'suite')"
	if got := rep.UnmeasuredReasons["m"]; got != want {
		t.Errorf("reason = %q\nwant      %q", got, want)
	}
	if rep.Total != 0 {
		t.Errorf("Total = %d, want 0", rep.Total)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != "" || g.UnrepeatedKilledBy != "" {
		t.Errorf("KillConfirmation=%q UnrepeatedKilledBy=%q, want both empty on an unmeasured mutant", g.KillConfirmation, g.UnrepeatedKilledBy)
	}
}

// No confirmer, no detailed output: there is no "off" — the kill is
// confirmed by repeating, in exactly two extra runs.
func TestConfirmWithoutAConfirmerRepeats(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want m", rep.Killed)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != KillConfirmedRepeated {
		t.Errorf("KillConfirmation = %q, want %q", g.KillConfirmation, KillConfirmedRepeated)
	}
	// baseline, canary, grading, then the repeat on the mutant and the
	// compliant re-run.
	if n := len(j.calls()); n != 5 {
		t.Errorf("runs = %d (%v), want 5", n, j.calls())
	}
}

// A narrowed command that runs no test exits 0 — a zero-test vitest run
// does. That is not the test passing alone, and must never become `alone`.
func TestConfirmZeroTestRunIsNotConfirmation(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmMutantCode, confirmK):     {rPass},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != KillConfirmedRepeated {
		t.Errorf("KillConfirmation = %q, want %q", g.KillConfirmation, KillConfirmedRepeated)
	}
	for _, c := range j.calls() {
		if c.code == confirmCode && c.cmd == "k a.py::test_x" {
			t.Errorf("the compliant step-A run happened after the mutant step-A run passed: %v", j.calls())
		}
	}
}

// A timed-out step-A run is "not confirmed", never a kill: it goes to the
// repeat. A timed-out repeat on the mutant counts as failing, so the
// compliant re-run decides.
func TestConfirmTimeoutsGoToTheNextStep(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rTimeout},
		scriptKey(confirmMutantCode, confirmK):     {rTimeout},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if g := rep.PerMutant["m"]; !hasID(rep.Killed, "m") || g.KillConfirmation != KillConfirmedRepeated {
		t.Errorf("Killed=%v KillConfirmation=%q, want m killed %q", rep.Killed, g.KillConfirmation, KillConfirmedRepeated)
	}
}

// A compliant re-run that times out did not pass, so nothing is inferred:
// unmeasured, as for a compliant re-run that fails.
func TestConfirmCompliantTimeoutIsUnmeasured(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmCode, confirmSuite):       {rPass, rTimeout},
	}))
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Unmeasured, "m") || !strings.HasPrefix(rep.UnmeasuredReasons["m"], "kill not confirmed:") {
		t.Errorf("Unmeasured=%v reason=%q, want m unmeasured with a kill-not-confirmed reason", rep.Unmeasured, rep.UnmeasuredReasons["m"])
	}
}

// CONFIRMATION IS SERIAL. Four candidates graded four at a time; no
// confirmation run may overlap another run of any kind, or the contention
// that produced the false failure could produce it again on the repeat.
func TestConfirmRunsSerially(t *testing.T) {
	script := map[string][]scriptedResult{
		scriptKey(confirmCode, confirmSuite): {rPass},
		scriptKey(CanaryCode, confirmSuite):  {rFail},
	}
	var mutants []Mutant
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		code := "MUTANT " + id + "\n"
		mutants = append(mutants, Mutant{ID: id, Replace: code})
		script[scriptKey(code, confirmSuite)] = []scriptedResult{rFail, rFail}
	}
	j := newScriptedJail(t, script)
	var (
		mu                sync.Mutex
		inFlight          int
		seen              = map[string]int{}
		maxDuringConfirm  int
		confirmationCalls int
	)
	j.onCall = func(code string, cmd []string) {
		mu.Lock()
		key := scriptKey(code, cmd)
		seen[key]++
		// A run of a mutant's suite command seen before is its repeat; a
		// compliant suite run after the baseline is a confirmation's.
		confirming := code != CanaryCode && seen[key] > 1
		inFlight++
		if confirming {
			confirmationCalls++
			if inFlight > maxDuringConfirm {
				maxDuringConfirm = inFlight
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		if confirming && inFlight > maxDuringConfirm {
			maxDuringConfirm = inFlight
		}
		inFlight--
		mu.Unlock()
	}
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, mutants, confirmSuite, WithConcurrency(4))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Killed) != 4 {
		t.Fatalf("Killed = %v, want all four", rep.Killed)
	}
	if confirmationCalls != 8 {
		t.Errorf("confirmation runs = %d, want 8 (a repeat and a compliant re-run per candidate)", confirmationCalls)
	}
	if maxDuringConfirm != 1 {
		t.Errorf("max runs in flight during confirmation = %d, want 1", maxDuringConfirm)
	}
}

// Duplicates inherit the CONFIRMED outcome: the confirmation runs once, for
// the representative, and the twin is a survivor with the same footnote.
func TestDuplicateInheritsConfirmation(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rPass},
		scriptKey(confirmMutantCode, confirmK):     {rPass},
	}))
	mutants := []Mutant{{ID: "m1", Replace: confirmMutantCode}, {ID: "m2", Replace: confirmMutantCode}}
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, mutants, confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Survived, "m1") || !hasID(rep.Survived, "m2") {
		t.Fatalf("Survived = %v, want m1 and m2", rep.Survived)
	}
	for _, id := range []string{"m1", "m2"} {
		if got := rep.PerMutant[id].UnrepeatedKilledBy; got != confirmKilledBy {
			t.Errorf("%s UnrepeatedKilledBy = %q, want %q", id, got, confirmKilledBy)
		}
	}
	k, repeat := 0, 0
	for _, c := range j.calls() {
		if c.code == confirmMutantCode && c.cmd == "k a.py::test_x" {
			k++
		}
		if c.code == confirmMutantCode && c.cmd == "suite" {
			repeat++
		}
	}
	if k != 1 || repeat != 2 {
		t.Errorf("step-A runs = %d, mutant suite runs = %d; want 1 and 2 (grading + one repeat)", k, repeat)
	}
}

// A cancelled run during confirmation fails the report, as a mutant that
// could not be run always has: an unconfirmed candidate is not a verdict.
func TestConfirmCancelledFailsTheReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rFail, rFail},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	grading := 0
	j.onCall = func(code string, cmd []string) {
		if code == confirmMutantCode {
			grading++
			if grading == 2 { // the first confirmation run
				cancel()
			}
		}
	}
	rep, err := Score(ctx, j, nil, "a.py", confirmCode, oneMutant(), confirmSuite)
	if err == nil {
		t.Fatalf("Score returned no error; report %+v", rep)
	}
	if rep.Total != 0 || rep.Killed != nil || rep.PerMutant != nil || rep.CompliantPass {
		t.Errorf("report = %+v, want Report{}", rep)
	}
}

// A survivor was never a candidate: no confirmation runs, no new fields.
func TestSurvivorIsNeverConfirmed(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rPass},
	}))
	rep, err := Score(context.Background(), detailedScriptedJail{j}, nil, "a.py", confirmCode, oneMutant(), confirmSuite,
		WithFailureParser(pythonFailureParser(t)), WithKillConfirmer(testConfirmer))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Survived, "m") {
		t.Fatalf("Survived = %v, want m", rep.Survived)
	}
	if n := len(j.calls()); n != 3 {
		t.Errorf("runs = %d (%v), want 3 — no confirmation for a survivor", n, j.calls())
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != "" || g.UnrepeatedKilledBy != "" {
		t.Errorf("KillConfirmation=%q UnrepeatedKilledBy=%q, want both empty", g.KillConfirmation, g.UnrepeatedKilledBy)
	}
}

// A timeout kill already carries its own confirmation (the compliant
// re-probe); it is not confirmed again and its KillConfirmation stays "".
func TestTimeoutKillIsNotReconfirmed(t *testing.T) {
	j := newScriptedJail(t, baseScript(map[string][]scriptedResult{
		scriptKey(confirmMutantCode, confirmSuite): {rTimeout},
		scriptKey(confirmCode, confirmSuite):       {rPass, rPass},
	}))
	rep, err := Score(context.Background(), j, nil, "a.py", confirmCode, oneMutant(), confirmSuite)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want m", rep.Killed)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation != "" {
		t.Errorf("KillConfirmation = %q, want empty on a timeout kill", g.KillConfirmation)
	}
	// baseline, canary, grading (timed out), compliant re-probe.
	if n := len(j.calls()); n != 4 {
		t.Errorf("runs = %d (%v), want 4", n, j.calls())
	}
}
