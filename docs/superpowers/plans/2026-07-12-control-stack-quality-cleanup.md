<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Control-stack code-quality cleanup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the verified DRY + correctness findings from the 3-reviewer audit of the control-gate/authoring stack, without changing external behavior — so the code that will certify real PRs is top-shelf.

**Architecture:** Six mostly-independent tasks. Behavior-preserving refactors (the existing test suites are the safety net); new tests only where new surface is added. Touches `internal/controlspec`, `internal/controlgate`, `internal/brain`, and — deliberately in scope — the **live merge gate** `internal/gate`.

**Tech Stack:** Go 1.26.5; DuckDB (`go-duckdb/v2`), `database/sql`.

## Global Constraints
- SPDX header on every new file (no new files expected; all edits are to existing files).
- **TDD**; per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Behavior-preserving:** these are refactors + small correctness fixes. Existing tests MUST stay green unchanged (except where a signature legitimately changes — Task 6 — then update the callers/tests). Do NOT alter fail-closed semantics, determinism (injected clocks), or owner-scoping.
- **Merge-critical care:** Tasks 5 & 6 touch the live merge gate + control gate runners. The fail-closed invariant (success posted only on real exit-0 + signed; every error path records `Passed=false` and posts non-success) must remain exactly as-is.
- No SQL injection: any string interpolated into SQL must be an internal constant literal, never caller input; all values stay `?`-parameterized.
- corral metaphor; "control owner", never "CISO".

## File Structure
- `internal/controlspec/store.go` — scan/list extraction (T1); `GetCandidate` (T2); `verdicts` migration (T3). (modify)
- `internal/controlspec/bundle.go` — atomic `ImportBundle` (T3). (modify)
- `internal/controlspec/gate_tests.go` — `RowsAffected` error handling (T3). (modify)
- `internal/controlspec/*_test.go` — new/adjusted tests (T1-T3). (modify)
- `internal/brain/controltools.go` — `getControl` uses `GetCandidate` (T2); `n_mutants` ceiling + Unmarshal comment (T4). (modify)
- `internal/gate/runner.go` — `FailClosed` extraction + `Save` logging (T5). (modify)
- `internal/gate/config.go` or a new `internal/gate/failclosed.go` — the shared `FailClosed` helper (T5). (modify/new)
- `internal/brain/gate.go` — `defaultGateRecordURL` (T5). (modify)
- `internal/brain/controlgate.go` — `fail` delegates to `gate.FailClosed`; `RecordID` threading + attempts comment (T5, T6). (modify)
- `internal/controlgate/post.go` — `PostControlGate` returns the `recordID` (T6). (modify)

---

## Task 1: controlspec — collapse the triplicated gate_tests scan/list [HIGH DRY]

**Files:** Modify `internal/controlspec/store.go`; existing tests in `internal/controlspec/gate_tests_test.go` are the net.

**Interfaces:**
- Produces (package-private): `gateTestCols` const, `scanGateTest(sc rowScanner, owner string) (GateTest, error)`, `(*Store).listGateTests(op, whereVetted, owner string) ([]GateTest, error)`. `ListPending`/`ListVetted`/`GetVetted` collapse onto them; their signatures are UNCHANGED.

This is a behavior-preserving extraction. `ListPending` and `ListVetted` are ~30 near-identical lines (differ only by `vetted = FALSE` vs `TRUE`); the GateTest column-list + `Scan` + unmarshal + UTC block is repeated in `GetVetted`/`ListPending`/`ListVetted` (4 positional sites counting the `SaveCandidate` INSERT). The existing tests (`TestRecipeRoundTrip`, `TestListVetted`, `TestListPending`, `TestGateTestsHumanGate` or similar) already cover all three read paths — they are your regression net.

