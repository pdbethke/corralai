package eval

import (
	"bytes"
	"context"
	"testing"
)

type fakeRunner struct {
	calls int
	byID  map[string]RunResult // canned result per target id
}

func (f *fakeRunner) RunOne(_ context.Context, t Target) (RunResult, error) {
	f.calls++
	r := f.byID[t.ID]
	r.TargetID = t.ID
	return r, nil
}

func fakeManifest() Manifest {
	return Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "a", Goal: "g", TestCmd: "c", code: "x", testCode: "y"},
		{ID: "b", Goal: "g", TestCmd: "c", code: "x", testCode: "y"},
	}}
}

func TestRunIteratesAndBoundsByOnlyAndIterations(t *testing.T) {
	f := &fakeRunner{byID: map[string]RunResult{"a": {Survivors: 1}}}
	cfg := Config{Iterations: 2, Only: []string{"a"}, ProgressPath: t.TempDir() + "/p.json"}
	res, err := Run(context.Background(), fakeManifest(), cfg, f, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 || len(res) != 2 {
		t.Fatalf("--only a --iterations 2 → want 2 runs, got calls=%d res=%d", f.calls, len(res))
	}
	for _, r := range res {
		if r.TargetID != "a" {
			t.Fatalf("ran wrong target: %s", r.TargetID)
		}
	}
}

func TestRunResumesFromProgress(t *testing.T) {
	pp := t.TempDir() + "/p.json"
	f1 := &fakeRunner{byID: map[string]RunResult{}}
	cfg := Config{Iterations: 2, Only: []string{"a"}, ProgressPath: pp}
	Run(context.Background(), fakeManifest(), cfg, f1, &bytes.Buffer{})
	// Second invocation with the SAME progress file + iterations: nothing new to do.
	f2 := &fakeRunner{byID: map[string]RunResult{}}
	res, _ := Run(context.Background(), fakeManifest(), cfg, f2, &bytes.Buffer{})
	if f2.calls != 0 || len(res) != 0 {
		t.Fatalf("resume: want 0 new runs, got %d", f2.calls)
	}
}
