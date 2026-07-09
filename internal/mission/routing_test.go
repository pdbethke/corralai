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
}

func (f *fakeLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	f.called = true
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
				"builder": "qwen2.5-coder:14b",
				"tester": "qwen2.5-coder:7b",
				"pentester": "claude-3-5-sonnet"
			},
			"load_order": ["qwen2.5-coder:14b", "qwen2.5-coder:7b"]
		}`,
	}
	perf := &fakePerf{
		stats: []ModelStats{
			{Model: "qwen2.5-coder:14b", Role: "builder", TasksCompleted: 10, AvgTaskDuration: 15.0, ExecPassRatePct: 90.0},
		},
	}
	policy := rolemodel.New()

	mgr := &StaffingManager{
		Perf:       perf,
		LLM:        llm,
		RoleModels: policy,
	}

	resources := WorkstationResources{
		CPUCores:     8,
		TotalRAMGB:   32.0,
		GPUVRAMGB:    12.0, // fits 14b (9.8G) or 7b (4.9G) but not both (14.7G total)
		PulledModels: []string{"qwen2.5-coder:14b", "qwen2.5-coder:7b"},
	}

	assignments, _, err := mgr.Judge(context.Background(), "build a dashboard", resources, perf.GetRoleModelStats(), 3, 3)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !llm.called {
		t.Fatal("Generate was not called")
	}

	clamped := mgr.Clamp(assignments, resources)

	// Since 14b (9.8G) + 7b (4.9G) = 14.7G, which is > 12.0GB GPU VRAM,
	// Clamp should consolidate local roles to default model (qwen2.5-coder:7b).
	if clamped["builder"] != "qwen2.5-coder:7b" {
		t.Errorf("expected builder clamped to default qwen2.5-coder:7b due to VRAM limits, got %q", clamped["builder"])
	}
	if clamped["tester"] != "qwen2.5-coder:7b" {
		t.Errorf("expected tester clamped to qwen2.5-coder:7b, got %q", clamped["tester"])
	}
	// pentester is a cloud model, should remain unchanged
	if clamped["pentester"] != "claude-3-5-sonnet" {
		t.Errorf("expected pentester to remain claude-3-5-sonnet, got %q", clamped["pentester"])
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
		{Model: "qwen2.5-coder:7b", Role: "builder", TasksCompleted: 20, ExecPassRatePct: 95},
		{Model: "llama3.2:3b", Role: "builder", TasksCompleted: 2, ExecPassRatePct: 100}, // thin data
	}
	eligible := []string{"qwen2.5-coder:7b", "llama3.2:3b", "deepseek-coder:6.7b"} // deepseek untested for builder
	brief := buildLeaderboardBrief(stats, eligible, 5)

	if !strings.Contains(brief, "qwen2.5-coder:7b as builder") || !strings.Contains(brief, "confident") {
		t.Fatalf("a 20-task cell should read as confident:\n%s", brief)
	}
	if !strings.Contains(brief, "THIN") {
		t.Fatalf("a 2-task cell must be flagged THIN, not treated as a ranking:\n%s", brief)
	}
	if !strings.Contains(brief, "deepseek-coder:6.7b") || !strings.Contains(strings.ToLower(brief), "probe") {
		t.Fatalf("an untested eligible model must surface as a probe candidate:\n%s", brief)
	}
}
