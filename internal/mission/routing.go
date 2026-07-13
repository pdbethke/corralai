// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

type HostInfo struct {
	Agent   string
	Role    string
	Model   string
	Backend string
	TS      int64
}

type ModelStats struct {
	Model           string
	Role            string
	TasksCompleted  int
	AvgTaskDuration float64
	ExecPassRatePct float64
}

type HostTracker interface {
	ListHosts() []HostInfo
}

type PerformanceTracker interface {
	GetRoleModelStats() []ModelStats
}

type LLMClient interface {
	Generate(ctx context.Context, system, prompt string) (string, error)
	Available() bool
}

type WorkstationResources struct {
	CPUCores     int      `json:"cpu_cores"`
	TotalRAMGB   float64  `json:"total_ram_gb"`
	GPUVRAMGB    float64  `json:"gpu_vram_gb"`
	PulledModels []string `json:"pulled_models"`
	LoadedModels []string `json:"loaded_models"`
}

type StaffingManager struct {
	Perf       PerformanceTracker
	LLM        LLMClient
	RoleModels *rolemodel.Policy
}

// maxStaffAttempts bounds how many times Staff retries a failing Judge probe
// before giving up: a permanently-failing probe (bad LLM endpoint, malformed
// response) backs off instead of burning a 30s round-trip indefinitely.
const maxStaffAttempts = 3

// staffedModelRef derives the model reference for a clamped assignment. Clamped
// values are model IDENTIFIERS — an Ollama name:tag (qwen2.5-coder:7b) or a cloud
// model name — NEVER "backend:model", so we must not split on ':' (doing so
// corrupted every local tag). Cloud models map to their provider backend; every-
// thing else is an Ollama tag kept whole.
func staffedModelRef(model string) rolemodel.ModelRef {
	backend := "ollama"
	if isCloudModel(model) {
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "claude"):
			backend = "anthropic"
		case strings.Contains(lower, "gpt"):
			backend = "openai"
		case strings.Contains(lower, "gemini"):
			backend = "openai" // NOTE: audit L-item — engine vs backend.go gemini mapping disagree; out of scope for this task, keep as-is
		}
	}
	return rolemodel.ModelRef{Backend: backend, Model: model}
}

