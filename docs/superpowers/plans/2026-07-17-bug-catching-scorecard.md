<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Bug-catching scorecard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A DuckDB-native, record-anchored scoreboard of which model actually *catches bugs*, proven by execution — recall (`proven_missed / survivors`) + precision per (model, role) — fed from the adversarial pool on every converged run, surfaced via `/api/bugcatch` and `corral scorecard`.

**Architecture:** A new append-only DuckDB store (`internal/bugcatch`, mirroring `internal/buildstore`), fed by a new optional `BugCatchSink` on the pure `advpool.Driver` (like the existing `LeaderboardSink`), wired in the brain via `brain.Options` and `StartAdversarialPool`, and read back through a `BuildBugCatchScorecard` view + an authz'd HTTP endpoint + a CLI verb.

**Tech Stack:** Go, go-duckdb/v2 (via `database/sql`), the existing `advpool`/`brain`/`certify` packages.

## Global Constraints

- **A catch is execution-proven or it is not counted.** `catches` derives ONLY from `advpool.Verdict.ProvenMissed`. No claim/self-report path may reach `catches`.
- **Store is append-only.** No `UPDATE`/`DELETE`; one row per (run × model × role). Mirror `buildstore.Open` (go-duckdb/v2, `CREATE TABLE IF NOT EXISTS` on open, parameterized `?`). **No `time.Now()` inside the store** — the caller (driver, via its `Now`) supplies `ts`, exactly as `buildstore.Save` takes timestamps as params.
- **The sink is optional + nil-safe** (nil ⇒ no-op), like the driver's existing `Signer`/`Leaderboard`/`Events`. The pool/leaderboard/certify suites stay green.
- **Feed on BOTH `certified` and `needs-review`** converged runs (a catch or a miss is meaningful regardless of verdict) — unlike `LeaderboardSink`, which is `certified`-only.
- **Per-model attribution is a precondition** (the `client:corral-svc/<name>` identity fix). A seat whose model is empty records `model="(unknown model)"`, never a principal.
- **Recall is the informative v1 headline.** Precision is plumbed from `authored_tests`/`sound_tests` but will read near-100% until a richer authored-test vacuity check lands (a follow-up); do not over-invest in the soundness signal in v1.
- **No MotherDuck claim.** The store opens `CORRALAI_BUGCATCH_DSN` (default local file); an `md:` DSN is a later, separately-proven flip.

---

### Task 1: The `internal/bugcatch` DuckDB store

**Files:**
- Create: `internal/bugcatch/store.go`
- Test: `internal/bugcatch/store_test.go`

**Interfaces:**
- Produces:
  - `type Observation struct { TS time.Time; RecordID int64; RecordHead string; MissionID int64; Repo, Commit, Model, Role, Source string; Catches, Opportunities, SoundTests, AuthoredTests, CriticFlags, MutantsPlanted, MutantsSurvived int }`
  - `type Cell struct { Model, Role string; Catches, Opportunities int; Recall *float64; SoundTests, AuthoredTests int; Precision *float64; CriticFlags, MutantsPlanted, MutantsSurvived, Runs int }`
  - `func Open(dsn string) (*Store, error)`
  - `func (s *Store) Record(ctx context.Context, obs []Observation) error`
  - `func (s *Store) Scorecard(ctx context.Context) ([]Cell, error)`
  - `func (s *Store) Close() error`

- [ ] **Step 1: Write the failing test**

