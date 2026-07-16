// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/gate"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/telemetry"
	"github.com/pdbethke/corralai/internal/testgen"
)

// defaultAdvPoolModel and defaultAdvPoolCriticModel are the static fallback
// role assignment used when no leaderboard evidence is available yet (cold
// start) — deliberately TWO DISTINCT models so the pool is decorrelated from
// its very first run, never just "whatever happens to be configured".
const (
	defaultAdvPoolModel       = "qwen2.5-coder:7b"
	defaultAdvPoolCriticModel = "llama3.2:3b"
)

// advPoolTestPath derives the synthetic test-file name a candidate test is
// written to in the jail workspace from the code file's own path: same base
// name, `_test.go` suffix, same directory — so it lands in the same package
// as the code under test regardless of which of the two (dev-authored or
// pool-authored) test source is currently being scored. Go does not care
// about the test file's exact base name, only that it ends in `_test.go`
// and shares the package.
func advPoolTestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.go"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.go")
}

// advPoolBase returns the go.mod scaffold shared by the pool's Scorer and
// Validator. v1 is go-only, mirroring stage_control's fixed lang assumption.
//
// The default testCmd is deliberately RECURSIVE ("./...", never
// controlgate.LangScaffold's own "./" default): a run's code_path is very
// commonly a subdirectory (e.g. internal/auth/login.go), which lands the
// candidate files under internal/auth/ in the jail workspace — "go test ./"
// from the module root only ever sees the root package (no .go files there),
// so a non-recursive default would silently no-op the scorer/compile-check
// for every subdirectory target. This is the SAME asymmetry bug CompileTest
// had (I-1): the scorer already honors the run's own TestCmd when set, this
// only fixes the fallback used when TestCmd is empty.
func advPoolBase() (base map[string]string, testCmd []string) {
	base, _, _ = controlgate.LangScaffold("go")
	return base, []string{"go", "test", "./..."}
}

// advpoolScorer adapts adequacy.Score (the SAME deterministic, brain-side,
// jail-run mutation scorer the control gate uses) to advpool.Scorer. This is
// the soundness-#1 seam: the driver never trusts a worker's self-reported
// kill rate, only what this Scorer actually observes running in the jail.
type advpoolScorer struct {
	jail adequacy.Jail
}

func (s advpoolScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	base, defaultCmd := advPoolBase()
	cmd := strings.Fields(testCmd)
	if len(cmd) == 0 {
		cmd = defaultCmd
	}
	scoreBase := make(map[string]string, len(base)+1)
	for k, v := range base {
		scoreBase[k] = v
	}
	scoreBase[advPoolTestPath(codePath)] = test

	rep, err := adequacy.Score(ctx, s.jail, scoreBase, codePath, code, mutants, cmd)
	if err != nil {
		return 0, nil, fmt.Errorf("advpool: score: %w", err)
	}
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	survivors := make([]adequacy.Mutant, 0, len(rep.Survived))
	for _, id := range rep.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}
	return rep.KillRate(), survivors, nil
}

// advpoolValidator brain-side-validates a worker's structured artifacts
// before the driver trusts them: CompileTest jail-compiles a candidate test
// against the code (via `go vet`, which type-checks test files without
// executing them — the "does it compile" check, never "does it pass",
// which would corrupt CompileTest's meaning); ParseMutants is testgen's
// proven mutant-output parser (the Task 1.2 seam), reused verbatim so a
// distributed worker's raw response parses identically to the in-process
// generator's own output.
//
// CompileTest MUST cover the same scope the Scorer actually runs against
// (I-1): a subdirectory code_path (e.g. internal/auth/login.go) lands the
// candidate code+test under internal/auth/ in the jail workspace, so
// `go vet ./` (module root, non-recursive) sees zero .go files there and
// fails EVERY authored test regardless of whether it actually compiles —
// the run then never converges. `go vet ./...` is recursive and always
// covers whatever directory the files actually landed in.
type advpoolValidator struct {
	jail adequacy.Jail
}

