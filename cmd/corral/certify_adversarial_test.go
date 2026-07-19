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