```go
// internal/bugcatch/store_test.go
package bugcatch

import (
	"context"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/bc.duckdb")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestScorecardRecallPrecisionAndMootRuns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	// Run A (needs-review): test-writer caught 1 of 2 gaps; authored a sound test.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 10, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 2, SoundTests: 1, AuthoredTests: 1,
	}}))
	// Run B (certified, moot): 0 gaps → contributes soundness but NO recall opportunity.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 11, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 0, Opportunities: 0, SoundTests: 1, AuthoredTests: 1,
	}}))
	cells, err := s.Scorecard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tw *Cell
	for i := range cells {
		if cells[i].Role == "test-writer" {
			tw = &cells[i]
		}
	}
	if tw == nil {
		t.Fatal("no test-writer cell")
	}
	if tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("catches/opps = %d/%d, want 1/2", tw.Catches, tw.Opportunities)
	}
	if tw.Recall == nil || *tw.Recall != 0.5 {
		t.Fatalf("recall = %v, want 0.5 (moot run adds no opportunity)", tw.Recall)
	}
	if tw.Precision == nil || *tw.Precision != 1.0 {
		t.Fatalf("precision = %v, want 1.0 over 2 sound/2 authored", tw.Precision)
	}
	if tw.Runs != 2 {
		t.Fatalf("runs = %d, want 2", tw.Runs)
	}
}

func TestScorecardNilWhenNoDenominator(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A mutant-generator seat: no test-writer opportunities/authored → recall+precision nil.
	must(t, s.Record(ctx, []Observation{{
		TS: time.Unix(1, 0).UTC(), RecordID: 12, Model: "claude-sonnet-5", Role: "mutant-generator",
		Source: "pool", MutantsPlanted: 5, MutantsSurvived: 1,
	}}))
	cells, _ := s.Scorecard(ctx)
	if len(cells) != 1 || cells[0].Recall != nil || cells[0].Precision != nil {
		t.Fatalf("mutant-generator cell must have nil recall/precision: %+v", cells)
	}
	if cells[0].MutantsPlanted != 5 || cells[0].MutantsSurvived != 1 {
		t.Fatalf("adversary line = %d/%d, want 5/1", cells[0].MutantsPlanted, cells[0].MutantsSurvived)
	}
}

func must(t *testing.T, err error) { t.Helper(); if err != nil { t.Fatal(err) } }
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/bugcatch/ -run TestScorecard -v`
Expected: FAIL — `undefined: Open` / package has no Go files.

- [ ] **Step 3: Implement the store**