func (v advpoolValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	base, _ := advPoolBase()
	ws := make(map[string]string, len(base)+2)
	for k, val := range base {
		ws[k] = val
	}
	ws[codePath] = code
	ws[advPoolTestPath(codePath)] = test

	compiles, err := v.jail.RunTest(ctx, ws, []string{"go", "vet", "./..."})
	if err != nil {
		return fmt.Errorf("advpool: compile-verify test: %w", err)
	}
	if !compiles {
		return fmt.Errorf("advpool: test does not compile")
	}
	return nil
}

func (v advpoolValidator) ParseMutants(raw string) ([]adequacy.Mutant, error) {
	return testgen.ParseMutantsOutput(raw)
}

func (v advpoolValidator) ParseTest(raw string) string {
	return testgen.ParseTestOutput(raw)
}

// advpoolSigner signs a terminal Verdict via the SAME certify chain
// certifyBuild/report_build uses (opts.CertifyKey/BuildStore/Witness) —
// mirroring certifierAdapter (gate.go) and controlRunner's Cert field
// (controlgate.go): the verdict is marshaled and sha256-digested (mirroring
// controlgate.PostControlGate's digest pattern) so the signed record's
// output_digest is a tamper-evident fingerprint of every Verdict field
// (subject = repo@commit, byproducts = the digest), then certified with a
// distinct actor so a signed advpool record is never confused with a human
// `corral certify` submission, a merge-gate run, or a control-gate run.
type advpoolSigner struct{ opts Options }

func (s advpoolSigner) SignVerdict(ctx context.Context, v advpool.Verdict) (int64, string, error) {
	exitCode := 0
	if v.Status != advpool.StatusCertified {
		exitCode = 1
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, "", fmt.Errorf("advpool: marshal verdict: %w", err)
	}
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// ProducedBy surfaces the run's role assignment as human-readable
	// "role:model" strings directly on the signed record (M-2), rather than
	// leaving the models only re-derivable by unpacking output_digest against
	// a separately-stored Verdict. Sorted so the record is deterministic.
	roles := make([]string, 0, len(v.ModelsByRole))
	for role := range v.ModelsByRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	producedBy := make([]string, 0, len(roles))
	for _, role := range roles {
		producedBy = append(producedBy, role+":"+v.ModelsByRole[role])
	}

	out, err := certifyBuild(ctx, s.opts, reportBuildIn{
		Repo:         v.Repo,
		Commit:       v.Commit,
		Command:      "corral/adversarial-pool",
		ExitCode:     exitCode,
		OutputDigest: digest,
		ProducedBy:   producedBy,
	}, "corral-advpool")
	if err != nil {
		return 0, "", err
	}
	return out.ID, out.Head, nil
}

// advpoolLeaderboardSink feeds the gate-earned (model, role, outcome)
// fitness signal — from a terminal, SIGNED Verdict only, never a worker's
// self-report (soundness #5/#6) — into the brain's telemetry timeline via
// the same best-effort rec() helper certifyBuild uses for build_certified
// events. nil tel => Record is a documented no-op (telemetry is optional
// everywhere else in the brain; the pool must not depend on it to run).
// A later task can read this event stream back into
// mission.PerformanceTracker.GetRoleModelStats(); this slice only needs to
// satisfy advpool.LeaderboardSink.
type advpoolLeaderboardSink struct {
	tel *telemetry.Store
}

func (s advpoolLeaderboardSink) Record(model, role, outcome string) {
	rec(s.tel, 0, "advpool_leaderboard", model, role, map[string]any{"outcome": outcome})
}

// advPoolBetter reports whether a's ModelStats outrank b's for staffing
// purposes: higher exec pass rate wins; ties broken by more completed tasks
// (more evidence); final tiebreak is the model name so the pick is
// deterministic regardless of map/slice iteration order.
func advPoolBetter(a, b mission.ModelStats) bool {
	if a.ExecPassRatePct != b.ExecPassRatePct {
		return a.ExecPassRatePct > b.ExecPassRatePct
	}
	if a.TasksCompleted != b.TasksCompleted {
		return a.TasksCompleted > b.TasksCompleted
	}
	return a.Model < b.Model
}

