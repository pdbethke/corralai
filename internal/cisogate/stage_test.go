// SPDX-License-Identifier: Elastic-2.0

package cisogate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/authoring"
	"github.com/pdbethke/corralai/internal/controlspec"
)

// fakeLLM is a deterministic stand-in for testgen.LLM. It keys its response
// on the system prompt (onSystem) when set, otherwise returns the canned
// resp — letting the same shape serve either the writer seat (WriteTest +
// GenerateMutants share one fake keyed on system prompt) or an independent
// reviewer seat (a single canned resp). It also records the last system/user
// prompts it was asked, so a test can assert the reviewer was called with
// its own distinct prompt and saw the right survivor.
type fakeLLM struct {
	onSystem func(sys string) string
	resp     string

	gotSystem string
	gotUser   string
}

func (f *fakeLLM) Ask(ctx context.Context, system, user string) (string, error) {
	f.gotSystem = system
	f.gotUser = user
	if f.onSystem != nil {
		return f.onSystem(system), nil
	}
	return f.resp, nil
}

// fakeJail is a deterministic stand-in for adequacy.Jail: it keys its
// verdict on both the command (build vs test, by cmd[1]) and the code
// content at "auth.go".
type fakeJail struct {
	onRun func(files map[string]string, cmd []string) bool
}

func (fj *fakeJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return fj.onRun(files, cmd), nil
}

func TestStageCandidate(t *testing.T) {
	writer := &fakeLLM{onSystem: func(sys string) string {
		if strings.Contains(sys, "TEST-WRITER") {
			return "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"
		}
		return "===MUTATION_1===\nOK m1\n===MUTATION_2===\nBAD m2\n===MUTATION_3===\nOK m3\n"
	}}
	reviewer := &fakeLLM{resp: "MUTANT m3: GAP: the test misses the m3 path\n"}
	jail := &fakeJail{onRun: func(files map[string]string, cmd []string) bool {
		code := files["auth.go"]
		if cmd[1] == "build" {
			return !strings.Contains(code, "BAD") // m2 discarded
		}
		return code == "COMPLIANT" || strings.Contains(code, "m3") // m1 killed, m3 survives
	}}
	store, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	req := StageRequest{
		Request: authoring.Request{
			Goal: "deny by default", Code: "COMPLIANT", Lang: "go", CodePath: "auth.go", TestPath: "auth_gate_test.go",
			Base: map[string]string{"go.mod": "module target\ngo 1.26\n"}, NMutants: 3,
			BuildCmd: []string{"go", "build", "./"}, TestCmd: []string{"go", "test", "./"},
		},
		Owner: "ciso@bankz", GoalID: "asvs-v4.1.1", Target: "bankz/app:auth.go", Now: time.Unix(1_700_000_000, 0).UTC(),
	}
	gt, err := StageCandidate(context.Background(), writer, reviewer, jail, store, req)
	if err != nil {
		t.Fatal(err)
	}

	// stored UNVETTED with the right evidence
	if gt.Vetted {
		t.Fatal("staged candidate must be unvetted")
	}
	pend, _ := store.ListPending("ciso@bankz")
	if len(pend) != 1 || pend[0].Goal != "asvs-v4.1.1" {
		t.Fatalf("not staged: %+v", pend)
	}
	if pend[0].KillRate < 0.49 || pend[0].KillRate > 0.51 {
		t.Errorf("kill rate = %v, want 0.5", pend[0].KillRate)
	}
	// the survivor (m3) was triaged and the verdict persisted as JSON
	if !strings.Contains(pend[0].VerdictsJSON, "m3") || !strings.Contains(pend[0].VerdictsJSON, "RealGap") {
		t.Errorf("verdict not stored: %q", pend[0].VerdictsJSON)
	}
	// reviewer got an INDEPENDENT seat (its own prompt) and saw the survivor m3
	if !strings.Contains(reviewer.gotSystem, "TEST-REVIEWER") || !strings.Contains(reviewer.gotUser, "MUTANT m3") {
		t.Errorf("reviewer not called independently on the survivor: sys=%q", reviewer.gotSystem)
	}
}
