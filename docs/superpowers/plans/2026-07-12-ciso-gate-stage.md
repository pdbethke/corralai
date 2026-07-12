<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: StageCandidate (author → triage → store)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tie the tiers together: **author** a candidate test, **triage** its surviving mutants (independent reviewer → gap vs equivalent), and **store** the candidate *unvetted* with its full evidence (kill rate, survived/discarded, verdicts) for the CISO to review. This puts a candidate into the human-gate queue (`ListPending`) — the composition that turns the built parts into "agentic testing produces something a human approves."

**Architecture:** Two small enabling changes then the composition. (1) `authoring.Result` exposes the *valid scored mutants* so survivors can be recovered by ID for triage. (2) `controlspec.GateTest` gets an **opaque** `VerdictsJSON` column — the store stays type-agnostic about verdicts (no cross-package type import, no dup); the caller marshals `[]testgen.Verdict` in and the CISO surface unmarshals it out. (3) `internal/cisogate.StageCandidate` runs `authoring.Author` → filters survivors from the report → `testgen.TriageSurvivors` → `store.SaveCandidate`. All injected interfaces (two `testgen.LLM` seats — writer and reviewer — + `adequacy.Jail` + a real `controlspec.Store`), so it's testable with fakes.

**Tech Stack:** Go 1.26.5; `internal/authoring`, `internal/testgen`, `internal/adequacy`, `internal/controlspec`, `encoding/json`.

## Global Constraints
- SPDX header on every new file.
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **No live model in tests** (two fake `testgen.LLM` seats + a fake `adequacy.Jail` + a real temp-DuckDB `controlspec.Store`).
- **Independence** — the writer seat and the reviewer seat are SEPARATE `testgen.LLM` arguments to `StageCandidate` (in production, different models/prompts; the reviewer must not be the writer).
- **Stored unvetted** — `StageCandidate` stores via `SaveCandidate`, which forces `vetted=false`; a staged candidate is never auto-gating.
- **Deterministic** — no `time.Now()` in the composition (caller-stamped `Now`); the store stays clock-free.
- **DRY / no dup** — do NOT add a second `Verdict` struct; the store persists verdicts as opaque JSON.
- corral metaphor.

## File Structure
- `internal/authoring/authoring.go` — add `Mutants` to `Result`; populate it in `Author`. (modify)
- `internal/controlspec/types.go` — add `VerdictsJSON` to `GateTest`. (modify)
- `internal/controlspec/store.go` — add the `verdicts` column to `gate_tests`; persist/read it. (modify)
- `internal/cisogate/stage.go` — `StageRequest`, `StageCandidate`. (new)
- `internal/cisogate/stage_test.go` — the composition test. (new)

---

## Task 1: authoring.Result exposes the scored mutants

**Files:** Modify `internal/authoring/authoring.go`; Test `internal/authoring/authoring_test.go`

**Interfaces:**
- Produces: `Result.Mutants []adequacy.Mutant` — the VALID (compile-verified) mutants that were scored, so a caller can recover a survivor's code by its ID.

- [ ] **Step 1: Extend `TestAuthor`** (from the existing test) to also assert `res.Mutants` contains exactly the valid mutants (m1, m3), NOT the discarded non-compiling one (m2):
```go
	if len(res.Mutants) != 2 || res.Mutants[0].ID != "m1" || res.Mutants[1].ID != "m3" {
		t.Fatalf("Result.Mutants should be the valid scored mutants [m1 m3], got %+v", res.Mutants)
	}
```
- [ ] **Step 2: Run, watch fail** (`res.Mutants` undefined / empty).
- [ ] **Step 3: Implement.** Add `Mutants []adequacy.Mutant` to the `Result` struct (with a doc comment: "the valid, compile-verified mutants that were scored — the invalid/non-compiling ones are in Discarded"). In `Author`, after `compileVerify` and `Score`, set `Result{Test: test, Report: rep, Discarded: discarded, Mutants: valid}`.
- [ ] **Step 4: Run, watch pass.** `go test ./internal/authoring/...` → PASS. **Commit:** `feat(authoring): Result exposes the valid scored mutants (for survivor triage)`.

---

## Task 2: controlspec.GateTest carries an opaque verdicts JSON