// advPoolBestByRole picks the best-earned model per role from the
// leaderboard's stats.
// routableModel reports whether a leaderboard model name is a real routing
// target. The leaderboard records an UNATTRIBUTED completion under the
// unknownModel sentinel ("(unknown model)") — you can never route a role TO
// that (a worker cannot run a model called "(unknown model)"), and an empty
// name is meaningless too. Skipping both means a run whose worker's model was
// not attributed can never poison the next run's routing; the env/default
// model stands instead.
func routableModel(m string) bool { return m != "" && m != unknownModel }

func advPoolBestByRole(stats []mission.ModelStats) map[string]string {
	best := map[string]mission.ModelStats{}
	for _, c := range stats {
		if !routableModel(c.Model) {
			continue
		}
		cur, ok := best[c.Role]
		if !ok || advPoolBetter(c, cur) {
			best[c.Role] = c
		}
	}
	out := make(map[string]string, len(best))
	for role, c := range best {
		out[role] = c.Model
	}
	return out
}

// advPoolBestExcluding picks the best-earned model for role that is NOT
// exclude — the decorrelation-enforcing pick for test-critic (must not
// share a model with test-writer). Returns "" when no eligible candidate
// has any evidence for role.
func advPoolBestExcluding(stats []mission.ModelStats, role, exclude string) string {
	var best *mission.ModelStats
	for i := range stats {
		c := stats[i]
		if c.Role != role || c.Model == exclude || !routableModel(c.Model) {
			continue
		}
		if best == nil || advPoolBetter(c, *best) {
			cc := c
			best = &cc
		}
	}
	if best == nil {
		return ""
	}
	return best.Model
}

// advPoolFallbackCritic picks a static critic model guaranteed to differ
// from writer — the cold-start (no leaderboard evidence) decorrelation
// backstop.
func advPoolFallbackCritic(writer string) string {
	if writer != defaultAdvPoolCriticModel {
		return defaultAdvPoolCriticModel
	}
	return defaultAdvPoolModel
}

// parseAdvPoolModels parses CORRALAI_ADVPOOL_MODELS
// ("mutant-generator=<m>,test-writer=<m>,test-critic=<m>") into a base
// RoleAssignment. Returns (nil, nil) for the empty string (unset → caller uses
// the hardcoded defaults). Every one of the three known roles must be present
// with a non-empty model, no unknown role keys are allowed, and the result
// must pass decorrelation (test-critic != test-writer) — otherwise an error is
// returned and the caller falls back to the hardcoded defaults rather than
// starting the pool on an operator typo.
func parseAdvPoolModels(s string) (advpool.RoleAssignment, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	known := map[string]bool{
		advpool.RoleMutantGenerator: true,
		advpool.RoleTestWriter:      true,
		advpool.RoleTestCritic:      true,
	}
	out := advpool.RoleAssignment{}
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("advpool models: %q is not role=model", strings.TrimSpace(pair))
		}
		role, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if !known[role] {
			return nil, fmt.Errorf("advpool models: unknown role %q (want mutant-generator|test-writer|test-critic)", role)
		}
		if val == "" {
			return nil, fmt.Errorf("advpool models: empty model for role %q", role)
		}
		out[role] = val
	}
	for _, r := range []string{advpool.RoleMutantGenerator, advpool.RoleTestWriter, advpool.RoleTestCritic} {
		if out[r] == "" {
			return nil, fmt.Errorf("advpool models: missing role %q", r)
		}
	}
	if err := advpool.CheckDecorrelation(out); err != nil {
		return nil, fmt.Errorf("advpool models: %w", err)
	}
	return out, nil
}

