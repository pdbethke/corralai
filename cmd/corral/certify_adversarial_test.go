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
