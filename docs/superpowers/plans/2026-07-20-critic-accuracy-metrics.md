# Critic-Accuracy Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Score the test-critic's findings by execution — persist each per `(model, test-critic)` with a real-vs-hallucination adjudication and surface a critic **precision** number in the bug-catching scorecard.

**Architecture:** The critic self-classifies each finding as `whole-test` or `dead-check` (a new structured field). During the run, while the jail and mutants are live, a **conservative auto-refute** runs each `whole-test`-flagged test *alone* against the run's mutants: if it kills ≥1 mutant, "it can never fail" is disproven → auto-refute. `dead-check` findings are never auto-scored. A new mutable DuckDB store (`internal/criticscore`) holds each finding with an adjudication a superuser can override. The scorecard reads a per-model precision aggregate from it.

**Tech Stack:** Go 1.26.x, DuckDB (via the existing `database/sql` driver), the `internal/lang` plugin seam, `internal/advpool` driver, `internal/agentworker` tool-calling, MCP tools + `corral` CLI.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-07-20-critic-accuracy-metrics-design.md` — every task implicitly includes it.
- **Deploy gate (non-negotiable, all Go tasks):** `gofmt -l .` prints nothing; `bash scripts/check-security.sh` exits 0 (gosec MEDIUM+ — e.g. `os.WriteFile` uses `0o600` not `0o644`, or a `#nosec Gxxx: <reason>`); `go test ./... -race` passes. `go build`+`go test` passing is NOT sufficient to deploy.
- **Scope enum values (verbatim):** `whole-test`, `dead-check`. **Adjudication values (verbatim):** `unadjudicated`, `confirmed`, `refuted`. **Source values (verbatim):** `auto`, `human`.
- **Auto only ever *refutes*, never *confirms*.** 0 kills is inconclusive. Only a human sets `confirmed`.
- **`dead-check` findings are never auto-touched** — the mis-scoring guardrail. A missing/unparseable scope defaults to `dead-check` (fail-safe).
- **Never gate on critic findings.** They stay advisory; this only measures the critic.
- **DuckDB is single-process:** the CLI reads/writes over the brain HTTP API, never the daemon's file directly (mirror `cmd/corral/scorecard.go:37-80`).
- **Metric persistence + human gate are brain-path only** (like all bugcatch metrics). `--local` computes the auto verdict inline and shows it on the verdict/tape but persists nothing.
- **DRY / additive migrations:** new DuckDB columns go through the additive-migration ledger pattern (mirror `internal/bugcatch/store.go:24-33,117`). New `queue.Finding` columns are additive on the SQLite `findings` table.

---

### Task 1: Structured scope on critic findings

Give a finding three new fields the critic fills, so downstream code knows *what* is being claimed. Additive and non-breaking: other roles simply leave them empty.

**Files:**
- Modify: `internal/queue/findings.go` (Finding struct ~35-51; `AddFinding` INSERT ~66-83; `findingsSelect`/`queryFindings` scan)
- Modify: `internal/queue/store.go` (the `findings` table DDL + additive `ALTER TABLE` migration — find the `CREATE TABLE ... findings` and the migration list)
- Modify: `internal/agentworker/agentworker.go` (the `report_finding` tool schema ~141-148; finding assembly ~215-223)
- Modify: `internal/advpool/roles.go` (`renderTestCritic` ~104-113 — instruct the critic to set scope + selector)
- Test: `internal/queue/findings_test.go`, `internal/agentworker/agentworker_test.go`

**Interfaces:**
- Produces: `queue.Finding` gains `Scope string` (json `scope`), `TestFile string` (json `test_file`), `TestSelector string` (json `test_selector`). Valid scopes: `"whole-test"`, `"dead-check"`. Empty is allowed at the queue layer (defaulted to `dead-check` in Task 3, not here).

- [ ] **Step 1: Write the failing test (queue round-trip)**

In `internal/queue/findings_test.go`:

```go
func TestFindingScopeRoundTrip(t *testing.T) {
	s := newTestStore(t) // use whatever the package's existing test-store helper is
	mid, err := s.CreateMission("m")
	if err != nil { t.Fatal(err) }
	id, err := s.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "test-critic", Type: "note", Severity: "low",
		Target: "tests/test_x.py::T::test_a", Evidence: "always passes",
		Scope: "whole-test", TestFile: "tests/test_x.py",
		TestSelector: "tests/test_x.py::T::test_a",
	})
	if err != nil { t.Fatalf("AddFinding: %v", err) }
	got, err := s.Findings(mid, "")
	if err != nil { t.Fatal(err) }
	var f *queue.Finding
	for i := range got { if got[i].ID == id { f = &got[i] } }
	if f == nil { t.Fatal("finding not found") }
	if f.Scope != "whole-test" || f.TestFile != "tests/test_x.py" || f.TestSelector != "tests/test_x.py::T::test_a" {
		t.Fatalf("scope fields not round-tripped: %+v", f)
	}
}
```

