// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"
)

// hookedRunOutput is what a run the record hook wrapped prints: the
// runner's lines, then the record block (or the line standing in for a
// record over the bound).
const (
	hookedRunOutput  = "FAIL tests/f.test.js\n  ● adds\n\ncorral-record-begin jest-json\n{\"testResults\":[]}\ncorral-record-end\n"
	skippedRunOutput = "FAIL tests/f.test.js\n  ● adds\n\ncorral-record-skipped jest-json 40000\n"
)

// The test writer is shown what the runner printed, never the record the
// hook printed after it: a record is corral's reading aid, not the
// runner's account to the writer, and on a large suite it would fill the
// prompt.
func TestWriterFailureDetailShowsNoRecord(t *testing.T) {
	for _, out := range []string{hookedRunOutput, skippedRunOutput} {
		got := writerFailureDetail(out)
		if strings.Contains(got, "corral-record-") || strings.Contains(got, "testResults") {
			t.Errorf("writerFailureDetail kept the record:\n%s", got)
		}
		if !strings.Contains(got, "● adds") {
			t.Errorf("writerFailureDetail lost the printed lines:\n%s", got)
		}
	}
}

// The together check keeps the runner's printed lines for the record
// (authored_together_output), not the hook's record: its last 2000 bytes
// were the record's alone on any run whose record is that size.
func TestAuthoredTogetherOutputKeepsNoRecord(t *testing.T) {
	for _, out := range []string{hookedRunOutput, skippedRunOutput} {
		got := authoredTogetherOutput(out)
		if strings.Contains(got, "corral-record-") || strings.Contains(got, "testResults") {
			t.Errorf("authoredTogetherOutput kept the record:\n%s", got)
		}
		if !strings.Contains(got, "● adds") {
			t.Errorf("authoredTogetherOutput lost the printed lines:\n%s", got)
		}
	}
}
