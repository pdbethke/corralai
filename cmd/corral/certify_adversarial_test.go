// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
)

func TestAdvVerdictDecodesToolPayload(t *testing.T) {
	// Exactly what get_adversarial_run marshals (advpool.Verdict has no json
	// tags -> capitalized keys; VacuousFindings elements use queue.Finding's
	// lowercase tags).
	payload := `{
	  "run_id": 7, "found": true, "converged": true,
	  "verdict": {
	    "Repo": "pdbethke/corralai", "Commit": "88b6ff7",
	    "DevKillRate": 0.5, "MutantsTotal": 8, "Survivors": 4, "ProvenMissed": 2,
	    "VacuousFindings": [
	      {"type": "note", "severity": "high", "target": "TestValidatePassword",
	       "evidence": "calls ValidatePassword without checking its input"}
	    ],
	    "ModelsByRole": {"test-writer": "qwen2.5-coder:7b", "test-critic": "llama3.2:3b"},
	    "Status": "needs-review", "RecordID": 41, "RecordHead": "head41"
	  }
	}`
	var st advStatus
	if err := json.Unmarshal([]byte(payload), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.Converged || st.Verdict == nil {
		t.Fatalf("converged=%v verdict=%v", st.Converged, st.Verdict)
	}
	v := st.Verdict
	if v.DevKillRate != 0.5 || v.MutantsTotal != 8 || v.Survivors != 4 || v.ProvenMissed != 2 {
		t.Fatalf("numbers wrong: %+v", v)
	}
	if v.Status != "needs-review" || v.RecordID != 41 || v.RecordHead != "head41" {
		t.Fatalf("status/record wrong: %+v", v)
	}
	if len(v.VacuousFindings) != 1 || v.VacuousFindings[0].Target != "TestValidatePassword" {
		t.Fatalf("findings wrong: %+v", v.VacuousFindings)
	}
	if v.ModelsByRole["test-writer"] != "qwen2.5-coder:7b" {
		t.Fatalf("models wrong: %+v", v.ModelsByRole)
	}
}

// TestAdvVerdictWireRoundTripCarriesCoverage is the test that would have
// caught the Critical: the brain marshals a *live* advpool.Verdict struct
// with encoding/json (advStatus{Verdict: &v}), and advpool.Verdict has NO
// json tags of its own — so its wire keys are whatever Go's default
// marshaling produces for its exported field names, i.e. capitalized
// ("RegionsTotal", not "regions_total"). advVerdict's json tags exist ONLY
// to match that default output byte-for-byte. A prior change gave the three
// coverage tags (RegionsTotal/RegionsProbed/DroppedRegions) snake_case
// values; nothing here decoded a REAL marshaled Verdict, so the mismatch
// shipped and a partial audit silently decoded as zeros. Do NOT "tidy" these
// three json tags to snake_case — they must stay capitalized to match the
// brain's actual wire format, not idiomatic Go JSON style.
func TestAdvVerdictWireRoundTripCarriesCoverage(t *testing.T) {
	src := advpool.Verdict{
		Repo: "pdbethke/corralai", Commit: "88b6ff7", Lang: "go",
		DevKillRate: 0.5, MutantsTotal: 8, Survivors: 4, ProvenMissed: 2,
		RegionsTotal:   5,
		RegionsProbed:  3,
		DroppedRegions: []string{"parseConfig", "renderReport"},
		Status:         "needs-review", RecordID: 41, RecordHead: "head41",
	}
	// Mirror the brain's actual wire shape: get_adversarial_run's output
	// embeds *advpool.Verdict directly and marshals with encoding/json.
	wire := struct {
		RunID     int64            `json:"run_id"`
		Found     bool             `json:"found"`
		Converged bool             `json:"converged"`
		Verdict   *advpool.Verdict `json:"verdict"`
	}{RunID: 7, Found: true, Converged: true, Verdict: &src}

	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var st advStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Verdict == nil {
		t.Fatal("decoded verdict is nil")
	}
	v := st.Verdict
	if v.RegionsTotal != 5 {
		t.Errorf("RegionsTotal = %d, want 5 (json tags must be capitalized to match advpool.Verdict's default marshaling)", v.RegionsTotal)
	}
	if v.RegionsProbed != 3 {
		t.Errorf("RegionsProbed = %d, want 3", v.RegionsProbed)
	}
	if len(v.DroppedRegions) != 2 || v.DroppedRegions[0] != "parseConfig" {
		t.Errorf("DroppedRegions = %v, want [parseConfig renderReport]", v.DroppedRegions)
	}
}

// TestRenderAdvVerdictPrintsPartialAudit proves renderAdvVerdict surfaces the
// coverage shortfall: a PARTIAL AUDIT line with the exact probed/total counts
// and dropped-region names when RegionsProbed < RegionsTotal, and its
// complete absence when coverage is full. A deleted print block here would
// silently swallow the shortfall the signed statement still carries.
func TestRenderAdvVerdictPrintsPartialAudit(t *testing.T) {
	partial := advVerdict{
		Repo: "pdbethke/corralai", Commit: "88b6ff7deadbeef",
		DevKillRate: 0.5, MutantsTotal: 8, Survivors: 4, ProvenMissed: 2,
		RegionsTotal: 5, RegionsProbed: 3, DroppedRegions: []string{"parseConfig", "renderReport"},
		Status: "needs-review",
	}
	var buf bytes.Buffer
	renderAdvVerdict(&buf, "fence.go", partial)
	s := buf.String()
	if !strings.Contains(s, "PARTIAL AUDIT: 3 of 5 regions probed") {
		t.Fatalf("missing PARTIAL AUDIT summary:\n%s", s)
	}
	if !strings.Contains(s, "parseConfig; renderReport") {
		t.Fatalf("missing dropped-region names:\n%s", s)
	}

	full := partial
	full.RegionsTotal = 3
	full.RegionsProbed = 3
	full.DroppedRegions = nil
	buf.Reset()
	renderAdvVerdict(&buf, "fence.go", full)
	if strings.Contains(buf.String(), "PARTIAL AUDIT") {
		t.Fatalf("full coverage must not print PARTIAL AUDIT:\n%s", buf.String())
	}
}

// fakeAdvClient scripts StartRun + a sequence of RunStatus results.
type fakeAdvClient struct {
	startErr   error
	runID      int64
	spec       advStartSpec // captured
	statuses   []advStatus  // returned in order; last one repeats
	statusErr  error
	statusCall int
}

func (f *fakeAdvClient) StartRun(_ context.Context, _ string, spec advStartSpec) (int64, error) {
	f.spec = spec
	if f.startErr != nil {
		return 0, f.startErr
	}
	return f.runID, nil
}
func (f *fakeAdvClient) RunStatus(_ context.Context, _ string, _ int64) (advStatus, error) {
	if f.statusErr != nil {
		return advStatus{}, f.statusErr
	}
	i := f.statusCall
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	f.statusCall++
	return f.statuses[i], nil
}

func noSleep(time.Duration) {}

// gitStubRunner satisfies cmdRunner returning canned git context; RunCommand
// is unused by the adversarial path.
type gitStubRunner struct{}

func (gitStubRunner) GitOutput(args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "config --get remote.origin.url":
		return "pdbethke/corralai", nil
	case "rev-parse HEAD":
		return "88b6ff7", nil
	}
	return "", nil
}
func (gitStubRunner) GitVerifyCommit(string) (string, bool, error) { return "", false, nil }
func (gitStubRunner) RunCommand([]string, io.Writer, io.Writer) (int, time.Duration, []byte, error) {
	return 0, 0, nil, nil
}