- [ ] **Step 1: Confirm the net.** `go test ./internal/controlspec/ -v` → note the tests that exercise `ListPending`/`ListVetted`/`GetVetted` all pass BEFORE the change.
- [ ] **Step 2: Implement the extraction** in `store.go`:
```go
// gateTestCols is the gate_tests column list, in the order scanGateTest reads.
// Interpolated into SQL as a constant literal only — never caller input.
const gateTestCols = `goal, target, test, kill_rate, survived, discarded, ` +
	`vetted, created_ts, vetted_ts, verdicts, code_path, test_path`

// rowScanner is satisfied by both *sql.Row (QueryRow) and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanGateTest decodes one gate_tests row selected as gateTestCols.
func scanGateTest(sc rowScanner, owner string) (GateTest, error) {
	gt := GateTest{Owner: owner}
	var survived, discarded string
	var createdTS, vettedTS sql.NullTime
	if err := sc.Scan(&gt.Goal, &gt.Target, &gt.Test, &gt.KillRate, &survived, &discarded,
		&gt.Vetted, &createdTS, &vettedTS, &gt.VerdictsJSON, &gt.CodePath, &gt.TestPath); err != nil {
		return GateTest{}, err
	}
	if err := json.Unmarshal([]byte(survived), &gt.Survived); err != nil {
		return GateTest{}, err
	}
	if err := json.Unmarshal([]byte(discarded), &gt.Discarded); err != nil {
		return GateTest{}, err
	}
	gt.CreatedTS = createdTS.Time.UTC()
	gt.VettedTS = vettedTS.Time.UTC()
	return gt, nil
}

// listGateTests is the shared body for ListPending/ListVetted. whereVetted is
// an internal constant literal ("vetted = FALSE"/"vetted = TRUE"), never input.
func (s *Store) listGateTests(op, whereVetted, owner string) ([]GateTest, error) {
	rows, err := s.db.Query(
		`SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND `+whereVetted+` ORDER BY goal, target`,
		owner)
	if err != nil {
		return nil, fmt.Errorf("controlspec: %s: %w", op, err)
	}
	defer rows.Close()
	var out []GateTest
	for rows.Next() {
		gt, err := scanGateTest(rows, owner)
		if err != nil {
			return nil, fmt.Errorf("controlspec: %s: scan: %w", op, err)
		}
		out = append(out, gt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("controlspec: %s: %w", op, err)
	}
	return out, nil
}
```
  Then collapse the three methods (keep their exact signatures + doc comments):
  - `ListPending` body → `return s.listGateTests("list pending", "vetted = FALSE", owner)`
  - `ListVetted` body → `return s.listGateTests("list vetted", "vetted = TRUE", owner)`
  - `GetVetted`: change its SELECT to `SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND goal = ? AND target = ? AND vetted = TRUE`, keep the `QueryRow(...)` → capture the `*sql.Row`, handle `sql.ErrNoRows` → `(GateTest{}, false, nil)` as today, else `gt, err := scanGateTest(row, owner)` (set `gt.Owner` already done inside scan) and return `(gt, true, nil)`. Note: `GetVetted` currently selects `test, kill_rate, ...` WITHOUT `goal, target` (it already knows them from args) — after this change it selects `gateTestCols` (which includes goal/target); scanGateTest overwrites `gt.Goal`/`gt.Target` with the row values (identical to the args). That's fine and consistent.
- [ ] **Step 3: Run the net.** `go test ./internal/controlspec/...` → all still PASS (behavior unchanged). Then full gate. **Commit:** `refactor(controlspec): collapse triplicated gate_tests scan/list into scanGateTest + listGateTests`.

---

## Task 2: controlspec `GetCandidate` + `getControl` uses it [LOW DRY-consistency]

