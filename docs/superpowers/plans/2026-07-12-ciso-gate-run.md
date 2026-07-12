<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the running-tier check (list vetted + run against head)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The deterministic heart of the gate dimension: **list a CISO's vetted tests** and **run them against a PR's head code** in the jail, aggregating to a single pass/fail (fail-closed: any vetted test fails → the gate fails). This is what a per-commit `corral/ciso-gate` check will execute; the forge poll/checkout/sign/post wiring rides on top (deliberate integration, not this plan).

**Architecture:** (1) `controlspec.ListVetted(owner)` — the gate reads the owner's vetted (CISO-approved) tests (mirror of `ListPending`, `vetted = TRUE`). (2) `internal/cisogate.RunCisoGate` — for each vetted test, assemble a workspace (base + the target's head code + the vetted test), run the test in the injected `adequacy.Jail`, and aggregate. Injected jail → testable with a fake; no forge, no signing, no live model here.

**Tech Stack:** Go 1.26.5; `internal/controlspec`, `internal/adequacy`.

## Global Constraints
- SPDX header on every new file.
- **TDD**; per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Fail-closed aggregate:** `RunCisoGate.Pass` is true ONLY if every check passed; any single fail → `Pass=false`. A jail error propagates (never silently passes).
- **Empty checks = vacuous pass, documented:** with zero checks `Pass=true` (nothing to fail) — the CALLER enforces coverage (an *uncovered* goal must be handled as a fail by the forge-wiring layer, per the spec's "report as uncovered, never silently skip"). Document this on `RunCisoGate`.
- **Deterministic**: no `time.Now()`; results preserve check input order.
- Mirror `controlspec.ListPending` for `ListVetted`; do not break existing controlspec tests.
- corral metaphor.

## File Structure
- `internal/controlspec/store.go` — add `ListVetted`. (modify)
- `internal/cisogate/run.go` — `CisoCheck`, `CisoTestResult`, `CisoResult`, `RunCisoGate`. (new)
- `internal/controlspec/gate_tests_test.go` — `ListVetted` test. (modify)
- `internal/cisogate/run_test.go` — `RunCisoGate` test. (new)

---

## Task 1: controlspec.ListVetted

**Files:** Modify `internal/controlspec/store.go`; Test `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `func (*Store) ListVetted(owner string) ([]GateTest, error)` — owner's `vetted=TRUE` tests, ordered.

- [ ] **Step 1: Failing test — only vetted tests are listed.**
```go
func TestListVetted(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = s.SaveCandidate(GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "t1", Test: "x", KillRate: 1, CreatedTS: now})
	_ = s.SaveCandidate(GateTest{Owner: "ciso@bankz", Goal: "g2", Target: "t2", Test: "y", KillRate: 1, CreatedTS: now})
	// vet only g1
	_, _ = s.Promote("ciso@bankz", "g1", "t1", now)

	v, err := s.ListVetted("ciso@bankz")
	if err != nil { t.Fatal(err) }
	if len(v) != 1 || v[0].Goal != "g1" || !v[0].Vetted {
		t.Fatalf("ListVetted should return only the promoted g1: %+v", v)
	}
	// owner isolation
	if o, _ := s.ListVetted("dev@bankz"); len(o) != 0 {
		t.Fatalf("vetted test leaked across owners: %+v", o)
	}
}
```
- [ ] **Step 2: Run, watch fail** (`ListVetted` undefined).
- [ ] **Step 3: Implement** `ListVetted` mirroring `ListPending` but `WHERE owner = ? AND vetted = TRUE ORDER BY goal, target` (same scan + JSON-unmarshal of survived/discarded + NullTime handling).
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS. **Commit:** `feat(controlspec): ListVetted — the CISO-approved tests the gate runs`.

---

## Task 2: cisogate.RunCisoGate

**Files:** Create `internal/cisogate/run.go`; Test `internal/cisogate/run_test.go`

**Interfaces:**
```go
type CisoCheck struct {
    Test     controlspec.GateTest // the vetted test (Test content, Goal, Target)
    HeadCode string               // the target file's content at the PR head commit
    CodePath string               // the target's filename in the workspace, e.g. "auth.go"
    TestPath string               // where the vetted test goes, e.g. "auth_ciso_test.go"
}
type CisoTestResult struct {
    Goal, Target string
    Passed       bool
}
type CisoResult struct {
    Pass    bool             // fail-closed: true ONLY if every check passed
    Results []CisoTestResult
}
// RunCisoGate runs each vetted test against its target's head code in the jail and
// aggregates. Pass is true only if ALL checks passed (empty checks → true, vacuously;
// the caller must treat an uncovered goal as a fail — this primitive only reports the
// checks it was given). A jail error aborts and propagates (never a silent pass).
func RunCisoGate(ctx context.Context, jail adequacy.Jail, base map[string]string, checks []CisoCheck, testCmd []string) (CisoResult, error)
```

- [ ] **Step 1: Failing test — aggregate pass/fail over vetted tests against head (fake jail).**
```go
func TestRunCisoGate(t *testing.T) {
	// fake jail: a test "passes" unless the head code contains "VIOLATION".
	jail := &fakeJail{onRun: func(files map[string]string, cmd []string) bool {
		return !strings.Contains(files["auth.go"], "VIOLATION")
	}}
	base := map[string]string{"go.mod": "module target\ngo 1.26\n"}

	// two vetted tests; the second target's head code violates → overall fail.
	checks := []CisoCheck{
		{Test: controlspec.GateTest{Goal: "g1", Target: "t1", Test: "package target\n// t1"}, HeadCode: "package target\n// clean", CodePath: "auth.go", TestPath: "auth_ciso_test.go"},
		{Test: controlspec.GateTest{Goal: "g2", Target: "t2", Test: "package target\n// t2"}, HeadCode: "package target\n// VIOLATION", CodePath: "auth.go", TestPath: "auth_ciso_test.go"},
	}
	res, err := RunCisoGate(context.Background(), jail, base, checks, []string{"go", "test", "./"})
	if err != nil { t.Fatal(err) }
	if res.Pass { t.Fatal("one vetted test failed → gate must fail (fail-closed)") }
	if len(res.Results) != 2 || !res.Results[0].Passed || res.Results[1].Passed {
		t.Fatalf("per-test results wrong: %+v", res.Results)
	}
	// all-pass case
	clean := []CisoCheck{checks[0]}
	if r, _ := RunCisoGate(context.Background(), jail, base, clean, []string{"go", "test", "./"}); !r.Pass {
		t.Fatal("all vetted tests pass → gate passes")
	}
	// empty checks → vacuous pass (caller enforces coverage)
	if r, _ := RunCisoGate(context.Background(), jail, base, nil, []string{"go", "test", "./"}); !r.Pass || len(r.Results) != 0 {
		t.Fatalf("empty checks → vacuous pass, no results: %+v", r)
	}
}
```
(Define a local `fakeJail{onRun func(files map[string]string, cmd []string) bool}` implementing `adequacy.Jail`'s `RunTest`.)

- [ ] **Step 2: Run, watch fail** (`RunCisoGate` undefined).
- [ ] **Step 3: Implement `run.go`.**
```go
func RunCisoGate(ctx context.Context, jail adequacy.Jail, base map[string]string, checks []CisoCheck, testCmd []string) (CisoResult, error) {
	res := CisoResult{Pass: true}
	for _, c := range checks {
		ws := make(map[string]string, len(base)+2)
		for k, v := range base {
			ws[k] = v
		}
		ws[c.CodePath] = c.HeadCode
		ws[c.TestPath] = c.Test.Test
		passed, err := jail.RunTest(ctx, ws, testCmd)
		if err != nil {
			return CisoResult{}, fmt.Errorf("cisogate: run vetted test for %s@%s: %w", c.Test.Goal, c.Test.Target, err)
		}
		res.Results = append(res.Results, CisoTestResult{Goal: c.Test.Goal, Target: c.Test.Target, Passed: passed})
		if !passed {
			res.Pass = false
		}
	}
	return res, nil
}
```

- [ ] **Step 4: Run, watch pass.** `go test ./internal/cisogate/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(cisogate): RunCisoGate — run vetted CISO tests against a head commit (fail-closed)`.

---

## Self-Review
- **Spec coverage (§4g running tier core):** list the CISO-vetted tests ✓; run them against the PR head in the jail ✓; fail-closed aggregate (any fail → gate fails) ✓; empty-checks caveat documented (caller enforces coverage) ✓; deterministic under a fake jail ✓.
- **No placeholders:** complete `ListVetted` mirror + `RunCisoGate` + fake-jail tests.
- **Type consistency:** `ListVetted` returns `[]GateTest`; `RunCisoGate` consumes `controlspec.GateTest` + `adequacy.Jail`.
- **Determinism / fail-closed:** results in check order; `Pass` false on any single fail; a jail error aborts (never a silent pass).
- **Out of scope (the deliberate integration):** the forge wiring (poll PR → `CheckoutPR` → extract each target's head code → `GetVetted`/`ListVetted` → `RunCisoGate` → sign the verdict → post `corral/ciso-gate` as a distinct required check on the repo gate); drift diagnostics (compile-fail vs real-fail); goal→file mapping (only run tests for changed files); the CISO surface; the brain wiring (real jail + model).