func certifiedStatus() advStatus {
	return advStatus{RunID: 7, Found: true, Converged: true, Verdict: &advVerdict{
		Repo: "pdbethke/corralai", Commit: "88b6ff7", DevKillRate: 1.0,
		MutantsTotal: 6, Survivors: 0, ProvenMissed: 0,
		ModelsByRole: map[string]string{"mutant-generator": "qwen2.5-coder:7b", "test-writer": "qwen2.5-coder:7b", "test-critic": "llama3.2:3b"},
		Status:       "certified", RecordID: 41, RecordHead: "head41",
	}}
}

func writeTmpFiles(t *testing.T) (code, test string) {
	t.Helper()
	dir := t.TempDir()
	code = dir + "/fence.go"
	test = dir + "/fence_test.go"
	if err := os.WriteFile(code, []byte("package fence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(test, []byte("package fence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return code, test
}

func TestAdversarialCertifiedExitsZero(t *testing.T) {
	code, _ := writeTmpFiles(t) // sibling _test.go exists in the same dir
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "neutralize the fence", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", rc, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "CERTIFIED") || !strings.Contains(s, "record 41") {
		t.Fatalf("render missing headline/record:\n%s", s)
	}
	// --test defaulted to the sibling and both files were sent.
	if f.spec.DevTestPath == "" || f.spec.Code == "" || f.spec.DevTestCode == "" {
		t.Fatalf("spec not fully populated: %+v", f.spec)
	}
	if f.spec.TestCmd != "go test ./..." {
		t.Fatalf("TestCmd = %q, want 'go test ./...'", f.spec.TestCmd)
	}
}

func TestAdversarialNeedsReviewExitsThree(t *testing.T) {
	code, _ := writeTmpFiles(t)
	nr := certifiedStatus()
	nr.Verdict.Status = "needs-review"
	nr.Verdict.DevKillRate = 0.5
	nr.Verdict.MutantsTotal = 8
	nr.Verdict.Survivors = 4
	nr.Verdict.ProvenMissed = 2
	nr.Verdict.VacuousFindings = []advFinding{{Type: "note", Severity: "high", Target: "TestValidatePassword", Evidence: "calls ValidatePassword without checking its input"}}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{nr}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 3 {
		t.Fatalf("exit = %d, want 3", rc)
	}
	s := out.String()
	if strings.Contains(s, "CERTIFIED") {
		t.Fatalf("needs-review must NOT print CERTIFIED:\n%s", s)
	}
	if !strings.Contains(s, "NEEDS-REVIEW") || !strings.Contains(s, "TestValidatePassword") {
		t.Fatalf("render missing needs-review status or the pan:\n%s", s)
	}
}

func TestAdversarialHandsBackAuthoredTest(t *testing.T) {
	// The sharing payoff: when the pool authored a killing test for a gap the
	// dev suite missed, `corral certify --adversarial` prints it so the dev can
	// adopt it — with a hand-back message naming the test file to add it to.
	code, testPath := writeTmpFiles(t)
	nr := certifiedStatus()
	nr.Verdict.Status = "needs-review"
	nr.Verdict.ProvenMissed = 1
	nr.AuthoredTest = "func TestNeutralizesSentinel(t *testing.T) {\n\tif got := F(); got { t.Fatal(\"gap\") }\n}\n"
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{nr}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--test", testPath, "--goal", "g", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 3 {
		t.Fatalf("exit = %d, want 3", rc)
	}
	s := out.String()
	if !strings.Contains(s, "authored a test that catches a gap") {
		t.Fatalf("missing the hand-back message:\n%s", s)
	}
	if !strings.Contains(s, "TestNeutralizesSentinel") {
		t.Fatalf("the authored test itself must be printed for the dev to adopt:\n%s", s)
	}
	if !strings.Contains(s, testPath) {
		t.Fatalf("hand-back should name the dev test file (%s) to add it to:\n%s", testPath, s)
	}
}

