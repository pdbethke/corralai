// SPDX-License-Identifier: Elastic-2.0
package mission

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// The staffing planner is a model; what it proposes is a self-report, never a
// declaration. These tests pin that a proposal is assignable only when it is a
// candidate EXACTLY as listed, and that the backend comes from where the
// candidate was found — never from the model's spelling (founder ruling
// 2026-11-12: "any guessing is a security hole").

func candidatesFixture() WorkstationResources {
	return WorkstationResources{
		PulledModels:   []string{"gpt-oss:20b", "qwen2.5-coder:7b"},
		DeclaredModels: []rolemodel.ModelRef{{Backend: "anthropic", Model: "claude-sonnet-5"}},
	}
}

func TestClampNeverReadsAVendorFromAName(t *testing.T) {
	proposals := map[string]string{
		// A local model whose name starts "gpt": the old staffedModelRef sent it to openai.
		"listed-gpt-name": "gpt-oss:20b",
		"declared-cloud":  "anthropic/claude-sonnet-5",
		// Each of these the old code staffed onto a vendor by its spelling.
		"bare-claude":    "claude-sonnet-5",
		"bare-gpt":       "gpt-4o",
		"bare-gemini":    "gemini-3.6-flash",
		"undeclared-vm":  "openai/gpt-oss:20b",
		"prefix-of-pull": "qwen2.5-coder", // the old Clamp matched pulled models by prefix
	}
	got := (&StaffingManager{}).Clamp(proposals, candidatesFixture())

	want := map[string]rolemodel.ModelRef{
		"listed-gpt-name": {Backend: "ollama", Model: "gpt-oss:20b"},
		"declared-cloud":  {Backend: "anthropic", Model: "claude-sonnet-5"},
	}
	if len(got) != len(want) {
		t.Errorf("Clamp staffed %d roles, want %d: %+v", len(got), len(want), got)
	}
	for role, w := range want {
		if got[role] != w {
			t.Errorf("%s = %+v, want %+v", role, got[role], w)
		}
	}
	for _, role := range []string{"bare-claude", "bare-gpt", "bare-gemini", "undeclared-vm", "prefix-of-pull"} {
		if ref, ok := got[role]; ok {
			t.Errorf("%s (%q) was staffed as %+v; a name that is not a candidate must leave the role unstaffed", role, proposals[role], ref)
		}
	}
}

func TestClampFillsNoRoleTheplannerSkipped(t *testing.T) {
	got := (&StaffingManager{}).Clamp(map[string]string{}, candidatesFixture())
	if len(got) != 0 {
		t.Fatalf("Clamp filled roles nobody proposed: %+v (it used to stand a pulled qwen in for security-breaker and correctness-reviewer)", got)
	}
}

func TestJudgeShowsTheCandidatesAndNoExampleModels(t *testing.T) {
	llm := &fakeLLM{response: `{"role_assignments":{},"load_order":[]}`}
	mgr := &StaffingManager{LLM: llm}
	if _, _, err := mgr.Judge(context.Background(), "d", candidatesFixture(), nil, 3, 3); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	for _, want := range []string{"gpt-oss:20b", "qwen2.5-coder:7b", "anthropic/claude-sonnet-5"} {
		if !strings.Contains(llm.prompt, want) {
			t.Errorf("prompt does not offer candidate %q:\n%s", want, llm.prompt)
		}
	}
	// The prompt used to name models and vendors for the planner to reach for
	// (qwen2.5-coder:7b and llama3.2:3b as cold-start defaults; "gemini,
	// claude, gpt" as cloud APIs) whatever the operator had.
	all := llm.system + llm.prompt
	for _, banned := range []string{"llama3.2:3b", "llama-3b", "qwen-coder", "gemini", "default models"} {
		if strings.Contains(strings.ToLower(all), strings.ToLower(banned)) {
			t.Errorf("planner text names %q, which the operator did not declare", banned)
		}
	}
}