// Staff runs one bounded Sense→Judge→Clamp staffing pass for directive and
// applies the clamped result to RoleModels — the standalone equivalent of the
// old engine build-Tick's staffMission driver (now that staffing survives
// without a running engine). It retries a failing Judge probe up to
// maxStaffAttempts times, each bounded by a 30s timeout, and recovers from a
// panicking LLM backend so a broken probe can never take the caller down; it
// gives up (returning the last error) rather than retrying forever.
func (s *StaffingManager) Staff(directive string) error {
	var lastErr error
	for attempt := 1; attempt <= maxStaffAttempts; attempt++ {
		if err := s.staffOnce(directive); err != nil {
			lastErr = err
			log.Printf("staffing: judge probe failed (attempt %d/%d): %v", attempt, maxStaffAttempts, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("staffing: gave up after %d attempts: %w", maxStaffAttempts, lastErr)
}

// staffOnce runs a single Sense→Judge→Clamp pass and, on success, applies the
// clamped assignments to RoleModels. A panic inside Judge (e.g. a broken LLM
// backend) is recovered and surfaced as an error rather than crashing the caller.
func (s *StaffingManager) staffOnce(directive string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("staffing panic recovered: %v", r)
		}
	}()
	resources := s.Sense()
	stats := s.Perf.GetRoleModelStats()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assignments, loadOrder, jErr := s.Judge(ctx, directive, resources, stats, 3, 3)
	if jErr != nil {
		return jErr
	}
	clamped := s.Clamp(assignments, resources)
	log.Printf("staffing complete. Clamped: %+v, Load Order: %v", clamped, loadOrder)
	for role, model := range clamped {
		s.RoleModels.Set(role, staffedModelRef(model)) // threadsafe: the UI writes this same policy from the mission-create handler
	}
	return nil
}

func (s *StaffingManager) Sense() WorkstationResources {
	res := WorkstationResources{
		CPUCores: runtime.NumCPU(),
	}
	res.TotalRAMGB = getSystemRAM()
	res.GPUVRAMGB = getGPUVRAM()
	res.PulledModels, res.LoadedModels = queryOllama()
	return res
}

// minConfidentSamples is the completed-task count below which a leaderboard cell
// is "thin" — a data point, not a verdict. Below it the planner is told not to
// treat the cell as a ranking (cold-start honesty).
const minConfidentSamples = 5

// buildLeaderboardBrief renders the earned model×role evidence for the staffing
// planner: each cell labeled by confidence (so thin data isn't mistaken for a
// ranking), plus untested eligible models per role (probe candidates) so the
// planner can EXPLORE instead of always exploiting an early leader. Deterministic
// — the LLM reasons over this signal; Go just makes it honest. Output is stable
// (roles and models sorted) so the same evidence yields the same brief.
func buildLeaderboardBrief(stats []ModelStats, eligibleModels []string, minSamples int) string {
	if len(stats) == 0 {
		return "No historical performance data yet (cold start) — staff from sensible defaults and treat this mission as evidence-gathering, not as optimizing a ranking that doesn't exist."
	}
	haveData := map[string]map[string]bool{} // role -> model -> seen
	var b strings.Builder
	b.WriteString("Earned leaderboard (from the verify gate; [confidence] in brackets):\n")
	for _, c := range stats {
		conf := "THIN: insufficient evidence, do NOT treat as a ranking"
		if c.TasksCompleted >= minSamples {
			conf = "confident"
		}
		b.WriteString(fmt.Sprintf("- %s as %s: %d tasks, %.0f%% pass, %.1fs avg [%s]\n",
			c.Model, c.Role, c.TasksCompleted, c.ExecPassRatePct, c.AvgTaskDuration, conf))
		if haveData[c.Role] == nil {
			haveData[c.Role] = map[string]bool{}
		}
		haveData[c.Role][c.Model] = true
	}
	roles := make([]string, 0, len(haveData))
	for r := range haveData {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	var probes strings.Builder
	for _, role := range roles {
		var untested []string
		for _, m := range eligibleModels {
			if !haveData[role][m] {
				untested = append(untested, m)
			}
		}
		sort.Strings(untested)
		if len(untested) > 0 {
			probes.WriteString(fmt.Sprintf("- %s: untested → %s\n", role, strings.Join(untested, ", ")))
		}
	}
	if probes.Len() > 0 {
		b.WriteString("\nUntested eligible models (probe candidates — occasionally staff one instead of the leader to keep the leaderboard learning):\n")
		b.WriteString(probes.String())
	}
	return b.String()
}

func (s *StaffingManager) Judge(ctx context.Context, directive string, resources WorkstationResources, stats []ModelStats, thoroughness int, footprint int) (map[string]string, []string, error) {
	if s.LLM == nil || !s.LLM.Available() {
		return nil, nil, fmt.Errorf("LLM client not available")
	}

	brief := buildLeaderboardBrief(stats, resources.PulledModels, minConfidentSamples)

	systemPrompt := `You are the Corralai swarm staffing planner. Analyze the directive, workstation hardware resources, available local/cloud models, and historical performance stats. Propose the optimal role-to-model staffing assignment and load order to run an adversarial-verification swarm that BREAKS the change under review in order to CERTIFY it — not one that builds it.

Respond ONLY with a valid JSON object matching this schema:
{
  "role_assignments": {
    "security-breaker": "model_name",
    "correctness-reviewer": "model_name",
    "exploit-attempter": "model_name",
    "edge-hunter": "model_name"
  },
  "load_order": ["model_name", ...]
}

Constraints:
1. Local models in the load order should fit in available GPU VRAM. Estimate VRAM for a local model as parameter_size * 0.7 GB.
2. Prefer models with high pass rates / low duration — but ONLY for cells marked [confident]. A cell marked [THIN] is a single data point, not a ranking; do not overfit it.
3. If no stats are available (cold-start), use default models (e.g. qwen2.5-coder:7b, llama3.2:3b) and treat the mission as evidence-gathering.
4. Always allocate security-breaker and correctness-reviewer roles.
5. If a local model is not pulled, do not assign it unless it is a cloud model API (e.g. gemini, claude, gpt).
6. EXPLORE, don't ossify: when a role has untested eligible models (probe candidates), occasionally staff one instead of the current leader so the leaderboard keeps learning — especially when the leader's evidence is thin or the alternatives are entirely unmeasured.`

	prompt := fmt.Sprintf(`Directive: %s
Available Resources:
- CPU Cores: %d
- System RAM: %.1f GB
- GPU VRAM: %.1f GB
- Pulled local models: %v
- Currently loaded models: %v

Optimization Weights (1-5 range):
- Thoroughness: %d/5 (higher means prioritize smarter/larger models like sonnet/gemini/qwen-coder; lower means prioritize speed/smaller models like llama-3b)
- Resource Footprint: %d/5 (higher means minimize GPU memory footprint, potentially sharing one model; lower means utilize full available VRAM)

Historical Leaderboard:
%s`, directive, resources.CPUCores, resources.TotalRAMGB, resources.GPUVRAMGB, resources.PulledModels, resources.LoadedModels, thoroughness, footprint, brief)

	resp, err := s.LLM.Generate(ctx, systemPrompt, prompt)
	if err != nil {
		return nil, nil, err
	}

	var result struct {
		RoleAssignments map[string]string `json:"role_assignments"`
		LoadOrder       []string          `json:"load_order"`
	}

	respClean := cleanJSONResponse(resp)
	if err := json.Unmarshal([]byte(respClean), &result); err != nil {
		return nil, nil, fmt.Errorf("parse LLM staffing response: %w: original response: %s", err, resp)
	}

	return result.RoleAssignments, result.LoadOrder, nil
}

func (s *StaffingManager) Clamp(assignments map[string]string, resources WorkstationResources) map[string]string {
	clamped := make(map[string]string)
	defaultModel := "qwen2.5-coder:7b"
	if len(resources.PulledModels) > 0 {
		smallestQwen := ""
		smallestQwenSize := 999.0
		for _, m := range resources.PulledModels {
			if strings.Contains(strings.ToLower(m), "qwen") {
				size := estimateVRAM(m)
				if size < smallestQwenSize {
					smallestQwenSize = size
					smallestQwen = m
				}
			}
		}
		if smallestQwen != "" {
			defaultModel = smallestQwen
		} else {
			defaultModel = resources.PulledModels[0]
		}
	}

	var totalVRAMNeeded float64
	assignedModels := make(map[string]bool)

	for role, model := range assignments {
		if isCloudModel(model) {
			clamped[role] = model
			continue
		}

		pulled := false
		for _, pm := range resources.PulledModels {
			if pm == model || strings.HasPrefix(pm, model) || strings.HasPrefix(model, pm) {
				model = pm
				pulled = true
				break
			}
		}

		if !pulled {
			model = defaultModel
		}

		if !assignedModels[model] {
			assignedModels[model] = true
			totalVRAMNeeded += estimateVRAM(model)
		}

		clamped[role] = model
	}

	if resources.GPUVRAMGB > 0 && totalVRAMNeeded > resources.GPUVRAMGB {
		log.Printf("staffing: proposed models need %.1f GB VRAM, exceeding physical %.1f GB. Consolidating all local roles to %s.",
			totalVRAMNeeded, resources.GPUVRAMGB, defaultModel)
		for role, model := range clamped {
			if !isCloudModel(model) {
				clamped[role] = defaultModel
			}
		}
	}

	if clamped["security-breaker"] == "" {
		clamped["security-breaker"] = defaultModel
	}
	if clamped["correctness-reviewer"] == "" {
		clamped["correctness-reviewer"] = defaultModel
	}

	return clamped
}

func getSystemRAM() float64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").CombinedOutput()
		if err == nil {
			var bytes uint64
			if _, ferr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &bytes); ferr == nil {
				return float64(bytes) / 1024 / 1024 / 1024
			}
		}
	} else if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					var mem uint64
					if _, ferr := fmt.Sscanf(line, "MemTotal: %d kB", &mem); ferr == nil {
						return float64(mem) / 1024 / 1024
					}
				}
			}
		}
	}
	return 16.0
}