```go
// internal/bugcatch/store.go
// SPDX-License-Identifier: Elastic-2.0

// Package bugcatch is the append-only, record-anchored DuckDB store behind the
// bug-catching scorecard: which model actually catches bugs, proven by
// execution. One row per (converged pool run × model × role); the headline
// recall/precision come only from execution-proven catches (advpool ProvenMissed).
// Mirrors internal/buildstore's DuckDB pattern (CREATE IF NOT EXISTS on open,
// parameterized SQL, timestamps supplied by the caller — no time.Now() here).
package bugcatch

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

type Store struct{ db *sql.DB }

type Observation struct {
	TS         time.Time
	RecordID   int64
	RecordHead string
	MissionID  int64
	Repo       string
	Commit     string
	Model      string
	Role       string
	Source     string // "pool"

	Catches       int
	Opportunities int
	SoundTests    int
	AuthoredTests int

	CriticFlags     int
	MutantsPlanted  int
	MutantsSurvived int
}

type Cell struct {
	Model           string   `json:"model"`
	Role            string   `json:"role"`
	Catches         int      `json:"catches"`
	Opportunities   int      `json:"opportunities"`
	Recall          *float64 `json:"recall,omitempty"`
	SoundTests      int      `json:"sound_tests"`
	AuthoredTests   int      `json:"authored_tests"`
	Precision       *float64 `json:"precision,omitempty"`
	CriticFlags     int      `json:"critic_flags,omitempty"`
	MutantsPlanted  int      `json:"mutants_planted,omitempty"`
	MutantsSurvived int      `json:"mutants_survived,omitempty"`
	Runs            int      `json:"runs"`
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("bugcatch: open %q: %w", dsn, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS bugcatch_observations (
		ts TIMESTAMP, record_id BIGINT, record_head VARCHAR, mission_id BIGINT,
		repo VARCHAR, commit VARCHAR, model VARCHAR, role VARCHAR, source VARCHAR,
		catches INTEGER, opportunities INTEGER, sound_tests INTEGER, authored_tests INTEGER,
		critic_flags INTEGER, mutants_planted INTEGER, mutants_survived INTEGER
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("bugcatch: create table: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Record(ctx context.Context, obs []Observation) error {
	if len(obs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bugcatch: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, o := range obs {
		model := o.Model
		if model == "" {
			model = "(unknown model)"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bugcatch_observations VALUES
			(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			o.TS, o.RecordID, o.RecordHead, o.MissionID, o.Repo, o.Commit, model, o.Role, o.Source,
			o.Catches, o.Opportunities, o.SoundTests, o.AuthoredTests,
			o.CriticFlags, o.MutantsPlanted, o.MutantsSurvived); err != nil {
			return fmt.Errorf("bugcatch: insert: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) Scorecard(ctx context.Context) ([]Cell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, role,
		SUM(catches), SUM(opportunities),
		CASE WHEN SUM(opportunities) > 0 THEN SUM(catches)*1.0/SUM(opportunities) END,
		SUM(sound_tests), SUM(authored_tests),
		CASE WHEN SUM(authored_tests) > 0 THEN SUM(sound_tests)*1.0/SUM(authored_tests) END,
		SUM(critic_flags), SUM(mutants_planted), SUM(mutants_survived), COUNT(*)
		FROM bugcatch_observations
		GROUP BY model, role
		ORDER BY SUM(catches) DESC, model, role`)
	if err != nil {
		return nil, fmt.Errorf("bugcatch: scorecard: %w", err)
	}
	defer rows.Close()
	var out []Cell
	for rows.Next() {
		var c Cell
		if err := rows.Scan(&c.Model, &c.Role, &c.Catches, &c.Opportunities, &c.Recall,
			&c.SoundTests, &c.AuthoredTests, &c.Precision, &c.CriticFlags,
			&c.MutantsPlanted, &c.MutantsSurvived, &c.Runs); err != nil {
			return nil, fmt.Errorf("bugcatch: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the tests to green**

Run: `go test ./internal/bugcatch/ -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/bugcatch/store.go internal/bugcatch/store_test.go
git commit -m "bugcatch: append-only DuckDB store for the bug-catching scorecard"
```

---

### Task 2: The `BugCatchSink` on the pool driver + the feed

**Files:**
- Modify: `internal/advpool/driver.go` (add the sink interface + field + the feed in `tickAggregate`)
- Test: `internal/advpool/driver_test.go` (append a feed test)

**Interfaces:**
- Consumes: the run state (`run.testWriterMoot`, `run.poolScored`, `run.authoredTest`) + the signed `Verdict` (`ProvenMissed`, `Survivors`, `MutantsTotal`, `VacuousFindings`, `ModelsByRole`, `RecordID`, `RecordHead`).
- Produces:
  - `type BugCatchObservation struct { Model, Role string; Catches, Opportunities, SoundTests, AuthoredTests, CriticFlags, MutantsPlanted, MutantsSurvived int }`
  - `type BugCatchSink interface { Record(recordID int64, recordHead string, obs []BugCatchObservation) }`
  - a new `BugCatch BugCatchSink` field on `Driver`.

- [ ] **Step 1: Write the failing test**

```go
// internal/advpool/driver_test.go  (append)
type fakeBugCatch struct {
	recordID int64
	obs      []BugCatchObservation
}

func (f *fakeBugCatch) Record(recordID int64, _ string, obs []BugCatchObservation) {
	f.recordID = recordID
	f.obs = append(f.obs, obs...)
}

func obsFor(obs []BugCatchObservation, role string) (BugCatchObservation, bool) {
	for _, o := range obs {
		if o.Role == role {
			return o, true
		}
	}
	return BugCatchObservation{}, false
}

func TestBugCatchSinkFedOnConvergence(t *testing.T) {
	// A needs-review run with 2 survivors, 1 proven-missed → the test-writer seat
	// records catches=1, opportunities=2; the mutant-generator seat records
	// potency; the critic seat records its vacuous-flag count.
	bc := &fakeBugCatch{}
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 1, mutantsTotal: 4, vacuous: 1,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	d.BugCatch = bc
	drivePoolToConvergence(t, d, missionID) // ticks through sign + aggregate

	if bc.recordID == 0 {
		t.Fatal("BugCatch sink was not fed after signing")
	}
	tw, ok := obsFor(bc.obs, "test-writer")
	if !ok || tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("test-writer obs = %+v, want catches=1 opportunities=2", tw)
	}
	if tw.AuthoredTests != 1 || tw.SoundTests != 1 {
		t.Fatalf("test-writer authored/sound = %d/%d, want 1/1", tw.AuthoredTests, tw.SoundTests)
	}
	crit, ok := obsFor(bc.obs, "test-critic")
	if !ok || crit.CriticFlags != 1 || crit.Model != "gemini-3.5-flash" {
		t.Fatalf("critic obs = %+v, want flags=1 model=gemini-3.5-flash", crit)
	}
	mg, ok := obsFor(bc.obs, "mutant-generator")
	if !ok || mg.MutantsPlanted != 4 || mg.MutantsSurvived != 2 {
		t.Fatalf("mutant-generator obs = %+v, want planted=4 survived=2", mg)
	}
	// Execution-proven invariant: catches never exceeds proven_missed.
	if tw.Catches > 1 {
		t.Fatal("catches exceeded proven_missed — a claim leaked into the headline")
	}
}
```

> **Implementer note:** `newScoredRun`, `scoredRun`, and `drivePoolToConvergence` are helpers you construct by adapting the existing convergence tests (`TestTick_PoolAdequacy_ScoresProvenMissed` / `completeFullRun` in `driver_test.go`). Reuse their fakes (Scorer/Validator/Signer). If the existing helpers already drive a run to a signed verdict, extend the one nearest in shape rather than writing new fakes. Keep the assertion contract above.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/advpool/ -run TestBugCatchSink -v`
Expected: FAIL — `d.BugCatch` undefined.

- [ ] **Step 3: Add the interface, field, and feed**

In `internal/advpool/driver.go`, near `LeaderboardSink`:

```go
// BugCatchObservation is one seat's execution-proven contribution from a single
// converged run (see internal/bugcatch). Catches come ONLY from ProvenMissed.
type BugCatchObservation struct {
	Model, Role                                 string
	Catches, Opportunities                      int
	SoundTests, AuthoredTests                   int
	CriticFlags, MutantsPlanted, MutantsSurvived int
}

// BugCatchSink is the optional per-run bug-catching feed (nil ⇒ no-op), mirroring
// LeaderboardSink but fed on EVERY converged run (certified AND needs-review) —
// a catch or a miss is meaningful regardless of the overall verdict.
type BugCatchSink interface {
	Record(recordID int64, recordHead string, obs []BugCatchObservation)
}
```

Add to the `Driver` struct (beside `Leaderboard LeaderboardSink`):
```go
	BugCatch BugCatchSink
```

In `tickAggregate`, AFTER the `d.Signer` block sets `v.RecordID`/`v.RecordHead` (and regardless of `v.Status`), before the `d.emit(... "pool_verdict" ...)`:
```go
	if d.BugCatch != nil {
		d.BugCatch.Record(v.RecordID, v.RecordHead, bugCatchObservations(run, v))
	}
```

And the pure builder (package-level func in `driver.go`):
```go
// bugCatchObservations derives each seat's execution-proven contribution from
// the run state + the signed verdict. Catches = ProvenMissed only.
func bugCatchObservations(run *runState, v Verdict) []BugCatchObservation {
	var out []BugCatchObservation
	// test-writer: the execution-proven catcher.
	authored, sound := 0, 0
	if !run.testWriterMoot {
		authored = 1
		if run.poolScored && run.authoredTest != "" { // compiled + scored ⇒ a valid discriminating test
			sound = 1
		}
	}
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole["test-writer"], Role: RoleTestWriter,
		Catches: v.ProvenMissed, Opportunities: v.Survivors,
		AuthoredTests: authored, SoundTests: sound,
	})
	// test-critic: theater-detection (judgement, lower-confidence).
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole["test-critic"], Role: RoleTestCritic,
		CriticFlags: len(v.VacuousFindings),
	})
	// mutant-generator: adversary potency (not a catcher).
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole["mutant-generator"], Role: RoleMutantGenerator,
		MutantsPlanted: v.MutantsTotal, MutantsSurvived: v.Survivors,
	})
	return out
}
```

> **Implementer note:** confirm the exact role constant names (`RoleTestWriter`/`RoleTestCritic`/`RoleMutantGenerator`) in `internal/advpool/roles.go` and the exact field name of the vacuous findings on `Verdict` (`VacuousFindings`) — use whatever the code actually declares. If `ModelsByRole` keys differ from the constants, key off the same strings the driver already uses when it builds `ModelsByRole`.

- [ ] **Step 4: Run the test to green**

Run: `go test ./internal/advpool/ -run TestBugCatchSink -v && go test ./internal/advpool/ -count=1`
Expected: PASS, and the whole advpool suite stays green (the sink is additive + nil-safe).

- [ ] **Step 5: Commit**

```bash
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "advpool: optional BugCatchSink fed per converged run (proven catches only)"
```

---

### Task 3: Brain adapter + wiring

**Files:**
- Modify: `internal/brain/advpool.go` (add `advpoolBugCatchSink` adapter; set `driver.BugCatch` in `StartAdversarialPool`)
- Modify: `internal/brain/brain.go` or wherever `Options` lives (add `BugCatch *bugcatch.Store` field)
- Modify: `cmd/corral/main.go` (open the store from `CORRALAI_BUGCATCH_DSN`, thread into `Options`)
- Test: `internal/brain/advpool_test.go` (adapter maps driver obs → store rows)

**Interfaces:**
- Consumes: `advpool.BugCatchSink`, `bugcatch.Store`, `advpool.Verdict` context (repo/commit/mission).
- Produces: `advpoolBugCatchSink` (satisfies `advpool.BugCatchSink`); a wired `driver.BugCatch`.

- [ ] **Step 1: Write the failing test**

```go
// internal/brain/advpool_test.go  (append)
func TestBugCatchSinkPersistsToStore(t *testing.T) {
	store, err := bugcatch.Open(t.TempDir() + "/bc.duckdb")
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { store.Close() })

	sink := advpoolBugCatchSink{
		store: store,
		clock: func() time.Time { return time.Unix(1000, 0).UTC() },
		missionID: 7, repo: "git@x:y.git", commit: "abc123",
	}
	sink.Record(42, "headhash", []advpool.BugCatchObservation{
		{Model: "claude-sonnet-5", Role: "test-writer", Catches: 1, Opportunities: 2, AuthoredTests: 1, SoundTests: 1},
	})
	cells, err := store.Scorecard(context.Background())
	if err != nil { t.Fatal(err) }
	if len(cells) != 1 || cells[0].Catches != 1 || cells[0].Opportunities != 2 {
		t.Fatalf("scorecard = %+v, want one cell catches=1 opps=2", cells)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/brain/ -run TestBugCatchSinkPersists -v`
Expected: FAIL — `advpoolBugCatchSink` undefined.

- [ ] **Step 3: Implement the adapter + wiring**

In `internal/brain/advpool.go` (mirror `advpoolLeaderboardSink`):

```go
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
		})
	}
	if err := s.store.Record(context.Background(), rows); err != nil {
		log.Printf("advpool: bugcatch record failed (mission %d record %d): %v", s.missionID, recordID, err)
	}
}
```

In `StartAdversarialPool`, where the driver's `Signer`/`Leaderboard`/`Events` are set, add (guarded on the store being configured):
```go
	if opts.BugCatch != nil {
		driver.BugCatch = advpoolBugCatchSink{
			store: opts.BugCatch, clock: rt.now, // reuse the runtime's clock; or time.Now if none
			missionID: mid, repo: rs.Repo, commit: rs.Commit,
		}
	}
