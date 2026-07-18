// SPDX-License-Identifier: Elastic-2.0

package authoring

import (
	"context"
	"strings"
	"testing"
)

// fakeLLM is a deterministic stand-in for testgen.LLM: it keys its response
// on the system prompt so a single fake can serve both WriteTest (system
// contains "TEST-WRITER") and GenerateMutants (system contains
// "SEEDED-VIOLATION GENERATOR").
type fakeLLM struct {
	onSystem func(sys string) string
}

func (f *fakeLLM) Ask(ctx context.Context, system, user string) (string, error) {
	return f.onSystem(system), nil
}

// fakeAuthorJail is a deterministic stand-in for adequacy.Jail: it keys its
// verdict on both the command (build vs test, by cmd[1]) and the code
// content at "auth.go", so it can drive compileVerify and adequacy.Score
// with different rules from the same object.
type fakeAuthorJail struct {
	onRun func(files map[string]string, cmd []string) bool
}

func (fj *fakeAuthorJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return fj.onRun(files, cmd), nil
}

func TestAuthor(t *testing.T) {
	// fake LLM: WriteTest gets writeTestSystem → returns a test; GenerateMutants gets
	// genMutantsSystem → returns 3 mutants (one won't compile).
	m := &fakeLLM{onSystem: func(sys string) string {
		if strings.Contains(sys, "TEST-WRITER") {
			return "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"
		}
		// SEARCH/REPLACE hunks applied to the "COMPLIANT" original: each yields a
		// mutant whose full content is "OK m1" / "BAD m2" / "OK m3" (what the fake
		// jail keys on). "BAD" won't build, so m2 is discarded before scoring.
		return "===MUTATION_1===\n<<<<<<< SEARCH\nCOMPLIANT\n=======\nOK m1\n>>>>>>> REPLACE\n" +
			"===MUTATION_2===\n<<<<<<< SEARCH\nCOMPLIANT\n=======\nBAD m2\n>>>>>>> REPLACE\n" +
			"===MUTATION_3===\n<<<<<<< SEARCH\nCOMPLIANT\n=======\nOK m3\n>>>>>>> REPLACE\n"
	}}
	// fake jail: compile-verify (build cmd) → true unless code contains "BAD";
	// score (test cmd) → test passes on COMPLIANT and on m3 (survivor), fails on m1 (killed).
	jail := &fakeAuthorJail{onRun: func(files map[string]string, cmd []string) bool {
		code := files["auth.go"]
		if cmd[1] == "build" {
			return !strings.Contains(code, "BAD")
		}
		// test cmd: pass (true) on compliant + m3; fail (false) on m1
		return code == "COMPLIANT" || strings.Contains(code, "m3")
	}}
	req := Request{
		Goal: "g", Code: "COMPLIANT", Lang: "go", CodePath: "auth.go", TestPath: "auth_gate_test.go",
		Base: map[string]string{"go.mod": "module target\ngo 1.26\n"}, NMutants: 3,
		BuildCmd: []string{"go", "build", "./"}, TestCmd: []string{"go", "test", "./"},
	}
	res, err := Author(context.Background(), m, jail, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Test, "func TestGoal") {
		t.Errorf("test not returned: %q", res.Test)
	}
	// m2 (BAD) discarded as non-compiling — NOT scored; only m1,m3 reach Score.
	if len(res.Discarded) != 1 || res.Discarded[0] != "m2" {
		t.Errorf("discarded = %v, want [m2]", res.Discarded)
	}
	if !res.Report.CompliantPass || res.Report.Total != 2 {
		t.Fatalf("report scored the wrong mutant set: %+v", res.Report)
	}
	// m1 killed (test failed on it), m3 survived (test passed on it) → kill rate 0.5
	if kr := res.Report.KillRate(); kr < 0.49 || kr > 0.51 {
		t.Errorf("kill rate = %v, want 0.5 (m2 must not inflate it)", kr)
	}
	// Result.Mutants must be the valid, compile-verified mutants that were
	// scored — [m1 m3], NOT the discarded non-compiling m2 — so a caller can
	// recover a survivor's (m3's) code by ID.
	if len(res.Mutants) != 2 || res.Mutants[0].ID != "m1" || res.Mutants[1].ID != "m3" {
		t.Fatalf("Result.Mutants should be the valid scored mutants [m1 m3], got %+v", res.Mutants)
	}
}

func TestAuthorUnsupportedLang(t *testing.T) {
	_, err := Author(context.Background(), &fakeLLM{}, &fakeAuthorJail{}, Request{Lang: "cobol", Code: "x"})
	if err == nil {
		t.Fatal("unsupported language must error before calling the model")
	}
}