func TestJudgeRefusesWithNoCandidates(t *testing.T) {
	llm := &fakeLLM{response: `{}`}
	_, _, err := (&StaffingManager{LLM: llm}).Judge(context.Background(), "d", WorkstationResources{}, nil, 3, 3)
	if err == nil || !strings.Contains(err.Error(), "OLLAMA_URL") || !strings.Contains(err.Error(), "CORRALAI_ROLE_MODELS") {
		t.Fatalf("Judge with no candidates: err = %v, want a refusal naming OLLAMA_URL and CORRALAI_ROLE_MODELS", err)
	}
	if llm.called {
		t.Fatal("the planner was asked to staff with nothing to choose from")
	}
}

func TestQueryOllamaHasNoDefaultAddress(t *testing.T) {
	t.Setenv("OLLAMA_URL", "")
	if pulled, loaded := queryOllama(); pulled != nil || loaded != nil {
		t.Fatalf("queryOllama with OLLAMA_URL unset listed %v / %v; it used to fall back to 127.0.0.1:11434", pulled, loaded)
	}
}

func TestDeclaredFromKeepsOnlyRefsThatNameABackend(t *testing.T) {
	p, bad := rolemodel.Parse("a=anthropic:claude-x,b=qwen-bare,c=anthropic:claude-x,d=ollama:qwen2.5-coder:7b")
	if len(bad) != 0 {
		t.Fatalf("Parse: bad %v", bad)
	}
	got := DeclaredFrom(p)
	want := []rolemodel.ModelRef{{Backend: "anthropic", Model: "claude-x"}, {Backend: "ollama", Model: "qwen2.5-coder:7b"}}
	if len(got) != len(want) {
		t.Fatalf("DeclaredFrom = %+v, want %+v (a bare role=model names no backend, so it is no source; duplicates collapse)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DeclaredFrom[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestStaffSetsTheBackendItFoundTheModelIn runs the whole pass — Sense against
// a fake OLLAMA_URL daemon, Judge, Clamp, apply — and checks what reaches the
// role policy, which is what spawn hands corral-agent as MODEL_BACKEND.
func TestStaffSetsTheBackendItFoundTheModelIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "gpt-oss:20b"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
		}
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_URL", srv.URL)

	llm := &fakeLLM{response: `{"role_assignments":{
		"security-breaker":"gpt-oss:20b",
		"correctness-reviewer":"anthropic/claude-x",
		"edge-hunter":"gpt-4o"}}`}
	policy := rolemodel.New()
	mgr := &StaffingManager{LLM: llm, Perf: &fakePerf{}, RoleModels: policy,
		Declared: []rolemodel.ModelRef{{Backend: "anthropic", Model: "claude-x"}}}
	if err := mgr.Staff("d"); err != nil {
		t.Fatalf("Staff: %v", err)
	}
	if ref, ok := policy.Lookup("security-breaker"); !ok || ref != (rolemodel.ModelRef{Backend: "ollama", Model: "gpt-oss:20b"}) {
		t.Errorf("security-breaker = %+v (ok=%v), want the ollama daemon that listed it", ref, ok)
	}
	if ref, ok := policy.Lookup("correctness-reviewer"); !ok || ref != (rolemodel.ModelRef{Backend: "anthropic", Model: "claude-x"}) {
		t.Errorf("correctness-reviewer = %+v (ok=%v), want the declared anthropic ref", ref, ok)
	}
	if ref, ok := policy.Lookup("edge-hunter"); ok {
		t.Errorf("edge-hunter staffed as %+v from an undeclared name", ref)
	}
}

// A pass whose required roles end up empty is a failure, not a success that
// applied nothing (it used to return nil).
func TestStaffFailsWhenARequiredRoleIsLeftEmpty(t *testing.T) {
	llm := &fakeLLM{response: `{"role_assignments":{"security-breaker":"m","correctness-reviewer":"gpt-4o"}}`}
	policy := rolemodel.New()
	mgr := &StaffingManager{LLM: llm, Perf: &fakePerf{}, RoleModels: policy,
		Declared: []rolemodel.ModelRef{{Backend: "ollama", Model: "m"}}}
	t.Setenv("OLLAMA_URL", "")
	err := mgr.Staff("d")
	if err == nil || !strings.Contains(err.Error(), "correctness-reviewer") {
		t.Fatalf("Staff = %v, want a failure naming correctness-reviewer", err)
	}
	if policy.Len() != 0 {
		t.Fatalf("a failed pass applied %v", policy.Snapshot())
	}
}

func TestEstimateVRAMReadsTheSizeOrSaysUnknown(t *testing.T) {
	cases := []struct {
		tag   string
		gb    float64
		known bool
	}{
		{"qwen2.5-coder:7b", 4.9, true},
		{"gpt-oss:20b", 14, true}, // the old suffix list had no 20b and costed it as 7b
		{"qwen3.6:35b-a3b", 24.5, true},
		{"qwen2.5:1.5b", 1.05, true},
		{"llama3.2:3b", 2.1, true},
		{"mistral:latest", 0, false}, // used to be assumed 7B
		{"claude-x", 0, false},
	}
	for _, c := range cases {
		gb, known := estimateVRAM(c.tag)
		if known != c.known || (known && (gb < c.gb-0.01 || gb > c.gb+0.01)) {
			t.Errorf("estimateVRAM(%q) = %.2f,%v; want %.2f,%v", c.tag, gb, known, c.gb, c.known)
		}
	}
}

func TestClampDoesNotConsolidateOnAnUnknownSize(t *testing.T) {
	res := WorkstationResources{GPUVRAMGB: 8, PulledModels: []string{"qwen2.5-coder:7b", "mistral:latest", "qwen2.5-coder:14b"}}
	got := (&StaffingManager{}).Clamp(map[string]string{
		"security-breaker": "qwen2.5-coder:14b", "correctness-reviewer": "mistral:latest", "edge-hunter": "qwen2.5-coder:7b",
	}, res)
	if got["security-breaker"].Model != "qwen2.5-coder:14b" || got["correctness-reviewer"].Model != "mistral:latest" {
		t.Fatalf("Clamp consolidated on a size it could not read: %+v", got)
	}
}

func TestClampLeavesADeclaredOllamaRefOnItsDeclaredServer(t *testing.T) {
	declared := rolemodel.ModelRef{Backend: "ollama", Model: "big:70b"}
	res := WorkstationResources{GPUVRAMGB: 8, PulledModels: []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b"},
		DeclaredModels: []rolemodel.ModelRef{declared}}
	got := (&StaffingManager{}).Clamp(map[string]string{
		"security-breaker": "qwen2.5-coder:14b", "correctness-reviewer": "qwen2.5-coder:7b", "edge-hunter": "ollama/big:70b",
	}, res)
	if got["edge-hunter"] != declared {
		t.Errorf("declared ref rewritten to %+v", got["edge-hunter"])
	}
	if got["security-breaker"].Model != "qwen2.5-coder:7b" {
		t.Errorf("listed 14b+7b over 8 GB not consolidated: %+v", got)
	}
}

func TestCandidateCollisionIsNoCandidate(t *testing.T) {
	res := WorkstationResources{PulledModels: []string{"anthropic/x"}, DeclaredModels: []rolemodel.ModelRef{{Backend: "anthropic", Model: "x"}}}
	if _, ok := candidateRefs(res)["anthropic/x"]; ok {
		t.Fatal("a name that is both a listed local tag and a declared key was kept as a candidate")
	}
}

func TestStatsByCandidateCreditsTheDeclaredKey(t *testing.T) {
	res := WorkstationResources{
		PulledModels:   []string{"qwen2.5-coder:7b"},
		DeclaredModels: []rolemodel.ModelRef{{Backend: "anthropic", Model: "claude-x"}, {Backend: "openai", Model: "dup"}, {Backend: "google", Model: "dup"}},
	}
	got := statsByCandidate([]ModelStats{{Model: "claude-x"}, {Model: "qwen2.5-coder:7b"}, {Model: "dup"}}, res)
	want := []string{"anthropic/claude-x", "qwen2.5-coder:7b", "dup"}
	for i, w := range want {
		if got[i].Model != w {
			t.Errorf("row %d = %q, want %q", i, got[i].Model, w)
		}
	}
}