(If the package has no `newTestStore`/`CreateMission` helper by these names, use the exact helpers the existing `findings_test.go` uses — read the file first.)

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/queue/ -run TestFindingScopeRoundTrip -v`
Expected: FAIL (unknown field `Scope`).

- [ ] **Step 3: Add the fields + persistence**

In `findings.go`, add to the `Finding` struct (after `Target`):
```go
	Scope        string `json:"scope,omitempty"`         // whole-test|dead-check (critic findings)
	TestFile     string `json:"test_file,omitempty"`     // repo-relative file holding the flagged test
	TestSelector string `json:"test_selector,omitempty"` // language-runnable single-test selector
```
Extend the `INSERT INTO findings (...)` column list + `VALUES` + args to include `scope,test_file,test_selector` (add `f.Scope, f.TestFile, f.TestSelector`). Extend `findingsSelect` and the `queryFindings` row `Scan(...)` to read the three columns (append at the end, matching column order). In `internal/queue/store.go`, add the three columns to the `findings` CREATE TABLE and an additive `ALTER TABLE findings ADD COLUMN ...` migration entry (mirror how the existing `reporter_model`/`reporter_backend` columns were added — grep for `reporter_backend` to find both the DDL and the migration).

- [ ] **Step 4: Run the queue test, verify pass**

Run: `go test ./internal/queue/ -run TestFindingScopeRoundTrip -v` → PASS.

- [ ] **Step 5: Write the failing test (agentworker plumbs scope)**

In `internal/agentworker/agentworker_test.go`, mirror the existing `report_finding` test (~109). Have the fake model call `report_finding` with arguments including `"scope":"whole-test","test_file":"t.py","test_selector":"t.py::test_a"`; assert the returned `findings[0].Scope=="whole-test"`, `.TestFile=="t.py"`, `.TestSelector=="t.py::test_a"`.

- [ ] **Step 6: Run it, verify it fails**

Run: `go test ./internal/agentworker/ -run TestReport -v` (match the actual test name) → FAIL (fields empty).

- [ ] **Step 7: Add the tool props + assembly**

In `agentworker.go`, in the `report_finding` schema `properties` (~143-147) add three OPTIONAL props (do NOT add to the required list):
```go
				"scope":         map[string]any{"type": "string", "description": "for a flagged test: whole-test (the ENTIRE test can never fail) or dead-check (a specific check is dead but the test still asserts something real)"},
				"test_file":     map[string]any{"type": "string", "description": "repo-relative path of the file holding the flagged test"},
				"test_selector": map[string]any{"type": "string", "description": "a runnable selector for the single flagged test (e.g. path::Class::test_name for pytest, TestName for go)"},
```
In the finding assembly (~218-222) add:
```go
		Scope:        str("scope"),
		TestFile:     str("test_file"),
		TestSelector: str("test_selector"),