```

> **Implementer note:** use whatever clock the runtime already threads (search `rt.now`/`Now` in `advpool.go`); if there is none, pass `time.Now`. `mid` is the tracking mission id created just above; `rs` is the `RunSpec`.

Add the `Options` field (in the file where `type Options struct` lives — grep it):
```go
	// BugCatch, if set, receives per-run execution-proven bug-catching
	// observations from the adversarial pool (internal/bugcatch scorecard store).
	BugCatch *bugcatch.Store
```

In `cmd/corral/main.go`, beside the other store opens (grep for `buildstore.Open`):
```go
	var bugCatchStore *bugcatch.Store
	if dsn := env("CORRALAI_BUGCATCH_DSN", filepath.Join(claudeDir, "corralai_bugcatch.duckdb")); dsn != "" {
		bugCatchStore, err = bugcatch.Open(dsn)
		if err != nil {
			log.Fatalf("open bugcatch store: %v", err)
		}
		defer bugCatchStore.Close()
	}
	// ... in the brain.Options literal:
	//     BugCatch: bugCatchStore,
```

> **Implementer note:** match the existing default-path convention for the other DuckDB stores (`claudeDir`/`~/.claude/...`) and the existing `env(...)` helper; don't invent a new one.

- [ ] **Step 4: Run the tests to green**

Run: `go test ./internal/brain/ -run TestBugCatch -v && go build ./... && go test ./internal/brain/ -count=1`
Expected: PASS; whole tree builds; brain suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/brain/advpool.go internal/brain/advpool_test.go cmd/corral/main.go <options-file>
git commit -m "brain: wire the bugcatch scorecard store into the adversarial pool"
```