// advPoolAssign builds a decorrelation-enforced role assignment. The base
// mutant-generator/test-writer models come from `defaults` (the operator's
// CORRALAI_ADVPOOL_MODELS, or the hardcoded constants when defaults is nil);
// the leaderboard's best-earned model per role still overrides when it has
// evidence; test-critic is then forced to the best-earned model that is NOT
// the test-writer's (falling back to a distinct default) so the result can
// never fail CheckDecorrelation.
func advPoolAssign(staffing *mission.StaffingManager, defaults advpool.RoleAssignment) advpool.RoleAssignment {
	mg, tw, tc := defaultAdvPoolModel, defaultAdvPoolModel, defaultAdvPoolCriticModel
	if defaults != nil {
		if m := defaults[advpool.RoleMutantGenerator]; m != "" {
			mg = m
		}
		if m := defaults[advpool.RoleTestWriter]; m != "" {
			tw = m
		}
		if m := defaults[advpool.RoleTestCritic]; m != "" {
			tc = m
		}
	}
	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: mg,
		advpool.RoleTestWriter:      tw,
	}

	var stats []mission.ModelStats
	if staffing != nil && staffing.Perf != nil {
		stats = staffing.Perf.GetRoleModelStats()
	}
	if len(stats) > 0 {
		best := advPoolBestByRole(stats)
		if m, ok := best[advpool.RoleMutantGenerator]; ok {
			assign[advpool.RoleMutantGenerator] = m
		}
		if m, ok := best[advpool.RoleTestWriter]; ok {
			assign[advpool.RoleTestWriter] = m
		}
	}

	critic := tc
	if critic == assign[advpool.RoleTestWriter] {
		critic = advPoolFallbackCritic(assign[advpool.RoleTestWriter])
	}
	if m := advPoolBestExcluding(stats, advpool.RoleTestCritic, assign[advpool.RoleTestWriter]); m != "" {
		critic = m
	}
	assign[advpool.RoleTestCritic] = critic
	return assign
}

// advPoolTickMaxErrors bounds how many consecutive Tick errors on the SAME
// active run the runtime tolerates before giving up on it and clearing the
// active slot. Without this bound, a persistently-malformed artifact (the
// Phase-4 caveat: a reissued mutant-generator/test-writer task that keeps
// producing unparseable/non-compiling output) would wedge the pool on one
// dead run FOREVER, refusing every future start_adversarial_run call. A few
// retries still absorb a transient jail hiccup.
const advPoolTickMaxErrors = 20

// AdvPoolRuntime is the wired adversarial-pool driver plus the bookkeeping
// (single active run, per-run consecutive-error counters) the tick loop and
// the start_adversarial_run tool share. Exactly one *AdvPoolRuntime backs a
// brain: the tool and the tick loop must operate on the SAME Driver instance
// so the in-memory run state Driver.StartRun begins is the same state
// Driver.Tick later advances.
type AdvPoolRuntime struct {
	driver   *advpool.Driver
	missions *mission.Store
	staffing *mission.StaffingManager
	defaults advpool.RoleAssignment // operator CORRALAI_ADVPOOL_MODELS base (nil = hardcoded)

	mu         sync.Mutex
	activeID   int64
	tickErrors map[int64]int
}

// AdvPoolRunSpec is start_adversarial_run's input: the code under review
// plus the developer's own tests for it (advpool.RunSpec, MCP-schema'd).
type AdvPoolRunSpec struct {
	Repo        string `json:"repo" jsonschema:"the repository (e.g. owner/name)"`
	Commit      string `json:"commit" jsonschema:"the commit sha under review"`
	Goal        string `json:"goal" jsonschema:"the correctness/security goal the code must satisfy"`
	CodePath    string `json:"code_path" jsonschema:"repo-relative path of the code under review, e.g. internal/auth/login.go"`
	Code        string `json:"code" jsonschema:"the code under review, full file source"`
	DevTestPath string `json:"dev_test_path" jsonschema:"repo-relative path of the developer's own test file"`
	DevTestCode string `json:"dev_test_code" jsonschema:"the developer's own test file source"`
	TestCmd     string `json:"test_cmd" jsonschema:"the test command, e.g. \"go test ./\""`
	NMutants    int    `json:"n_mutants,omitempty" jsonschema:"how many seeded-violation mutants to generate (default 5)"`
}

