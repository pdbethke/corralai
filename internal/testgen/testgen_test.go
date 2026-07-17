// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// wantGoTestWriterSystem/wantGoMutantSystem are byte-for-byte copies of the
// go plugin's internal/lang goPlugin.TestWriterSystem() / MutantSystem().
// testgen_test.go cannot import internal/lang: lang -> internal/controlgate
// -> internal/testgen (stage.go) -> (test binary) -> internal/lang would be
// an import cycle in the test build (controlgate already imports testgen;
// lang already imports controlgate for LangScaffold). These are test-only
// fixtures pinning what the go plugin promises; internal/lang/go_test.go
// separately pins the plugin's own strings, so a drift between the two
// copies is caught on either side.
const wantGoTestWriterSystem = `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`

const wantGoMutantSystem = `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same signature and package (a drop-in replacement that compiles) and must genuinely fail the goal — vary HOW it fails. No no-ops, no compile errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`

// fakeLLM records the system/user prompts it was asked and returns a canned
// response — no live model in these tests (spikes already proved output quality;
// this package proves prompt construction and response parsing).
type fakeLLM struct {
	resp               string
	err                error
	gotSystem, gotUser string
	called             bool
}

func (f *fakeLLM) Ask(ctx context.Context, system, user string) (string, error) {
	f.called = true
	f.gotSystem, f.gotUser = system, user
	return f.resp, f.err
}

func TestWriteTest(t *testing.T) {
	f := &fakeLLM{resp: "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"}
	sigs := []repoindex.Signature{{Name: "ValidatePassword", Kind: "func", Params: []repoindex.Param{{Name: "pw", Type: "string"}}, Results: []string{"error"}, Exported: true}}
	out, err := WriteTest(context.Background(), f, wantGoTestWriterSystem, "passwords >= 12 chars", "package target\nfunc ValidatePassword(pw string) error { return nil }", sigs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func TestGoal") || strings.Contains(out, "```") {
		t.Fatalf("unexpected test output: %q", out)
	}
	// prompt construction: the user prompt must carry the goal, the code, and the signature JSON.
	for _, want := range []string{"passwords >= 12 chars", "ValidatePassword", `"Name":"ValidatePassword"`} {
		if !strings.Contains(f.gotUser, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, f.gotUser)
		}
	}
}

func TestWriteTestEmptyResponseErrors(t *testing.T) {
	if _, err := WriteTest(context.Background(), &fakeLLM{resp: "   "}, wantGoTestWriterSystem, "g", "c", nil); err == nil {
		t.Fatal("empty model response must error")
	}
}

func TestGenerateMutants(t *testing.T) {
	f := &fakeLLM{resp: "===MUTATION_1===\npackage target\nfunc F() int { return 9 }\n===MUTATION_2===\npackage target\nfunc F() int { return 8 }\n"}
	muts, err := GenerateMutants(context.Background(), f, wantGoMutantSystem, "F returns >0", "package target\nfunc F() int { return 1 }", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 2 || muts[0].ID != "m1" {
		t.Fatalf("mutants wrong: %+v", muts)
	}
	if !strings.Contains(f.gotUser, "2 distinct") { // instruction carried the count
		t.Errorf("generator prompt missing the count instruction: %s", f.gotUser)
	}
}

func TestGenerateMutantsNoneErrors(t *testing.T) {
	if _, err := GenerateMutants(context.Background(), &fakeLLM{resp: "no markers here"}, wantGoMutantSystem, "g", "c", nil, 3); err == nil {
		t.Fatal("unparseable response must error")
	}
}

// TestWriteTestPromptUnchanged pins the exact prompt WriteTestPrompt renders
// so this refactor (extracting it out of WriteTest) cannot drift the text a
// distributed worker will later send to its own model.
func TestWriteTestPromptUnchanged(t *testing.T) {
	sigs := []repoindex.Signature{{Name: "Add", Kind: "func", Results: []string{"int"}, Exported: true}}
	sys, usr := WriteTestPrompt(wantGoTestWriterSystem, "cover Add", "func Add(a,b int)int{return a+b}", sigs)
	if sys != wantGoTestWriterSystem {
		t.Fatalf("system prompt drifted:\ngot:  %q\nwant: %q", sys, wantGoTestWriterSystem)
	}
	if !strings.Contains(sys, "You are a TEST-WRITER.") {
		t.Fatal("system prompt drifted")
	}
	for _, want := range []string{"GOAL:\ncover Add", "func Add(a,b int)int{return a+b}", `"Name":"Add"`} {
		if !strings.Contains(usr, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, usr)
		}
	}
}

func TestParseTestOutputStripsFences(t *testing.T) {
	got := ParseTestOutput("```go\npackage x\n```")
	if strings.Contains(got, "```") {
		t.Errorf("fences not stripped: %q", got)
	}
}

// TestGenerateMutantsPromptUnchanged pins the exact prompt GenerateMutantsPrompt
// renders so this refactor (extracting it out of GenerateMutants) cannot drift
// the text a distributed worker will later send to its own model.
func TestGenerateMutantsPromptUnchanged(t *testing.T) {
	sigs := []repoindex.Signature{{Name: "F", Kind: "func", Results: []string{"int"}, Exported: true}}
	sys, usr := GenerateMutantsPrompt(wantGoMutantSystem, "F returns >0", "func F() int { return 1 }", sigs, 3)
	if sys != wantGoMutantSystem {
		t.Fatalf("system prompt drifted:\ngot:  %q\nwant: %q", sys, wantGoMutantSystem)
	}
	if !strings.Contains(sys, "You are a MUTATION-TESTING ENGINE.") {
		t.Fatal("system prompt drifted")
	}
	// The QA framing that fixed the safety-refusal must stay: it's load-bearing,
	// not cosmetic (see genMutantsSystem's comment). Guard the intent so a future
	// "tidy-up" can't silently re-introduce attack-sounding phrasing.
	for _, want := range []string{"MUTATION-TESTING ENGINE", "never deployed", "GAP IN THE TESTS"} {
		if !strings.Contains(sys, want) {
			t.Errorf("mutation-testing framing lost the phrase %q — this is what keeps safety-aligned models from refusing", want)
		}
	}
	for _, want := range []string{"GOAL:\nF returns >0", "func F() int { return 1 }", `"Name":"F"`, "Produce exactly 3 distinct mutations."} {
		if !strings.Contains(usr, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, usr)
		}
	}
}

func TestParseMutantsOutput(t *testing.T) {
	raw := "===MUTATION_1===\npackage target\nfunc F() int { return 9 }\n===MUTATION_2===\npackage target\nfunc F() int { return 8 }\n"
	muts, err := ParseMutantsOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []adequacy.Mutant{
		{ID: "m1", Code: "package target\nfunc F() int { return 9 }"},
		{ID: "m2", Code: "package target\nfunc F() int { return 8 }"},
	}
	if len(muts) != len(want) {
		t.Fatalf("mutants wrong: %+v", muts)
	}
	for i := range want {
		if muts[i] != want[i] {
			t.Errorf("mutant %d = %+v, want %+v", i, muts[i], want[i])
		}
	}
}

func TestParseMutantsOutputMalformedErrors(t *testing.T) {
	if _, err := ParseMutantsOutput("no markers here"); err == nil {
		t.Fatal("unparseable response must error")
	}
}