---

### Task 4: `BuildBugCatchScorecard` view + `/api/bugcatch` endpoint

**Files:**
- Create: `internal/brain/bugcatch_view.go`
- Modify: `internal/ui/ui.go` (register `/api/bugcatch`, mirror the `/api/leaderboard` handler + authz)
- Test: `internal/brain/bugcatch_view_test.go`

**Interfaces:**
- Consumes: `bugcatch.Store`.
- Produces:
  - `type ScorecardCell struct { bugcatch.Cell; Provisional bool }` (embed + the honesty flag)
  - `type Scorecard struct { Cells []ScorecardCell }`
  - `func BuildBugCatchScorecard(store *bugcatch.Store) (Scorecard, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/brain/bugcatch_view_test.go
func TestBuildScorecardFlagsThinCellsProvisional(t *testing.T) {
	store, _ := bugcatch.Open(t.TempDir() + "/bc.duckdb")
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	// 2 runs for claude test-writer (< 3 ⇒ provisional).
	for i := 0; i < 2; i++ {
		store.Record(ctx, []bugcatch.Observation{{
			TS: time.Unix(int64(i), 0).UTC(), Model: "claude-sonnet-5", Role: "test-writer",
			Source: "pool", Catches: 1, Opportunities: 1, SoundTests: 1, AuthoredTests: 1,
		}})
	}
	sc, err := BuildBugCatchScorecard(store)
	if err != nil { t.Fatal(err) }
	if len(sc.Cells) != 1 || !sc.Cells[0].Provisional {
		t.Fatalf("2-run cell must be provisional: %+v", sc.Cells)
	}
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./internal/brain/ -run TestBuildScorecard -v` → FAIL (undefined).