func TestAdversarialNoAuthoredTestNoHandBack(t *testing.T) {
	// A perfect dev suite (0 survivors) makes the test-writer moot — no authored
	// test, so no hand-back noise.
	code, _ := writeTmpFiles(t)
	st := certifiedStatus() // AuthoredTest left empty
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{st}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test", "./..."}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 0 {
		t.Fatalf("exit = %d, want 0", rc)
	}
	if strings.Contains(out.String(), "authored a test") {
		t.Fatalf("no authored test → must not print a hand-back:\n%s", out.String())
	}
}

func TestAdversarialPollsUntilConverged(t *testing.T) {
	code, _ := writeTmpFiles(t)
	running := advStatus{RunID: 7, Found: true, Converged: false}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{running, running, certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--timeout", "10s", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0 after polling", rc)
	}
	if f.statusCall < 3 {
		t.Fatalf("polled %d times, want >= 3", f.statusCall)
	}
}

func TestAdversarialMissingFlagsUsage(t *testing.T) {
	var out, errBuf bytes.Buffer
	// No --code.
	rc := runCertifyAdversarial([]string{"--adversarial", "--brain", "http://b", "--goal", "g", "--", "go", "test"}, &fakeAdvClient{}, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 2 {
		t.Fatalf("missing --code: exit = %d, want 2", rc)
	}
	// No `-- cmd`.
	code, _ := writeTmpFiles(t)
	rc = runCertifyAdversarial([]string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g"}, &fakeAdvClient{}, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 2 {
		t.Fatalf("missing -- cmd: exit = %d, want 2", rc)
	}
}

func TestAdversarialTimeoutExitsOne(t *testing.T) {
	code, _ := writeTmpFiles(t)
	running := advStatus{RunID: 7, Found: true, Converged: false}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{running}}
	var out, errBuf bytes.Buffer
	// --timeout 0 => the deadline is already past after StartRun; first
	// not-converged poll trips the timeout.
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--timeout", "0s", "--", "go", "test"}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 1 {
		t.Fatalf("timeout: exit = %d, want 1", rc)
	}
	if !strings.Contains(errBuf.String(), "run_id 7") {
		t.Fatalf("timeout message should name the run id for re-query:\n%s", errBuf.String())
	}
}