```

- [ ] **Step 8: Update the critic prompt**

In `roles.go` `renderTestCritic`, add a line instructing: when flagging a test, set `scope` to `whole-test` or `dead-check`, and set `test_file` + `test_selector` to the exact runnable selector for that one test. Keep the existing "flag ONLY a demonstrably vacuous test" guidance.

- [ ] **Step 9: Run tests + gate, commit**

Run: `go test ./internal/queue/ ./internal/agentworker/ ./internal/advpool/ -race` → PASS; `gofmt -l internal/queue internal/agentworker internal/advpool` → empty.
```bash
git add internal/queue internal/agentworker internal/advpool
git commit -m "feat(findings): structured scope/test_file/test_selector on critic findings"
```

---

### Task 2: `lang.Plugin.SingleTestCmd` — run one test

Add the capability to produce a command that runs exactly one test, per language. This is the narrow slice of per-test attribution the auto-refute needs.

**Files:**
- Modify: `internal/lang/lang.go` (Plugin interface ~13-24)
- Modify: `internal/lang/python.go`, `internal/lang/go.go`, `internal/lang/ruby.go`, `internal/lang/javascript.go`, `internal/lang/typescript.go`
- Test: `internal/lang/lang_test.go` (or the per-plugin test files if that's the convention)

**Interfaces:**
- Produces: `SingleTestCmd(testPath, selector string) (cmd []string, ok bool)` on `lang.Plugin`. `ok=false` ⇒ language can't run a single test yet (auto-refute skipped, sound). `testPath` is repo-relative; `selector` is the critic's `test_selector`.

- [ ] **Step 1: Write the failing test**

In `internal/lang/lang_test.go`:
```go
func TestSingleTestCmd(t *testing.T) {
	py, _ := lang.ByName("python")
	cmd, ok := py.SingleTestCmd("tests/test_recipes.py", "tests/test_recipes.py::RandomPermutationTests::test_full_permutation")
	if !ok || len(cmd) == 0 { t.Fatalf("python: want ok cmd, got ok=%v cmd=%v", ok, cmd) }
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "test_full_permutation") { t.Fatalf("python cmd missing selector: %v", cmd) }

	g, _ := lang.ByName("go")
	gcmd, ok := g.SingleTestCmd("foo_test.go", "TestNegativeTake")
	if !ok || !strings.Contains(strings.Join(gcmd, " "), "TestNegativeTake") { t.Fatalf("go: %v ok=%v", gcmd, ok) }

	rb, _ := lang.ByName("ruby")
	if _, ok := rb.SingleTestCmd("x", "y"); ok { t.Fatal("ruby should report ok=false (unimplemented)") }
}
```

- [ ] **Step 2: Run it, verify it fails (compile error — method missing)**

Run: `go test ./internal/lang/ -run TestSingleTestCmd` → FAIL (undefined method).

- [ ] **Step 3: Add the interface method + implementations**

In `lang.go` Plugin interface add:
```go
	// SingleTestCmd yields a command that runs exactly the one test named by
	// selector in testPath. ok=false when the language can't yet target a
	// single test — callers must treat that as "no auto-signal", never a pass.
	SingleTestCmd(testPath, selector string) (cmd []string, ok bool)
```
`python.go`:
```go
func (pythonPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" { return nil, false }
	return []string{"python3", "-m", "pytest", "-q", selector}, true
}
```
(pytest selectors are already `path::Class::test`; `testPath` is unused for python — keep the param for the uniform signature.)
`go.go`:
```go
func (goPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" { return nil, false }
	return []string{"go", "test", "-run", "^" + selector + "$", "./..."}, true
}
```
`ruby.go`, `javascript.go`, `typescript.go`:
```go
func (rubyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }
```
(use the correct receiver type per file; grep each file for its existing receiver, e.g. `func (rubyPlugin)`.)

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/lang/ -run TestSingleTestCmd -v` → PASS. Then `go build ./...` (confirms every plugin satisfies the extended interface).

- [ ] **Step 5: Commit**
```bash
git add internal/lang
git commit -m "feat(lang): SingleTestCmd — run exactly one test (python+go; others fail-closed)"
```

---

### Task 3: Adjudication decision (pure logic + the guardrail)

The heart of soundness, as a pure function with no I/O.

**Files:**
- Create: `internal/advpool/critic_adjudicate.go`
- Test: `internal/advpool/critic_adjudicate_test.go`

**Interfaces:**
- Produces:
  ```go
  const (
      AdjUnadjudicated = "unadjudicated"
      AdjConfirmed     = "confirmed"
      AdjRefuted       = "refuted"
      ScopeWholeTest   = "whole-test"
      ScopeDeadCheck   = "dead-check"
  )
  // NormalizeScope maps "" or any unknown value to dead-check (fail-safe).
  func NormalizeScope(s string) string
  // AutoAdjudication decides the AUTO verdict for one finding given how many
  // mutants the flagged test killed when run ALONE, and whether the run could
  // run it at all (ran). Only whole-test + ran + kills>=1 refutes; everything
  // else is unadjudicated. NEVER returns confirmed.
  func AutoAdjudication(scope string, ran bool, kills int) string
  ```

- [ ] **Step 1: Write the failing tests (the guardrail is the crux)**