// AdvPoolRunOut is start_adversarial_run's output: the id of the mission
// tracking the run.
type AdvPoolRunOut struct {
	RunID int64 `json:"run_id"`
}

// AdvPoolQuery is get_adversarial_run's input: a run id from start_adversarial_run.
type AdvPoolQuery struct {
	RunID int64 `json:"run_id" jsonschema:"the run id returned by start_adversarial_run"`
}

// AdvPoolStatusOut is get_adversarial_run's output: a run's status and, once
// converged, its signed Verdict. Verdict is nil while the run is still ticking
// or for an unknown id.
type AdvPoolStatusOut struct {
	RunID     int64            `json:"run_id"`
	Found     bool             `json:"found"`
	Converged bool             `json:"converged"`
	Verdict   *advpool.Verdict `json:"verdict,omitempty"`
}

// maxAdvPoolMutants mirrors maxControlMutants: bounds the compute a single
// admin request can trigger (each mutant costs jail spawns).
const maxAdvPoolMutants = 20

// StartRun begins one adversarial-pool run: builds the RunSpec, extracts
// signatures (best-effort — a failure degrades to no signatures rather than
// refusing the run), picks a decorrelation-enforced role assignment off the
// live leaderboard, mints a tracking mission (reusing mission.CreateMission
// for the SAME create+id pattern every other mission goes through; q=nil so
// CreateMission does not also enqueue its placeholder phase — the pool's own
// DAG is enqueued by driver.StartRun, not by CreateMission's plan-to-tasks
// path), and enqueues the run's DAG. Refuses a second run while one is
// already active (single active run — slice 1's explicit scope limit).
func (rt *AdvPoolRuntime) StartRun(in AdvPoolRunSpec) (int64, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.activeID != 0 {
		return 0, fmt.Errorf("advpool: run %d is already active — only one active run is supported in this slice", rt.activeID)
	}

	nMutants := in.NMutants
	if nMutants <= 0 {
		nMutants = 5
	}
	if nMutants > maxAdvPoolMutants {
		nMutants = maxAdvPoolMutants
	}
	rs := advpool.RunSpec{
		Repo: in.Repo, Commit: in.Commit, Goal: in.Goal,
		CodePath: in.CodePath, Code: in.Code,
		DevTestPath: in.DevTestPath, DevTestCode: in.DevTestCode,
		TestCmd: in.TestCmd, NMutants: nMutants,
	}

	sigs, err := repoindex.ExtractSignatures(rs.Code, "go")
	if err != nil {
		log.Printf("advpool: extract signatures for %s@%s: %v (continuing with no signatures)", rs.Repo, rs.Commit, err)
		sigs = nil
	}

	assign := advPoolAssign(rt.staffing, rt.defaults)
	if err := advpool.CheckDecorrelation(assign); err != nil {
		// Unreachable given advPoolAssign's construction — fail loudly rather
		// than silently starting a run under a decorrelation bug.
		return 0, fmt.Errorf("advpool: internal assignment failed decorrelation (bug): %w", err)
	}

	mid, err := mission.CreateMission(rt.missions, nil,
		fmt.Sprintf("adversarial-pool: %s@%s", rs.Repo, rs.Commit),
		[]mission.PhaseSpec{{Name: "adversarial-pool", Role: "system", Count: 1}}, false)
	if err != nil {
		return 0, fmt.Errorf("advpool: create tracking mission: %w", err)
	}

	rt.driver.Assign = assign
	if err := rt.driver.StartRun(mid, rs, sigs); err != nil {
		return 0, fmt.Errorf("advpool: start run: %w", err)
	}
	rt.activeID = mid
	return mid, nil
}

// RunStatus reports a run's status/verdict by id, delegating to the driver
// (which retains converged runs). Used by the get_adversarial_run tool so an
// external caller can poll an async run to convergence.
func (rt *AdvPoolRuntime) RunStatus(runID int64) (advpool.RunState, bool) {
	return rt.driver.RunStatus(runID)
}