func TestAdversarialStartErrorExitsOne(t *testing.T) {
	code, _ := writeTmpFiles(t)
	f := &fakeAdvClient{startErr: errors.New("boom")}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test"}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 1 {
		t.Fatalf("start error: exit = %d, want 1", rc)
	}
}

func TestAdversarialDetectsPythonLanguage(t *testing.T) {
	dir := t.TempDir()
	codePath := dir + "/foo.py"
	testPath := dir + "/test_foo.py"
	if err := os.WriteFile(codePath, []byte("def f(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("def test_f(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", codePath, "--goal", "g", "--poll", "1ms", "--", "pytest"}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", rc, errBuf.String())
	}
	if f.spec.Lang != "python" {
		t.Fatalf("spec.Lang = %q, want python", f.spec.Lang)
	}
	if f.spec.DevTestPath != testPath {
		t.Fatalf("spec.DevTestPath = %q, want %q", f.spec.DevTestPath, testPath)
	}
}

func TestAdversarialDetectsGoLanguage(t *testing.T) {
	code, testPath := writeTmpFiles(t) // fence.go / fence_test.go
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", code, "--goal", "g", "--poll", "1ms", "--", "go", "test", "./..."}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", rc, errBuf.String())
	}
	if f.spec.Lang != "go" {
		t.Fatalf("spec.Lang = %q, want go", f.spec.Lang)
	}
	if f.spec.DevTestPath != testPath {
		t.Fatalf("spec.DevTestPath = %q, want %q", f.spec.DevTestPath, testPath)
	}
}

func TestAdversarialUnknownLanguageExitsTwo(t *testing.T) {
	dir := t.TempDir()
	codePath := dir + "/foo.xyz"
	if err := os.WriteFile(codePath, []byte("???\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeAdvClient{}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", codePath, "--goal", "g", "--", "some", "cmd"}
	rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf)
	if rc != 2 {
		t.Fatalf("exit = %d, want 2", rc)
	}
	if !strings.Contains(errBuf.String(), "unknown language") {
		t.Fatalf("stderr missing 'unknown language': %s", errBuf.String())
	}
	if f.spec != (advStartSpec{}) {
		t.Fatalf("StartRun should not have been called, but spec was captured: %+v", f.spec)
	}
}