`internal/advpool/critic_adjudicate_test.go`:
```go
func TestNormalizeScope(t *testing.T) {
	for in, want := range map[string]string{
		"whole-test": "whole-test", "dead-check": "dead-check",
		"": "dead-check", "garbage": "dead-check",
	} {
		if got := advpool.NormalizeScope(in); got != want {
			t.Errorf("NormalizeScope(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAutoAdjudication(t *testing.T) {
	cases := []struct{ scope string; ran bool; kills int; want string }{
		{"whole-test", true, 1, advpool.AdjRefuted},        // proven can-fail => hallucination refuted
		{"whole-test", true, 3, advpool.AdjRefuted},
		{"whole-test", true, 0, advpool.AdjUnadjudicated},  // 0 kills is inconclusive, NEVER auto-confirm
		{"whole-test", false, 0, advpool.AdjUnadjudicated}, // couldn't run => no signal
		{"dead-check", true, 5, advpool.AdjUnadjudicated},  // THE GUARDRAIL: live test kills, but the claim was about a check
		{"dead-check", true, 0, advpool.AdjUnadjudicated},
		{"", true, 9, advpool.AdjUnadjudicated},            // empty scope => dead-check => never auto
	}
	for _, c := range cases {
		if got := advpool.AutoAdjudication(c.scope, c.ran, c.kills); got != c.want {
			t.Errorf("AutoAdjudication(%q,%v,%d)=%q want %q", c.scope, c.ran, c.kills, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/advpool/ -run 'TestNormalizeScope|TestAutoAdjudication'` → FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/advpool/critic_adjudicate.go`:
```go
// SPDX-License-Identifier: Elastic-2.0
package advpool

const (
	AdjUnadjudicated = "unadjudicated"
	AdjConfirmed     = "confirmed"
	AdjRefuted       = "refuted"
	ScopeWholeTest   = "whole-test"
	ScopeDeadCheck   = "dead-check"
)

func NormalizeScope(s string) string {
	if s == ScopeWholeTest { return ScopeWholeTest }
	return ScopeDeadCheck // "", unknown, or dead-check all collapse here (fail-safe)
}

