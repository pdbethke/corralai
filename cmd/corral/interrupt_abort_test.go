// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/queue"
)

var errSeatInterrupted = fmt.Errorf("seat: %w", agentbackend.ErrInterrupted)

// An interrupted writer is not a writer that failed: filing it under the
// WriterProviderFailed sentinel would let the run converge and record a
// verdict for an audit the operator stopped. The interrupt aborts.
func TestAnInterruptedWriterAbortsRatherThanFilingWriterFailed(t *testing.T) {
	for _, role := range []string{advpool.RoleTestWriter, advpool.RoleMutantGeneratorShadow, advpool.RoleTestCritic} {
		q, err := queue.Open(filepath.Join(t.TempDir(), "q.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		const mission int64 = 1
		if err := q.Enqueue(mission, []queue.TaskSpec{{Key: "k-" + role, Role: role, Title: "t", Instruction: "do"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.PromoteReady(mission); err != nil {
			t.Fatal(err)
		}
		chatterFor := func(string) agentworker.Chatter { return failingChatter{err: errSeatInterrupted} }
		var progress bytes.Buffer
		_, err = runReadyTasks(context.Background(), q, mission, chatterFor, nil, nil, 1, &progress)
		if !errors.Is(err, agentbackend.ErrInterrupted) {
			t.Errorf("%s: an interrupted seat must abort the run with the interrupt, got %v", role, err)
		}
		tasks, _ := q.List(mission)
		for _, task := range tasks {
			if task.Result == advpool.WriterProviderFailedResult || task.Result == advpool.ShadowProviderFailedResult {
				t.Errorf("%s: the interrupt was filed as a provider failure (%q)", role, task.Result)
			}
		}
		q.Close()
	}
}

// interruptedSeat is a seat whose call was stopped by an interrupt.
type interruptedSeat struct{}

func (interruptedSeat) Chat([]agentbackend.Message, []any) (agentbackend.Message, error) {
	return agentbackend.Message{Usage: agentbackend.Usage{Unmetered: true}}, errSeatInterrupted
}

// An interrupted review writes nothing — no "recorded unverified" entry, no
// push — and exits non-zero saying it was interrupted, whichever seat the
// interrupt stopped.
func TestAnInterruptedReviewWritesNoEntry(t *testing.T) {
	requireReviewSandbox(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	for _, stopped := range []string{"rev", "ver"} {
		newReviewerBackend = func(model, _ string) (agentbackend.Backend, error) {
			if model == stopped {
				return interruptedSeat{}, nil
			}
			return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"c","tier":"CODE-READ","file":"pkg/a.go","line":1}],"sound":[]}`}, nil
		}
		ledger := filepath.Join(t.TempDir(), "ledger")
		var out, errb bytes.Buffer
		code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "rev", "--verifier-model", "ver", "--ledger", ledger}, &out, &errb)
		if code != exitInterrupted || !strings.Contains(errb.String(), "interrupted") {
			t.Errorf("%s interrupted: want a non-zero exit saying interrupted, got %d\n%s", stopped, code, errb.String())
		}
		if entries, _ := os.ReadDir(filepath.Join(ledger, "scans")); len(entries) != 0 {
			t.Errorf("%s interrupted: %d ledger entries written for a stopped review", stopped, len(entries))
		}
		if strings.Contains(errb.String(), "recorded unverified") {
			t.Errorf("%s interrupted: reported as recorded unverified:\n%s", stopped, errb.String())
		}
	}
}

// lateSignalVerifier answers in full, but the operator's interrupt lands just
// as it returns: after the last seat call, before anything is written.
type lateSignalVerifier struct{ reply string }

func (v lateSignalVerifier) Chat([]agentbackend.Message, []any) (agentbackend.Message, error) {
	agentbackend.Interrupt()
	return agentbackend.Message{Role: "assistant", Content: v.reply}, nil
}

// A signal after the last agent call still stops the record: review checks
// the one predicate right before its statement, ledger entry and push.
func TestAReviewInterruptedAfterItsLastCallWritesNoEntry(t *testing.T) {
	requireReviewSandbox(t)
	t.Cleanup(agentbackend.ResetInterrupted)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(model, _ string) (agentbackend.Backend, error) {
		if model == "rev" {
			return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"c","tier":"CODE-READ","file":"pkg/a.go","line":1}],"sound":[]}`}, nil
		}
		return lateSignalVerifier{reply: `{"opinion":"fine","refutations":[{"id":"R1","verdict":"STANDS","argument":"yes"}]}`}, nil
	}
	ledger := filepath.Join(t.TempDir(), "ledger")
	push := filepath.Join(t.TempDir(), "wh.duckdb")
	stmt := filepath.Join(t.TempDir(), "review.statement.json")
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "rev", "--verifier-model", "ver", "--ledger", ledger, "--push", push, "--attest", stmt}, &out, &errb)
	if matches, _ := filepath.Glob(stmt + "*"); len(matches) != 0 {
		t.Fatalf("an interrupted review left its statement on disk: %v", matches)
	}
	if code != exitInterrupted || !strings.Contains(errb.String(), "interrupted — nothing recorded") {
		t.Fatalf("want a non-zero exit saying nothing was recorded, got %d\n%s", code, errb.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(ledger, "scans")); len(entries) != 0 {
		t.Fatalf("%d ledger entries written after the interrupt", len(entries))
	}
	if _, err := os.Stat(push); !os.IsNotExist(err) {
		t.Fatalf("pushed after the interrupt")
	}
}

// certify --local signs inside the run. A signal that lands after the verdict
// was signed found a complete, recorded run: it is reported as recorded, never
// as "nothing recorded".
func TestALateSignalAfterSigningIsReportedAsRecorded(t *testing.T) {
	t.Cleanup(agentbackend.ResetInterrupted)
	agentbackend.Interrupt()
	if !localInterruptRefuses(nil, &advpool.Verdict{}) {
		t.Error("an interrupted run with no signed verdict must be refused")
	}
	if localInterruptRefuses(nil, &advpool.Verdict{RecordID: 7}) {
		t.Error("a verdict already signed is a recorded run; the late signal must not disown it")
	}
	if !localInterruptRefuses(errSeatInterrupted, nil) {
		t.Error("an interrupt error must be refused")
	}
	agentbackend.ResetInterrupted()
	if localInterruptRefuses(nil, &advpool.Verdict{}) {
		t.Error("no interrupt, nothing to refuse")
	}
}

// A review interrupted after its statement was written removes it, so
// "nothing recorded" is still true; a file it cannot remove is named instead
// of being covered by that claim.
func TestRefuseInterruptedAfterRemovesWhatWasWrittenOrNamesIt(t *testing.T) {
	dir := t.TempDir()
	stmt, env := filepath.Join(dir, "s.json"), filepath.Join(dir, "s.json.dsse.json")
	mustWrite(t, stmt, "{}")
	mustWrite(t, env, "{}")
	var errb bytes.Buffer
	if code := refuseInterruptedAfter(&errb, "corral review", []string{stmt, env}); code != exitInterrupted || !strings.Contains(errb.String(), "interrupted — nothing recorded") {
		t.Fatalf("exit %d, %q", code, errb.String())
	}
	for _, f := range []string{stmt, env} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", f)
		}
	}
	// A path that cannot be removed (a non-empty directory) is named, and
	// "nothing recorded" is not claimed.
	stuck := filepath.Join(dir, "stuck")
	mustWrite(t, filepath.Join(stuck, "x"), "x")
	errb.Reset()
	refuseInterruptedAfter(&errb, "corral review", []string{stuck})
	if strings.Contains(errb.String(), "nothing recorded") || !strings.Contains(errb.String(), stuck) {
		t.Fatalf("an unremovable file must be named, never covered by \"nothing recorded\": %q", errb.String())
	}
}