// TestRenderAdvVerdictBaselineFailed pins Bug D's fix: when the dev suite could
// not pass on the unmutated code (baseline failed), the readout says
// COULD-NOT-GRADE and does NOT fabricate a kill rate / killed tally from a run
// where nothing was actually graded.
func TestRenderAdvVerdictBaselineFailed(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "internal/adequacy/score.go", advVerdict{
		Lang: "go", Commit: "abc1234", MutantsTotal: 20,
		DevKillRate: 0, Survivors: 0, Status: "needs-review",
		BaselineFailed: true,
	})
	out := b.String()
	if !strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("baseline-failed verdict must report COULD-NOT-GRADE, got:\n%s", out)
	}
	if strings.Contains(out, "dev_kill_rate") {
		t.Fatalf("baseline-failed verdict must not print a kill rate (nothing was graded):\n%s", out)
	}
	for _, bad := range []string{"killed 20/20", "killed 0/20"} {
		if strings.Contains(out, bad) {
			t.Fatalf("baseline-failed verdict must not print a killed tally (%q):\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "0 graded") {
		t.Fatalf("baseline-failed verdict should say mutants generated but not graded:\n%s", out)
	}
}

// TestRenderAdvVerdictNormalPathUnaffected guards that the honest-reporting
// branch does not change the readout for a real, graded verdict.
func TestRenderAdvVerdictNormalPathUnaffected(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", advVerdict{
		Lang: "go", MutantsTotal: 20, Survivors: 5, DevKillRate: 0.75, Status: "needs-review",
	})
	out := b.String()
	if !strings.Contains(out, "dev_kill_rate: 0.75") {
		t.Fatalf("a graded verdict must still print its kill rate:\n%s", out)
	}
	if strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("a graded verdict must not claim could-not-grade:\n%s", out)
	}
}

// TestRenderAdvVerdictSuiteIgnoresFile is the canary's readout: a suite that
// passed on deliberately invalid source graded nothing, so the --local summary
// must say COULD-NOT-GRADE instead of printing the fabricated 0.00 that made
// this indistinguishable from a genuinely terrible suite. It must ALSO not
// reuse the baseline-failed wording — the two send operators to different
// places.
func TestRenderAdvVerdictSuiteIgnoresFile(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "internal/adequacy/score.go", advVerdict{
		Lang: "go", Commit: "abc1234", MutantsTotal: 20,
		DevKillRate: 0, Survivors: 0, Status: "needs-review",
		SuiteIgnoresFile: true,
	})
	out := b.String()
	if !strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("a suite that ignores the file must report COULD-NOT-GRADE, got:\n%s", out)
	}
	if strings.Contains(out, "dev_kill_rate") {
		t.Fatalf("nothing was graded — no kill rate may be printed:\n%s", out)
	}
	if !strings.Contains(out, "never compiles or imports this file") {
		t.Fatalf("the readout must name THIS diagnosis, not the baseline one:\n%s", out)
	}
	if strings.Contains(out, "baseline build/test failed") {
		t.Fatalf("a suite that ignores the file is not a failed baseline:\n%s", out)
	}
}

// TestAdvVerdictFromPoolCarriesSuiteIgnoresFile proves the flag survives the
// pool→wire conversion; without it the --local readout silently loses the
// diagnosis and falls through to the fabricated 0.00.
func TestAdvVerdictFromPoolCarriesSuiteIgnoresFile(t *testing.T) {
	got := advVerdictFromPool(advpool.Verdict{SuiteIgnoresFile: true})
	if !got.SuiteIgnoresFile {
		t.Error("advVerdictFromPool dropped SuiteIgnoresFile")
	}
}

// TestAdvVerdictFromPoolCarriesTimedOut proves TimedOut survives the
// pool→wire conversion — without it certify --local's own verdict block
// (unlike --repo's report, which already marks it) would silently read
// like a clean converged run.
func TestAdvVerdictFromPoolCarriesTimedOut(t *testing.T) {
	got := advVerdictFromPool(advpool.Verdict{TimedOut: true, DevScored: true})
	if !got.TimedOut {
		t.Error("advVerdictFromPool dropped TimedOut")
	}
	if !got.DevScored {
		t.Error("advVerdictFromPool dropped DevScored — without it, renderAdvVerdict cannot tell an unmeasured timeout from a measured one on the wire")
	}

	// The false-negative direction matters just as much: an unmeasured
	// timeout (DevScored: false) must round-trip as false, not silently
	// upgrade to true.
	unmeasured := advVerdictFromPool(advpool.Verdict{TimedOut: true, DevScored: false})
	if unmeasured.DevScored {
		t.Error("advVerdictFromPool turned an unmeasured timeout's DevScored=false into true")
	}
}

