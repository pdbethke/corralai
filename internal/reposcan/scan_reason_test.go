// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type reasonErr struct{ r string }

func (e reasonErr) Error() string      { return "boom" }
func (e reasonErr) ScanReason() string { return e.r }

// TestExecutorErrReasonPrefersTheSpecificOne pins that a precise refusal keeps
// its identity instead of being flattened into the catch-all.
func TestExecutorErrReasonPrefersTheSpecificOne(t *testing.T) {
	if got := executorErrReason(reasonErr{ReasonTestCmdCannotCollect}); got != ReasonTestCmdCannotCollect {
		t.Errorf("carried reason = %q, want %q", got, ReasonTestCmdCannotCollect)
	}
	// Wrapped, the way a caller would return it.
	wrapped := fmt.Errorf("audit: %w", reasonErr{ReasonTestCmdCannotCollect})
	if got := executorErrReason(wrapped); got != ReasonTestCmdCannotCollect {
		t.Errorf("wrapped reason = %q, want %q", got, ReasonTestCmdCannotCollect)
	}
	// No opinion, and plain errors, both fall back.
	if got := executorErrReason(reasonErr{"  "}); got != ReasonExecutorError {
		t.Errorf("blank reason = %q, want the catch-all", got)
	}
	if got := executorErrReason(errors.New("plain")); got != ReasonExecutorError {
		t.Errorf("plain error = %q, want the catch-all", got)
	}
}

type recordedErr struct{ terminal, recorded string }

func (e recordedErr) Error() string          { return e.terminal }
func (e recordedErr) RecordedDetail() string { return e.recorded }

// TestAnExecutorErrorsRecordedDetailIsWhatTheRowHolds: an executor error
// that knows its terminal text names something the record must not hold (a
// host path) says so with RecordedDetail, and that, not Error(), is the
// result's Detail, which the signed Row.Detail is copied from. Wrapped, the
// recorded text still wins; with none (or blank), Error() is kept as before.
func TestAnExecutorErrorsRecordedDetailIsWhatTheRowHolds(t *testing.T) {
	const host = "/home/someone/zz-marker/report.json"
	for name, err := range map[string]error{
		"bare":    recordedErr{terminal: "the report " + host + " broke", recorded: "the report sha256:ab broke"},
		"wrapped": fmt.Errorf("audit: %w", recordedErr{terminal: "the report " + host + " broke", recorded: "the report sha256:ab broke"}),
	} {
		ex := &errExec{err: err}
		out := Scan(context.Background(), []Job{{Path: "a.go"}}, ex, nil, 1)
		if out[0].Detail != "the report sha256:ab broke" {
			t.Errorf("%s: Detail = %q, want the recorded text", name, out[0].Detail)
		}
	}
	out := Scan(context.Background(), []Job{{Path: "a.go"}}, &errExec{err: recordedErr{terminal: "plain words", recorded: " "}}, nil, 1)
	if out[0].Detail != "plain words" {
		t.Errorf("blank recorded text: Detail = %q, want Error()", out[0].Detail)
	}
}

type errExec struct{ err error }

func (e *errExec) Execute(context.Context, Job) (FileResult, error) { return FileResult{}, e.err }
