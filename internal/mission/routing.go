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

func (s *StaffingManager) Sense() WorkstationResources {
	res := WorkstationResources{
		CPUCores: runtime.NumCPU(),
	}
	res.TotalRAMGB = getSystemRAM()
	res.GPUVRAMGB = getGPUVRAM()
	res.PulledModels, res.LoadedModels = queryOllama()
	return res
}

func (s *StaffingManager) Judge(ctx context.Context, directive string, resources WorkstationResources, stats []ModelStats, thoroughness int, footprint int) (map[string]string, []string, error) {
	if s.LLM == nil || !s.LLM.Available() {
		return nil, nil, fmt.Errorf("LLM client not available")
	}

	var statsBuf strings.Builder
	for _, cell := range stats {
		statsBuf.WriteString(fmt.Sprintf("- Model: %s, Role: %s, TasksCompleted: %d, AvgDuration: %.1fs, PassRate: %.1f%%\n",
			cell.Model, cell.Role, cell.TasksCompleted, cell.AvgTaskDuration, cell.ExecPassRatePct))
	}

	systemPrompt := `You are the Corralai swarm staffing planner. Analyze the directive, workstation hardware resources, available local/cloud models, and historical performance stats. Propose the optimal role-to-model staffing assignment and load order to run the swarm.

Respond ONLY with a valid JSON object matching this schema:
{
  "role_assignments": {
    "builder": "model_name",
    "tester": "model_name",
    "pentester": "model_name",
    "reviewer": "model_name"
  },
  "load_order": ["model_name", ...]
}

Constraints:
1. Local models in the load order should fit in available GPU VRAM. Estimate VRAM for a local model as parameter_size * 0.7 GB.
2. Prioritize models that have high success rates or low average duration in historical stats.
3. If no stats are available (cold-start), use default models (e.g. qwen2.5-coder:7b, llama3.2:3b).
4. Always allocate builder and tester roles.
5. If a local model is not pulled, do not assign it unless it is a cloud model API (e.g. gemini, claude, gpt).`

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
%s`, directive, resources.CPUCores, resources.TotalRAMGB, resources.GPUVRAMGB, resources.PulledModels, resources.LoadedModels, thoroughness, footprint, statsBuf.String())

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

	if clamped["builder"] == "" {
		clamped["builder"] = defaultModel
	}
	if clamped["tester"] == "" {
		clamped["tester"] = defaultModel
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
