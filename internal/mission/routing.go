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
	CPUCores int `json:"cpu_cores"`
	// TotalRAMGB is 0 when it could not be read — unknown, never a guess.
	TotalRAMGB   float64  `json:"total_ram_gb"`
	GPUVRAMGB    float64  `json:"gpu_vram_gb"`
	PulledModels []string `json:"pulled_models"`
	LoadedModels []string `json:"loaded_models"`
	// DeclaredModels are the models the operator named WITH a backend
	// (CORRALAI_ROLE_MODELS "role=backend:model"). With PulledModels — what the
	// OLLAMA_URL daemon lists — they are the only models staffing may assign.
	DeclaredModels []rolemodel.ModelRef `json:"declared_models"`
}

type StaffingManager struct {
	Perf       PerformanceTracker
	LLM        LLMClient
	RoleModels *rolemodel.Policy
	// Declared is the operator's declared role policy, snapshotted at startup
	// before staffing writes RoleModels, so a staffed model never becomes a
	// "declaration" for the next pass. Entries with no backend are not a
	// source: "role=model" says which model, not who serves it.
	Declared []rolemodel.ModelRef
}

// maxStaffAttempts bounds how many times Staff retries a failing Judge probe
// before giving up: a permanently-failing probe (bad LLM endpoint, malformed
// response) backs off instead of burning a 30s round-trip indefinitely.
const maxStaffAttempts = 3

// A staffing proposal is an LLM's output, not a declaration. The vendor that
// serves a model decides where the change under review and an API key are
// sent, so it is never read off the model's spelling (founder ruling
// 2026-11-12: "any guessing is a security hole"). staffedModelRef used to do
// exactly that — "claude" in the name meant anthropic, "gpt" openai, and
// "gemini" also openai, which was wrong outright — and a local gpt-oss:20b
// would have been staffed onto OpenAI. A model is now assignable only when it
// is a candidate (candidateRefs): listed by the OLLAMA_URL daemon, or declared
// by the operator with its backend. Clamp returns the candidate's own ref, so
// the backend always comes from where the model was found.

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
	for role, ref := range clamped {
		s.RoleModels.Set(role, ref) // threadsafe: the UI topology and spawn read this same policy
	}
	return nil
}

func (s *StaffingManager) Sense() WorkstationResources {
	res := WorkstationResources{
		CPUCores: runtime.NumCPU(),
	}
	res.TotalRAMGB = getSystemRAM()
	res.GPUVRAMGB = getGPUVRAM()
	res.DeclaredModels = declaredWithBackend(s.Declared)
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
		return "No historical performance data yet (cold start) — staff from the candidate list and treat this mission as evidence-gathering, not as optimizing a ranking that doesn't exist."
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

	candidates := candidateKeys(resources)
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("staffing: no candidate models — set OLLAMA_URL to a daemon that lists models, or declare models with their backend in CORRALAI_ROLE_MODELS (role=backend:model); corral does not name a model for you")
	}
	brief := buildLeaderboardBrief(stats, candidates, minConfidentSamples)

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
3. If no stats are available (cold-start), choose among the candidate models and treat the mission as evidence-gathering.
4. Always allocate security-breaker and correctness-reviewer roles.
5. Assign ONLY models from the candidate list, written exactly as listed. Any other name is discarded and its role is left unstaffed.
6. EXPLORE, don't ossify: when a role has untested eligible models (probe candidates), occasionally staff one instead of the current leader so the leaderboard keeps learning — especially when the leader's evidence is thin or the alternatives are entirely unmeasured.`

	prompt := fmt.Sprintf(`Directive: %s
Available Resources:
- CPU Cores: %d
- System RAM: %s
- GPU VRAM: %.1f GB
- Currently loaded models: %v

Candidate models (the ONLY names you may assign, exactly as written):
%s

Optimization Weights (1-5 range):
- Thoroughness: %d/5 (higher means prioritize larger, more capable models; lower means prioritize speed and smaller models)
- Resource Footprint: %d/5 (higher means minimize GPU memory footprint, potentially sharing one model; lower means utilize full available VRAM)