// TestRenderAdvVerdictTimedOutDoesNotClaimTheTestWriterOrCriticRan is the
// review-caught false-reassurance bug: a banked timeout verdict (real
// dev_kill_rate/survivors, but the test-writer/critic never ran) used to
// print "proven_missed: 0" and "no vacuous tests flagged" — both FALSE, and
// indistinguishable from a converged below-threshold audit. It must instead
// say plainly that the pool did not converge and that proven_missed/critic
// review are not meaningful numbers.
func TestRenderAdvVerdictTimedOutDoesNotClaimTheTestWriterOrCriticRan(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "src/flask/cli.py", advVerdict{
		Lang: "python", Commit: "abc1234", MutantsTotal: 24,
		DevKillRate: 0.46, Survivors: 13, Status: "needs-review",
		TimedOut: true, DevScored: true,
	})
	out := b.String()
	if !strings.Contains(out, "dev_kill_rate: 0.46") {
		t.Fatalf("the real measured kill rate must still print:\n%s", out)
	}
	if !strings.Contains(out, "TIMED OUT") {
		t.Fatalf("a timed-out verdict must say so plainly:\n%s", out)
	}
	if strings.Contains(out, "proven_missed: 0") {
		t.Fatalf("must not claim proven_missed: 0 — the test-writer never ran, that is not a real zero:\n%s", out)
	}
	if strings.Contains(out, "no vacuous tests flagged") {
		t.Fatalf("must not claim the critic found nothing — the critic never ran:\n%s", out)
	}
}

// TestRenderAdvVerdictPoolTestUnsoundExplainsTheZero is F2's downstream
// rendering check: a compiling authored test (TestWriterFailed false) whose
// report never genuinely graded must print an explanation distinct from
// TestWriterFailed's — proven_missed: 0 here means "not scored," not "the
// pool could not author a compiling test."
func TestRenderAdvVerdictPoolTestUnsoundExplainsTheZero(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "src/flask/cli.py", advVerdict{
		Lang: "python", Commit: "abc1234", MutantsTotal: 24,
		DevKillRate: 0.46, Survivors: 13, Status: "needs-review",
		PoolTestUnsound: true, DevScored: true,
	})
	out := b.String()
	if !strings.Contains(out, "proven_missed: 0") {
		t.Fatalf("proven_missed must still print (this run DID converge, unlike TimedOut):\n%s", out)
	}
	if !strings.Contains(out, "did not pass on the unmutated code") {
		t.Fatalf("must explain the PoolTestUnsound diagnosis distinctly from TestWriterFailed's:\n%s", out)
	}
	if strings.Contains(out, "could not author a compiling test") {
		t.Fatalf("must NOT print TestWriterFailed's wording — this test DID compile:\n%s", out)
	}
}

// TestRenderAdvVerdictUnmeasuredTimeoutDoesNotFabricateAZeroKillRate is the
// re-review catch: TimedOut alone does NOT mean the dev suite was measured
// — a run reachable ONLY on the brain/--adversarial path (advpool.Driver.Tick's
// own RunDeadline branch, not just --local's bankableTimeoutVerdict) can sign
// a TimedOut verdict whose mutant-generator never finished, so DevKillRate is
// a zero value nothing computed, not a real 0.00. Before this fix,
// advVerdict had no DevScored field at all, so this exact shape printed
// "status: NEEDS-REVIEW (dev suite killed 0/0 mutants)" / "dev_kill_rate:
// 0.00" under the TIMED OUT banner — an operator would read that as "your
// suite caught nothing," the fabricated accusation this whole codebase
// exists to prevent.
func TestRenderAdvVerdictUnmeasuredTimeoutDoesNotFabricateAZeroKillRate(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "src/flask/cli.py", advVerdict{
		Lang: "python", Commit: "abc1234",
		Status: "needs-review",
		// DevKillRate/MutantsTotal/Survivors deliberately left at their zero
		// values, mirroring a real timeoutVerdict() output when
		// run.devScored was never set true — the mutant-generator itself
		// never finished before RunDeadline fired.
		TimedOut: true, DevScored: false,
	})
	out := b.String()
	if !strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("an unmeasured timeout must report COULD-NOT-GRADE, got:\n%s", out)
	}
	if strings.Contains(out, "dev_kill_rate") {
		t.Fatalf("nothing was measured — no kill rate may be printed at all:\n%s", out)
	}
	if strings.Contains(out, "killed 0/0") {
		t.Fatalf("must not print a fabricated 0/0 killed tally:\n%s", out)
	}
}

