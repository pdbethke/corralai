<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Adversarial-pool role→model config seam — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let an operator name the pool's cold-start role→model assignment via `CORRALAI_ADVPOOL_MODELS`, so the pool can be pointed at frontier models (instead of the hardcoded `qwen2.5-coder:7b`/`llama3.2:3b`) — with decorrelation still enforced and the effective assignment logged at startup. This is what makes an honest frontier run possible: the stamped model = the model the worker actually runs = what the signed verdict records.

**Architecture:** `advPoolAssign` currently seeds its base map from two hardcoded constants. Add a pure parser `parseAdvPoolModels(env) → (advpool.RoleAssignment, error)`, thread a `defaults advpool.RoleAssignment` through `advPoolAssign` and onto `AdvPoolRuntime` (so both `StartAdversarialPool`'s initial driver `Assign` and `StartRun`'s per-run reassignment use the same base), and read `CORRALAI_ADVPOOL_MODELS` in `StartAdversarialPool`. Malformed/decorrelated env → LOUD log + fall back to the hardcoded defaults (never silently run local models the operator didn't intend, and never crash the brain over a pool typo). Leaderboard evidence still overrides per-role when present (the flywheel is unchanged).

**Tech Stack:** Go 1.26.5; `internal/brain/advpool.go`, `internal/advpool` (RoleAssignment, CheckDecorrelation).

**Spec context:** extends `docs/superpowers/specs/2026-07-14-adversarial-pool-design.md` (the "threshold hardcoded 0.8 / models hardcoded — expose later" note). No new metaphor/rename; off-by-default and admin-only unchanged.

## Global Constraints

- New/edited files keep `// SPDX-License-Identifier: Elastic-2.0`.
- **Decorrelation is inviolable:** the effective assignment must always pass `advpool.CheckDecorrelation` (test-critic ≠ test-writer). An env that violates it is rejected (loud log) in favor of the hardcoded defaults — the pool must never start decorrelated.
- **Fail loud, never silently wrong:** a set-but-malformed `CORRALAI_ADVPOOL_MODELS` logs an error naming the problem and falls back to the hardcoded defaults; it does NOT crash the brain and does NOT silently proceed as if unset. The effective role→model assignment is logged at startup so it's never ambiguous which models will run.
- **No behavior change when the env is unset:** `advPoolAssign(nil)` with no env must produce byte-identical assignment to today (mutant-generator=test-writer=qwen, test-critic=llama).
- Verify gate before commit: `gofmt -l` (empty), `go build ./...`, `go test ./internal/brain/... ./internal/advpool/...`, `bash scripts/check-security.sh` (exit 0).

---

### Task 1: `CORRALAI_ADVPOOL_MODELS` seam

**Files:**
- Modify: `internal/brain/advpool.go` (the `defaultAdvPoolModel`/`defaultAdvPoolCriticModel` consts ~line 34; `advPoolAssign` ~line 297; `AdvPoolRuntime` struct ~line 340; `StartRun` ~line 383; `StartAdversarialPool` ~line 504)
- Test: `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `advpool.RoleAssignment` (a `map[advpool.Role]string`), `advpool.RoleMutantGenerator`/`RoleTestWriter`/`RoleTestCritic`, `advpool.CheckDecorrelation(assign) error`.
- Produces: `func parseAdvPoolModels(s string) (advpool.RoleAssignment, error)`; `advPoolAssign` gains a `defaults advpool.RoleAssignment` first param; `AdvPoolRuntime` gains a `defaults advpool.RoleAssignment` field used by `StartRun`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/brain/advpool_test.go`:

```go
func TestParseAdvPoolModels(t *testing.T) {
	// Happy path: all three roles, decorrelated.
	got, err := parseAdvPoolModels("mutant-generator=anthropic/claude-sonnet-4-6,test-writer=anthropic/claude-sonnet-4-6,test-critic=google/gemini-2.5-flash")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got[advpool.RoleMutantGenerator] != "anthropic/claude-sonnet-4-6" ||
		got[advpool.RoleTestWriter] != "anthropic/claude-sonnet-4-6" ||
		got[advpool.RoleTestCritic] != "google/gemini-2.5-flash" {
		t.Fatalf("wrong assignment: %+v", got)
	}

	// Whitespace tolerated.
	if _, err := parseAdvPoolModels(" mutant-generator = a , test-writer = b , test-critic = c "); err != nil {
		t.Fatalf("whitespace form should parse: %v", err)
	}

	// Decorrelation violation (critic == writer) → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b,test-critic=b"); err == nil {
		t.Fatalf("critic==writer must be rejected")
	}

	// Missing a role → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b"); err == nil {
		t.Fatalf("missing test-critic must be rejected")
	}

	// Unknown role key → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b,test-critic=c,pentester=d"); err == nil {
		t.Fatalf("unknown role must be rejected")
	}

	// Empty value → error.
	if _, err := parseAdvPoolModels("mutant-generator=,test-writer=b,test-critic=c"); err == nil {
		t.Fatalf("empty model must be rejected")
	}

	// Empty string → (nil, nil): "unset", caller uses hardcoded defaults.
	got, err = parseAdvPoolModels("")
	if err != nil || got != nil {
		t.Fatalf("empty string should be (nil,nil), got (%v,%v)", got, err)
	}
}

func TestAdvPoolAssignUsesDefaults_UnsetIdenticalToToday(t *testing.T) {
	// nil defaults → the hardcoded qwen/llama assignment (no behavior change).
	got := advPoolAssign(nil, nil)
	if got[advpool.RoleMutantGenerator] != defaultAdvPoolModel ||
		got[advpool.RoleTestWriter] != defaultAdvPoolModel ||
		got[advpool.RoleTestCritic] != defaultAdvPoolCriticModel {
		t.Fatalf("nil defaults must reproduce today's assignment, got %+v", got)
	}

	// Provided defaults (no leaderboard staffing) → those models, decorrelation intact.
	base := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "anthropic/claude-sonnet-4-6",
		advpool.RoleTestWriter:      "anthropic/claude-sonnet-4-6",
		advpool.RoleTestCritic:      "google/gemini-2.5-flash",
	}
	got = advPoolAssign(nil, base)
	if got[advpool.RoleTestWriter] != "anthropic/claude-sonnet-4-6" || got[advpool.RoleTestCritic] != "google/gemini-2.5-flash" {
		t.Fatalf("provided defaults not used: %+v", got)
	}
	if err := advpool.CheckDecorrelation(got); err != nil {
		t.Fatalf("assignment must stay decorrelated: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/brain/ -run 'TestParseAdvPoolModels|TestAdvPoolAssignUsesDefaults' -v`
Expected: FAIL to compile — `parseAdvPoolModels` undefined and `advPoolAssign` takes one arg not two.

- [ ] **Step 3: Implement the parser + thread the defaults**

Add the parser (put it near `advPoolAssign`). **Types (verified):** `advpool.RoleAssignment` is `map[string]string`; `advpool.RoleMutantGenerator`/`RoleTestWriter`/`RoleTestCritic` are plain string constants (`"mutant-generator"`/`"test-writer"`/`"test-critic"`); `advpool.CheckDecorrelation(RoleAssignment) error` checks `critic != "" && critic == writer`. So keys are plain strings — do NOT use `advpool.Role` (that's a struct, not the key type).

```go
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
```

Change `advPoolAssign` to take a `defaults` base. Replace the two hardcoded-const seeds with the provided defaults (falling back to the consts when `defaults` is nil), keeping the rest — leaderboard override + critic decorrelation-forcing — unchanged:

```go
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
```

(Note the one behavior nuance to preserve: when `defaults` provides a critic model equal to the test-writer model, that can't happen through `parseAdvPoolModels` — it rejects critic==writer — but `advPoolAssign` still defends against it via `advPoolFallbackCritic`, exactly as before for the const path.)

Add a `defaults` field to `AdvPoolRuntime` and use it in `StartRun`'s reassignment:

```go
type AdvPoolRuntime struct {
	driver   *advpool.Driver
	missions *mission.Store
	staffing *mission.StaffingManager
	defaults advpool.RoleAssignment // operator CORRALAI_ADVPOOL_MODELS base (nil = hardcoded)

	mu         sync.Mutex
	activeID   int64
	tickErrors map[int64]int
}
```

In `StartRun`, change `assign := advPoolAssign(rt.staffing)` → `assign := advPoolAssign(rt.staffing, rt.defaults)`.

In `StartAdversarialPool`, read + parse the env, log loudly on error and fall back, then use it for the initial driver assignment and store it on the runtime; log the effective assignment:

```go
	defaults, derr := parseAdvPoolModels(os.Getenv("CORRALAI_ADVPOOL_MODELS"))
	if derr != nil {
		log.Printf("advpool: CORRALAI_ADVPOOL_MODELS invalid (%v) — falling back to defaults %s/%s", derr, defaultAdvPoolModel, defaultAdvPoolCriticModel)
		defaults = nil
	}

	jail := adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)
	assign := advPoolAssign(opts.Staffing, defaults)
	driver, err := advpool.NewDriver(opts.Queue, advpoolScorer{jail: jail}, advpoolValidator{jail: jail}, assign, 0.8)
	// ...unchanged...
	rt := &AdvPoolRuntime{
		driver:     driver,
		missions:   opts.Missions,
		staffing:   opts.Staffing,
		defaults:   defaults,
		tickErrors: make(map[int64]int),
	}
	// ...unchanged...
	log.Printf("advpool: ENABLED — polling every %s; role models: mutant-generator=%s test-writer=%s test-critic=%s",
		interval, assign[advpool.RoleMutantGenerator], assign[advpool.RoleTestWriter], assign[advpool.RoleTestCritic])
```

Add the `"os"` import to `internal/brain/advpool.go` if not already present. Confirm `advPoolAssign`'s other call site (there are exactly two: `StartAdversarialPool` and `StartRun`) — both must pass the new second arg.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/brain/ -run 'TestParseAdvPoolModels|TestAdvPoolAssign' -v` then `go test ./internal/brain/... ./internal/advpool/...`
Expected: PASS (new + existing; the unset-path test proves no behavior change).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/brain/advpool.go internal/brain/advpool_test.go
go build ./... && go test ./internal/brain/... ./internal/advpool/... && bash scripts/check-security.sh
git add internal/brain/advpool.go internal/brain/advpool_test.go
git commit -m "advpool: CORRALAI_ADVPOOL_MODELS seam for operator-set role models (frontier-ready)"
```

## Self-Review (plan author)

- **Spec coverage:** the single deliverable (env seam, decorrelation-enforced, fail-loud, unset==today) maps to Task 1's steps.
- **Type consistency:** `advPoolAssign(staffing, defaults)` two-arg form used identically in both call sites; `parseAdvPoolModels` returns `advpool.RoleAssignment`; role literals taken from `advpool.Role*` constants (implementer verifies the exact string values).
- **No behavior change when unset:** `TestAdvPoolAssignUsesDefaults_UnsetIdenticalToToday` locks it.