- [ ] **Step 3: Implement the view**

```go
// internal/brain/bugcatch_view.go
// SPDX-License-Identifier: Elastic-2.0
package brain

import "github.com/pdbethke/corralai/internal/bugcatch"

// provisionalBelow: a cell with fewer than this many runs is a data point, not a
// ranking (the explore-in-production lesson). Consumers must not present it as a leader.
const provisionalBelow = 3

type ScorecardCell struct {
	bugcatch.Cell
	Provisional bool `json:"provisional"`
}

type Scorecard struct {
	Cells []ScorecardCell `json:"cells"`
}

func BuildBugCatchScorecard(store *bugcatch.Store) (Scorecard, error) {
	if store == nil {
		return Scorecard{}, nil
	}
	cells, err := store.Scorecard(contextTODO())
	if err != nil {
		return Scorecard{}, err
	}
	out := make([]ScorecardCell, 0, len(cells))
	for _, c := range cells {
		out = append(out, ScorecardCell{Cell: c, Provisional: c.Runs < provisionalBelow})
	}
	return Scorecard{Cells: out}, nil
}
```

> **Implementer note:** use `context.Background()` (the `contextTODO()` above is shorthand — replace it). Match the JSON-tag style of the existing `Leaderboard` view.

Register the endpoint in `internal/ui/ui.go` beside `/api/leaderboard` (line ~237), with the SAME authz wrapper the leaderboard handler uses, read-only:
```go
	mux.HandleFunc("/api/bugcatch", s.bugcatch)
// ...
func (s *server) bugcatch(w http.ResponseWriter, r *http.Request) {
	// mirror s.leaderboard: authz, then:
	sc, err := brain.BuildBugCatchScorecard(s.bugCatch)
	if err != nil { http.Error(w, err.Error(), 500); return }
	writeJSON(w, sc)
}
```

> **Implementer note:** thread the `*bugcatch.Store` into the UI `server`/`Deps` the same way `s.queue`/`s.hosts`/`s.tel` are threaded (grep how `leaderboard` gets its stores). Use the existing `writeJSON`/authz helpers — don't invent new ones.