**Files:** Modify `internal/controlspec/types.go`, `internal/controlspec/store.go`; Test `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `GateTest.VerdictsJSON string` — an opaque JSON blob of the reviewer's per-mutant verdicts; the store never interprets it.

- [ ] **Step 1: Extend `TestGateTestsSaveGetPending`** to set + round-trip `VerdictsJSON`:
```go
	gt.VerdictsJSON = `[{"MutantID":"m2","RealGap":true,"Rationale":"misses empty grants"}]`
	// ...after SaveCandidate + ListPending:
	if pend[0].VerdictsJSON != gt.VerdictsJSON {
		t.Fatalf("verdicts json not round-tripped: %q", pend[0].VerdictsJSON)
	}
```
- [ ] **Step 2: Run, watch fail** (`VerdictsJSON` undefined).
- [ ] **Step 3: Implement.** Add `VerdictsJSON string` to `GateTest` (doc: "opaque JSON of the reviewer's []Verdict; the store does not interpret it — the CISO surface decodes it"). In `OpenStore`, add a `verdicts VARCHAR NOT NULL DEFAULT ''` column to the `gate_tests` `CREATE TABLE`. In `SaveCandidate`, persist `gt.VerdictsJSON` (INSERT the new column; if empty, store `''`). In `GetVetted` and `ListPending`, SELECT and scan the `verdicts` column into `VerdictsJSON`. Keep everything else (vetted=false forcing, owner-scope, clock discipline) unchanged.
  - **Note:** existing `gate_tests` rows from before this column existed won't have `verdicts`; the `DEFAULT ''` + `CREATE TABLE IF NOT EXISTS` handles fresh DBs. (No migration concern — the feature isn't in production yet.)
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS (existing gate_tests + control_goals tests too). **Commit:** `feat(controlspec): GateTest carries opaque reviewer-verdicts JSON`.

---

## Task 3: cisogate.StageCandidate — the composition

**Files:** Create `internal/cisogate/stage.go`, `internal/cisogate/stage_test.go`

**Interfaces:**
```go
type StageRequest struct {
    authoring.Request           // Goal (intent), Code, Lang, CodePath, TestPath, Base, NMutants, BuildCmd, TestCmd
    Owner  string               // the CISO principal
    GoalID string               // the controlspec Goal.ID this test verifies
    Target string               // the target identifier stored on the GateTest
    Now    time.Time            // caller-stamped
}
// StageCandidate authors a candidate test, triages its surviving mutants with an
// INDEPENDENT reviewer seat, and stores the candidate UNVETTED for CISO review.
// Returns the stored (unvetted) GateTest.
func StageCandidate(ctx context.Context, writer, reviewer testgen.LLM, jail adequacy.Jail, store *controlspec.Store, req StageRequest) (controlspec.GateTest, error)
```

- [ ] **Step 1: Failing test — the full stage flow (fake writer + fake reviewer + fake jail + real store).**
```go
func TestStageCandidate(t *testing.T) {
	writer := &fakeLLM{onSystem: func(sys string) string {
		if strings.Contains(sys, "TEST-WRITER") {
			return "```go\npackage target\nimport \"testing\"\nfunc TestGoal(t *testing.T){}\n```"
		}
		return "===MUTATION_1===\nOK m1\n===MUTATION_2===\nBAD m2\n===MUTATION_3===\nOK m3\n"
	}}
	reviewer := &fakeLLM{resp: "MUTANT m3: GAP: the test misses the m3 path\n"}
	jail := &fakeJail{onRun: func(files map[string]string, cmd []string) bool {
		code := files["auth.go"]
		if cmd[1] == "build" { return !strings.Contains(code, "BAD") } // m2 discarded
		return code == "COMPLIANT" || strings.Contains(code, "m3")     // m1 killed, m3 survives
	}}
	store, _ := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer store.Close()

	req := StageRequest{
		Request: authoring.Request{Goal: "deny by default", Code: "COMPLIANT", Lang: "go", CodePath: "auth.go", TestPath: "auth_gate_test.go",
			Base: map[string]string{"go.mod": "module target\ngo 1.26\n"}, NMutants: 3, BuildCmd: []string{"go", "build", "./"}, TestCmd: []string{"go", "test", "./"}},
		Owner: "ciso@bankz", GoalID: "asvs-v4.1.1", Target: "bankz/app:auth.go", Now: time.Unix(1_700_000_000, 0).UTC(),
	}
	gt, err := StageCandidate(context.Background(), writer, reviewer, jail, store, req)
	if err != nil { t.Fatal(err) }

	// stored UNVETTED with the right evidence
	if gt.Vetted { t.Fatal("staged candidate must be unvetted") }
	pend, _ := store.ListPending("ciso@bankz")
	if len(pend) != 1 || pend[0].Goal != "asvs-v4.1.1" { t.Fatalf("not staged: %+v", pend) }
	if pend[0].KillRate < 0.49 || pend[0].KillRate > 0.51 { t.Errorf("kill rate = %v, want 0.5", pend[0].KillRate) }
	// the survivor (m3) was triaged and the verdict persisted as JSON
	if !strings.Contains(pend[0].VerdictsJSON, "m3") || !strings.Contains(pend[0].VerdictsJSON, "GAP-ness") && !strings.Contains(pend[0].VerdictsJSON, "RealGap") {
		t.Errorf("verdict not stored: %q", pend[0].VerdictsJSON)
	}
	// reviewer got an INDEPENDENT seat (its own prompt) and saw the survivor m3
	if !strings.Contains(reviewer.gotSystem, "TEST-REVIEWER") || !strings.Contains(reviewer.gotUser, "MUTANT m3") {
		t.Errorf("reviewer not called independently on the survivor: sys=%q", reviewer.gotSystem)
	}
}
```
(Reuse the `fakeLLM`/`fakeJail` shapes from the testgen/authoring tests — define local copies in `stage_test.go` matching the injected interfaces. The verdict-JSON assertion should just confirm the survivor's ID and the marshaled `RealGap` field are present.)

- [ ] **Step 2: Run, watch fail** (`StageCandidate` undefined).
- [ ] **Step 3: Implement `stage.go`.**
```go
func StageCandidate(ctx context.Context, writer, reviewer testgen.LLM, jail adequacy.Jail, store *controlspec.Store, req StageRequest) (controlspec.GateTest, error) {
	res, err := authoring.Author(ctx, writer, jail, req.Request)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	// Recover the surviving mutants' code (by ID) from the valid scored set.
	byID := make(map[string]adequacy.Mutant, len(res.Mutants))
	for _, m := range res.Mutants {
		byID[m.ID] = m
	}
	var survivors []adequacy.Mutant
	for _, id := range res.Report.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}
	verdicts, err := testgen.TriageSurvivors(ctx, reviewer, req.Goal, req.Code, res.Test, survivors)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	vj, err := json.Marshal(verdicts) // nil verdicts → "null"; harmless (opaque to the store)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	gt := controlspec.GateTest{
		Owner: req.Owner, Goal: req.GoalID, Target: req.Target,
		Test: res.Test, KillRate: res.Report.KillRate(),
		Survived: res.Report.Survived, Discarded: res.Discarded,
		VerdictsJSON: string(vj), CreatedTS: req.Now,
	}
	if err := store.SaveCandidate(gt); err != nil {
		return controlspec.GateTest{}, err
	}
	return gt, nil
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/cisogate/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(cisogate): StageCandidate — author, triage survivors, store unvetted for CISO review`.

---

## Self-Review
- **Spec coverage (§4f authoring→human-gate handoff):** author → triage → store a candidate unvetted, with kill rate + survived + discarded + verdicts, in the CISO's ListPending queue ✓; independent reviewer seat ✓; stored unvetted (never auto-gates) ✓.
- **DRY:** no second `Verdict` type — verdicts persist as opaque JSON the store doesn't interpret; the surface decodes with `testgen.Verdict`.
- **Type consistency:** `Result.Mutants`, `GateTest.VerdictsJSON`, `StageRequest`/`StageCandidate` stable; composes the real `Author`/`TriageSurvivors`/`SaveCandidate`.
- **Determinism:** no `time.Now()` in the composition (caller `Now`); the survivor lookup is a map build + ordered range over `Report.Survived` (stable).
- **The load-bearing behavior under test:** a candidate lands in `ListPending` unvetted, with the survivor triaged and the verdict persisted; the reviewer is a distinct seat that saw the survivor.
- **Out of scope (later):** the CISO approval surface (render + Promote/Reject); the gate dimension (4g) reading `GetVetted`; goal→file mapping; the brain wiring (real model seats + jail); regenerate-on-strengthen.
