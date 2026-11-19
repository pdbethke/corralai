// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

type fakeLLM struct {
	response string
	called   bool
	system   string
	prompt   string
}

func (f *fakeLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	f.called = true
	f.system, f.prompt = system, prompt
	return f.response, nil
}

func (f *fakeLLM) Available() bool { return true }

type fakePerf struct {
	stats []ModelStats
}

func (f *fakePerf) GetRoleModelStats() []ModelStats {
	return f.stats
}

func TestStaffingSense(t *testing.T) {
	mgr := &StaffingManager{}
	res := mgr.Sense()
	if res.CPUCores <= 0 {
		t.Errorf("expected CPUCores > 0, got %d", res.CPUCores)
	}
	if res.TotalRAMGB <= 0 {
		t.Errorf("expected TotalRAMGB > 0, got %f", res.TotalRAMGB)
	}
}

func TestStaffingJudgeAndClamp(t *testing.T) {
	llm := &fakeLLM{
		response: `{
			"role_assignments": {
				"security-breaker": "qwen2.5-coder:14b",
				"correctness-reviewer": "qwen2.5-coder:7b",
				"exploit-attempter": "anthropic/claude-sonnet-5"
			},
			"load_order": ["qwen2.5-coder:14b", "qwen2.5-coder:7b"]
		}`,
	}
	perf := &fakePerf{
		stats: []ModelStats{
			{Model: "qwen2.5-coder:14b", Role: "security-breaker", TasksCompleted: 10, AvgTaskDuration: 15.0, ExecPassRatePct: 90.0},
		},
	}
	mgr := &StaffingManager{Perf: perf, LLM: llm, RoleModels: rolemodel.New()}

	resources := WorkstationResources{
		CPUCores:       8,
		TotalRAMGB:     32.0,
		GPUVRAMGB:      12.0, // fits 14b (9.8G) or 7b (4.9G) but not both (14.7G total)
		PulledModels:   []string{"qwen2.5-coder:14b", "qwen2.5-coder:7b", "llama3.2:3b"},
		DeclaredModels: []rolemodel.ModelRef{{Backend: "anthropic", Model: "claude-sonnet-5"}},
	}

	assignments, _, err := mgr.Judge(context.Background(), "certify a dashboard change", resources, perf.GetRoleModelStats(), 3, 3)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !llm.called {
		t.Fatal("Generate was not called")
	}

	clamped := mgr.Clamp(assignments, resources)

	// 14b + 7b = 14.7G > 12G, so local roles consolidate onto the smallest
	// model the PLANNER proposed (7b) — not the smaller llama3.2:3b it never
	// chose, and not a model picked for its name.
	want := rolemodel.ModelRef{Backend: "ollama", Model: "qwen2.5-coder:7b"}
	for _, role := range []string{"security-breaker", "correctness-reviewer"} {
		if clamped[role] != want {
			t.Errorf("%s = %+v, want %+v", role, clamped[role], want)
		}
	}
	// The declared cloud model keeps its seat and the backend it was declared with.
	if got := clamped["exploit-attempter"]; got != (rolemodel.ModelRef{Backend: "anthropic", Model: "claude-sonnet-5"}) {
		t.Errorf("exploit-attempter = %+v, want the declared anthropic ref", got)
	}
}

// TestBuildLeaderboardBrief is the exploration guard's deterministic core: the
// staffing brief must be honest about thin data (so an n=2 winner isn't treated
// as a ranking) and surface untested eligible models as probe candidates (so the
// planner can explore instead of ossifying around an early leader).
func TestBuildLeaderboardBrief(t *testing.T) {
	// Cold start: no stats → say so, don't fabricate a ranking.
	if got := buildLeaderboardBrief(nil, []string{"qwen2.5-coder:7b"}, 5); !strings.Contains(got, "cold start") {
		t.Fatalf("empty stats should announce cold start, got %q", got)
	}

	stats := []ModelStats{
		{Model: "qwen2.5-coder:7b", Role: "tester", TasksCompleted: 20, ExecPassRatePct: 95},
		{Model: "llama3.2:3b", Role: "tester", TasksCompleted: 2, ExecPassRatePct: 100}, // thin data
	}
	eligible := []string{"qwen2.5-coder:7b", "llama3.2:3b", "deepseek-coder:6.7b"} // deepseek untested for tester
	brief := buildLeaderboardBrief(stats, eligible, 5)

	if !strings.Contains(brief, "qwen2.5-coder:7b as tester") || !strings.Contains(brief, "confident") {
		t.Fatalf("a 20-task cell should read as confident:\n%s", brief)
	}
	if !strings.Contains(brief, "THIN") {
		t.Fatalf("a 2-task cell must be flagged THIN, not treated as a ranking:\n%s", brief)
	}
	if !strings.Contains(brief, "deepseek-coder:6.7b") || !strings.Contains(strings.ToLower(brief), "probe") {
		t.Fatalf("an untested eligible model must surface as a probe candidate:\n%s", brief)
	}
}
