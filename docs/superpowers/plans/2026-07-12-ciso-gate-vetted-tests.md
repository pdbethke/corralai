<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Plan: the vetted-test store (the human gate mechanism)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store a candidate CISO test with its adequacy evidence in an **unvetted** state, and give the CISO **Promote** / **Reject** — reusing the memory-vetting cycle verbatim (`shared=false` → `SetShared`; only vetted is authoritative). Only a **vetted** test may gate. This is the human-gate mechanism the whole control loop hinges on.

**Architecture:** Extend `internal/controlspec` (the CISO's owned artifacts already live here — goals; now also their vetted tests). Add a `gate_tests` DuckDB table and `GateTest` type alongside the existing `control_goals`. Owner-scoped, clock-injected, parameterized SQL, `[]string` fields JSON-encoded — exactly like the existing `controlspec` store. A fresh candidate is always unvetted (re-authoring un-vets); `Promote` flips it; `GetVetted` returns only vetted tests (what the gate reads); `ListPending` returns unvetted candidates (what the CISO reviews).

**Tech Stack:** Go 1.26.5; `github.com/marcboeker/go-duckdb/v2` (as the existing controlspec store); `encoding/json`.

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Deterministic**: no `time.Now()` in the store — `CreatedTS`/`VettedTS` and `Promote`'s `now` are caller-stamped (mirror `SaveGoal`).
- **Owner-scoped**: every read/write is keyed by the owning CISO principal; no cross-owner leak.
- **A fresh candidate is ALWAYS unvetted** — `SaveCandidate` forces `vetted=false` (re-authoring a goal's test must be re-approved; mirrors "unvetted until a human promotes").
- **Only vetted gates**: `GetVetted` returns a row ONLY when `vetted=TRUE`. An unvetted candidate is invisible to the gate.
- Mirror the existing `internal/controlspec/store.go` idiom (opaque MotherDuck-ready dsn, `CREATE TABLE IF NOT EXISTS` on open, `INSERT OR REPLACE`, parameterized `?`). Do NOT break the existing `control_goals` store/tests.
- corral metaphor.

## File Structure
- `internal/controlspec/types.go` — add `GateTest`. (modify)
- `internal/controlspec/store.go` — add `gate_tests` table creation in `OpenStore`; add `SaveCandidate`/`GetVetted`/`ListPending`. (modify)
- `internal/controlspec/gate_tests.go` — `Promote`/`Reject`. (new)
- `internal/controlspec/gate_tests_test.go` — tests. (new)

## Interfaces (produced — the gate dimension + CISO approval surface consume these)
```go
type GateTest struct {
    Owner     string    // the CISO principal
    Goal      string    // the controlspec Goal.ID this test verifies
    Target    string    // the target the test binds to, e.g. "owner/repo:path/auth.go"
    Test      string    // the test file content
    KillRate  float64   // mutation adequacy score at authoring time
    Survived  []string  // surviving (uncaught) mutant IDs — the reviewer/CISO triage material
    Discarded []string  // mutant IDs discarded as non-compiling (invalid probes)
    Vetted    bool      // false = unvetted (cannot gate); true = CISO-approved
    CreatedTS time.Time // caller-stamped
    VettedTS  time.Time // when promoted; zero while unvetted
}
func (*Store) SaveCandidate(GateTest) error                              // always stored unvetted
func (*Store) GetVetted(owner, goal, target string) (GateTest, bool, error) // vetted rows only
func (*Store) ListPending(owner string) ([]GateTest, error)             // unvetted candidates awaiting review
func (*Store) Promote(owner, goal, target string, now time.Time) (bool, error) // → vetted; ok=false if no unvetted row
func (*Store) Reject(owner, goal, target string) (bool, error)          // delete; ok=false if no such row
```

---

## Task 1: the gate_tests store — save (unvetted), get-vetted, list-pending

**Files:**
- Modify: `internal/controlspec/types.go`, `internal/controlspec/store.go`
- Test: `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `GateTest`, `SaveCandidate`, `GetVetted`, `ListPending`.
- Consumes: the existing `controlspec.Store` + its DuckDB idiom; `encoding/json` for `Survived`/`Discarded`.

- [ ] **Step 1: Failing test — save a candidate (unvetted), it's not gettable-as-vetted, it IS pending; owner isolation.**
```go
func TestGateTestsSaveGetPending(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "asvs-v2.1.1", Target: "bankz/app:auth.go",
		Test: "package target\n// test", KillRate: 0.83, Survived: []string{"m2"}, Discarded: []string{"m5"}, CreatedTS: now}
	if err := s.SaveCandidate(gt); err != nil { t.Fatal(err) }

	// unvetted → NOT returned by GetVetted
	if _, ok, _ := s.GetVetted("ciso@bankz", "asvs-v2.1.1", "bankz/app:auth.go"); ok {
		t.Fatal("an unvetted candidate must not be gettable as vetted")
	}
	// but IS in the pending list, with fields intact
	pend, err := s.ListPending("ciso@bankz")
	if err != nil { t.Fatal(err) }
	if len(pend) != 1 || pend[0].Goal != "asvs-v2.1.1" || pend[0].KillRate != 0.83 ||
		len(pend[0].Survived) != 1 || pend[0].Survived[0] != "m2" || pend[0].Vetted {
		t.Fatalf("pending wrong: %+v", pend)
	}
	// owner isolation
	if p, _ := s.ListPending("dev@bankz"); len(p) != 0 {
		t.Fatalf("candidate leaked across owners: %+v", p)
	}
}
```

- [ ] **Step 2: Run, watch fail** (`GateTest`/`SaveCandidate` undefined).

- [ ] **Step 3: Implement.** Add `GateTest` to `types.go`. In `store.go`'s `OpenStore`, after the `control_goals` create, add a second `CREATE TABLE IF NOT EXISTS gate_tests`:
```sql
CREATE TABLE IF NOT EXISTS gate_tests (
  owner VARCHAR NOT NULL, goal VARCHAR NOT NULL, target VARCHAR NOT NULL,
  test VARCHAR NOT NULL, kill_rate DOUBLE NOT NULL,
  survived VARCHAR NOT NULL, discarded VARCHAR NOT NULL,
  vetted BOOLEAN NOT NULL, created_ts TIMESTAMP NOT NULL, vetted_ts TIMESTAMP,
  PRIMARY KEY (owner, goal, target)
)
```
`SaveCandidate`: `INSERT OR REPLACE`, **always `vetted=false`, `vetted_ts=NULL`** (JSON-encode `Survived`/`Discarded`; a nil slice encodes as `[]`). `GetVetted`: `SELECT ... WHERE owner=? AND goal=? AND target=? AND vetted=TRUE`, `sql.ErrNoRows → (GateTest{}, false, nil)`, JSON-decode the slices. `ListPending`: `SELECT ... WHERE owner=? AND vetted=FALSE ORDER BY goal, target`. Handle the nullable `vetted_ts` with `sql.NullTime` (zero `time.Time` when NULL). Every value bound via `?`; no `time.Now()`.

- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS (the existing control_goals tests too). **Commit:** `feat(controlspec): gate_tests store — candidate CISO tests (always unvetted)`.

---

## Task 2: Promote / Reject — the human gate transitions

**Files:**
- Create: `internal/controlspec/gate_tests.go`
- Test: `internal/controlspec/gate_tests_test.go`

**Interfaces:**
- Produces: `Promote`, `Reject`.
- Consumes: Task 1's `gate_tests` table + `GateTest`/`GetVetted`/`ListPending`.

- [ ] **Step 1: Failing test — the full human-gate lifecycle.**
```go
func TestGateTestsPromoteReject(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	vetTime := time.Unix(1_700_000_500, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "t1", Test: "x", KillRate: 1, CreatedTS: now}
	_ = s.SaveCandidate(gt)

	// Promote an existing unvetted candidate → ok, then it's vetted + gettable + not pending.
	ok, err := s.Promote("ciso@bankz", "g1", "t1", vetTime)
	if err != nil || !ok { t.Fatalf("promote: ok=%v err=%v", ok, err) }
	got, ok, _ := s.GetVetted("ciso@bankz", "g1", "t1")
	if !ok || !got.Vetted || !got.VettedTS.Equal(vetTime) {
		t.Fatalf("after promote: %+v ok=%v", got, ok)
	}
	if p, _ := s.ListPending("ciso@bankz"); len(p) != 0 {
		t.Fatalf("promoted test still pending: %+v", p)
	}
	// Promote when there is no UNVETTED row → ok=false (already vetted / absent).
	if ok, _ := s.Promote("ciso@bankz", "g1", "t1", vetTime); ok {
		t.Fatal("re-promoting an already-vetted test should report ok=false")
	}
	// Reject removes it entirely.
	if ok, _ := s.Reject("ciso@bankz", "g1", "t1"); !ok {
		t.Fatal("reject of an existing test should report ok=true")
	}
	if _, ok, _ := s.GetVetted("ciso@bankz", "g1", "t1"); ok {
		t.Fatal("rejected test must be gone")
	}
}
```

- [ ] **Step 2: Run, watch fail.** Then implement `gate_tests.go`:
```go
// Promote marks an UNVETTED candidate vetted (the CISO's approval — only a
// vetted test may gate). Returns ok=false when there is no unvetted row to
// promote (already vetted, or absent). now is caller-stamped.
func (s *Store) Promote(owner, goal, target string, now time.Time) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE gate_tests SET vetted = TRUE, vetted_ts = ? WHERE owner = ? AND goal = ? AND target = ? AND vetted = FALSE`,
		now, owner, goal, target)
	if err != nil {
		return false, fmt.Errorf("controlspec: promote: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Reject deletes a candidate (vetted or not). Returns ok=false when no such row.
func (s *Store) Reject(owner, goal, target string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM gate_tests WHERE owner = ? AND goal = ? AND target = ?`,
		owner, goal, target)
	if err != nil {
		return false, fmt.Errorf("controlspec: reject: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
```

- [ ] **Step 3: Run, watch pass.** `go test ./internal/controlspec/...` → PASS. Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(controlspec): CISO Promote/Reject — the human vetting gate for gate tests`.

---

## Self-Review
- **Spec coverage (§4f human gate):** candidate stored unvetted ✓; Promote/Reject as the human transition (reuse the memory shared→SetShared cycle) ✓; only vetted gates (`GetVetted` filters `vetted=TRUE`) ✓; adequacy evidence (kill rate + survived/discarded) carried for the CISO's review ✓; ListPending is the CISO's review queue ✓.
- **No placeholders:** complete SQL + Go for both tasks.
- **Type consistency:** `GateTest` stable; `SaveCandidate`/`GetVetted`/`ListPending`/`Promote`/`Reject` signatures fixed; mirrors `SaveGoal`/`GetGoal`.
- **Determinism / owner-scope:** no `time.Now()` (caller-stamps `CreatedTS`/`VettedTS`/`now`); every query owner-filtered; `ListPending` `ORDER BY` for stable order.
- **The invariants under test:** a fresh candidate is unvetted and invisible to the gate; re-promoting an already-vetted test reports `ok=false` (the `vetted=FALSE` guard in the UPDATE); reject removes it.
- **Out of scope (later plans):** the reviewer triage LLM agent (classify survived mutants: real gap vs equivalent); the CISO approval SURFACE (CLI/API/UI exposing ListPending + Promote/Reject with the adequacy report); the gate dimension (4g) reading `GetVetted`; the authoring→SaveCandidate wiring.
