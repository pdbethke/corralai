// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// goP is the single source of truth for the go plugin's system prompts used
// across this test file — internal/lang/go.go's goPlugin.TestWriterSystem()
// / MutantSystem(). testgen_test.go is package testgen (not testgen_test), and
// internal/lang does not import internal/testgen, so this import is cycle-free.
var goP, _ = lang.ByName("go")

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
	out, err := WriteTest(context.Background(), f, goP.TestWriterSystem(), "passwords >= 12 chars", "package target\nfunc ValidatePassword(pw string) error { return nil }", sigs)
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
	if _, err := WriteTest(context.Background(), &fakeLLM{resp: "   "}, goP.TestWriterSystem(), "g", "c", nil); err == nil {
		t.Fatal("empty model response must error")
	}
}

func TestGenerateMutants(t *testing.T) {
	code := "package target\nfunc F() int { return 1 }\n"
	f := &fakeLLM{resp: srBlock("1", "return 1", "return 9") + srBlock("2", "return 1", "return -1")}
	muts, err := GenerateMutants(context.Background(), f, goP.MutantSystem(), "F returns >0", code, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 2 || muts[0].ID != "m1" {
		t.Fatalf("mutants wrong: %+v", muts)
	}
	// The hunks were applied to the original, producing full mutant files.
	if !strings.Contains(muts[0].Code, "return 9") || !strings.Contains(muts[0].Code, "package target") {
		t.Errorf("m1 should be the full file with the applied edit: %q", muts[0].Code)
	}
	if !strings.Contains(f.gotUser, "2 distinct") { // instruction carried the count
		t.Errorf("generator prompt missing the count instruction: %s", f.gotUser)
	}
}

func TestGenerateMutantsNoneErrors(t *testing.T) {
	if _, err := GenerateMutants(context.Background(), &fakeLLM{resp: "no markers here"}, goP.MutantSystem(), "g", "c", nil, 3); err == nil {
		t.Fatal("unparseable response must error")
	}
}

// TestWriteTestPromptUnchanged pins the exact prompt WriteTestPrompt renders
// so this refactor (extracting it out of WriteTest) cannot drift the text a
// distributed worker will later send to its own model.
func TestWriteTestPromptUnchanged(t *testing.T) {
	sigs := []repoindex.Signature{{Name: "Add", Kind: "func", Results: []string{"int"}, Exported: true}}
	sys, usr := WriteTestPrompt(goP.TestWriterSystem(), "cover Add", "func Add(a,b int)int{return a+b}", sigs)
	if sys != goP.TestWriterSystem() {
		t.Fatalf("system prompt drifted:\ngot:  %q\nwant: %q", sys, goP.TestWriterSystem())
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
	sys, usr := GenerateMutantsPrompt(goP.MutantSystem(), "F returns >0", "func F() int { return 1 }", sigs, 3)
	if sys != goP.MutantSystem() {
		t.Fatalf("system prompt drifted:\ngot:  %q\nwant: %q", sys, goP.MutantSystem())
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
	// The system prompt must NO LONGER ask for whole-file copies (they don't
	// scale) — the output format is the centralized SEARCH/REPLACE spec, in the
	// user prompt.
	if strings.Contains(sys, "COMPLETE file") || strings.Contains(sys, "<complete file>") {
		t.Error("MutantSystem must not still instruct whole-file mutants — the scalable SEARCH/REPLACE format is centralized in the task")
	}
	for _, want := range []string{"GOAL:\nF returns >0", "func F() int { return 1 }", `"Name":"F"`, "Produce exactly 3 distinct mutations.", "<<<<<<< SEARCH", ">>>>>>> REPLACE"} {
		if !strings.Contains(usr, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, usr)
		}
	}
}

func TestParseMutantsOutput(t *testing.T) {
	orig := "package target\nfunc F() int { return 1 }\n"
	raw := srBlock("1", "return 1", "return 9") + srBlock("2", "return 1", "return 8")
	muts, err := ParseMutantsOutput(raw, orig)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) != 2 || muts[0].ID != "m1" || muts[1].ID != "m2" {
		t.Fatalf("mutants wrong: %+v", muts)
	}
	if !strings.Contains(muts[0].Code, "return 9") || !strings.Contains(muts[1].Code, "return 8") {
		t.Errorf("hunks not applied to the original: %+v", muts)
	}
	// Tamper-evident link to the exact original, identical across its mutants.
	if muts[0].ParentSHA256 == "" || muts[0].ParentSHA256 != muts[1].ParentSHA256 {
		t.Errorf("ParentSHA256 must be set and identical: %q / %q", muts[0].ParentSHA256, muts[1].ParentSHA256)
	}
}

func TestParseMutantsOutputMalformedErrors(t *testing.T) {
	if _, err := ParseMutantsOutput("no markers here", "code"); err == nil {
		t.Fatal("unparseable response must error")
	}
}