// TestRenderAdvVerdictBaselineFailedPrintsSuiteOutput pins the fix for the
// least debuggable outcome an audit can produce. certify --repo has printed the
// failing baseline's own output since the day two paid audits dead-ended with
// nothing to go on; certify --local computed the identical string and dropped
// it on the floor, so a first run against a real project reported
// "a build/environment issue" and left the operator no way to find out which.
//
// Found by running corral against a TypeScript project for the first time: the
// suite could not resolve "localhost" inside the jail, and none of that reached
// the readout.
func TestRenderAdvVerdictBaselineFailedPrintsSuiteOutput(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "src/client/ApiError.ts", advVerdict{
		Lang: "typescript", Commit: "719343a", MutantsTotal: 14,
		DevKillRate: 0, Survivors: 0, Status: "needs-review",
		BaselineFailed: true,
		BaselineOutput: "Error: getaddrinfo EAI_AGAIN localhost\n  code: 'EAI_AGAIN'",
	})
	out := b.String()
	if !strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("a failed baseline must still refuse to grade:\n%s", out)
	}
	if !strings.Contains(out, "EAI_AGAIN") {
		t.Fatalf("the failing baseline's own output must reach the readout:\n%s", out)
	}
	if strings.Contains(out, "dev_kill_rate") {
		t.Fatalf("a failed baseline must never print a fabricated kill rate:\n%s", out)
	}
}

// TestRenderAdvVerdictBaselineFailedWithoutOutput guards the empty case: a
// runner that produced nothing must not print a dangling, empty "the suite
// said:" header promising detail that isn't there.
func TestRenderAdvVerdictBaselineFailedWithoutOutput(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.ts", advVerdict{
		Lang: "typescript", MutantsTotal: 3, Status: "needs-review",
		BaselineFailed: true, BaselineOutput: "   \n  ",
	})
	out := b.String()
	if strings.Contains(out, "the suite said") {
		t.Fatalf("must not print an empty suite-output header:\n%s", out)
	}
	if !strings.Contains(out, "COULD-NOT-GRADE") {
		t.Fatalf("still must refuse to grade:\n%s", out)
	}
}

// TestRenderAdvVerdictNamesTheUncollectedAuthoredTest pins the split of the
// most misleading message this tool produces.
//
// corral runs a positive control that PROVES whether the test command ever
// reached the authored test's own file — and then collapsed that answer into
// "it did not pass on the unmutated code (or never reads the file)", leaving
// the operator to guess between a broken test and a too-narrow command. It is
// nearly always the command: the authored test is a NEW file beside the
// developer's, so a command pinned to one path never collects it, and
// proven_missed reads 0 forever while looking like a clean bill of health.
func TestRenderAdvVerdictNamesTheUncollectedAuthoredTest(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "src/client/ApiError.ts", advVerdict{
		Lang: "typescript", MutantsTotal: 15, Survivors: 3, DevKillRate: 0.8,
		Status: "needs-review", DevScored: true,
		PoolTestUnsound: true, AuthoredTestNotCollected: true,
	})
	out := b.String()
	if !strings.Contains(out, "NEVER RAN IT") {
		t.Fatalf("must say the command never ran the authored test:\n%s", out)
	}
	if strings.Contains(out, "did not pass on the unmutated code —") {
		t.Fatalf("must NOT also offer the other diagnosis; that is the ambiguity being removed:\n%s", out)
	}
}

// TestRenderAdvVerdictKeepsCleanCodeFailureWording: when the positive control
// did NOT fire, the authored test really did fail against correct code, and the
// operator must still be told that — not the command advice.
func TestRenderAdvVerdictKeepsCleanCodeFailureWording(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.ts", advVerdict{
		Lang: "typescript", MutantsTotal: 10, Survivors: 2, DevKillRate: 0.8,
		Status: "needs-review", DevScored: true,
		PoolTestUnsound: true, AuthoredTestNotCollected: false,
	})
	out := b.String()
	if !strings.Contains(out, "did not pass on the unmutated code") {
		t.Fatalf("a genuine clean-code failure must still say so:\n%s", out)
	}
	if strings.Contains(out, "NEVER RAN IT") {
		t.Fatalf("must not blame the command when the control did not fire:\n%s", out)
	}
}