Historical Leaderboard:
%s`, directive, resources.CPUCores, ramLine(resources.TotalRAMGB), resources.GPUVRAMGB, resources.LoadedModels, candidateLines(resources), thoroughness, footprint, brief)

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

// Clamp turns the planner's proposal into assignments corral can stand behind.
// A proposed name that is not a candidate, exactly as listed, is discarded and
// its role left unstaffed: there is no prefix match, no swap to a "close"
// model, and no fill-in model for a role the planner skipped. Each of those
// used to exist (a pulled qwen was preferred by name as the stand-in), and
// each was corral choosing a model on the operator's behalf.
//
// The one change Clamp still makes is FIT: when the accepted local models
// together exceed the GPU's VRAM, every local role is consolidated onto the
// smallest local model the planner itself proposed — a model it chose, from a
// declared source, so nothing new is introduced.
func (s *StaffingManager) Clamp(assignments map[string]string, resources WorkstationResources) map[string]rolemodel.ModelRef {
	cands := candidateRefs(resources)
	clamped := make(map[string]rolemodel.ModelRef)
	roles := make([]string, 0, len(assignments))
	for role := range assignments {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	var totalVRAMNeeded float64
	counted := map[string]bool{}
	smallestLocal, smallestSize := "", 0.0
	for _, role := range roles {
		proposed := strings.TrimSpace(assignments[role])
		ref, ok := cands[proposed]
		if !ok {
			log.Printf("staffing: role %q left unstaffed — %q is not a candidate (not listed by the OLLAMA_URL daemon, not declared with a backend in CORRALAI_ROLE_MODELS)", role, proposed)
			continue
		}
		clamped[role] = ref
		if ref.Backend != localBackend {
			continue
		}
		size := estimateVRAM(ref.Model)
		if !counted[ref.Model] {
			counted[ref.Model] = true
			totalVRAMNeeded += size
		}
		if smallestLocal == "" || size < smallestSize || (size == smallestSize && ref.Model < smallestLocal) {
			smallestLocal, smallestSize = ref.Model, size
		}
	}

	if resources.GPUVRAMGB > 0 && totalVRAMNeeded > resources.GPUVRAMGB && smallestLocal != "" {
		log.Printf("staffing: proposed local models need %.1f GB VRAM, exceeding physical %.1f GB. Consolidating all local roles to %s (the smallest the planner proposed).",
			totalVRAMNeeded, resources.GPUVRAMGB, smallestLocal)
		for role, ref := range clamped {
			if ref.Backend == localBackend {
				clamped[role] = rolemodel.ModelRef{Backend: localBackend, Model: smallestLocal}
			}
		}
	}

	for _, required := range []string{"security-breaker", "correctness-reviewer"} {
		if _, ok := clamped[required]; !ok {
			log.Printf("staffing: required role %q is unstaffed — corral does not fill it with a model nobody chose", required)
		}
	}
	return clamped
}

// localBackend is the backend of a model the OLLAMA_URL daemon listed: that
// listing is where it was found, so that daemon is what serves it.
const localBackend = "ollama"

// DeclaredFrom snapshots the operator's role policy as staffing's declared
// candidates. Call it once at startup, before staffing writes the policy.
// Refs are sorted so the planner sees the same list every time.
func DeclaredFrom(p *rolemodel.Policy) []rolemodel.ModelRef {
	seen := map[rolemodel.ModelRef]bool{}
	var out []rolemodel.ModelRef
	for _, r := range p.Snapshot() {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return declaredKey(out[i]) < declaredKey(out[j]) })
	return declaredWithBackend(out)
}

// declaredWithBackend keeps the declared refs that say who serves them.
func declaredWithBackend(refs []rolemodel.ModelRef) []rolemodel.ModelRef {
	var out []rolemodel.ModelRef
	for _, r := range refs {
		if strings.TrimSpace(r.Backend) != "" && strings.TrimSpace(r.Model) != "" {
			out = append(out, r)
		}
	}
	return out
}

// declaredKey is how a declared model is shown to the planner and matched
// back: backend/model. A listed local model is shown bare, as the daemon names
// it — an ollama tag contains ':' (qwen2.5-coder:7b), so ':' cannot separate a
// backend, and a bare name is matched only against the listing.
func declaredKey(r rolemodel.ModelRef) string { return r.Backend + "/" + r.Model }

// candidateRefs maps every assignable name to the ref it stands for.
func candidateRefs(res WorkstationResources) map[string]rolemodel.ModelRef {
	m := map[string]rolemodel.ModelRef{}
	for _, name := range res.PulledModels {
		m[name] = rolemodel.ModelRef{Backend: localBackend, Model: name}
	}
	for _, r := range res.DeclaredModels {
		m[declaredKey(r)] = r
	}
	return m
}

// candidateKeys is candidateRefs' names, sorted.
func candidateKeys(res WorkstationResources) []string {
	m := candidateRefs(res)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// candidateLines renders the candidate list for the planner, saying where each
// group came from.
func candidateLines(res WorkstationResources) string {
	var b strings.Builder
	if len(res.PulledModels) == 0 {
		b.WriteString("- listed by the local model daemon: none\n")
	} else {
		fmt.Fprintf(&b, "- listed by the local model daemon: %s\n", strings.Join(res.PulledModels, ", "))
	}
	if len(res.DeclaredModels) == 0 {
		b.WriteString("- declared by the operator: none")
	} else {
		keys := make([]string, 0, len(res.DeclaredModels))
		for _, r := range res.DeclaredModels {
			keys = append(keys, declaredKey(r))
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "- declared by the operator: %s", strings.Join(keys, ", "))
	}
	return b.String()
}

// ramLine says "unknown" for a RAM size that could not be read rather than
// handing the planner a number nobody measured.
func ramLine(gb float64) string {
	if gb <= 0 {
		return "unknown (could not be read)"
	}
	return fmt.Sprintf("%.1f GB", gb)
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
	// Unknown. This returned 16.0, a size nobody measured, which the planner
	// then sized models against.
	return 0
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

// queryOllama lists what the OLLAMA_URL daemon serves. With OLLAMA_URL unset
// there is no local source and nothing is listed: it used to fall back to
// 127.0.0.1:11434, an address nobody declared (corral-agent and the seat
// resolver refuse the same unset variable).
func queryOllama() ([]string, []string) {
	url := strings.TrimSpace(os.Getenv("OLLAMA_URL"))
	if url == "" {
		log.Printf("staffing: OLLAMA_URL is not set, so no local models are candidates")
		return nil, nil
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