// loop ticks the active run every interval until ctx is cancelled — the
// same select{ctx.Done / ticker.C} shape as gate.Poller.Loop.
func (rt *AdvPoolRuntime) loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.tick(ctx)
		}
	}
}

// tick advances the active run one step, if any. A Tick error is logged
// LOUDLY every time (never silently swallowed) and counted per-run; once a
// run has errored advPoolTickMaxErrors times in a row, tick gives up on it
// (clears the active slot) so a persistently-malformed artifact can't wedge
// the pool forever. A converged run (non-nil Verdict) also clears the
// active slot, freeing the next start_adversarial_run call.
func (rt *AdvPoolRuntime) tick(ctx context.Context) {
	rt.mu.Lock()
	id := rt.activeID
	rt.mu.Unlock()
	if id == 0 {
		return
	}

	verdict, err := rt.driver.Tick(ctx, id)
	if err != nil {
		rt.mu.Lock()
		rt.tickErrors[id]++
		n := rt.tickErrors[id]
		rt.mu.Unlock()
		log.Printf("advpool: tick run %d: %v (consecutive errors %d/%d)", id, err, n, advPoolTickMaxErrors)
		if n >= advPoolTickMaxErrors {
			log.Printf("advpool: run %d giving up after %d consecutive tick errors — clearing the active slot", id, advPoolTickMaxErrors)
			rt.mu.Lock()
			if rt.activeID == id {
				rt.activeID = 0
			}
			delete(rt.tickErrors, id)
			rt.mu.Unlock()
		}
		return
	}

	rt.mu.Lock()
	delete(rt.tickErrors, id)
	rt.mu.Unlock()
	if verdict != nil {
		log.Printf("advpool: run %d converged: status=%s dev_kill_rate=%.2f mutants=%d survivors=%d proven_missed=%d",
			id, verdict.Status, verdict.DevKillRate, verdict.MutantsTotal, verdict.Survivors, verdict.ProvenMissed)
		// The tracking mission is created status='running' and never
		// transitioned otherwise; MissionHistoryList skips running/paused
		// missions, so an un-transitioned pool mission never surfaces in
		// /api/history and the export meta comes out 0/0/0 for a run that
		// actually finished. Map the verdict onto a terminal status so it
		// gets counted.
		status := "needs-review"
		if verdict.Status == advpool.StatusCertified {
			status = "certified"
		}
		if rt.missions != nil {
			if err := rt.missions.SetMissionStatus(id, status); err != nil {
				log.Printf("advpool: run %d set mission status %q: %v", id, status, err)
			}
		}
		rt.mu.Lock()
		if rt.activeID == id {
			rt.activeID = 0
		}
		rt.mu.Unlock()
	}
}

