// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/eval"
)

type fakeEvalRunner struct{ calls int }

func (f *fakeEvalRunner) RunOne(_ context.Context, t eval.Target) (eval.RunResult, error) {
	f.calls++
	// gappy target has a gap, thorough doesn't — so the report calibrates.
	if strings.Contains(t.ID, "gappy") {
		return eval.RunResult{Survivors: 1, ProvenMissed: 1}, nil
	}
	return eval.RunResult{Survivors: 0, DevKillRate: 1.0}, nil
}

func TestRunEvalDrivesHarnessAndPrintsCalibratedReport(t *testing.T) {
	f := &fakeEvalRunner{}
	var out, errb bytes.Buffer
	rc := runEval(
		[]string{"--corpus", "../../eval/corpus/manifest.json", "--iterations", "1",
			"--only", "passwd-thorough,passwd-gappy", "--progress", t.TempDir() + "/p.json"},
		func(_, _ string) eval.PoolRunner { return f },
		&out, &errb)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if f.calls != 2 {
		t.Fatalf("want 2 runs (2 targets × 1 iter), got %d", f.calls)
	}
	if !strings.Contains(out.String(), "CALIBRATED") {
		t.Fatalf("report should render calibration verdict:\n%s", out.String())
	}
}
