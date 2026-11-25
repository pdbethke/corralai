// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// What a run the record hook wrapped prints: the runner's lines, then the
// record block, or the line standing in for a record over the bound.
var hookedBaselineOutputs = []string{
	"ModuleNotFoundError: No module named 'x'\n\ncorral-record-begin junit\n<testsuites><testsuite name=\"s\"/></testsuites>\ncorral-record-end\n",
	"ModuleNotFoundError: No module named 'x'\n\ncorral-record-skipped junit 40000\n",
}

// shownNoRecord fails when what a door showed carries the hook's record or
// lost the runner's printed line.
func shownNoRecord(t *testing.T, door, got string) {
	t.Helper()
	if strings.Contains(got, "corral-record-") || strings.Contains(got, "testsuites") {
		t.Errorf("%s showed the record:\n%s", door, got)
	}
	if !strings.Contains(got, "No module named 'x'") {
		t.Errorf("%s lost the printed lines:\n%s", door, got)
	}
}

// A red baseline's "the suite said" is what the runner printed: on certify
// --repo (noteRedBaseline, and the timed-out baseline's last output) and on
// certify --local (renderAdvVerdict), never the record the hook printed
// after it.
func TestRedBaselineDoorsShowNoRecord(t *testing.T) {
	for _, out := range hookedBaselineOutputs {
		var buf bytes.Buffer
		l := localExecutor{progress: &buf}
		l.noteRedBaseline("a.py", "baseline failed", out)
		shownNoRecord(t, "noteRedBaseline", buf.String())

		buf.Reset()
		if _, ok := l.timedOutBaseline(reposcan.Job{Path: "a.py"}, timedOutWith{out}); !ok {
			t.Fatal("timedOutBaseline did not take a timed-out baseline")
		}
		shownNoRecord(t, "timedOutBaseline", buf.String())

		var b strings.Builder
		renderAdvVerdict(&b, "a.py", advVerdict{Lang: "python", MutantsTotal: 1, Status: "needs-review", BaselineFailed: true, BaselineOutput: out})
		shownNoRecord(t, "renderAdvVerdict", b.String())
	}
}

// timedOutWith is a baseline the jail killed at its bound, having printed
// out.
type timedOutWith struct{ out string }

func (timedOutWith) RunBaseline() (bool, error)   { return false, nil }
func (b timedOutWith) BaselineOutput() string     { return b.out }
func (timedOutWith) BaselineTimedOut() bool       { return true }
func (timedOutWith) BaselineBound() time.Duration { return time.Second }