**Files:** Modify `internal/controlspec/store.go`, `internal/brain/controltools.go`; Test `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `func (s *Store) GetCandidate(owner, goal, target string) (GateTest, bool, error)` — the `vetted = FALSE` twin of `GetVetted`, built on Task 1's `scanGateTest`.
- `getControl` (brain) calls it instead of `ListPending`-and-filter.

- [ ] **Step 1: Failing test** — append to `gate_tests_test.go`:
```go
func TestGetCandidate(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = s.SaveCandidate(GateTest{Owner: "o@x", Goal: "g1", Target: "a.go", Test: "T1", CodePath: "a.go", TestPath: "a_test.go", KillRate: 1, CreatedTS: now})
	got, ok, err := s.GetCandidate("o@x", "g1", "a.go")
	if err != nil || !ok || got.Test != "T1" || got.CodePath != "a.go" {
		t.Fatalf("GetCandidate should return the unvetted row: %+v ok=%v err=%v", got, ok, err)
	}
	// vetted rows are NOT candidates
	_, _ = s.Promote("o@x", "g1", "a.go", now)
	if _, ok, _ := s.GetCandidate("o@x", "g1", "a.go"); ok {
		t.Fatal("a promoted (vetted) row must not be returned by GetCandidate")
	}
	// absent → (false, nil)
	if _, ok, _ := s.GetCandidate("o@x", "nope", "x"); ok {
		t.Fatal("absent candidate must be ok=false")
	}
}
```
- [ ] **Step 2: Run, watch fail** (`GetCandidate` undefined).
- [ ] **Step 3: Implement** `GetCandidate` in `store.go` (mirror `GetVetted`, `vetted = FALSE`):
```go
// GetCandidate returns the UNVETTED candidate for (owner, goal, target) —
// the vetted=FALSE twin of GetVetted, for the owner-review surface.
func (s *Store) GetCandidate(owner, goal, target string) (GateTest, bool, error) {
	row := s.db.QueryRow(
		`SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND goal = ? AND target = ? AND vetted = FALSE`,
		owner, goal, target)
	gt, err := scanGateTest(row, owner)
	if err == sql.ErrNoRows {
		return GateTest{}, false, nil
	}
	if err != nil {
		return GateTest{}, false, fmt.Errorf("controlspec: get candidate: %w", err)
	}
	return gt, true, nil
}
```
  Then rewrite `getControl` in `internal/brain/controltools.go`:
```go
func getControl(store *controlspec.Store, owner, goal, target string) (controlspec.GateTest, error) {
	gt, ok, err := store.GetCandidate(owner, goal, target)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	if !ok {
		return controlspec.GateTest{}, fmt.Errorf("controltools: no pending candidate %s@%s", goal, target)
	}
	return gt, nil
}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/... ./internal/brain/ -run 'TestGetCandidate|TestGetControl'` → PASS. Full gate. **Commit:** `refactor(controlspec): GetCandidate twin; getControl targets one row instead of list-and-filter`.

---

## Task 3: controlspec correctness — atomic ImportBundle, verdicts migration, RowsAffected [MED+LOW]

**Files:** Modify `internal/controlspec/bundle.go`, `internal/controlspec/store.go`, `internal/controlspec/gate_tests.go`; Test `internal/controlspec/bundle_test.go`

**Interfaces:**
- `ImportBundle` becomes atomic (all-or-nothing) via a transaction; a shared `goalUpsert(exec, g)` used by both `SaveGoal` and the tx loop. `Promote`/`Reject` wrap `RowsAffected()` errors. `verdicts` gets an `ALTER … IF NOT EXISTS` for migration parity.

- [ ] **Step 1: Failing test** — append to `bundle_test.go` (atomicity happy-path + count; the rollback path is review-verified since inducing a mid-tx failure cleanly is impractical):
```go
func TestImportBundleAtomicCount(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	b := Bundle{Standard: "OWASP ASVS", Version: "4.0.3", Requirements: []Requirement{
		{Ref: "V2.1.1", Intent: "passwords >= 12", Level: "L1", Mode: "executable"},
		{Ref: "V4.1.1", Intent: "deny by default", Level: "L1", Mode: "executable"},
	}}
	n, err := ImportBundle(s, "o@x", b, time.Unix(1_700_000_000, 0).UTC())
	if err != nil || n != 2 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	goals, _ := s.ListGoals("o@x")
	if len(goals) != 2 {
		t.Fatalf("expected 2 goals, got %d", len(goals))
	}
}
```
- [ ] **Step 2: Run, watch pass-or-fail** (this asserts current behavior for the happy path; it should already pass — it's the regression pin for the refactor. Run it, confirm green, THEN refactor and keep it green.)
- [ ] **Step 3: Implement.**
  - `store.go`: extract the SaveGoal SQL into a shared upsert taking an executor, so both `*sql.DB` and `*sql.Tx` satisfy it:
```go
// sqlExec is satisfied by *sql.DB and *sql.Tx.
type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func goalUpsert(x sqlExec, g Goal) error {
	_, err := x.Exec(
		`INSERT OR REPLACE INTO control_goals (owner, id, standard, ref, intent, level, mode, created_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.Owner, g.ID, g.Standard, g.Ref, g.Intent, g.Level, g.Mode, g.CreatedTS)
	return err
}
```
  and `SaveGoal` becomes:
```go
func (s *Store) SaveGoal(g Goal) error {
	if err := goalUpsert(s.db, g); err != nil {
		return fmt.Errorf("controlspec: save goal: %w", err)
	}
	return nil
}
```
  Add the `verdicts` migration to the ALTER slice in `OpenStore` (parity with code_path/test_path):
```go
		`ALTER TABLE gate_tests ADD COLUMN IF NOT EXISTS verdicts VARCHAR DEFAULT ''`,
```
  - `bundle.go` `ImportBundle`: wrap the loop in a transaction (all-or-nothing):
```go
func ImportBundle(s *Store, owner string, b Bundle, now time.Time) (int, error) {
	std := strings.TrimSpace(b.Standard + " " + b.Version)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("controlspec: import bundle: begin: %w", err)
	}
	defer tx.Rollback() // no-op after a successful Commit
	n := 0
	for _, r := range b.Requirements {
		g := Goal{ID: "asvs-" + strings.ToLower(r.Ref), Owner: owner, Standard: std,
			Ref: r.Ref, Intent: r.Intent, Level: r.Level, Mode: r.Mode, CreatedTS: now}
		if err := goalUpsert(tx, g); err != nil {
			return 0, fmt.Errorf("controlspec: import bundle: %w", err) // rollback via defer; 0 written
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("controlspec: import bundle: commit: %w", err)
	}
	return n, nil
}
```
  (`bundle.go` needs no new import; `s.db` is accessed within the package.)
  - `gate_tests.go`: wrap `RowsAffected()` errors in `Promote` and `Reject`:
```go
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("controlspec: promote: rows affected: %w", err)
	}
	return n > 0, nil
```
  (and the analogous change in `Reject` with `"controlspec: reject: rows affected"`).
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → all PASS (incl. the new count test + existing bundle/goal/promote/reject tests). Full gate. **Commit:** `fix(controlspec): atomic ImportBundle, verdicts migration parity, checked RowsAffected`.

---

## Task 4: control tools — clamp n_mutants ceiling + note the trusted Unmarshal [MED+LOW]

**Files:** Modify `internal/brain/controltools.go`; Test `internal/brain/controltools_test.go`

**Interfaces:** `stageControl` clamps `nMutants` to a `[1, maxControlMutants]` range.

- [ ] **Step 1: Failing test** — append to `controltools_test.go` (reuse the `TestStageControl` harness's `stager` stub that captures the built request):
```go
func TestStageControl_ClampsMutantCeiling(t *testing.T) {
	store, _ := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = store.SaveGoal(controlspec.Goal{ID: "g1", Owner: "o@x", Intent: "x", CreatedTS: now})
	var gotReq controlgate.StageRequest
	stage := func(_ context.Context, req controlgate.StageRequest) (controlspec.GateTest, error) {
		gotReq = req
		return controlspec.GateTest{Owner: req.Owner, Goal: req.GoalID, Target: req.Target, CreatedTS: req.Now}, nil
	}
	if _, err := stageControl(context.Background(), store, stage, "o@x", "g1", "a.go", "c", "go", "a.go", "a_test.go", 10000, now); err != nil {
		t.Fatal(err)
	}
	if gotReq.NMutants != maxControlMutants {
		t.Fatalf("n_mutants=10000 must clamp to %d, got %d", maxControlMutants, gotReq.NMutants)
	}
}
```
- [ ] **Step 2: Run, watch fail** (`maxControlMutants` undefined / not clamped).
- [ ] **Step 3: Implement** in `controltools.go`:
```go
// maxControlMutants bounds how many seeded violations stage_control will score.
// Each mutant costs two jail spawns (build + test); a generous adequacy sample
// is well under this. Caps the compute an admin request can trigger.
const maxControlMutants = 20
```
  and in `stageControl` replace the floor-only clamp:
```go
	if nMutants <= 0 {
		nMutants = 5
	}
	if nMutants > maxControlMutants {
		nMutants = maxControlMutants
	}
```
  Also replace the bare swallowed unmarshal with a justifying comment (trusted same-process round-trip — `VerdictsJSON` is the brain's own `json.Marshal` output):
```go
	var verdicts []testgen.Verdict
	if gt.VerdictsJSON != "" {
		// VerdictsJSON is the brain's own json.Marshal output (stage.go) — a
		// trusted same-process round-trip; a decode failure is not reachable
		// from external input, so an empty Triage on the impossible error is safe.
		_ = json.Unmarshal([]byte(gt.VerdictsJSON), &verdicts)
	}
```
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/ -run TestStageControl` → PASS. Full gate. **Commit:** `fix(controltools): clamp stage_control n_mutants ceiling (bounded jail spawns)`.

---

## Task 5: gate — extract FailClosed + defaultGateRecordURL + log the swallowed Save [MED DRY + MED]

**Files:** Create `internal/gate/failclosed.go` (or add to `runner.go`); Modify `internal/gate/runner.go`, `internal/brain/gate.go`, `internal/brain/controlgate.go`. The existing gate + control-runner tests are the net; add one focused FailClosed test.

**Interfaces:**
- Produces: `func FailClosed(ctx context.Context, store *Store, status StatusPoster, repoURL string, repo string, pr PRRef, statusCtx, target, state, msg string, now func() time.Time) error` — records `Run{Passed:false}` (logging a Save error) then posts a non-success status. Both `gate.Runner.fail` and `brain.controlRunner.fail` delegate to it.
- `defaultGateRecordURL(repo, sha string) string` in `internal/brain/gate.go`, used by both `StartGate` and `StartControlGate`.

> The two `fail()` helpers encode the "always record Passed=false, then post non-success" invariant in two packages, byte-identical bar the store field name. Centralizing removes the drift risk on the single most safety-relevant helper. Signature note: pass the primitives (`repo` string, `pr`, `statusCtx`) rather than a `Policy`, so both callers (whose policies differ) can use it.

- [ ] **Step 1: Failing test** — `internal/gate/failclosed_test.go` (or append to a gate test file): with a fake `StatusPoster` + a real temp `Store`, `FailClosed(...,"failure","msg")` records a `Passed=false` run (verify via `GetBySHA`) AND posts state `"failure"` with `msg`. (Model the fake poster on the existing gate runner tests' fakes.)
- [ ] **Step 2: Run, watch fail** (`FailClosed` undefined).
- [ ] **Step 3: Implement.**
  - `internal/gate/failclosed.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"log"
	"time"
)

// FailClosed is the single home of the gate fail-closed exit: record the head
// as Passed=false (so it isn't re-run), then post a non-success commit status.
// Both the merge runner and the control runner delegate here so the safety
// invariant lives in ONE place. A Save error is logged (not swallowed) — a
// dropped dedupe write would otherwise re-run the gate every poll, invisibly.
func FailClosed(ctx context.Context, store *Store, status StatusPoster, repoURL, repo string, pr PRRef, statusCtx, target, state, msg string, now func() time.Time) error {
	if err := store.Save(Run{Repo: repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: false, RanAt: now()}); err != nil {
		log.Printf("gate: fail-closed save dedupe %s@%s: %v", repo, pr.HeadSHA, err)
	}
	return status.SetCommitStatus(ctx, repoURL, pr.HeadSHA, statusCtx, state, target, msg)
}
```
  - `internal/gate/runner.go`: `Runner.fail` becomes a one-line delegation:
```go
func (r *Runner) fail(ctx context.Context, repoURL string, p Policy, pr PRRef, target, state, msg string) error {
	return FailClosed(ctx, r.Store, r.Status, repoURL, p.Repo, pr, p.Context, target, state, msg, r.Now)
}
```
    and log the swallowed SUCCESS-path Save (runner.go:106):
```go
	if err := r.Store.Save(Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: passed, RecordID: recordID, RanAt: r.Now()}); err != nil {
		log.Printf("gate: save dedupe %s@%s: %v", p.Repo, pr.HeadSHA, err)
	}
```
    (add `"log"` import if absent.)
  - `internal/brain/controlgate.go`: `controlRunner.fail` delegates too:
```go
func (r *controlRunner) fail(ctx context.Context, repoURL string, p gate.Policy, pr gate.PRRef, target, state, msg string) error {
	return gate.FailClosed(ctx, r.RunStore, r.Status, repoURL, p.Repo, pr, p.Context, target, state, msg, r.Now)
}
```
    Note `r.Status` is `controlgate.StatusPoster`; `gate.FailClosed` wants `gate.StatusPoster` — the interfaces are structurally identical, but Go needs the concrete type to satisfy the param. `r.Status` holds `opts.Repo` (`*repo.Engine`), which satisfies both. If the compiler rejects the interface-to-interface pass, change `controlRunner.Status`'s field type to `gate.StatusPoster` (it's the same method set; `*repo.Engine` satisfies it) — verify which compiles and use that. Also log the control runner's swallowed success-path Save (controlgate.go:132) the same way.
  - `internal/brain/gate.go`: extract the default RecordURL:
```go
func defaultGateRecordURL(repoName, sha string) string {
	return "/api/gate/run?repo=" + url.QueryEscape(repoName) + "&sha=" + url.QueryEscape(sha)
}
```
    and use it in both `StartGate` (replace the inline closure default) and `StartControlGate` (`internal/brain/controlgate.go`, replace its identical inline closure).
- [ ] **Step 4: Run, watch pass.** `go test ./internal/gate/... ./internal/brain/...` → all PASS (the existing runner + control-runner fail-closed tests are the real net — they must stay green, proving behavior is preserved). Full gate. **Commit:** `refactor(gate): single FailClosed helper + defaultGateRecordURL; log dropped dedupe Save`.

---

## Task 6: control gate — thread the signed RecordID + fix the attempts-map comment [LOW]

**Files:** Modify `internal/controlgate/post.go`, `internal/brain/controlgate.go`; Test `internal/controlgate/post_test.go`

**Interfaces:**
- `PostControlGate` returns the signed `recordID`: `func PostControlGate(...) (int64, error)`. `controlRunner.Run` persists it into the dedupe row instead of `RecordID: 0`.

- [ ] **Step 1: Failing test** — update `post_test.go`: the existing `PostControlGate` tests must now capture two returns; add an assertion that on the success path the returned `recordID` equals what the fake `Certifier` returned (the fake returns e.g. `7`). (The fake cert already returns a fixed id — assert it's propagated.)
- [ ] **Step 2: Run, watch fail** (signature mismatch / recordID not returned).
- [ ] **Step 3: Implement.**
  - `post.go`: capture + return the recordID:
```go
func PostControlGate(ctx context.Context, cert Certifier, poster StatusPoster, req PostRequest, res ControlResult) (int64, error) {
	state, exit := "success", 0
	if !res.Pass {
		state, exit = "failure", 1
	}
	b, _ := json.Marshal(res)
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	recordID, _, err := cert.Certify(ctx, req.RepoURL, req.HeadSHA, "corral/control-gate", exit, digest)
	if err != nil {
		return 0, fmt.Errorf("controlgate: certify verdict (not posting unsigned): %w", err)
	}
	if err := poster.SetCommitStatus(ctx, req.RepoURL, req.HeadSHA, req.Context, state, req.RecordURL(req.HeadSHA), describeResult(res)); err != nil {
		return recordID, err
	}
	return recordID, nil
}
```
  - `internal/brain/controlgate.go` `controlRunner.Run`: capture the recordID and persist it:
```go
	recordID, err := controlgate.PostControlGate(ctx, r.Cert, r.Status, req, res)
	if err != nil {
		// no unsigned green: certify failed, nothing posted (bounded-retry logic unchanged)
		... existing attempts/cap logic ...
	}
	delete(r.attempts, key)
	if err := r.RunStore.Save(gate.Run{Repo: p.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number, Passed: res.Pass, RecordID: recordID, RanAt: r.Now()}); err != nil {
		log.Printf("control-gate: save dedupe %s@%s: %v", p.Repo, pr.HeadSHA, err)
	}
	return nil
```
    (Preserve the exact bounded-retry / attempts logic from Task-5-era code — only the `PostControlGate` call now yields `recordID, err`, and the success Save uses `RecordID: recordID` + logging. If Task 5 already added the success-Save logging here, keep it.)
  - Soften the `attempts`-map comment (controlgate.go ~line 56-60): replace the "still bounded per process" claim, which a force-push (new SHA, old key never revisited) can violate, with an accurate note, e.g. *"in-memory, reset on restart; a force-pushed old SHA's key is not actively evicted, so under sustained signing failure + churn the map can grow — acceptable for a bounded-failure guard, revisit with size-cap/TTL eviction if it ever matters."*
- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlgate/... ./internal/brain/...` → PASS. Full gate + `bash scripts/check-security.sh`. **Commit:** `fix(controlgate): thread signed RecordID into the control dedupe row; correct attempts-map comment`.

---

## Self-Review

- **Findings coverage:** A1+A2 (scan/list DRY) → T1; A2c/C2 (getControl) → T2; A3 (import atomicity) + A5 (verdicts migration) + A4 (RowsAffected) → T3; C1 (n_mutants) + C3 (Unmarshal comment) → T4; B1 (Save log) + B2 (FailClosed) + B3 (RecordURL) → T5; B6 (RecordID) + B5 (attempts comment) → T6. B4 (poller N+1) + the MCP-gating repetition + StartGate/StartControlGate wholesale merge → deliberately NOT touched (correctly-rejected, per the review). ✓
- **Behavior-preserving:** T1/T2/T5 are extractions with the existing suites as the net; T3/T4/T6 add small, tested behavior. Only T6 changes a signature (`PostControlGate`), with its test + sole caller updated. ✓
- **Placeholder scan:** every step carries concrete code; the FailClosed interface-pass caveat (T5) names the exact fallback (type the field as `gate.StatusPoster`). ✓
- **Type consistency:** `scanGateTest(rowScanner)` serves `*sql.Row` + `*sql.Rows`; `goalUpsert(sqlExec)` serves `*sql.DB` + `*sql.Tx`; `gate.FailClosed` takes primitives so both runners (different Policy shapes) call it; `PostControlGate`'s new `(int64, error)` is threaded to its one caller. ✓
- **Fail-closed preserved:** T5 centralizes the invariant without changing it (record Passed=false, post non-success); T6 keeps the no-unsigned-green + bounded-retry logic intact, only adding the recordID + Save logging. ✓
- **Merge-critical:** T5/T6 touch the live gate; the existing runner fail-closed tests are the regression gate and must stay green unchanged. ✓
