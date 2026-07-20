// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/gate"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// defaultAdvPoolModel and defaultAdvPoolCriticModel are the static fallback
// role assignment used when no leaderboard evidence is available yet (cold
// start) — deliberately TWO DISTINCT models so the pool is decorrelated from
// its very first run, never just "whatever happens to be configured".
const (
	defaultAdvPoolModel       = "qwen2.5-coder:7b"
	defaultAdvPoolCriticModel = "llama3.2:3b"
)

// resolveRunLang resolves the ONE language plugin a run's grading path will
// actually use — from the code file's extension (a .py file is pytest-graded
// no matter what inLang says). An explicit inLang is treated as an assertion:
// it must agree with the detected plugin's name, or the run is refused
// (fail-closed) rather than silently grading under a mismatched toolchain.
// Preflighting the toolchain is NOT done here (it hits the host) — callers
// preflight the returned plugin themselves.
func resolveRunLang(inLang, codePath string) (golang.Plugin, error) {
	detected, ok := golang.Detect(codePath)
	if !ok {
		return nil, fmt.Errorf("advpool: no language plugin for %q — refusing to grade", codePath)
	}
	if in := strings.TrimSpace(inLang); in != "" && in != detected.Name() {
		return nil, fmt.Errorf("advpool: declared language %q disagrees with code_path %q (detected %q) — refusing", inLang, codePath, detected.Name())
	}
	return detected, nil
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

// advpoolEventSink adapts the driver's reasoning events to the brain's
// telemetry store, keyed on the run's mission id so BuildReplayStream surfaces
// them in the run's replay. Actor is the fixed pool actor. nil tel => rec()
// no-ops (telemetry optional everywhere).
type advpoolEventSink struct{ tel *telemetry.Store }

func (s advpoolEventSink) Emit(missionID int64, kind, subject string, detail map[string]any) {
	rec(s.tel, missionID, kind, "corral-advpool", subject, detail)
}

// advpoolBugCatchSink persists a converged run's per-seat bug-catching
// observations into the DuckDB scorecard store, stamping the run context
// (ts via the brain clock, mission/repo/commit, source="pool") the pure driver
// does not carry. Satisfies advpool.BugCatchSink.
type advpoolBugCatchSink struct {
	store     *bugcatch.Store
	clock     func() time.Time
	missionID int64
	repo      string
	commit    string
}

func (s advpoolBugCatchSink) Record(recordID int64, recordHead string, obs []advpool.BugCatchObservation) {
	if s.store == nil {
		return
	}
	rows := make([]bugcatch.Observation, 0, len(obs))
	for _, o := range obs {
		rows = append(rows, bugcatch.Observation{
			TS: s.clock(), RecordID: recordID, RecordHead: recordHead,
			MissionID: s.missionID, Repo: s.repo, Commit: s.commit,
			Model: o.Model, Role: o.Role, Source: "pool",
			Catches: o.Catches, Opportunities: o.Opportunities,
			SoundTests: o.SoundTests, AuthoredTests: o.AuthoredTests,
			CriticFlags: o.CriticFlags, MutantsPlanted: o.MutantsPlanted, MutantsSurvived: o.MutantsSurvived,
			Shard: o.Shard, Region: o.Region, RegionComplexity: o.RegionComplexity, RegionLines: o.RegionLines,
			TestComplexity: o.TestComplexity, ParseRetries: o.ParseRetries, Dropped: o.Dropped, Shadow: o.Shadow,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		log.Printf("advpool: bugcatch record failed (mission %d record %d): %v", s.missionID, recordID, err)
	}
}

// advpoolCriticSink persists a converged run's execution-checked critic
// findings into the criticscore DuckDB store, stamping the run context
// (ts via the brain clock, mission/repo/commit) the pure driver does not
// carry — mirrors advpoolBugCatchSink exactly. The stable finding ID is
// "<recordID>:<queueFindingID>" (see criticscore.Finding's doc comment),
// composed here since the driver's observation carries the two halves
// separately. Satisfies advpool.CriticFindingSink.
type advpoolCriticSink struct {
	store     *criticscore.Store
	clock     func() time.Time
	missionID int64
	repo      string
	commit    string
}

func (s advpoolCriticSink) Record(recordID int64, recordHead string, obs []advpool.CriticFindingObservation) {
	if s.store == nil {
		return
	}
	ts := float64(s.clock().Unix())
	rows := make([]criticscore.Finding, 0, len(obs))
	for _, o := range obs {
		rows = append(rows, criticscore.Finding{
			ID:         fmt.Sprintf("%d:%d", recordID, o.QueueFindingID),
			TS:         ts,
			RecordID:   recordID,
			RecordHead: recordHead,
			Repo:       s.repo, Commit: s.commit, MissionID: s.missionID,
			Model:        o.Model,
			TargetTest:   o.TargetTest,
			TestFile:     o.TestFile,
			TestSelector: o.TestSelector,
			Scope:        o.Scope,
			Evidence:     o.Evidence,
			Severity:     o.Severity,
			Adjudication: o.Adjudication,
			Source:       o.Source,
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		log.Printf("advpool: criticscore record failed (mission %d record %d): %v", s.missionID, recordID, err)
	}
}

// telemetrySigner wraps advpool.CertSigner to re-emit the brain's
// "build_certified" telemetry event after a successful sign — the behavior
// the brain had before the signer was relocated into the leaf advpool
// package (see CertSigner's own doc comment: that move deliberately dropped
// the Telemetry field, since `corral certify --local` has no telemetry store
// to feed and the leaf package must not depend on one to run). This wrapper
// restores the brain's old certifyBuild-driven event WITHOUT re-coupling
// advpool itself to telemetry — only the brain-side StartAdversarialPool
// wiring uses it; the bare CertSigner stays what --local uses.
//
// The event fields mirror certifyBuild's own rec() call exactly (actor
// "corral-advpool", subject repo@commit, detail {repo, commit, head,
// anchored}) so a dashboard/query built against the old event shape keeps
// working unchanged. "anchored" isn't part of the Signer interface's return
// values (recordID, head, error) — it's looked up from the just-saved build
// record via Store.Get so the value is the real one CertSigner persisted,
// not a guess.
type telemetrySigner struct {
	inner advpool.CertSigner
	tel   *telemetry.Store
	store *buildstore.Store
}

func (s telemetrySigner) SignVerdict(ctx context.Context, v advpool.Verdict) (int64, string, error) {
	id, head, err := s.inner.SignVerdict(ctx, v)
	if err != nil {
		return id, head, err
	}

	var anchored bool
	if s.store != nil {
		if m, ok, gerr := s.store.Get(id); gerr == nil && ok {
			if a, ok := m["anchored"].(bool); ok {
				anchored = a
			}
		}
	}

	rec(s.tel, 0, "build_certified", "corral-advpool", v.Repo+"@"+v.Commit, map[string]any{
		"repo":     v.Repo,
		"commit":   v.Commit,
		"head":     head,
		"anchored": anchored,
	})
	return id, head, nil
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
	driver      *advpool.Driver
	missions    *mission.Store
	staffing    *mission.StaffingManager
	defaults    advpool.RoleAssignment // operator CORRALAI_ADVPOOL_MODELS base (nil = hardcoded)
	bugCatch    *bugcatch.Store        // optional scorecard store; nil = feature off (see StartRun)
	criticScore *criticscore.Store     // optional critic-accuracy store; nil = feature off (see StartRun)
	// shadowModel is the daemon-wide default challenger model (resolved once
	// in StartAdversarialPool from CORRALAI_ADVPOOL_SHADOW_MODEL, "" =
	// advpool.DefaultShadowModel). Left at its Go zero value ("") by a test
	// that constructs AdvPoolRuntime directly rather than through
	// StartAdversarialPool — shadow stays OFF for those unless the test
	// sets it explicitly, matching every pre-existing Driver/StartRun test's
	// expectations. A per-call AdvPoolRunSpec.ShadowModel always overrides
	// this daemon default (see StartRun).
	shadowModel string

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
	NMutants    int    `json:"n_mutants,omitempty" jsonschema:"how many seeded-violation mutants to generate PER SHARD (default 5) — the run's total mutant count is this divided across the resolved shard width, see max_shards"`
	Lang        string `json:"lang,omitempty" jsonschema:"source language of the code under review (default: inferred from code_path extension, e.g. go, python)"`
	MaxShards   int    `json:"max_shards,omitempty" jsonschema:"max mutant-generator seats fanned out across the file's functions, one per complexity-balanced group (default 8, see advpool.DefaultMaxShards). Bounds PARALLELISM only — every function is still probed regardless of this value; n_mutants is divided across the resolved shard width so the run's TOTAL mutant budget stays roughly constant as this changes"`
	ShadowModel string `json:"shadow_model,omitempty" jsonschema:"per-run override for the challenger seat that attacks every region a SECOND time for a region-controlled model head-to-head (default: the daemon's configured shadow model, see CORRALAI_ADVPOOL_SHADOW_MODEL; \"off\" disables it for this run only). Recorded for comparison — NEVER gates the verdict, and never adds a test-writer seat. Roughly DOUBLES this run's mutant-generator LLM calls (one challenger seat per shard) AND adds one extra dev-suite jail execution PER SHARD (the challenger's mutants are scored against the same dev suite as the primary, via a separate Scorer.Score call — see advpool.Driver.runShadowPass), bounded to at most a quarter of the run's deadline (advpool.ShadowTimeBudget)"`
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
	// AuthoredTest is the pool's compiling killing test (when the dev suite left
	// survivors the pool then killed), handed back so the dev can adopt it. Not
	// part of the signed Verdict — evidence, not certified state.
	AuthoredTest string `json:"authored_test,omitempty"`
}

// maxAdvPoolMutants mirrors maxControlMutants: bounds the compute a single
// admin request can trigger (each mutant costs a jail spawn — see
// adequacy.Score, which runs the test command once per mutant).
//
// This is a TOTAL-run PRIMARY-mutant budget, not a per-shard one, even though
// the field it clamps (AdvPoolRunSpec.NMutants / advpool.RunSpec.NMutants) is
// documented as PER-SHARD (renderMutantGeneratorShard renders the same
// rs.NMutants, unchanged, into every shard's prompt — see
// internal/advpool/roles.go). Before slice 2 (mutant-generator sharding)
// NMutants WAS the whole run's budget, so clamping it directly at this
// constant was correct. Sharding repointed the field's meaning without
// touching this clamp, which would let an operator's pre-sharding value of
// maxAdvPoolMutants multiply by MaxShards worth of EXTRA jail spend on a
// hosted daemon serving many users — the opposite of what "bounds the compute
// a single admin request can trigger" is supposed to mean.
// clampAdvPoolMutants divides this budget across the resolved shard width so
// the PRIMARY total mutant count stays bounded by this constant.
//
// The real, honest invariant (not just "divide and hope"): clampAdvPoolMutants
// floors requested/maxShards to a PER-SHARD cap, but never lets that cap drop
// below 1 (a 0-mutant shard would silently drop that region's coverage — see
// ShardSymbols's "every function gets probed" guarantee). floor(N/k)*k <= N
// holds for ANY k <= N, so as long as maxShards never exceeds maxAdvPoolMutants
// the floor never needs forcing up to 1 in a way that breaks the total bound.
// But once maxShards EXCEEDS maxAdvPoolMutants, floor(N/k) is 0 and the
// floor-of-1 escape hatch forces perShardCap back up to 1 regardless — at
// which point the run's total is 1*maxShards, which is now GREATER than N.
// That escape hatch is real and by itself makes this NOT a total bound for an
// arbitrarily large maxShards (e.g. max_shards:100 on a 100-symbol file would
// yield 100 primary mutants, not <=20). Closing it requires bounding maxShards
// itself — see maxAdvPoolShards/clampAdvPoolMaxShards below, which callers
// (StartRun) MUST apply before calling clampAdvPoolMutants for the invariant
// above to actually hold. With shadow on, the run's mutant TOTAL (primary +
// shadow) is up to 2x this constant, since the challenger mirrors the
// primary's per-shard budget on the SAME shard width.
const maxAdvPoolMutants = 20

// maxAdvPoolShards bounds MaxShards for a hosted run — the ceiling
// clampAdvPoolMutants' doc comment above depends on to keep its total-mutant
// invariant honest. Deliberately set EQUAL to maxAdvPoolMutants: for any
// maxShards <= maxAdvPoolMutants, floor(maxAdvPoolMutants/maxShards) is
// already >= 1 without the floor-of-1 escape hatch ever needing to fire, so
// floor(N/k)*k <= N holds unconditionally and the run's primary total stays
// at or under maxAdvPoolMutants. Set this higher than maxAdvPoolMutants and
// that guarantee breaks again for shard counts in between. An operator's
// MaxShards request above this ceiling is honored up to the ceiling, never
// refused outright — mirroring clampAdvPoolMutants' own "clamp, don't error"
// posture for an operator's stale/oversized request.
const maxAdvPoolShards = maxAdvPoolMutants

// clampAdvPoolMaxShards bounds a hosted run's requested shard width
// (already defaulted by the caller) at maxAdvPoolShards — see that
// constant's doc for why the ceiling has to be exactly this value.
func clampAdvPoolMaxShards(requested int) int {
	if requested > maxAdvPoolShards {
		return maxAdvPoolShards
	}
	return requested
}

// clampAdvPoolMutants bounds a run's PER-SHARD mutant request (requested,
// already defaulted to 5 by the caller) so the run's TOTAL PRIMARY mutant
// count never exceeds maxAdvPoolMutants — PROVIDED maxShards has already
// been bounded by clampAdvPoolMaxShards (see maxAdvPoolMutants' doc comment
// for the floor-of-one escape hatch this function cannot close on its own).
// Dividing (floor) rather than reapplying the old per-run cap per shard is
// what keeps the constant's TOTAL-budget meaning honest post-sharding; a
// floor of 1 keeps every shard probed at all (a 0-mutant shard would
// silently drop that region's coverage, which is exactly what sharding
// exists to prevent — see ShardSymbols's "every function gets probed"
// guarantee).
func clampAdvPoolMutants(requested, maxShards int) int {
	if maxShards < 1 {
		maxShards = 1
	}
	perShardCap := maxAdvPoolMutants / maxShards
	if perShardCap < 1 {
		perShardCap = 1
	}
	if requested > perShardCap {
		return perShardCap
	}
	return requested
}

// resolveAdvPoolRunDeadline widens the daemon's driver.RunDeadline by
// advpool.ShadowTimeBudget whenever the daemon's shadow model is configured
// — the hosted-path fix for exactly the hole cmd/corral's own
// resolveRunDeadline already closed on the CLI (both now delegate their
// arithmetic to advpool.ResolveRunDeadline). Without this, shadow work
// pushing a run past RunDeadline forces a timeoutVerdict (Status:
// needs-review) — shadow, which must NEVER gate the verdict, changing it via
// the deadline channel instead of the scoring one. The existing
// runShadowPass credit-back does not close this on its own: it only credits
// shadow SCORING time, never the shadow mutant-GENERATION calls, which on
// the hosted path happen entirely in a REMOTE worker outside the driver's
// tick loop (unlike scoring, which the driver's own Scorer.Score call
// performs and can therefore credit back).
//
// The design wrinkle: Driver.RunDeadline is one field set ONCE at daemon
// startup, while ShadowModel is per-run (a start_adversarial_run caller may
// override it per call — see StartRun). There is no per-run deadline to
// widen selectively, so this widens the daemon-wide deadline
// UNCONDITIONALLY whenever the daemon default (shadowModel, already resolved
// from CORRALAI_ADVPOOL_SHADOW_MODEL) is non-empty, covering every run for as
// long as the daemon stays up — including a run whose caller turns shadow
// OFF for that one call (in.ShadowModel == "off"), which merely gets more
// headroom than it strictly needs. That is a safe direction to be wrong in:
// extra deadline margin never hurts a run that converges faster than it, and
// it keeps this a one-line daemon-startup decision rather than requiring
// RunDeadline to become per-run state.
//
// Extracted as a pure function (base + shadowModel in, a Duration out) so
// the widening itself is unit-testable without standing up the full
// StartAdversarialPool wiring (sandbox jail, buildstore, telemetry, mission
// store, queue).
func resolveAdvPoolRunDeadline(base time.Duration, shadowModel string) time.Duration {
	if shadowModel == "" {
		return base
	}
	return advpool.ResolveRunDeadline(base, shadowModel)
}

// StartRun begins one adversarial-pool run: builds the RunSpec, extracts
// signatures (best-effort — a failure degrades to no signatures rather than
// refusing the run), picks a decorrelation-enforced role assignment off the
// live leaderboard, mints a tracking mission (reusing mission.CreateMission
// for the SAME create+id pattern every other mission goes through; q=nil so
// CreateMission does not also enqueue its placeholder phase — the pool's own
// DAG is enqueued by driver.StartRun, not by CreateMission's plan-to-tasks
// path), and enqueues the run's DAG. Refuses a second run while one is
// already active (single active run — slice 1's explicit scope limit).
// poolDirective is the tracking mission's human-facing directive — it names the
// change under audit (the code path), not the raw repo URL + full commit hash,
// which read as noise on the recordings gallery card.
func poolDirective(rs advpool.RunSpec) string {
	if p := strings.TrimSpace(rs.CodePath); p != "" {
		return "adversarial pool · " + p
	}
	return "adversarial pool"
}

func (rt *AdvPoolRuntime) StartRun(in AdvPoolRunSpec) (int64, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.activeID != 0 {
		return 0, fmt.Errorf("advpool: run %d is already active — only one active run is supported in this slice", rt.activeID)
	}

	// MaxShards: this is what turns sharding ON for the hosted pool. Prior to
	// this, RunSpec.MaxShards was left at its zero value on every hosted run,
	// so advpool.ShardSymbols(sigs, 0) always returned nil and the pool ran
	// exactly one whole-file mutant-generator seat no matter how many
	// functions the file had — sharding existed only on `certify --local`.
	// The design spec calls for sharding on both paths; an operator can still
	// override the width per-call (mirrors the NMutants override below), but
	// the default alone is what makes an ordinary hosted run shard.
	maxShards := in.MaxShards
	if maxShards <= 0 {
		maxShards = advpool.DefaultMaxShards
	}
	// Ceiling BEFORE clampAdvPoolMutants divides by it — see
	// maxAdvPoolShards' doc for why this order matters: clampAdvPoolMutants's
	// total-bound guarantee only holds once maxShards itself is bounded.
	maxShards = clampAdvPoolMaxShards(maxShards)

	nMutants := in.NMutants
	if nMutants <= 0 {
		nMutants = 5
	}
	// clampAdvPoolMutants divides by the RESOLVED WIDTH (maxShards), not the
	// actual shard count ShardSymbols will end up producing — those differ
	// whenever a file has fewer extractable symbols than maxShards (e.g. a
	// 3-function file at the default width 8 shards to only 3 seats, but this
	// still divides by 8). That under-spends the per-shard budget rather than
	// over-spending it (a smaller perShardCap than the file could actually
	// use), so it is conservative, not a safety issue — computing the true
	// count here would mean extracting signatures + calling ShardSymbols
	// BEFORE this clamp (today that happens later, after the language plugin
	// resolves — see below), which is more reordering than this minor
	// tightening is worth. Left as a known, intentional slack rather than a
	// bug.
	nMutants = clampAdvPoolMutants(nMutants, maxShards)

	// Resolve the language the jail will actually grade with — from the code
	// file's extension (a .py file is pytest-graded no matter what in.Lang
	// says). An explicit in.Lang is an assertion: it MUST match, or the run
	// is refused (fail-closed) rather than grading under a mismatched
	// toolchain with a mislabeled signed Verdict.Lang.
	langPlugin, err := resolveRunLang(in.Lang, in.CodePath)
	if err != nil {
		return 0, err
	}
	if err := langPlugin.Preflight(); err != nil {
		return 0, fmt.Errorf("advpool: language toolchain unavailable — refusing to run: %w", err)
	}

	// ShadowModel: the challenger seat's model for THIS run. A per-call
	// override (in.ShadowModel) always wins — "off"/"none" disables it for
	// this run only, an explicit model name uses that model — falling back to
	// the daemon-wide default (rt.shadowModel, resolved once in
	// StartAdversarialPool from CORRALAI_ADVPOOL_SHADOW_MODEL) when the call
	// leaves it unset. rt.shadowModel is "" (off) for any AdvPoolRuntime built
	// outside StartAdversarialPool — e.g. every existing unit test — so this
	// enable is opt-in per test/deployment, never a silent behavior change for
	// code that constructs the runtime directly.
	//
	// Turning shadow on here was deliberately deferred (see the sharding
	// design spec's §8) until the daemon dispatch path — a shard task
	// claimed by a REAL remote worker over the task queue, not run in-process
	// like `certify --local` — was proven safe by tests (see
	// internal/agentworker and cmd/corral-agent's isStructuredRole coverage
	// for the mutant-generator-shadow role, and TestAdvPoolStartRun_*Shadow*
	// below). It roughly DOUBLES the run's mutant-generator LLM spend (one
	// challenger seat per shard) AND adds one extra dev-suite jail execution
	// PER SHARD (each shard's challenger mutants are scored against the SAME
	// dev suite via their own Scorer.Score call, bounded in total by
	// advpool.ShadowTimeBudget — see driver.go's runShadowPass); it never adds
	// a test-writer seat or a pool-adequacy scoring call, since a shadow seat
	// is measurement only and never gates the verdict.
	shadowModel := rt.shadowModel
	if s := strings.TrimSpace(in.ShadowModel); s != "" {
		shadowModel = advpool.ResolveShadowModel(s)
	}

	rs := advpool.RunSpec{
		Repo: in.Repo, Commit: in.Commit, Goal: in.Goal,
		CodePath: in.CodePath, Code: in.Code,
		DevTestPath: in.DevTestPath, DevTestCode: in.DevTestCode,
		TestCmd: in.TestCmd, NMutants: nMutants,
		Lang:        langPlugin.Name(),
		MaxShards:   maxShards,
		ShadowModel: shadowModel,
	}

	sigs, err := repoindex.ExtractSignatures(rs.Code, rs.Lang)
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
		poolDirective(rs),
		[]mission.PhaseSpec{{Name: "adversarial-pool", Role: "system", Count: 1}}, false)
	if err != nil {
		return 0, fmt.Errorf("advpool: create tracking mission: %w", err)
	}

	rt.driver.Assign = assign
	if rt.bugCatch != nil {
		rt.driver.BugCatch = advpoolBugCatchSink{
			store: rt.bugCatch, clock: time.Now,
			missionID: mid, repo: rs.Repo, commit: rs.Commit,
		}
	}
	if rt.criticScore != nil {
		rt.driver.CriticFindings = advpoolCriticSink{
			store: rt.criticScore, clock: time.Now,
			missionID: mid, repo: rs.Repo, commit: rs.Commit,
		}
	}
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
		status := advpool.StatusNeedsReview
		if verdict.Status == advpool.StatusCertified {
			status = advpool.StatusCertified
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
	driver, err := advpool.NewDriver(opts.Queue, advpool.JailScorer{Jail: jail}, advpool.JailValidator{Jail: jail}, assign, 0.8)
	if err != nil {
		return nil, fmt.Errorf("advpool: new driver: %w", err)
	}
	driver.Signer = telemetrySigner{
		inner: advpool.CertSigner{Key: opts.CertifyKey, Store: opts.BuildStore, Witness: opts.Witness},
		tel:   opts.Telemetry,
		store: opts.BuildStore,
	}
	driver.Leaderboard = advpoolLeaderboardSink{tel: opts.Telemetry}
	driver.Events = advpoolEventSink{tel: opts.Telemetry}

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

	// CORRALAI_ADVPOOL_SHADOW_MODEL: the daemon-wide default challenger model.
	// Unset/empty resolves to advpool.DefaultShadowModel (shadow ON by
	// default for a hosted run, mirroring certify --local's own default);
	// "off"/"none" (case-insensitive) disables it daemon-wide as an operator
	// kill switch, still overridable per-call via start_adversarial_run's
	// shadow_model field.
	shadowModel := advpool.ResolveShadowModel(os.Getenv("CORRALAI_ADVPOOL_SHADOW_MODEL"))

	// Widen RunDeadline for the daemon's shadow allowance — see
	// resolveAdvPoolRunDeadline's doc for the full rationale (the hole this
	// closes, and why it must be unconditional rather than per-run).
	driver.RunDeadline = resolveAdvPoolRunDeadline(driver.RunDeadline, shadowModel)

	rt := &AdvPoolRuntime{
		driver:      driver,
		missions:    opts.Missions,
		staffing:    opts.Staffing,
		defaults:    defaults,
		bugCatch:    opts.BugCatch,
		criticScore: opts.CriticScore,
		shadowModel: shadowModel,
		tickErrors:  make(map[int64]int),
	}

	interval := opts.AdvPoolPollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	log.Printf("advpool: ENABLED — polling every %s; role models: mutant-generator=%s test-writer=%s test-critic=%s",
		interval, assign[advpool.RoleMutantGenerator], assign[advpool.RoleTestWriter], assign[advpool.RoleTestCritic])
	if shadowModel == "" {
		log.Printf("advpool: shadow challenger OFF (CORRALAI_ADVPOOL_SHADOW_MODEL=off)")
	} else {
		log.Printf("advpool: shadow challenger ON by default — model=%s (override per-run via start_adversarial_run's shadow_model, or set CORRALAI_ADVPOOL_SHADOW_MODEL=off to disable daemon-wide)", shadowModel)
	}
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
				RunID:        in.RunID,
				Found:        found,
				Converged:    st.Converged,
				Verdict:      st.Verdict,
				AuthoredTest: st.AuthoredTest,
			}, nil
		})
}
