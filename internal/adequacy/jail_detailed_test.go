// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// scriptIsolator is the fake-jail pattern (see captureIsolator in
// jail_test.go): it satisfies sandbox.Isolator and wraps every command into a
// shell script of the test's choosing, so a jail run produces REAL output
// without needing bwrap on the host.
type scriptIsolator struct{ script string }

func (s *scriptIsolator) Wrap(command string, opts sandbox.Options, env []string) ([]string, error) {
	return []string{"sh", "-c", s.script}, nil
}
func (s *scriptIsolator) Preflight() error { return nil }
func (s *scriptIsolator) Name() string     { return "script" }

// TestJailImplementsDetailedJail: killed_by is populated only for a Jail that
// can hand back what a run PRINTED (adequacy.DetailedJail). The workspace
// substrate implements it; the bwrap jail did not — so `--substrate jail`
// recorded NULL in that column for every mutant it killed, and the feature
// silently existed for one substrate out of two. bwrapJail already keeps the
// output (RunTestVerbose), so honouring the contract costs nothing: it is the
// same run, the same exit code, the same verdict.
func TestJailImplementsDetailedJail(t *testing.T) {
	j := NewJail(&scriptIsolator{script: `echo "FAILED a_test.py::test_x - AssertionError"; exit 1`}, 0)
	dj, ok := j.(DetailedJail)
	if !ok {
		t.Fatal("the bwrap jail does not implement DetailedJail — `--substrate jail` can never record killed_by")
	}

	pass, out, err := dj.RunTestDetailed(context.Background(),
		map[string]string{"a.py": "x = 1\n"}, []string{"pytest"})
	if err != nil {
		t.Fatalf("RunTestDetailed: %v", err)
	}
	if pass {
		t.Error("a script exiting 1 reported a pass")
	}
	if !strings.Contains(string(out), "FAILED a_test.py::test_x") {
		t.Errorf("output = %q, want the run's own failure summary — that string IS killed_by", out)
	}
}

// The cap is the DetailedJail contract's, not the sandbox's: whatever the
// jail printed, the caller gets at most the last maxDetailedOutput of it.
func TestJailDetailedOutputIsCapped(t *testing.T) {
	// Ask for far more than the cap, then the line a parser wants, and give
	// the sandbox room to hand it all back.
	script := `head -c 2000000 /dev/zero | tr '\0' 'x'; echo; echo "FAILED a_test.py::test_x"; exit 1`
	j := NewJail(&scriptIsolator{script: script}, 0, WithMaxOutput(4<<20))
	dj, ok := j.(DetailedJail)
	if !ok {
		t.Fatal("the bwrap jail does not implement DetailedJail")
	}
	_, out, err := dj.RunTestDetailed(context.Background(), map[string]string{"a.py": "x\n"}, []string{"pytest"})
	if err != nil {
		t.Fatalf("RunTestDetailed: %v", err)
	}
	if len(out) > maxDetailedOutput {
		t.Errorf("returned %d bytes, want at most the %d-byte cap", len(out), maxDetailedOutput)
	}
}