// TestVerdictNamesASingleVendorHerd is the fourth participant (#104) surfacing
// on the readout. CheckDecorrelation keeps corral's three seats apart and knows
// nothing about whatever WROTE the code — and the DEFAULT assignment is one
// vendor in every seat. When the code was also written by that vendor's model,
// the lineage under audit planted the faults and graded the tests for its own
// work, and the kill rate is optimistic for a reason invisible in the number.
func TestVerdictNamesASingleVendorHerd(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", advVerdict{
		Lang: "go", MutantsTotal: 20, Survivors: 4, DevKillRate: 0.8, Status: "needs-review", DevScored: true,
		ModelsByRole: map[string]string{
			advpool.RoleMutantGenerator: "claude-sonnet-5",
			advpool.RoleTestWriter:      "claude-sonnet-5",
			advpool.RoleTestCritic:      "claude-haiku-4-5",
		},
	})
	out := b.String()
	if !strings.Contains(out, "decorrelation:") {
		t.Fatalf("a single-vendor herd must be stated on the verdict:\n%s", out)
	}
	if !strings.Contains(out, "WRITTEN by") {
		t.Fatalf("the caveat must name the actual risk — that the same lineage wrote the code:\n%s", out)
	}
}

// TestVerdictSilentOnACrossVendorHerd: when the seats genuinely span vendors
// there is nothing to warn about, and a caveat printed every time is a caveat
// readers learn to skip.
func TestVerdictSilentOnACrossVendorHerd(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", advVerdict{
		Lang: "go", MutantsTotal: 20, Survivors: 4, DevKillRate: 0.8, Status: "needs-review", DevScored: true,
		ModelsByRole: map[string]string{
			advpool.RoleMutantGenerator: "gemini-3.6-flash",
			advpool.RoleTestWriter:      "gemini-3.6-flash",
			advpool.RoleTestCritic:      "claude-haiku-4-5",
		},
	})
	if strings.Contains(b.String(), "decorrelation:") {
		t.Fatalf("a cross-vendor herd needs no caveat:\n%s", b.String())
	}
}

// TestSoleGradedVendorIgnoresTheShadowSeat: the challenger records a
// head-to-head and never gates a verdict, so its vendor says nothing about the
// independence of the result.
func TestSoleGradedVendorIgnoresTheShadowSeat(t *testing.T) {
	got := soleGradedVendor(map[string]string{
		advpool.RoleMutantGenerator:       "claude-sonnet-5",
		advpool.RoleTestWriter:            "claude-sonnet-5",
		advpool.RoleTestCritic:            "claude-haiku-4-5",
		advpool.RoleMutantGeneratorShadow: "gemini-3.6-flash",
	})
	if got != "anthropic" {
		t.Fatalf("the shadow seat must not break a single-vendor finding, got %q", got)
	}
}

// TestSoleGradedVendorSilentOnAnUnknownModel: silence is the honest answer when
// we cannot tell, and a caveat printed on a guess trains readers to ignore it.
func TestSoleGradedVendorSilentOnAnUnknownModel(t *testing.T) {
	if got := soleGradedVendor(map[string]string{
		advpool.RoleMutantGenerator: "some-local-7b",
		advpool.RoleTestWriter:      "claude-sonnet-5",
	}); got != "" {
		t.Fatalf("an unrecognized model must yield no claim, got %q", got)
	}
}

// TestSoleGradedVendorSkipsAnOffRole: `--critic-model off` is supported, and a
// seat that never ran says nothing either way.
func TestSoleGradedVendorSkipsAnOffRole(t *testing.T) {
	if got := soleGradedVendor(map[string]string{
		advpool.RoleMutantGenerator: "gemini-3.6-flash",
		advpool.RoleTestWriter:      "gemini-3.6-flash",
		advpool.RoleTestCritic:      "off",
	}); got != "google" {
		t.Fatalf("an off role must not block the finding, got %q", got)
	}
}