// AutoAdjudication is sound by construction: it only ever downgrades a
// whole-test "can never fail" claim to refuted when execution PROVED the test
// can fail (kills>=1). dead-check claims are never auto-touched (a live test
// killing mutants says nothing about whether one internal check is dead), and
// no path ever auto-confirms (0 kills is inconclusive, not proof of vacuity).
func AutoAdjudication(scope string, ran bool, kills int) string {
	if NormalizeScope(scope) == ScopeWholeTest && ran && kills >= 1 {
		return AdjRefuted
	}
	return AdjUnadjudicated
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/advpool/ -run 'TestNormalizeScope|TestAutoAdjudication' -v` → PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/advpool/critic_adjudicate.go internal/advpool/critic_adjudicate_test.go
git commit -m "feat(advpool): sound critic auto-adjudication decision + scope guardrail"
```

---

### Task 4: `internal/criticscore` — the per-finding store

A new mutable DuckDB store. Mirror `internal/controlspec/store.go` (mutable, promote/reject) for structure and `internal/bugcatch/store.go` for the additive-migration ledger + `Open(dsn)` + aggregate query conventions. Read both before writing.

**Files:**
- Create: `internal/criticscore/store.go`, `internal/criticscore/types.go`
- Test: `internal/criticscore/store_test.go`

**Interfaces:**
- Produces:
  ```go
  type Finding struct {
      ID                     string  // stable id: fmt.Sprintf("%d:%d", RecordID, QueueFindingID)
      TS                     float64
      RecordID               int64
      RecordHead             string
      Repo, Commit           string
      MissionID              int64
      Model                  string  // critic model
      TargetTest             string
      TestFile, TestSelector string
      Scope                  string  // whole-test|dead-check (already normalized)
      Evidence, Severity     string
      Adjudication           string  // unadjudicated|confirmed|refuted
      Source                 string  // auto|human
      AdjudicatedBy          string
      AdjudicatedTS          float64
  }
  type CriticCell struct {
      Model                                     string
      Confirmed, Refuted, Unadjudicated         int
      Precision                                 *float64 // confirmed/(confirmed+refuted); nil if denom 0
  }
  func Open(dsn string) (*Store, error)
  func (s *Store) Close() error
  func (s *Store) Record(ctx context.Context, fs []Finding) error       // upsert on ID; NEVER downgrades a human adjudication to auto
  func (s *Store) Adjudicate(ctx context.Context, id, verdict, by string) (bool, error) // human; sets source=human; wins over auto
  func (s *Store) ListPending(ctx context.Context) ([]Finding, error)   // adjudication='unadjudicated'
  func (s *Store) Get(ctx context.Context, id string) (Finding, bool, error)
  func (s *Store) List(ctx context.Context) ([]Finding, error)
  func (s *Store) Precision(ctx context.Context) ([]CriticCell, error)  // GROUP BY model
  ```
- Table `critic_findings(id PRIMARY KEY, ts, record_id, record_head, repo, commit, mission_id, model, target_test, test_file, test_selector, scope, evidence, severity, adjudication, source, adjudicated_by, adjudicated_ts)`.

- [ ] **Step 1: Write the failing tests**

`internal/criticscore/store_test.go`:
```go
func openTmp(t *testing.T) *criticscore.Store {
	t.Helper()
	s, err := criticscore.Open(filepath.Join(t.TempDir(), "cs.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndPrecision(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	must := func(err error){ if err != nil { t.Fatal(err) } }
	must(s.Record(ctx, []criticscore.Finding{
		{ID: "1:10", RecordID: 1, Model: "haiku", Scope: "whole-test", Adjudication: "refuted", Source: "auto"},
		{ID: "1:11", RecordID: 1, Model: "haiku", Scope: "dead-check", Adjudication: "unadjudicated", Source: "auto"},
		{ID: "2:12", RecordID: 2, Model: "gemini-pro", Scope: "dead-check", Adjudication: "confirmed", Source: "human"},
	}))
	// human-confirm the pending haiku one
	ok, err := s.Adjudicate(ctx, "1:11", "confirmed", "alice"); must(err)
	if !ok { t.Fatal("adjudicate should report changed") }
	cells, err := s.Precision(ctx); must(err)
	byModel := map[string]criticscore.CriticCell{}
	for _, c := range cells { byModel[c.Model] = c }
	h := byModel["haiku"]
	if h.Confirmed != 1 || h.Refuted != 1 || h.Precision == nil || *h.Precision != 0.5 {
		t.Fatalf("haiku precision wrong: %+v", h)
	}
	// Record must NOT downgrade the human 'confirmed' back to auto/unadjudicated
	must(s.Record(ctx, []criticscore.Finding{{ID: "1:11", RecordID: 1, Model: "haiku", Scope: "dead-check", Adjudication: "unadjudicated", Source: "auto"}}))
	f, ok, err := s.Get(ctx, "1:11"); must(err)
	if !ok || f.Adjudication != "confirmed" || f.Source != "human" {
		t.Fatalf("human adjudication was clobbered by Record: %+v", f)
	}
	pend, err := s.ListPending(ctx); must(err)
	if len(pend) != 0 { t.Fatalf("no pending expected, got %d", len(pend)) }
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/criticscore/` → FAIL (package missing).

- [ ] **Step 3: Implement the store**

Write `types.go` (the structs above) and `store.go`. Mirror `internal/controlspec/store.go` for: `Open` (sql.Open("duckdb", dsn), `PRAGMA`, `CREATE TABLE IF NOT EXISTS`), the additive-migration column ledger (mirror `bugcatch/store.go:24-33,117`), and `Close`. Key query semantics:
- `Record`: `INSERT ... ON CONFLICT (id) DO UPDATE SET ...` but **guard the human**: `... DO UPDATE SET adjudication=excluded.adjudication, source=excluded.source, ... WHERE critic_findings.source <> 'human'`. (DuckDB supports `ON CONFLICT ... DO UPDATE ... WHERE`; if the installed DuckDB build rejects the conflict-target `WHERE`, fall back to: SELECT existing source; skip the update when it is `human`.) Always set `ts` on first insert only.
- `Adjudicate`: `UPDATE critic_findings SET adjudication=?, source='human', adjudicated_by=?, adjudicated_ts=? WHERE id=? AND adjudication IN ('unadjudicated','confirmed','refuted')`; validate `verdict ∈ {confirmed,refuted}` (reject others with an error); return `rowsAffected>0`.
- `Precision`: `SELECT model, SUM(adjudication='confirmed'), SUM(adjudication='refuted'), SUM(adjudication='unadjudicated') FROM critic_findings GROUP BY model`; compute `Precision` in Go (nil when confirmed+refuted==0).
- File perms / `#nosec`: match the existing stores (no `0o644` writes).

- [ ] **Step 4: Run, verify pass** — `go test ./internal/criticscore/ -race -v` → PASS.

- [ ] **Step 5: Gate + commit**

`gofmt -l internal/criticscore` empty; `bash scripts/check-security.sh` OK.
```bash
git add internal/criticscore
git commit -m "feat(criticscore): mutable per-finding store with human-wins adjudication + precision"
```

---

### Task 5: Inline auto-refute in the driver + `CriticFindingSink`

Wire the auto-adjudication into `tickAggregate` (jail + mutants live) and emit findings to a sink, mirroring `BugCatchSink`.

**Files:**
- Modify: `internal/advpool/driver.go` (add `CriticFindingObservation` + `CriticFindingSink` near `BugCatchSink` ~88-94; add `CriticFindings CriticFindingSink` + `Lang` plugin access to the Driver; the auto-refute step in `tickAggregate` ~1109-1165; put results on `Verdict`)
- Modify: `internal/advpool/aggregate.go` if the adjudication needs to ride on `VacuousFindings` (it can stay on the `queue.Finding.Scope` + a parallel `[]CriticFindingObservation`)
- Test: `internal/advpool/driver_test.go` (use the existing `fakeScorer` ~66 to control kills)

**Interfaces:**
- Consumes: `advpool.Scorer.Score(ctx, codePath, code, test, mutants, testCmd)` (existing, gate.go:110) — call it with a **single-test** testCmd built from `lang.SingleTestCmd` joined by spaces; `kills = len(mutants) - len(survivors)`. `AutoAdjudication` (Task 3). `lang.Plugin.SingleTestCmd` (Task 2).
- Produces:
  ```go
  type CriticFindingObservation struct {
      QueueFindingID int64
      Model          string
      TargetTest, TestFile, TestSelector string
      Scope          string // normalized
      Evidence, Severity string
      Adjudication   string // auto verdict: refuted|unadjudicated
      Source         string // "auto"
  }
  type CriticFindingSink interface {
      Record(recordID int64, recordHead string, obs []CriticFindingObservation)
  }
  ```

- [ ] **Step 1: Write the failing test**

In `driver_test.go`, add a test that drives a run whose `fakeScorer` returns 0 survivors (so a single flagged test "kills" all mutants) for a `whole-test` critic finding, and asserts the emitted `CriticFindingObservation.Adjudication == "refuted"`. Add a second finding scoped `dead-check` on a killing test and assert it stays `"unadjudicated"`. Capture observations via a fake sink:
```go
type fakeCriticSink struct{ obs []advpool.CriticFindingObservation }
func (f *fakeCriticSink) Record(_ int64, _ string, o []advpool.CriticFindingObservation) { f.obs = append(f.obs, o...) }
```
Model the fakeScorer so a single-test testCmd yields kills≥1 (e.g. return `nil` survivors). Assert per-scope outcomes. (Read the existing driver_test harness ~300-460 to reuse its run-to-convergence helper and the `AddFinding` pattern at ~377.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/advpool/ -run TestCriticAutoRefute` → FAIL.

- [ ] **Step 3: Implement**

In `driver.go`: add the `CriticFindingObservation` struct + `CriticFindingSink` interface; add `CriticFindings CriticFindingSink` to the Driver's deps and a `Lang lang.Plugin` reference (or reuse whatever the driver already holds for the run's language — grep the Driver struct for an existing lang/plugin field before adding one). In `tickAggregate`, after `criticFindings := filterCriticFindings(...)` and after the verdict is signed (so `v.RecordID` is set, mirroring the `BugCatch.Record` guard at ~1162):
```go
if d.CriticFindings != nil && v.RecordID != 0 {
	var obs []CriticFindingObservation
	for _, f := range criticFindings {
		scope := NormalizeScope(f.Scope)
		ran, kills := false, 0
		if scope == ScopeWholeTest && f.TestSelector != "" {
			if cmd, ok := d.Lang.SingleTestCmd(f.TestFile, f.TestSelector); ok {
				// Reuse the gate Scorer against the run's mutants; a single-test
				// testCmd means kills = total - survivors. Best-effort: a jail
				// error must NEVER fail the audit — leave it unadjudicated.
				if _, survivors, err := d.Scorer.Score(ctx, run.codePath, run.compliantCode, run.devTest, run.mutants, strings.Join(cmd, " ")); err == nil {
					ran, kills = true, len(run.mutants)-len(survivors)
				} else {
					d.logf("advpool: critic auto-refute score failed for %q: %v", f.TestSelector, err) // use the driver's existing log helper
				}
			}
		}
		adj := AutoAdjudication(scope, ran, kills)
		obs = append(obs, CriticFindingObservation{
			QueueFindingID: f.ID, Model: v.ModelsByRole[RoleTestCritic],
			TargetTest: f.Target, TestFile: f.TestFile, TestSelector: f.TestSelector,
			Scope: scope, Evidence: f.Evidence, Severity: f.Severity,
			Adjudication: adj, Source: "auto",
		})
	}
	if len(obs) > 0 { d.CriticFindings.Record(v.RecordID, v.RecordHead, obs) }
}
```
Use the driver's actual field names for `run.codePath/compliantCode/devTest/mutants` — grep `runState` (driver.go ~280-291 and tickDevAdequacy ~751) for the exact accessors. If the run's mutants aren't retained on `runState`, retain them where `tickDevAdequacy` scored them (add a `mutants []adequacy.Mutant` field to `runState`, populated there) — note this in the commit.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/advpool/ -run TestCriticAutoRefute -race -v` → PASS; full `go test ./internal/advpool/ -race` still green.

- [ ] **Step 5: Commit**
```bash
git add internal/advpool
git commit -m "feat(advpool): inline conservative auto-refute of whole-test critic findings"
```

---

### Task 6: Brain wiring — store injection, sink adapter, MCP adjudication tools

**Files:**
- Modify: `internal/brain/advpool.go` (add a `CriticScore *criticscore.Store` alongside `bugCatch`; a sink adapter mirroring `advpoolBugCatchSink` ~94-114; wire it in `StartRun` ~666-671; inject via `StartAdversarialPool`/opts ~831)
- Modify: `internal/brain/brain.go` (`Options` — add `CriticScore` next to `BuildStore`/`BugCatch`; thread to the advpool runtime; register the MCP tools)
- Modify: the MCP tool registration site (grep for where `promote_control`/admin-gated tools register) — add `list_pending_critic_findings`, `get_critic_finding`, `adjudicate_critic_finding`, admin-gated + audited exactly like the control verbs
- Test: `internal/brain/advpool_test.go` (sink adapter maps observation→store row) + a tool test mirroring the control-tool tests

**Interfaces:**
- Consumes: `criticscore.Store` (Task 4), `advpool.CriticFindingObservation`/`CriticFindingSink` (Task 5).
- Produces: MCP tool `adjudicate_critic_finding{id string, verdict string}` (verdict ∈ confirmed|refuted) → `criticscore.Adjudicate(ctx, id, verdict, principal)`; `list_pending_critic_findings{}` → `ListPending`; `get_critic_finding{id}` → `Get`. All require admin principal (reuse the guard the control verbs use).

- [ ] **Step 1: Write the failing test (sink adapter)**

In `internal/brain/advpool_test.go`, mirror the bugcatch sink test: build an `advpoolCriticSink` with a temp `criticscore.Store`, call `Record(recordID, head, []advpool.CriticFindingObservation{...})`, then assert `store.Get` / `store.Precision` reflect it (correct model, adjudication, `id = "<recordID>:<queueFindingID>"`).

- [ ] **Step 2: Run, verify fail** — FAIL (adapter missing).

- [ ] **Step 3: Implement the adapter + wiring**

Add `advpoolCriticSink` (mirror `advpoolBugCatchSink` ~94-114): map each `CriticFindingObservation` → `criticscore.Finding` (stamp `ts`, `repo`, `commit`, `mission_id` from the run context the bugcatch sink already captures; `ID = fmt.Sprintf("%d:%d", recordID, obs.QueueFindingID)`), call `store.Record`. Wire `CriticScore` into `Options`, into the advpool runtime, and set the sink in `StartRun` when `rt.criticScore != nil`. Open the store in the brain's boot path next to where `BugCatch`/`BuildStore` open (grep the brain `main`/`New` for `bugcatch.Open`).

- [ ] **Step 4: Implement the MCP tools + test**

Register the three tools admin-gated (mirror `promote_control`/`reject_control`). Add a tool test mirroring the control-tool test: unauth principal → refused; admin → `adjudicate_critic_finding` flips the row and a subsequent `list_pending_critic_findings` no longer returns it.

- [ ] **Step 5: Run, verify pass + gate** — `go test ./internal/brain/ -race` PASS; gofmt + check-security OK.

- [ ] **Step 6: Commit**
```bash
git add internal/brain
git commit -m "feat(brain): criticscore store + sink + admin-gated adjudication MCP tools"
```

---

### Task 7: Surface it — `corral scorecard` critic precision + `/api/bugcatch` + `corral criticscore` CLI + docs

**Files:**
- Modify: `internal/brain/bugcatch_view.go` (augment the test-critic cell with critic precision from `criticscore.Precision`)
- Modify: `internal/ui/ui.go` (`/api/bugcatch` handler ~451 — include the new fields; optionally a `/api/criticscore` for the pending list the CLI reads)
- Modify: `cmd/corral/scorecard.go` (print a `C-PREC` column for the critic role; `--json` carries counts)
- Create: `cmd/corral/criticscore.go` (`corral criticscore list|show <id>|confirm <id>|refute <id>` over the brain API); register in `cmd/corral/main.go` dispatch (mirror `scorecard`/`control` registration)
- Modify: `README.md`, `ROADMAP.md` (move under Shipped), `site/src/content/docs/docs/*` (document the critic-precision column + the adjudication verbs + the brain-path caveat)
- Test: `internal/brain/bugcatch_view_test.go`, `cmd/corral/scorecard_test.go`

**Interfaces:**
- Consumes: `criticscore.Precision` / `ListPending` / `Adjudicate` (Tasks 4/6), the `/api/bugcatch` shape (existing).
- Produces: `brain.ScorecardCell` gains `CriticConfirmed, CriticRefuted, CriticUnadjudicated int` + `CriticPrecision *float64` (populated only for the `test-critic` role); `provisional` when `confirmed+refuted < provisionalBelow` (3).

- [ ] **Step 1: Write the failing test**

In `bugcatch_view_test.go`: seed a `criticscore.Store` with 2 confirmed + 1 refuted for model `haiku`; build the scorecard; assert the `haiku`/`test-critic` cell has `CriticPrecision != nil && *==2.0/3.0`, `CriticConfirmed==2`, `provisional==true` (denom 3 is NOT below 3 → actually `provisional=false`; pick seed counts so the assertion matches the real `provisionalBelow` — verify the constant is 3 and seed 2+1=3 → not provisional; add a second model with 1+0 → provisional true). Assert exact numbers.

- [ ] **Step 2: Run, verify fail** — FAIL (fields missing).

- [ ] **Step 3: Implement the view + API + CLI**

Augment `BuildBugCatchScorecard` to join `criticscore.Precision` onto the `test-critic` cells. Add the fields to the API JSON. Add the `C-PREC` column to `runScorecard` (show `—` for non-critic roles / nil precision, and a `~` provisional tag reusing `provisionalTag`). Implement `cmd/corral/criticscore.go` using the same `httpScorecardReader`-style client (read `/api/criticscore` pending; POST adjudication to the MCP/HTTP endpoint the tool exposes). Register in `main.go`.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/brain/ ./cmd/corral/ -race` PASS.

- [ ] **Step 5: Docs**

Update `README.md` (a line under the scorecard describing critic precision + `corral criticscore`), `ROADMAP.md` (move the "critic accuracy" item into Shipped under the scorecard bullet), and the getting-started/scorecard doc page. State the brain-path-only caveat honestly.

- [ ] **Step 6: Gate + commit**

`gofmt -l .` empty; `bash scripts/check-security.sh` OK; `go test ./... -race` green.
```bash
git add internal/brain internal/ui cmd/corral README.md ROADMAP.md site
git commit -m "feat(scorecard): surface critic precision + corral criticscore adjudication CLI + docs"
```

---

## Self-Review

**Spec coverage:**
- §1 structured scope → Task 1. §2 store → Task 4. §3 auto-refute + `RunSingleTest`/`SingleTestCmd` → Tasks 2+3+5. §4 human-gate (store + MCP + CLI) → Tasks 4+6+7. §5 surfacing → Task 7. Fail-closed rules → Tasks 3 (guardrail/never-confirm), 2 (ok=false), 5 (jail error → unadjudicated), 4 (record_id==0 guard via the driver call site + human-not-downgraded). Rollout/docs → Task 7. ✅ no gaps.

**Placeholder scan:** every code step carries real code or an exact "mirror `<file:line>`" with the specific adaptation; test steps carry real assertions. No TBD/TODO.

**Type consistency:** `Scope/TestFile/TestSelector` (Task 1) are the same names used in Tasks 3/5/6. `AutoAdjudication(scope, ran, kills)` (Task 3) is called with those exact args in Task 5. `CriticFindingObservation` fields (Task 5) map 1:1 to `criticscore.Finding` (Task 4) in Task 6's adapter. `CriticCell` (Task 4) feeds `ScorecardCell.CriticPrecision` (Task 7). Adjudication/scope/source constants are the verbatim spec values throughout.

**Known verification points for the implementer (not gaps — confirm against the code):** the exact `runState` accessor names in Task 5; whether the run retains its mutants (add the field if not); whether the installed DuckDB build accepts `ON CONFLICT ... DO UPDATE ... WHERE` (Task 4 gives the fallback); the exact admin-gate helper the control verbs use (Task 6).
