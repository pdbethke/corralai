# Swarm Slice 5 — Tests × Mutants Matrix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Score every test in a suite alone against every mutant (`T tests × M mutants`), yielding per-test adequacy, a human-gated "safe to delete" candidate list, and the matrix-driven critic auto-CONFIRM/refute signal.

**Architecture:** A new `lang` enumeration capability lists a suite's tests; a `TestEnumerator` seam runs the list command in the jail and captures stdout; a `ScoreReport` scorer method surfaces the full `adequacy.Report` (so a genuine zero-kill is distinguished from a baseline-fail); a pure `internal/matrix` engine builds per-test adequacy from a scoring callback; a `tickMatrix` driver phase runs it (opt-in `--matrix`), concurrency-bounded; `internal/matrixstore` persists per-test rows; the matrix drives critic adjudication into `criticscore`; `corral matrix` + `/api/matrix` surface it.

**Tech Stack:** Go 1.26.x, DuckDB (`database/sql`), the `internal/lang` plugin seam, `internal/advpool` driver, `internal/adequacy` scorer, `internal/criticscore`, MCP tools + `corral` CLI.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-07-20-swarm-slice-5-tests-mutants-matrix-design.md` — every task implicitly includes it.
- **Deploy gate (all Go tasks):** `gofmt -l .` prints nothing; `bash scripts/check-security.sh` exits 0 (gosec MEDIUM+ — no `os.WriteFile 0o644`, or a `#nosec Gxxx: <reason>`); `go test ./... -race` passes. Build+test passing is NOT sufficient.
- **Opt-in:** the matrix is OFF by default. It runs only when `RunSpec.Matrix == true` (from `--matrix` / the brain param). When off, today's behavior — including the single-test critic auto-refute — is byte-for-byte unchanged.
- **go + python only day one.** `ListTestsCmd` returns `ok=false` for ruby/js/ts → matrix skipped, fail-closed, verdict notes "matrix unavailable for <lang>". Never a partial/silent claim.
- **`scored` reads `CompliantPass` directly** (via `ScoreReport`), never inferred from `killRate>0`: `CompliantPass=false` → `scored=false` (baseline couldn't run); `CompliantPass=true` → `scored=true`, `kills=len(Killed)`. A genuine zero-kill (`scored && kills==0`) is a delete-candidate; a baseline-fail is not.
- **Auto never confirms without the catchable floor.** `whole-test` finding: `scored && kills≥1` → refuted; `scored && kills==0 && Result.Catchable` → confirmed; else unadjudicated. `dead-check` findings are NEVER auto-touched. Human adjudication always wins (the `criticscore` human-wins invariant is untouched). Source of an auto verdict is `"auto"`.
- **The matrix never gates a verdict.** `aggregate` is still called `blockingFindingOpen=false`. Measurement + advisory only.
- **"Safe to delete" is a CANDIDATE list, never an auto-delete.** Every surface states: "caught 0 of N planted mutants — review for deletion. Relative to this mutant set; a test may still guard behavior no mutant probed."
- **Cost is never hidden:** print `matrix: <T> tests × <M> mutants (<T*M> cells) across <N> workers` before running.
- **DuckDB single-process:** the CLI reads over the brain HTTP API, never the daemon's file. **Persistence is brain-path only.** `--local --matrix` computes + shows the matrix and drives adjudication inline, persisting only when a brain store is present.
- **DRY / additive migrations / SPDX headers** on new files.

---

### Task 1: `lang` test enumeration — `ListTestsCmd` + `ParseTestList`

Add the capability to (a) produce the command that lists a suite's tests and (b) parse that command's stdout into `SingleTestCmd`-compatible selectors. Two methods so the parser is pure and unit-testable against captured output.

**Files:**
- Modify: `internal/lang/lang.go` (Plugin interface ~13-28)
- Modify: `internal/lang/go.go`, `python.go`, `ruby.go`, `javascript.go`, `typescript.go`
- Test: `internal/lang/lang_test.go`

**Interfaces:**
- Produces on `lang.Plugin`:
  - `ListTestsCmd(testPath string) (cmd []string, ok bool)` — the command to enumerate tests. go/python real; ruby/js/ts `(nil,false)`.
  - `ParseTestList(output string) []string` — parse that command's stdout into selectors accepted verbatim by `SingleTestCmd(testPath, selector)`. Deterministic order (as emitted).

- [ ] **Step 1: Write the failing tests**

In `internal/lang/lang_test.go`:
```go
func TestListTestsCmd(t *testing.T) {
	py, _ := lang.ByName("python")
	cmd, ok := py.ListTestsCmd("tests/test_recipes.py")
	if !ok || strings.Join(cmd, " ") != "python3 -m pytest --collect-only -q tests/test_recipes.py" {
		t.Fatalf("python ListTestsCmd = %v ok=%v", cmd, ok)
	}
	g, _ := lang.ByName("go")
	gcmd, ok := g.ListTestsCmd("recipes_test.go")
	if !ok || strings.Join(gcmd, " ") != "go test -list .* ./..." {
		t.Fatalf("go ListTestsCmd = %v ok=%v", gcmd, ok)
	}
	rb, _ := lang.ByName("ruby")
	if _, ok := rb.ListTestsCmd("x"); ok { t.Fatal("ruby ListTestsCmd should be ok=false") }
}

func TestParseTestList(t *testing.T) {
	py, _ := lang.ByName("python")
	// pytest --collect-only -q prints one node id per line, then a summary line.
	pyOut := "tests/test_recipes.py::TakeTests::test_take\ntests/test_recipes.py::TakeTests::test_negative_take\n\n2 tests collected in 0.01s\n"
	got := py.ParseTestList(pyOut)
	want := []string{"tests/test_recipes.py::TakeTests::test_take", "tests/test_recipes.py::TakeTests::test_negative_take"}
	if !reflect.DeepEqual(got, want) { t.Fatalf("python ParseTestList = %v want %v", got, want) }

	g, _ := lang.ByName("go")
	// go test -list prints one test name per line, then an "ok  pkg  0.001s" line.
	goOut := "TestTake\nTestNegativeTake\nExampleTake\nok  \tgithub.com/x/recipes\t0.002s\n"
	ggot := g.ParseTestList(goOut)
	gwant := []string{"TestTake", "TestNegativeTake"} // only Test* — drop Example*/Benchmark* and the ok/PASS/FAIL trailer
	if !reflect.DeepEqual(ggot, gwant) { t.Fatalf("go ParseTestList = %v want %v", ggot, gwant) }
}
```
Ensure imports include `strings` and `reflect`.

- [ ] **Step 2: Run, verify fail** — `go test ./internal/lang/ -run 'TestListTestsCmd|TestParseTestList'` → FAIL (undefined methods).

- [ ] **Step 3: Add the interface methods + implementations**

In `lang.go` Plugin interface add:
```go
	// ListTestsCmd yields a command that ENUMERATES the individual tests in
	// testPath. ok=false when the language can't list tests yet — callers must
	// then skip the matrix, never assume an empty suite.
	ListTestsCmd(testPath string) (cmd []string, ok bool)
	// ParseTestList extracts SingleTestCmd-compatible selectors from the output
	// of ListTestsCmd, in emission order. Pure.
	ParseTestList(output string) []string
```
`python.go` (receiver per the file — grep, e.g. `pyPlugin`):
```go
func (pyPlugin) ListTestsCmd(testPath string) ([]string, bool) {
	if testPath == "" { return nil, false }
	return []string{"python3", "-m", "pytest", "--collect-only", "-q", testPath}, true
}
func (pyPlugin) ParseTestList(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// pytest -q node ids contain "::"; the summary line ("N tests collected")
		// and blank lines do not.
		if strings.Contains(line, "::") { out = append(out, line) }
	}
	return out
}
```
`go.go`:
```go
func (goPlugin) ListTestsCmd(testPath string) ([]string, bool) {
	return []string{"go", "test", "-list", ".*", "./..."}, true
}
func (goPlugin) ParseTestList(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// go test -list prints one identifier per line; keep only Test* funcs
		// (drop Example*/Benchmark*/Fuzz* and the "ok  pkg  0.00s" / PASS trailer,
		// which contain whitespace or don't start with "Test").
		if strings.HasPrefix(line, "Test") && !strings.ContainsAny(line, " \t") {
			out = append(out, line)
		}
	}
	return out
}
```
`ruby.go`, `javascript.go`, `typescript.go` (correct receiver per file):
```go
func (rubyPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }
func (rubyPlugin) ParseTestList(string) []string        { return nil }
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/lang/ -run 'TestListTestsCmd|TestParseTestList' -v` → PASS; then `go build ./...` (all plugins satisfy the extended interface).

- [ ] **Step 5: Commit**
```bash
git add internal/lang
git commit -m "feat(lang): ListTestsCmd + ParseTestList — enumerate a suite's tests (go+python)"
```

---

### Task 2: `Scorer.ScoreReport` — surface the full adequacy Report

The matrix must distinguish a baseline-fail (`CompliantPass=false`) from a genuine zero-kill. `Score` returns only killRate+survivors, collapsing both to "0". Add `ScoreReport` returning the whole `adequacy.Report`.

**Files:**
- Modify: `internal/advpool/driver.go` (the `Scorer` interface ~24-26)
- Modify: `internal/advpool/gate.go` (`JailScorer` ~110-128)
- Modify: `internal/advpool/driver_test.go` (the `fakeScorer` ~66-76 must implement the new method)
- Test: `internal/advpool/gate_test.go` (or wherever JailScorer is tested)

**Interfaces:**
- Consumes: `adequacy.Report{CompliantPass bool; Total int; Killed []string; Survived []string}` (existing).
- Produces on `advpool.Scorer`:
  ```go
  ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
  ```
  Returns the raw `adequacy.Report`. `JailScorer.ScoreReport` reuses `scoreWorkspace` + `adequacy.Score` (the same call `Score` already makes at gate.go:113) and returns the Report directly.

- [ ] **Step 1: Write the failing test**

In `internal/advpool/gate_test.go`, add a test that a JailScorer over a fake `adequacy.Jail` returns a Report with `CompliantPass` and `Killed` populated. Model a fake Jail whose `RunTest` returns `true` for compliant code and `false` for a mutant (killed). Assert `rep.CompliantPass == true`, `len(rep.Killed) == 1`. (Read the existing gate_test.go fake-jail helper and reuse it.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/advpool/ -run TestJailScorerReport` → FAIL (undefined `ScoreReport`).

- [ ] **Step 3: Implement**

Add to the `Scorer` interface in `driver.go`:
```go
	// ScoreReport is Score's richer sibling: it returns the full adequacy.Report
	// (CompliantPass + the Killed/Survived mutant IDs), so a caller can tell a
	// baseline that could not pass (CompliantPass=false) from a genuine zero-kill
	// (CompliantPass=true, len(Killed)==0). The matrix needs this distinction.
	ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
```
In `gate.go`, add to `JailScorer`:
```go
func (s JailScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	scoreBase, cmd := s.scoreWorkspace(codePath, test, testCmd)
	rep, err := adequacy.Score(ctx, s.Jail, scoreBase, codePath, code, mutants, cmd, adequacy.WithMutantTimeout(s.MutantTimeout))
	if err != nil {
		return adequacy.Report{}, fmt.Errorf("advpool: score report: %w", err)
	}
	return rep, nil
}
```
In `driver_test.go`, add `ScoreReport` to `fakeScorer` so it still satisfies `Scorer`. Give the fake a controllable Report (mirror how it controls Score's return): e.g. a `reportFn func(...) (adequacy.Report, error)` field or a simple default returning `adequacy.Report{CompliantPass: true}`. Keep existing tests compiling.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/advpool/ -race` → PASS (the interface change compiles across all callers).

- [ ] **Step 5: Commit**
```bash
git add internal/advpool
git commit -m "feat(advpool): Scorer.ScoreReport surfaces the full adequacy Report (CompliantPass + Killed)"
```

---

### Task 3: `internal/matrix` — the pure per-test adequacy engine

A pure orchestrator: given the test selectors, the mutant count, a worker bound, and a scoring callback, build the per-test adequacy rows. No jail/driver knowledge — unit-tested with a fake callback.

**Files:**
- Create: `internal/matrix/matrix.go`
- Test: `internal/matrix/matrix_test.go`

**Interfaces:**
- Produces:
  ```go
  type TestAdequacy struct {
      Selector, TestFile string
      Kills              int
      KilledMutantIDs    []string
      MutantsTotal       int
      Scored             bool // false if the test could not be scored (baseline-fail / jail error)
      DeleteCandidate    bool // Scored && MutantsTotal>0 && Kills==0
  }
  type Result struct {
      Rows         []TestAdequacy
      MutantsTotal int
      Catchable    bool // union of KilledMutantIDs across scored rows is non-empty
  }
  type TestRef struct{ Selector, TestFile string }
  // ScoreFn scores ONE test against the run's mutants. scored=false ⇒ the test
  // could not be scored (excluded from candidates + adjudication). killedIDs are
  // the mutant IDs this test caught (len == kills).
  type ScoreFn func(ctx context.Context, t TestRef) (kills int, killedIDs []string, scored bool)
  // Build scores every test (up to `workers` concurrently, workers<=0 ⇒ 1) and
  // assembles the Result. Deterministic Rows order: same as `tests` input.
  func Build(ctx context.Context, tests []TestRef, mutantsTotal, workers int, score ScoreFn) Result
  ```

- [ ] **Step 1: Write the failing tests**

`internal/matrix/matrix_test.go`:
```go
func TestBuild(t *testing.T) {
	tests := []matrix.TestRef{
		{Selector: "T::a", TestFile: "t.py"}, // load-bearing: kills 2
		{Selector: "T::b", TestFile: "t.py"}, // vacuous: scored, kills 0 -> delete-candidate
		{Selector: "T::c", TestFile: "t.py"}, // baseline-fail: not scored
	}
	score := func(_ context.Context, tr matrix.TestRef) (int, []string, bool) {
		switch tr.Selector {
		case "T::a":
			return 2, []string{"m1", "m2"}, true
		case "T::b":
			return 0, nil, true
		default:
			return 0, nil, false
		}
	}
	res := matrix.Build(context.Background(), tests, 3, 2, score)
	if len(res.Rows) != 3 { t.Fatalf("rows=%d", len(res.Rows)) }
	if res.Rows[0].Selector != "T::a" || res.Rows[2].Selector != "T::c" {
		t.Fatalf("order not preserved: %+v", res.Rows)
	}
	if res.Rows[0].DeleteCandidate { t.Error("load-bearing test must not be a delete-candidate") }
	if !res.Rows[1].DeleteCandidate { t.Error("scored zero-kill test MUST be a delete-candidate") }
	if res.Rows[2].DeleteCandidate { t.Error("baseline-fail (not scored) must NOT be a delete-candidate") }
	if res.Rows[2].Scored { t.Error("T::c should be scored=false") }
	if !res.Catchable { t.Error("Catchable must be true — some test killed a mutant") }
}

func TestBuildAllUncatchable(t *testing.T) {
	tests := []matrix.TestRef{{Selector: "x", TestFile: "t.py"}}
	res := matrix.Build(context.Background(), tests, 4, 1, func(context.Context, matrix.TestRef) (int, []string, bool) {
		return 0, nil, true // scored, but killed nothing
	})
	if res.Catchable { t.Error("Catchable must be FALSE when no test killed anything") }
	if !res.Rows[0].DeleteCandidate { t.Error("a scored zero-kill test is still a delete-candidate") }
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/matrix/` → FAIL (package missing).

- [ ] **Step 3: Implement**

`internal/matrix/matrix.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

// Package matrix builds per-test adequacy — for each test, which of a run's
// mutants it catches — by scoring each test alone. It is the pure orchestration
// core of the tests x mutants matrix (swarm slice 5); the jail/driver wiring
// lives in internal/advpool.
package matrix

import (
	"context"
	"sync"
)

type TestRef struct{ Selector, TestFile string }

type TestAdequacy struct {
	Selector, TestFile string
	Kills              int
	KilledMutantIDs    []string
	MutantsTotal       int
	Scored             bool
	DeleteCandidate    bool
}

type Result struct {
	Rows         []TestAdequacy
	MutantsTotal int
	Catchable    bool
}

type ScoreFn func(ctx context.Context, t TestRef) (kills int, killedIDs []string, scored bool)

// Build scores every test concurrently (up to `workers`, min 1) and assembles a
// deterministic Result. A row is a DeleteCandidate iff it was Scored, there were
// mutants, and it caught none. Catchable is true iff some scored test caught at
// least one mutant — the soundness floor for auto-confirming a zero-kill finding.
func Build(ctx context.Context, tests []TestRef, mutantsTotal, workers int, score ScoreFn) Result {
	if workers < 1 {
		workers = 1
	}
	rows := make([]TestAdequacy, len(tests))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, tr := range tests {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tr TestRef) {
			defer wg.Done()
			defer func() { <-sem }()
			kills, killed, scored := score(ctx, tr)
			rows[i] = TestAdequacy{
				Selector: tr.Selector, TestFile: tr.TestFile,
				Kills: kills, KilledMutantIDs: killed, MutantsTotal: mutantsTotal,
				Scored:          scored,
				DeleteCandidate: scored && mutantsTotal > 0 && kills == 0,
			}
		}(i, tr)
	}
	wg.Wait()
	res := Result{Rows: rows, MutantsTotal: mutantsTotal}
	for _, r := range rows {
		if r.Scored && r.Kills > 0 {
			res.Catchable = true
			break
		}
	}
	return res
}
```
(Row order is input order via indexed writes — no sort needed.)

- [ ] **Step 4: Run, verify pass** — `go test ./internal/matrix/ -race -v` → PASS (the `-race` run also proves the indexed concurrent writes are race-free).

- [ ] **Step 5: Commit**
```bash
git add internal/matrix
git commit -m "feat(matrix): pure per-test adequacy engine (Build) with delete-candidate + catchable floor"
```

---

### Task 4: `internal/matrixstore` — per-test adequacy DuckDB store

Append-per-run store (one row per test per converged run), mirroring `internal/bugcatch/store.go`. Read that file first for the `Open`/additive-migration/`Record`/perms conventions.

**Files:**
- Create: `internal/matrixstore/store.go`, `internal/matrixstore/types.go`
- Test: `internal/matrixstore/store_test.go`

**Interfaces:**
- Produces:
  ```go
  type Row struct {
      TS                     float64
      RecordID               int64
      RecordHead             string
      Repo, Commit           string
      MissionID              int64
      Lang                   string
      TestSelector, TestFile string
      Kills, MutantsTotal    int
      DeleteCandidate        bool
  }
  func Open(dsn string) (*Store, error)
  func (s *Store) Close() error
  func (s *Store) Record(ctx context.Context, rows []Row) error          // append; no-op on empty
  func (s *Store) DeleteCandidates(ctx context.Context) ([]Row, error)   // latest per (repo,commit,test_selector) where delete_candidate
  func (s *Store) List(ctx context.Context) ([]Row, error)               // recent rows, capped
  ```
- Table `matrix_test_adequacy(ts, record_id, record_head, repo, commit, mission_id, lang, test_selector, test_file, kills, mutants_total, delete_candidate)`.

- [ ] **Step 1: Write the failing test**

`internal/matrixstore/store_test.go`:
```go
func openTmp(t *testing.T) *matrixstore.Store {
	t.Helper()
	s, err := matrixstore.Open(filepath.Join(t.TempDir(), "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	return s
}
func TestRecordAndDeleteCandidates(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	must := func(e error){ if e != nil { t.Fatal(e) } }
	must(s.Record(ctx, []matrixstore.Row{
		{RecordID: 1, Repo: "r", Commit: "c", TestSelector: "T::a", Kills: 2, MutantsTotal: 3, DeleteCandidate: false},
		{RecordID: 1, Repo: "r", Commit: "c", TestSelector: "T::b", Kills: 0, MutantsTotal: 3, DeleteCandidate: true},
	}))
	cands, err := s.DeleteCandidates(ctx); must(err)
	if len(cands) != 1 || cands[0].TestSelector != "T::b" {
		t.Fatalf("delete candidates = %+v", cands)
	}
	all, err := s.List(ctx); must(err)
	if len(all) != 2 { t.Fatalf("list = %d rows", len(all)) }
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/matrixstore/` → FAIL (package missing).

- [ ] **Step 3: Implement**

Write `types.go` (the `Row` struct) and `store.go`, mirroring `internal/bugcatch/store.go`: `Open` (`sql.Open("duckdb", dsn)`, `CREATE TABLE IF NOT EXISTS matrix_test_adequacy (...)`), the additive-migration column ledger + `migrate*` (information_schema probe + ADD COLUMN), `Close`, and a transactional `Record` INSERT loop (no-op on empty; parameterized queries only). `DeleteCandidates`: `SELECT ... FROM matrix_test_adequacy WHERE delete_candidate GROUP BY / latest-per-key` — simplest correct form: filter `delete_candidate=TRUE` and, to get "latest per (repo,commit,test)", select the max `ts` per group (a window or a subquery `WHERE ts = (SELECT MAX(ts) ...)`; a straightforward GROUP BY on the key taking `MAX(ts)` then join is fine). `List`: recent rows `ORDER BY ts DESC LIMIT` a cap constant (mirror bugcatch's `observationsLimit`). No `os.WriteFile`; match bugcatch perms/`#nosec`.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/matrixstore/ -race -v` → PASS; `gofmt -l internal/matrixstore` empty; `bash scripts/check-security.sh` OK.

- [ ] **Step 5: Commit**
```bash
git add internal/matrixstore
git commit -m "feat(matrixstore): per-test adequacy DuckDB store (Record/DeleteCandidates/List)"
```

---

### Task 5: `tickMatrix` — the driver phase (enumerate → score → adjudicate)

Wire it all in the driver: a `TestEnumerator` seam (jail-backed stdout capture), `RunSpec.Matrix`, a `tickMatrix` phase that builds the matrix, a `MatrixSink`, and the matrix-driven critic adjudication (refute + confirm) replacing the single-test loop when the matrix ran.

**Files:**
- Modify: `internal/advpool/run.go` (`RunSpec` — add `Matrix bool`)
- Modify: `internal/advpool/driver.go` (add `TestEnumerator` interface + `Enumerator` optional field + `MatrixObservation`/`MatrixSink`; `run.matrix *matrix.Result` on `runState`; the `tickMatrix` phase; call it in the tick dispatch between `tickPoolAdequacy` and `tickAggregate`; in `tickAggregate` replace the single-test critic loop with the matrix-driven adjudication WHEN `run.matrix != nil`, and feed the `MatrixSink`)
- Modify: `internal/advpool/gate.go` (add a `JailEnumerator` implementing `TestEnumerator` via a stdout-capturing jail call — reuse `scoreWorkspace`)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Consumes: `matrix.Build`/`TestRef`/`Result` (Task 3); `lang.Plugin.ListTestsCmd`/`ParseTestList`/`SingleTestCmd` (Task 1 + existing); `Scorer.ScoreReport` (Task 2); `advpool.NormalizeScope`/`AutoAdjudication`/constants + `CriticFindingSink`/`CriticFindingObservation` (existing, critic feature).
- Produces:
  ```go
  // TestEnumerator runs a list command in the run's jail workspace and returns
  // its stdout. Optional on the Driver (nil ⇒ matrix skipped even if Matrix set).
  type TestEnumerator interface {
      Enumerate(ctx context.Context, codePath, code, test string, listCmd []string) (stdout string, err error)
  }
  type MatrixObservation struct {
      TestSelector, TestFile string
      Kills, MutantsTotal    int
      DeleteCandidate        bool
  }
  type MatrixSink interface {
      Record(recordID int64, recordHead string, obs []MatrixObservation)
  }
  ```

- [ ] **Step 1: Write the failing test**

In `driver_test.go`, add `TestMatrixDrivesAdjudicationAndCandidates`: a fake `TestEnumerator` returning two selectors' worth of stdout; a fake `ParseTestList` path (use the real python plugin via `RunSpec.Lang="python"`, or a fake enumerator whose output the real parser handles); a `fakeScorer.ScoreReport` returning, per test, a controllable Report (test A: `CompliantPass=true, Killed=[m1]`; test B: `CompliantPass=true, Killed=[]`; test C: `CompliantPass=false`). Drive a run with `RunSpec.Matrix=true` and two critic findings: a `whole-test` finding whose selector == test B (kills 0, catchable set) and a `whole-test` finding whose selector == test A (kills≥1). Capture a fake `MatrixSink` + the existing fake `CriticFindingSink`. Assert:
  - the matrix sink got a `DeleteCandidate` row for test B, none for A, and test C excluded/`scored=false` (no candidate);
  - the finding on test A → `CriticFindingObservation.Adjudication == advpool.AdjRefuted`;
  - the finding on test B → `advpool.AdjConfirmed` (catchable floor satisfied because test A killed something);
  - `Source == "auto"`.
Read the existing `TestCriticAutoRefute` for the run-to-convergence harness and reuse it.

- [ ] **Step 2: Run, verify fail** — `go test ./internal/advpool/ -run TestMatrixDrivesAdjudication` → FAIL.

- [ ] **Step 3: Implement**

Add `Matrix bool` to `RunSpec` (run.go) with a doc comment ("opt-in tests×mutants matrix; off preserves today's single-test critic auto-refute path byte-for-byte").

In `gate.go`, add `JailEnumerator` (a thin type holding the same jail + `BaseFiles` as `JailScorer`) implementing `TestEnumerator.Enumerate` by building the workspace via the shared `scoreWorkspace` logic and running `listCmd` through a **stdout-capturing** sandbox call (grep `internal/sandbox` for `Run(...) Result{Output}` — the `adequacy.Jail.RunTest` returns only bool, so enumeration needs the sandbox's Output-returning path). If the sandbox seam isn't directly reachable from gate.go, wire `JailEnumerator` in `cmd/corral/certify_local.go`/the brain where the sandbox is already constructed, and have it implement `TestEnumerator` there. **Verify jail concurrency-safety** before the concurrent scoring in `tickMatrix`: confirm each `Scorer.ScoreReport`/`sandbox.Run` uses an isolated temp workspace per call (it must, since `--swarm` already runs workers concurrently) — note the finding in the report. If not safe, set matrix workers to 1 and note it.

In `driver.go`:
- Add the `TestEnumerator`/`MatrixObservation`/`MatrixSink` types; add `Enumerator TestEnumerator` and `Matrix MatrixSink` optional fields to `Driver` (nil-ok, like the other sinks); add `matrix *matrix.Result` to `runState`.
- Add `tickMatrix(ctx, run)`: gate on `run.rs.Matrix && d.Enumerator != nil && run.mutants staged`. Resolve `p := langFor(run.rs)`; `listCmd, ok := p.ListTestsCmd(run.rs.DevTestPath)`; if `!ok`, log "matrix: unavailable for <lang>" and return (leave `run.matrix == nil`). `out, err := d.Enumerator.Enumerate(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, listCmd)`; on err, log + return (fail-soft). `sels := p.ParseTestList(out)`; build `[]matrix.TestRef` (TestFile = run.rs.DevTestPath). Log the cost readout `matrix: <len(sels)> tests × <len(run.mutants)> mutants (<product> cells) across <workers> workers`. Call `matrix.Build(ctx, refs, len(run.mutants), workers, scoreFn)` where `scoreFn` builds `p.SingleTestCmd(tr.TestFile, tr.Selector)` and calls `d.Scorer.ScoreReport(...)`: `scored = (err==nil && rep.CompliantPass)`, `kills = len(rep.Killed)`, `killedIDs = rep.Killed`. Store `run.matrix = &res`. Log `matrix: <D> delete-candidate test(s)`.
- Call `tickMatrix` in the tick dispatch AFTER `tickPoolAdequacy` and BEFORE `tickAggregate` (grep the dispatch ~line 640-652). It must run every tick idempotently OR gate on a `run.matrixDone` flag (mirror how `tickDevAdequacy` guards re-runs) so the expensive matrix runs ONCE, not every tick.
- In `tickAggregate`, where the critic auto-refute loop is (~1201-1249): when `run.matrix != nil`, for each `whole-test` finding, look up its selector in `run.matrix.Rows`; set the auto verdict via a small helper: `kills≥1 && scored` → `AdjRefuted`; `scored && kills==0 && run.matrix.Catchable` → `AdjConfirmed`; else `AdjUnadjudicated`. `dead-check` findings stay untouched. When `run.matrix == nil`, keep today's single-test loop verbatim. Also, when `run.matrix != nil && d.Matrix != nil && v.RecordID != 0`, emit `MatrixObservation`s to `d.Matrix.Record`.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/advpool/ -race` → PASS (full package, proving no regression to the existing critic path when Matrix is off).

- [ ] **Step 5: Commit**
```bash
git add internal/advpool
git commit -m "feat(advpool): tickMatrix — enumerate+score the tests×mutants matrix, drive critic adjudication"
```

---

### Task 6: Brain wiring — MatrixStore, sinks, enumerator, start param

**Files:**
- Modify: `internal/brain/advpool.go` (a `matrixStore` field + `advpoolMatrixSink` adapter mirroring `advpoolBugCatchSink` / `advpoolCriticSink`; wire the `JailEnumerator` as the run's `Enumerator`; set `RunSpec.Matrix` from the start param; the matrix→criticscore confirm already flows through the existing `CriticFindingSink`)
- Modify: `internal/brain/brain.go`/`identity.go` (`Options` — add `MatrixStore *matrixstore.Store`; thread it; open it next to `bugcatch.Open`/`criticscore.Open` in the brain boot path)
- Modify: the `start_adversarial_run` MCP tool input (grep it) — add an optional `matrix bool` param (default false), plumbed to `RunSpec.Matrix`
- Test: `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `matrixstore.Store` (Task 4); `advpool.MatrixObservation`/`MatrixSink`/`TestEnumerator` (Task 5).
- Produces: `advpoolMatrixSink` mapping `MatrixObservation` → `matrixstore.Row` (stamping ts/repo/commit/mission_id/lang from run context); a brain-side `TestEnumerator` (the `JailEnumerator` over the same jail the scorer uses).

- [ ] **Step 1: Write the failing test**

In `advpool_test.go`, mirror the bugcatch/critic sink tests: build `advpoolMatrixSink` over a temp `matrixstore.Store`, `Record` a couple of `MatrixObservation`s, assert `store.DeleteCandidates`/`List` reflect them (correct selector/kills/candidate + stamped repo/commit).

- [ ] **Step 2: Run, verify fail** — FAIL (adapter missing).

- [ ] **Step 3: Implement**

Add `advpoolMatrixSink` (mirror `advpoolCriticSink` field-for-field): map each `MatrixObservation` → `matrixstore.Row`, call `store.Record`. Wire `MatrixStore` into `Options`, thread to the advpool runtime, set the sink + the `Enumerator` in `StartRun` when the store/jail are present, set `RunSpec.Matrix` from the new `matrix` start param. Open the store in the brain boot path (grep `criticscore.Open`). Add the `matrix bool` param to `start_adversarial_run` (default false).

- [ ] **Step 4: Run, verify pass + gate** — `go test ./internal/brain/ -race` PASS; gofmt + check-security OK.

- [ ] **Step 5: Commit**
```bash
git add internal/brain
git commit -m "feat(brain): matrixstore + sink + enumerator + start_adversarial_run matrix param"
```

---

### Task 7: Surface it — `--matrix` flag, `corral matrix` CLI, `/api/matrix`, verdict summary, docs

**Files:**
- Modify: `cmd/corral/certify_local.go` (add `--matrix` flag → `RunSpec.Matrix`; wire the `JailEnumerator` as the driver's `Enumerator`; set matrix workers from the resolved swarm; the verdict/tape matrix summary + delete-candidate lines)
- Modify: `internal/ui/ui.go` (`GET /api/matrix` returning `matrixstore.List`/`DeleteCandidates`, mirroring `/api/bugcatch`)
- Create: `cmd/corral/matrix.go` (`corral matrix list` over `/api/matrix`; `--json`); register in `cmd/corral/main.go` dispatch (mirror `scorecard`/`criticscore`)
- Modify: `README.md`, `ROADMAP.md` (move the matrix item into Shipped under the swarm bullet), `site/src/content/docs/docs/running-it.mdx` (the matrix section)
- Test: `cmd/corral/matrix_test.go`, and extend a certify_local test for the `--matrix` flag plumbing
- Regenerate: run `scripts/gen-cli-docs.sh` (CI drift gate) after adding `corral matrix`

**Interfaces:**
- Consumes: `matrixstore` reads via the brain HTTP API (Task 4/6).
- Produces: `corral matrix list [--json]`; the `matrix:` verdict summary + delete-candidate lines on the CLI/tape.

- [ ] **Step 1: Write the failing test**

`cmd/corral/matrix_test.go`: mirror `scorecard_test.go`'s HTTP-reader test — a fake `httptest.Server` serving `/api/matrix` JSON; assert `corral matrix list` renders the rows + the delete-candidate section with the honest caveat string, and `--json` emits the raw rows. Assert the caveat text ("Relative to this mutant set") is present.

- [ ] **Step 2: Run, verify fail** — FAIL (command missing).

- [ ] **Step 3: Implement**

Add the `--matrix` bool flag (certify_local.go, default false, help text noting the cost), plumb to `RunSpec.Matrix`, wire the `Enumerator`, set matrix workers = `resolveSwarm(...)`. Emit the matrix summary + delete-candidates (with the caveat) on the verdict render + as a `pool_matrix` tape event. Add `GET /api/matrix` (mirror `/api/bugcatch`). Implement `cmd/corral/matrix.go` (mirror `cmd/corral/criticscore.go`'s HTTP client style — read over the brain API; the delete-candidate section prints the caveat verbatim). Register in `main.go`.

- [ ] **Step 4: Run, verify pass** — `go test ./cmd/corral/ ./internal/ui/ -race` PASS.

- [ ] **Step 5: Docs + CLI-doc regen**

Update `README.md` (a line: per-test adequacy + `corral matrix` + the safe-to-delete list, opt-in `--matrix`, go+python), `ROADMAP.md` (move slice 5 to Shipped), the running-it doc page (the matrix section, honest caveats + brain-path-only persistence + the cost note). Run `scripts/gen-cli-docs.sh` and commit the regenerated CLI reference (grep for it; `--check` is a CI gate).

- [ ] **Step 6: Gate + commit**

`gofmt -l .` empty; `bash scripts/check-security.sh` OK; `go test ./... -race` green; `scripts/gen-cli-docs.sh --check` clean.
```bash
git add cmd/corral internal/ui README.md ROADMAP.md site docs/cli
git commit -m "feat(matrix): --matrix flag + corral matrix CLI + /api/matrix + verdict summary + docs"
```

---

## Self-Review

**Spec coverage:**
- §1 `ListTests` enumeration → Task 1 (`ListTestsCmd`+`ParseTestList`). §2 matrix engine → Task 3 (`Build`) + Task 5 (`tickMatrix` wiring) + Task 2 (`ScoreReport` for the CompliantPass distinction). §3 store → Task 4. §4 safe-to-delete list → Tasks 3 (`DeleteCandidate`) + 5 (sink) + 7 (surface + caveat). §5 auto-CONFIRM tie-in → Task 5 (matrix-driven adjudication with the catchable floor) + Task 6 (brain criticscore feed). Cost readout → Task 5. Fail-closed (unsupported lang, baseline-fail, never-gates, human-wins) → Tasks 1/2/3/5. Rollout/docs → Task 7. ✅ no gaps.

**Placeholder scan:** every code step carries real code or a precise "mirror `<file>`" with the specific adaptation; test steps carry real assertions. One deliberate note: Task 3's `sort` line is explicitly flagged to drop if unused. No TBD/TODO.

**Type consistency:** `TestRef`/`TestAdequacy`/`Result`/`ScoreFn` (Task 3) are consumed with those exact names in Task 5. `ScoreReport` signature (Task 2) is called in Task 5's `scoreFn`. `MatrixObservation`/`MatrixSink`/`TestEnumerator` (Task 5) map 1:1 to `matrixstore.Row` (Task 4) in Task 6's adapter. `DeleteCandidate`/`Catchable`/`Scored` semantics are identical across Tasks 3/5/7. Adjudication constants (`AdjRefuted`/`AdjConfirmed`/`AdjUnadjudicated`) come from the existing critic feature, reused not redefined.

**Known verification points for the implementer (not gaps — confirm against the code):** the exact per-plugin receiver names (Task 1); the `sandbox.Run` stdout-capture path reachable for `JailEnumerator` (Task 5) and whether it belongs in gate.go or the CLI/brain wiring; **jail concurrency-safety** before parallel scoring in `tickMatrix` (Task 5 — set workers=1 + note if unsafe); the `tickMatrix` once-per-run guard (mirror `tickDevAdequacy`'s re-run guard); the exact `start_adversarial_run` input struct (Task 6); whether `errgroup` is vendored (Task 3 uses a plain semaphore+WaitGroup to avoid the dependency question).