- [ ] **Step 4: Run the tests to green** — `go test ./internal/brain/ ./internal/ui/ -count=1` → PASS; `go build ./...` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/brain/bugcatch_view.go internal/brain/bugcatch_view_test.go internal/ui/ui.go
git commit -m "brain/ui: BuildBugCatchScorecard view + read-only /api/bugcatch"
```

---

### Task 5: `corral scorecard` CLI verb

**Files:**
- Create: `cmd/corral/scorecard.go`
- Modify: `cmd/corral/main.go` (dispatch the `scorecard` subcommand)
- Test: `cmd/corral/scorecard_test.go`

**Interfaces:**
- Consumes: `bugcatch.Store` (opened from `CORRALAI_BUGCATCH_DSN`, same default as Task 3).
- Produces: `func runScorecard(args []string, store scorecardReader, stdout io.Writer) int` where `type scorecardReader interface { Scorecard(context.Context) ([]bugcatch.Cell, error) }` (so the test injects a fake).

- [ ] **Step 1: Write the failing test**

```go
// cmd/corral/scorecard_test.go
type fakeScore struct{ cells []bugcatch.Cell }
func (f fakeScore) Scorecard(context.Context) ([]bugcatch.Cell, error) { return f.cells, nil }

func TestScorecardTableAndJSON(t *testing.T) {
	r := 0.5
	f := fakeScore{cells: []bugcatch.Cell{{Model: "claude-sonnet-5", Role: "test-writer", Catches: 1, Opportunities: 2, Recall: &r, Runs: 2}}}

	var table bytes.Buffer
	if rc := runScorecard(nil, f, &table); rc != 0 { t.Fatalf("rc=%d", rc) }
	if !strings.Contains(table.String(), "claude-sonnet-5") || !strings.Contains(table.String(), "test-writer") {
		t.Fatalf("table missing model/role:\n%s", table.String())
	}
	if !strings.Contains(table.String(), "provisional") { // runs=2 < 3
		t.Fatalf("thin cell must be marked provisional:\n%s", table.String())
	}

	var j bytes.Buffer
	if rc := runScorecard([]string{"--json"}, f, &j); rc != 0 { t.Fatalf("rc=%d", rc) }
	if !strings.Contains(j.String(), `"recall"`) || !strings.Contains(j.String(), "0.5") {
		t.Fatalf("json missing recall:\n%s", j.String())
	}
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./cmd/corral/ -run TestScorecard -v` → FAIL (undefined).

- [ ] **Step 3: Implement the verb**

```go
// cmd/corral/scorecard.go
// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/bugcatch"
)

type scorecardReader interface {
	Scorecard(context.Context) ([]bugcatch.Cell, error)
}

func runScorecard(args []string, store scorecardReader, stdout io.Writer) int {
	fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the raw cells as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cells, err := store.Scorecard(context.Background())
	if err != nil {
		fmt.Fprintf(stdout, "corral scorecard: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cells)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tROLE\tCATCHES\tRECALL\tPRECISION\tRUNS\t")
	for _, c := range cells {
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\t%d%s\t\n",
			c.Model, c.Role, c.Catches, c.Opportunities,
			pctOrDash(c.Recall), pctOrDash(c.Precision), c.Runs, provisionalTag(c.Runs))
	}
	tw.Flush()
	return 0
}

func pctOrDash(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *p*100)
}

func provisionalTag(runs int) string {
	if runs < 3 {
		return " (provisional)"
	}
	return ""
}
```

Wire the dispatch in `cmd/corral/main.go` (beside the `certify`/`secret`/`control` subcommand switch): open the store from `CORRALAI_BUGCATCH_DSN` (same default as Task 3) and call `runScorecard(rest, store, os.Stdout)`.

- [ ] **Step 4: Run the tests to green** — `go test ./cmd/corral/ -run TestScorecard -v && go build ./...` → PASS + clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/scorecard.go cmd/corral/scorecard_test.go cmd/corral/main.go
git commit -m "corral: 'scorecard' verb — bug-catching recall/precision per model×role"
```

---

## After all tasks
- Regenerate CLI docs if the drift gate requires it: `bash scripts/gen-cli-docs.sh` (the new `scorecard` verb changes `corral -h`).
- The eval-harness (volume), a UI panel, and MotherDuck's live flip remain the follow-up slices named in the spec's non-goals.