func getGPUVRAM() float64 {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").CombinedOutput()
	if err == nil {
		var vram float64
		if _, ferr := fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &vram); ferr == nil {
			return vram / 1024
		}
	}

	out, err = exec.Command("rocm-smi", "--showmeminfo", "vram").CombinedOutput()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "VRAM Total Memory") {
				var vram uint64
				if idx := strings.Index(line, ":"); idx >= 0 {
					if _, ferr := fmt.Sscanf(strings.TrimSpace(line[idx+1:]), "%d", &vram); ferr == nil {
						return float64(vram) / 1024 / 1024 / 1024
					}
				}
			}
		}
	}

	return 0.0
}

func queryOllama() ([]string, []string) {
	url := os.Getenv("OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	url = strings.TrimSuffix(url, "/")

	var pulled []string
	var loaded []string

	client := http.Client{Timeout: 2 * time.Second}

	// #nosec G704
	resp, err := client.Get(url + "/api/tags")
	if err == nil {
		defer resp.Body.Close()
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.NewDecoder(resp.Body).Decode(&tags) == nil {
			for _, m := range tags.Models {
				pulled = append(pulled, m.Name)
			}
		}
	}

	// #nosec G704
	resp, err = client.Get(url + "/api/ps")
	if err == nil {
		defer resp.Body.Close()
		var ps struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.NewDecoder(resp.Body).Decode(&ps) == nil {
			for _, m := range ps.Models {
				loaded = append(loaded, m.Name)
			}
		}
	}

	return pulled, loaded
}

func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func isCloudModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "claude") || strings.Contains(m, "gpt") || strings.Contains(m, "gemini")
}

func estimateVRAM(model string) float64 {
	m := strings.ToLower(model)
	var size float64 = 7.0
	for _, suffix := range []string{"70b", "32b", "14b", "8b", "7b", "3b", "1.5b"} {
		if strings.Contains(m, suffix) {
			_, _ = fmt.Sscanf(suffix, "%fb", &size)
			break
		}
	}
	return size * 0.7
}
