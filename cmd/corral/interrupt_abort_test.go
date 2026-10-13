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
	for _, role := range []string{advpool.RoleTestWriter, advpool.RoleMutantGeneratorShadow} {
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
		if code == 0 || !strings.Contains(errb.String(), "interrupted") {
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