// StartAdversarialPool wires the pure advpool.Driver to the brain's real
// effects — a jail-backed Scorer/Validator over the SAME shared sandbox
// isolator the control gate uses, the certify-chain Signer, and a
// telemetry-fed LeaderboardSink — and starts the tick loop that advances the
// single active run. Off switches mirror StartGate/StartControlGate: nil
// GateBackend, Missions, Queue, CertifyKey, or BuildStore disables the pool
// entirely (no unsandboxed run, no unsigned verdict) rather than
// half-wiring it; StartAdversarialPool logs loudly and returns (nil, nil) —
// the caller must then leave Options.AdvPool nil so the tool is never
// registered.
func StartAdversarialPool(ctx context.Context, opts Options) (*AdvPoolRuntime, error) {
	if opts.GateBackend == nil || opts.Missions == nil || opts.Queue == nil || opts.CertifyKey == nil || opts.BuildStore == nil {
		log.Printf("advpool: DISABLED — needs GateBackend + Missions + Queue + CertifyKey + BuildStore configured (adversarial pool off)")
		return nil, nil
	}

	defaults, derr := parseAdvPoolModels(os.Getenv("CORRALAI_ADVPOOL_MODELS"))
	if derr != nil {
		log.Printf("advpool: CORRALAI_ADVPOOL_MODELS invalid (%v) — falling back to defaults %s/%s", derr, defaultAdvPoolModel, defaultAdvPoolCriticModel)
		defaults = nil
	}

	jail := adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)
	assign := advPoolAssign(opts.Staffing, defaults)
	driver, err := advpool.NewDriver(opts.Queue, advpoolScorer{jail: jail}, advpoolValidator{jail: jail}, assign, 0.8)
	if err != nil {
		return nil, fmt.Errorf("advpool: new driver: %w", err)
	}
	driver.Signer = advpoolSigner{opts: opts}
	driver.Leaderboard = advpoolLeaderboardSink{tel: opts.Telemetry}

	// RunDeadline is the wall-clock backstop checkNoProgress can't be: it
	// stands down whenever any task is claimed ("slow is not stuck"), so a
	// claimed-but-wedged task would otherwise stall a run forever. 12min is
	// generous — a healthy frontier run finishes in 2-4min — so this only
	// catches a genuine stall, converging it to a signed needs-review verdict.
	driver.RunDeadline = 12 * time.Minute
	if s := strings.TrimSpace(os.Getenv("CORRALAI_ADVPOOL_RUN_DEADLINE_S")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			driver.RunDeadline = time.Duration(n) * time.Second
		} else {
			log.Printf("advpool: CORRALAI_ADVPOOL_RUN_DEADLINE_S invalid (%q) — keeping default %s", s, driver.RunDeadline)
		}
	}

	rt := &AdvPoolRuntime{
		driver:     driver,
		missions:   opts.Missions,
		staffing:   opts.Staffing,
		defaults:   defaults,
		tickErrors: make(map[int64]int),
	}

	interval := opts.AdvPoolPollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	log.Printf("advpool: ENABLED — polling every %s; role models: mutant-generator=%s test-writer=%s test-critic=%s",
		interval, assign[advpool.RoleMutantGenerator], assign[advpool.RoleTestWriter], assign[advpool.RoleTestCritic])
	log.Printf("advpool: run-deadline=%s (a run that has not converged by then is signed needs-review and freed)", driver.RunDeadline)
	go rt.loop(ctx, interval)
	return rt, nil
}

// registerAdvPoolTools registers the admin-gated start_adversarial_run and
// get_adversarial_run tools over the shared AdvPoolRuntime (opts.AdvPool) —
// never called when that's nil (see server.go).
func registerAdvPoolTools(s *mcp.Server, opts Options) {
	rt := opts.AdvPool
	mcp.AddTool(s, &mcp.Tool{Name: "start_adversarial_run",
		Description: "ADMIN: start an adversarial-testing-pool run against a code+dev-test pair — mutant-generator, test-writer, and test-critic roles, decorrelation-enforced, gated by the brain-side adequacy jail, certified via the signed record chain."},
		func(_ context.Context, req *mcp.CallToolRequest, in AdvPoolRunSpec) (*mcp.CallToolResult, AdvPoolRunOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, AdvPoolRunOut{}, errAdminOnly
			}
			runID, err := rt.StartRun(in)
			if err != nil {
				return nil, AdvPoolRunOut{}, err
			}
			auditKnowledge(opts, req, "start_adversarial_run", map[string]any{"repo": in.Repo, "commit": in.Commit, "run_id": runID})
			return nil, AdvPoolRunOut{RunID: runID}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_adversarial_run",
		Description: "ADMIN: query an adversarial-pool run's status and (once converged) its signed verdict, by the run id returned from start_adversarial_run."},
		func(_ context.Context, req *mcp.CallToolRequest, in AdvPoolQuery) (*mcp.CallToolResult, AdvPoolStatusOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, AdvPoolStatusOut{}, errAdminOnly
			}
			st, found := rt.RunStatus(in.RunID)
			return nil, AdvPoolStatusOut{
				RunID:     in.RunID,
				Found:     found,
				Converged: st.Converged,
				Verdict:   st.Verdict,
			}, nil
		})
}
