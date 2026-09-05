// SPDX-License-Identifier: Elastic-2.0

// Package scanstore is the DuckDB ledger behind `corral certify --repo`: one
// row per invocation in `scans` (the header — provenance for the whole run)
// and one row per walked file in `scan_files` (the disposition — audited
// with a kill rate, or rejected with a reason). `certify --repo` already
// computes a complete disposition for every file it walks; today it prints
// that to stdout and discards it. This store is what keeps it, so a later
// question ("why did file X get skipped on scan N") has an answer.
// Mirrors internal/bugcatch's DuckDB pattern (CREATE IF NOT EXISTS on open,
// an additive migration list applied by probing information_schema.columns,
// parameterized SQL) and internal/buildstore's id-sequence pattern.
package scanstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed ledger of `certify --repo` scans and the
// per-file dispositions each one produced.
type Store struct{ db *sql.DB }

// Scan is one `certify --repo` invocation — the header row a scan's files
// are keyed to. It exists to carry PROVENANCE: a disposition without its
// substrate, engine version and model set is a claim without a warrant, and
// this is what a later analysis has to defend itself with.
type Scan struct {
	Owner, Repo, Commit string
	Substrate           string
	EngineVersion       string
	ModelSet            string
	Top                 int
	AllCandidates       bool
	DiffBase            string
	TotalFiles          int
	Candidates          int
	Audited             int
	// KillRate is *float64, not float64, for the same reason File.KillRate
	// is: reposcan.RepoReport.KillRate is math.NaN() when Audited == 0 (a
	// deliberate choice — see internal/reposcan/report.go — so a stored 0.0
	// never misrepresents "no measurement was made" as "terrible tests").
	// But DuckDB sorts NaN as larger than any other DOUBLE value: measured
	// against this exact driver, MAX(kill_rate) over a table containing NaN
	// returns NaN (displacing a real 0.9), `kill_rate > 0.5` MATCHES the NaN
	// row, and `ORDER BY kill_rate DESC LIMIT 1` surfaces the never-measured
	// scan FIRST — the exact inversion of "best-scoring". A caller SHOULD
	// convert math.NaN() to nil before constructing a Scan (see
	// certify_repo_record.go's killRatePtr, which does this at the
	// source, where the intent is visible); Record additionally
	// re-checks IsNaN itself as a backstop (see sanitizeKillRate below),
	// so this field's *float64 type is the load-bearing contract, not
	// caller discipline alone.
	KillRate      *float64
	CacheHits     int
	PreflightRan  bool
	PreflightNote string
	StartedAt     time.Time
	FinishedAt    time.Time
	// CorralVersion is the version string `corral version` prints for the
	// binary that ran this scan. EngineVersion above is the same value today,
	// but it is an INPUT to the verdict cache key (a job keyed under one
	// engine version must never be served for another), and a cache key is
	// free to stop being a version string. This column is the provenance
	// answer to "which build produced this row", and it must not change
	// meaning because the key did.
	CorralVersion string
	// Host and Cores are the box. The audit cost model is
	// O(mutants x the target's suite runtime) and the only lever an operator
	// has is the machine, so a wall-clock number in this ledger is
	// uninterpretable without the hardware it was earned on.
	Host  string
	Cores int
	// TreesRequested is how many private trees this scan ASKED each file's
	// pool for (resolveMutantConcurrency on the workspace substrate). It is
	// an intention, not a result — each file's own probe decides what it got,
	// and scan_files.trees records that. 0 on the jail substrate, which
	// builds no trees, and stored SQL NULL there for the same reason
	// File.Trees is: 0 is a number a query would average.
	TreesRequested int
	// TotalMillis is the scan's own wall clock, in milliseconds. Derivable
	// from FinishedAt - StartedAt, and stored anyway: this is the column the
	// cost-model page groups by, and a warehouse reader (DuckDB-WASM in a
	// browser, a MotherDuck share) should not have to subtract two
	// timestamps in every query to ask the first question anyone asks.
	TotalMillis int64
	// SelectionMillis is how long the scan's ONE instrumented coverage run
	// took — the pass that decides which tests execute which file.
	//
	// It lives at the SCAN grain because that is the grain it happens at. It
	// is also carried on every file's verdict (advpool.Timing.Selection) so a
	// per-file readout can name every phase of that file's audit, but it is
	// the SAME run shared by all of them: summing a per-file copy across a
	// scan would count one instrumented run once per file and invent time
	// nobody spent. This column is the one a cost query adds.
	//
	// *int64, and NULL under --whole-suite (or an unsupported language, or a
	// runner that could not be built): no pass ran, and a stored 0 would say
	// the pass ran for free.
	SelectionMillis *int64
	// SelectionReusedFrom is the id of the PRIOR scan whose instrumented
	// coverage evidence this scan reused, because its tree, instrumented
	// command and language plugin were all byte-identical to that scan's —
	// see internal/reposcan.TreeDigest and the selection_cache table. nil
	// on every scan that ran its own instrumented pass (or ran none at
	// all): a reused scan and a scan that instrumented nothing both leave
	// SelectionMillis nil, and this is the ONLY column that tells them
	// apart, so it must never be a fabricated 0 or a guess — only a real
	// prior scan id, or nothing.
	SelectionReusedFrom *int64
	// InputTokens, OutputTokens and ModelCalls are what the scan consumed
	// from the providers, scan-wide. The per-role breakdown lives in
	// scan_model_calls; these are the totals the run already printed to
	// stdout and, until now, discarded.
	InputTokens  int64
	OutputTokens int64
	ModelCalls   int64
	// SourcePushed records whether this scan's --push carried source bytes
	// (mutant code, the authored test, the verdict blob) to the operator's
	// warehouse. It is a CUSTODY fact and belongs in the record: "did our
	// code leave the box on that run" must be answerable from the ledger,
	// not from whoever remembers the argv.
	SourcePushed bool
	// StatementSHA256 is the sha256 of the signed --attest statement this
	// scan produced, or "" when --attest was not given. The warehouse row
	// carries the same value; this is the local half of the same link.
	StatementSHA256 string
	// RekorLogIndex and RekorUUID are the receipt --transparency earns when
	// it uploads the --attest statement to a public Rekor log: the entry's
	// position in the log and its UUID, the two coordinates a third party
	// needs to look the entry back up without trusting this ledger.
	//
	// RekorLogIndex is *int64, not int64, for the same NULL-not-zero reason
	// every other unmeasured column here is a pointer: log index 0 is a
	// REAL, valid position (the very first entry a fresh log ever
	// committed), so a scan that was never uploaded — --transparency was
	// not given, or the upload failed and this run fails open — must read
	// back nil, never a fabricated 0 that would misname it "logged at
	// index zero". RekorUUID has no such collision (there is no valid empty
	// UUID), so it stays a plain string, "" meaning "no receipt", exactly
	// like StatementSHA256 above.
	//
	// Both are written empty at Record time and STAMPED after, mirroring
	// StatementSHA256: the statement has to exist (and be uploaded) before
	// there is a receipt to record, and the scan row is written before the
	// statement is.
	RekorLogIndex *int64
	RekorUUID     string
}

// File is one row per file per scan: what corral decided about it, and, for
// rejected files, why. Evidence is first-class and NOT a detail: "paired"
// is a filename guess, "coverage" is an instrument's report from one
// instrumented suite run, "proven" is execution. A table that averages
// proof with guesswork is a leaderboard nobody can defend.
//
// KillRate is *float64, not float64: a rejected file was never scored, and
// it must read back as NULL, not 0.0. A stored 0.0 would later read as
// "your tests caught nothing here" about a file corral never graded — a
// false accusation, persisted.
type File struct {
	Path           string
	Lang           string
	Disposition    string // "audited" | "rejected"
	Reason         string // populated when Disposition == "rejected"
	KillRate       *float64
	Survivors      int
	Gradable       bool
	PreflightState string
	Evidence       string // "paired" | "coverage" | "proven"
	// Detail is the underlying error text behind Reason, when the caller
	// has one (today: reposcan.FileResult.Detail, populated for
	// executor-error rejections). "" for every other reason — Reason alone
	// is self-explanatory for those. A rejected row with a bare reason code
	// and no Detail is the ORIGINAL problem this field exists to fix: "why"
	// used to require re-running with a code trace, not a query.
	Detail string
	// TimedOut mirrors advpool.Verdict.TimedOut / reposcan.WeakFile.TimedOut:
	// true for an AUDITED row whose kill rate was banked from a run that hit
	// its wall-clock deadline before the pool converged. A claim carries how
	// it was earned — a later query over this ledger must be able to tell
	// "measured, but the pool did not finish" apart from a clean audited
	// row without re-deriving it from KillRate alone.
	TimedOut bool
	// TestWriterFailed mirrors advpool.Verdict.TestWriterFailed /
	// reposcan.WeakFile.TestWriterFailed: true for an AUDITED row whose pool
	// exhausted its compile-retry budget without authoring a compiling
	// killing test for at least one survivor. HONESTY NOTE: a row with
	// Survivors > 0 and this true is NOT a clean suite — no killing test was
	// PROVEN, not "no gaps were found". A later query over this ledger must
	// be able to tell that apart from an ordinary audited row without
	// re-deriving it, the same way TimedOut already lets it.
	TestWriterFailed bool
	// PoolTestUnsound mirrors advpool.Verdict.PoolTestUnsound /
	// reposcan.WeakFile.PoolTestUnsound: true for an AUDITED row whose pool
	// authored a COMPILING test (TestWriterFailed is false) whose scoring
	// report never genuinely graded (failed on the unmutated compliant code,
	// the canary was never killed, or nothing was scored). A DIFFERENT
	// diagnosis from TestWriterFailed with the same honesty consequence:
	// ProvenMissed reads 0 for a reason that is neither "clean" nor "tried
	// and missed", and a later query must be able to tell that apart too.
	PoolTestUnsound bool
	// ProvenMissed mirrors advpool.Verdict.ProvenMissed /
	// reposcan.WeakFile.ProvenMissed: survivors the pool's authored test
	// then killed BY EXECUTION — corral's strongest claim, a specific
	// demonstrated bug the dev suite misses. HONESTY NOTE: 0 here is
	// ambiguous on its own — combined with Survivors and TestWriterFailed it
	// resolves to one of three cases (no survivors to prove; writer never
	// authored a compiling test; writer's test proved nothing) — see
	// reposcan.WeakFile.ProvenMissed's doc for the full breakdown. A query
	// over this ledger that wants the unambiguous "real, demonstrated gap"
	// signal should filter on ProvenMissed > 0, not just != 0.
	ProvenMissed int
	// ProvenMutantIDs is the EVIDENCE behind ProvenMissed: a comma-separated
	// list of the mutant ids the pool's authored test actually killed, in the
	// scoring report's own order. ProvenMissed is a COUNT, and a count cannot
	// be interrogated — a row reading 0 could not be told apart from a row
	// reading 3 without re-running the audit, which on the repo path means
	// paying for it again (`certify --repo` has no tape flag).
	ProvenMutantIDs string
	// AuthoredTest is the pool's compiling authored test source, retained as
	// evidence for exactly the case that motivated it: a sound, collected,
	// genuinely-grading test that killed NOTHING. On 2026-07-31 a paid
	// pallets/flask audit did precisely that against 10 survivors, and the
	// entire surviving record of the attempt was a single integer, so the
	// question "what did it actually try?" had no answer at any price short of
	// another run. "" when no compiling test was ever produced.
	AuthoredTest string
	// TestSelection, SelectedTests and SuiteTests say WHICH MEASUREMENT this
	// row's kill rate is: the tests coverage evidence showed execute this
	// file ("coverage-context", SelectedTests of SuiteTests), rather than the
	// whole suite. A ledger that stores the number without the question it
	// answers cannot be queried honestly afterwards: a rate earned against 14
	// of 1431 tests and one earned against all 1431 are not comparable, and
	// nothing else in the row can tell them apart.
	TestSelection string
	SelectedTests int
	SuiteTests    int
	// SelectionFallback is the REASON this row was graded by the whole suite
	// (no selector for the language, --whole-suite, an evidence run that
	// failed). Empty when TestSelection is set — never both.
	SelectionFallback string
	// WriterMode mirrors advpool.Verdict.WriterMode: whether this file's
	// survivors were attacked by one writer call each ("per-survivor") or by
	// one call carrying them all ("batched"). NULL — never one of the two
	// spellings — on a row from a run that named no mode and on every row
	// written before this column existed: the two modes are not the same
	// measurement, so a query that groups by them must be able to exclude the
	// rows that cannot say which they are.
	WriterMode string
	// Uncovered: the evidence ran and found NO test executing this file. Its
	// KillRate is written NULL for the same reason a rejected file's is —
	// nothing graded the file, so a stored 0.0 would later read as "your
	// tests caught nothing here" about a measurement that was never made.
	Uncovered bool
	// ImportOnly refines Uncovered: nil (SQL NULL) for a row written before
	// this column existed, or for an audited row this scan simply did not
	// carry the refinement for (defensive only — every write path below
	// always sets it once Uncovered is known). *true means the file WAS
	// executed — at import/module-load time, coverage.py's own static
	// record, never inside a test's dynamic context — just never by a test
	// DIRECTLY (reposcan.ReasonImportOnly / advpool.Verdict.ImportOnly);
	// *false means Uncovered is a genuine "nothing executed this at all"
	// finding. Every reader that prints the word UNCOVERED off this row
	// (corral scans, corral seal) MUST check ImportOnly FIRST: calling an
	// imported-but-untested file "UNCOVERED — no test executes this file"
	// is the exact false claim reposcan.ReasonImportOnly exists to correct,
	// and it survives here unless every reader honours the same precedence
	// the candidacy exclusion already does.
	ImportOnly *bool
	// CoveringTests is the number of tests the selection evidence showed
	// execute this file — reposcan.Candidate.CoveringTests, carried whole.
	// nil (SQL NULL) for a row from a scan where the evidence never measured
	// this file at all (no evidence collected, or an evidence-collection
	// failure that fell back to pairing-only candidacy — see the
	// evidence-as-candidacy design) — never confused with a measured,
	// genuine zero. Additive: a row written before this column existed is
	// also NULL.
	CoveringTests *int
	// MutantsFrom is the sha256 of the RECORDED MUTANT SET this row's audit
	// replayed (`certify --repo --mutants`), and empty when the run generated
	// its own mutants — which is the normal case and every pre-`--mutants`
	// row. It is what makes two rows comparable: a kill rate is a score
	// against a specific exam, and mutants are authored by a model, so two
	// ordinary runs of the same file sat different exams. A shared
	// mutants_from is the evidence that they did not.
	MutantsFrom string
	// Trees and ConcurrencyNote mirror advpool.Verdict.Concurrency: how
	// many private trees the workspace substrate's probe scored this file
	// with at once, or — when it granted only one — why. Trees 0 is the one
	// explicit "not recorded" state — a row written before this column
	// existed, a rejected file that was never scored, or a substrate that
	// builds no trees — and is stored SQL NULL, never the integer 0, so a
	// caller (and a query) can always tell it from a measured one tree.
	Trees           int
	ConcurrencyNote string
	// SharedDirs is the comma-joined list of dependency directories that were
	// symlinked into every tree rather than copied (advpool.Concurrency's
	// Shared). Stored SQL NULL, not "", when the run shared nothing: they are
	// the one thing the trees did NOT hold privately, so "none" and "this
	// ledger does not say" are different answers.
	SharedDirs string
	// CacheKey is reposcan's content address for this file's audit — every
	// input that can change the verdict, hashed. It is what makes a later
	// scan able to reuse this row instead of re-running the suite once per
	// mutant. "" for a rejected file: nothing was measured, so there is
	// nothing to reuse.
	CacheKey string
	// VerdictJSON is the marshalled advpool.Verdict, stored whole rather than
	// rebuilt from this row's individual columns. A reconstitution assembled
	// field-by-field silently drops whatever the column list does not cover,
	// and a verdict served back MISSING a field is a different claim from the
	// one that was signed.
	VerdictJSON string
	// ComputedAt is when this verdict was actually earned, carried so a later
	// reuse can disclose its AGE. A scan that reports reused work without
	// saying how old it is presents stale measurement as current — the exact
	// self-flattering record corral exists to prevent.
	ComputedAt time.Time
	// ModelsByRole is advpool.Verdict.ModelsByRole, serialized with
	// reposcan.CanonicalKV so a per-file role assignment is byte-comparable
	// with the scan-wide model_set — the same canonicalization, not a
	// second one that could drift from it.
	ModelsByRole string
	// MutantsTotal is advpool.Verdict.MutantsTotal — the denominator a kill
	// rate is computed over. Kept as its own column, not just inside
	// VerdictJSON, because grading models means GROUP BY / SUM, and a blob
	// cannot be aggregated.
	MutantsTotal int
	// RegionsTotal and RegionsProbed are advpool.Verdict.RegionsTotal /
	// RegionsProbed — the mutant-generator seats the run dispatched, and the
	// seats that actually returned usable mutants.
	RegionsTotal  int
	RegionsProbed int
	// DroppedRegions are mutant-generator seats abandoned after
	// MaxShardRetries — the run's COVERAGE SHORTFALL. A kill rate is over the
	// mutants that were produced, not the ones that should have been, so a
	// row with dropped regions is a weaker claim than one without, and a
	// leaderboard that cannot see the difference is comparing unlike runs.
	DroppedRegions string
	// VacuousFindings is the COUNT of advpool.Verdict.VacuousFindings —
	// test-critic's designed-to-pass/vacuous flags on this file's run.
	VacuousFindings int
	// Status is advpool.Verdict.Status ("certified" | "needs-review").
	Status string
	// PromptShape mirrors advpool.Verdict.PromptShape: "chunk" when every
	// mutant-generator shard on this file's run saw only its own symbols'
	// bodies plus the file's preamble, "file" when even one shard fell back
	// to the whole file (including every unsharded run, which always shows
	// the whole file). "" for a row written before this column existed, or
	// a rejected file that was never scored — never a fabricated value.
	PromptShape string
	// MutantBudget / MutantBudgetRule / Complexity mirror
	// advpool.Verdict.MutantBudget: how many faults the seats were asked
	// for, by which rule ("complexity", "explicit", "default"), and the
	// file's summed symbol complexity the rule read. NULL on a row written
	// before these columns, or a file that generated nothing — never a
	// fabricated 0, since a kill rate over 8 mutants and one over 40 are
	// different measurements.
	MutantBudget     *int
	MutantBudgetRule string
	Complexity       *int
	// Symbols/SymbolsProbed/Decisions/DecisionsProbed mirror
	// advpool.Verdict.ExamCoverage: how much of the file's surface the
	// graded mutants reached. NULL when the coverage was never measured
	// (no signatures, no mutants, or a row from before these columns).
	Symbols         *int
	SymbolsProbed   *int
	Decisions       *int
	DecisionsProbed *int
	// AuthoredTestNotCollected mirrors advpool.Verdict.AuthoredTestNotCollected:
	// the run proved a killing test compiled and ran, but the dev suite's own
	// collection never picked it up, so ProvenMissed on this row is earned
	// against a test the target project would never actually execute.
	AuthoredTestNotCollected bool
	// BaselineFailed mirrors advpool.Verdict.BaselineFailed: the dev suite did
	// not pass on the UNMUTATED code, so DevKillRate on this row is
	// meaningless — the audit had nothing sound to measure a mutant against.
	BaselineFailed bool
	// SuiteBaselineMillis mirrors advpool.Verdict.BaselineDuration, in
	// milliseconds: the compliant (unmutated) suite's own wall-clock runtime.
	// It is the single input to the audit cost model — O(mutants x the
	// TARGET's suite runtime), measured at 1.46s for pallets/flask and 77s
	// for psf/requests, a 53x spread — so AVG(suite_baseline_ms) over this
	// ledger is what capacity planning should be computed FROM, not
	// extrapolated from one repo. Milliseconds, not a Go duration: this
	// column is read by SQL and by DuckDB-WASM in the browser, neither of
	// which has a decoder for time.Duration.
	SuiteBaselineMillis int64
	// CacheHit mirrors reposcan.FileResult.CacheHit: true when this row's
	// verdict was served from a prior scan's cache_key match rather than
	// earned by running this scan's own mutants. Exists alongside
	// ReusedFromScanID so an aggregate can exclude reused rows — without it,
	// enabling the cache would make one measurement count once per scan
	// forever, and whatever happened to be cached would dominate every
	// average.
	CacheHit bool
	// ReusedFromScanID is the id of the scan whose row this one reused, or
	// nil when this row was measured fresh. *int64, not int64: "not reused"
	// must read back as NULL, not a scan id of 0, which would be a foreign
	// key to nothing.
	ReusedFromScanID *int64
	// ParentSHA256 is the sha256 of the FILE'S OWN BYTES as audited — the
	// validity key the whole "is this verdict still current" question turns
	// on. A verdict is about bytes, not about a commit: a commit sha says the
	// repo moved, this says whether THIS file did. A reader holding the
	// checkout can answer "live or stale" with a hash and no re-audit.
	// Empty for a file nothing read (an exclusion decided at walk time).
	ParentSHA256 string
	// MutantsGraded, MutantsInvalid and MutantsTimedOut are the denominators
	// behind the kill rate, split by what actually happened to each mutant.
	// The local scan_mutants table only admits killed|survived (its CHECK
	// predates this change and DuckDB cannot alter a CHECK in place), so the
	// invalid and timed-out mutants have no row of their own here and are
	// carried at the FILE grain instead — a disclosed asymmetry with the
	// warehouse's corral_mutants, which is a new table and can be complete.
	MutantsGraded  int
	MutantsInvalid int
	// MutantsTimedOut is *int, not int, because NOTHING produces it yet: no
	// verdict field counts mutants that hit their deadline. A stored 0 would
	// be the positive claim "none timed out" on every row corral has ever
	// written, which is a measurement nobody made. nil until the task that
	// measures it lands; a genuine zero then means a genuine zero.
	MutantsTimedOut *int
	// The per-phase clock. Every one is *int64 and every one is SQL NULL
	// until the task that measures it lands: the single timing number that
	// existed before this change (SuiteBaselineMillis) is not where the
	// minutes go, and a stored 0 for a phase nothing timed would be averaged
	// into the cost model as a phase that costs nothing.
	SelectionMillis    *int64
	GenerationMillis   *int64
	PoolMillis         *int64
	DevPassMillis      *int64
	AuthoredPassMillis *int64
	CriticMillis       *int64
	TotalMillis        *int64
	// MutantMillisMedian and MutantMillisMax summarize scan_mutants.duration_ms
	// at the file grain, so the "which files are slow, and is it one mutant or
	// all of them" question is one row rather than an aggregate over the
	// mutant table. NULL until anything times a mutant.
	MutantMillisMedian *int64
	MutantMillisMax    *int64
	// ChallengerJaccard, ChallengerKappa and ChallengerSufficient are the
	// agreement between the primary test-writer seat and the challenger seat.
	// All three are pointers because "the challenger did not run" and "the
	// challenger agreed on nothing" are different claims, and a 0.0 cannot
	// tell them apart. NULL until the challenger task fills them.
	ChallengerJaccard    *float64
	ChallengerKappa      *float64
	ChallengerSufficient *bool
	// The pair's COUNTS, recorded whether or not the coefficient was
	// sufficient: how many survivors both writers faced, how many each
	// left unproven, and the union/intersection of those misses. Two
	// writers that both prove nearly everything have too few misses for
	// a coefficient — and that is a finding, not a NULL. On psf/requests
	// (2026-09-04) the shadow writer's 10 of 12 and 15 of 15 lived only in
	// the run log. NULL when no pair was measured at all.
	ChallengerMutants        *int
	ChallengerSurvivedWriter *int
	ChallengerSurvivedShadow *int
	ChallengerUnion          *int
	ChallengerShared         *int
	// GoalsDerived is how many goals reposcan derived for this file. 0 means
	// none were derived, which is a real and common answer, so this one is a
	// plain int rather than a pointer.
	GoalsDerived int
	// GoalReused is whether this file's goal was served from the goal cache
	// — a prior scan derived it from the SAME bytes — rather than freshly
	// derived by this scan. *bool, like ChallengerSufficient above: "not
	// reused" and "the question was never asked" (no cache wired, a
	// hand-written --goals entry, a pre-migration row) are different
	// claims, and a stored false cannot tell them apart. NULL until a
	// reused goal is actually recorded; true is the only value this column
	// ever fabricates nothing to avoid.
	GoalReused *bool
	// PerMutant and TestsPerMutantMin/Median/Max are
	// advpool.Verdict.TestSelection.PerMutant / .TestsPerMutant: whether each
	// mutant was graded by the tests that reach its own lines, and the spread
	// of how many tests that was. They live in the ledger, not only in the
	// pushed row, because the warehouse is the ledger pushed — a column the
	// warehouse carries and the ledger cannot supply would force a second,
	// drifting mapping straight out of the report.
	//
	// The three counts are pointers so an unmeasured spread is ABSENT rather
	// than {0,0,0}: a per-mutant run whose every mutant was rejected by the
	// compile gate graded nothing, and a stored 0-to-0 range would read as
	// "every mutant ran no tests".
	PerMutant            bool
	TestsPerMutantMin    *int
	TestsPerMutantMedian *int
	TestsPerMutantMax    *int
}

// scanFilesMigrationCols is the additive set of columns this package has
// ever needed on scan_files beyond the original bare-bones shape
// (scan_id/path/lang/disposition/reason/kill_rate/survivors/gradable), in
// the order they must be added — a ledger created before preflight_state
// and evidence existed gets them added on open; a ledger created after
// already has them and neither is re-added. Both are also present in the
// fresh CREATE TABLE IF NOT EXISTS above, so a brand-new store never runs
// these ALTERs; this list exists for a store created by an earlier version
// of this package (or, in the round-trip tests, one built by hand to prove
// the migration path actually runs).
var scanFilesMigrationCols = []struct{ name, ddl string }{
	{"preflight_state", "preflight_state VARCHAR"},
	{"evidence", "evidence VARCHAR"},
	{"detail", "detail VARCHAR"},
	{"timed_out", "timed_out BOOLEAN"},
	{"test_writer_failed", "test_writer_failed BOOLEAN"},
	{"proven_missed", "proven_missed INTEGER"},
	{"pool_test_unsound", "pool_test_unsound BOOLEAN"},
	{"proven_mutant_ids", "proven_mutant_ids VARCHAR"},
	{"authored_test", "authored_test VARCHAR"},
	{"cache_key", "cache_key VARCHAR"},
	{"verdict_json", "verdict_json VARCHAR"},
	{"computed_at", "computed_at TIMESTAMP"},
	{"models_by_role", "models_by_role VARCHAR"},
	{"mutants_total", "mutants_total INTEGER"},
	{"regions_total", "regions_total INTEGER"},
	{"regions_probed", "regions_probed INTEGER"},
	{"dropped_regions", "dropped_regions VARCHAR"},
	{"vacuous_findings", "vacuous_findings INTEGER"},
	{"status", "status VARCHAR"},
	{"authored_test_not_collected", "authored_test_not_collected BOOLEAN"},
	{"baseline_failed", "baseline_failed BOOLEAN"},
	{"cache_hit", "cache_hit BOOLEAN"},
	{"reused_from_scan_id", "reused_from_scan_id BIGINT"},
	{"suite_baseline_ms", "suite_baseline_ms BIGINT"},
	{"test_selection", "test_selection VARCHAR"},
	{"selected_tests", "selected_tests INTEGER"},
	{"suite_tests", "suite_tests INTEGER"},
	{"selection_fallback", "selection_fallback VARCHAR"},
	{"uncovered", "uncovered BOOLEAN"},
	{"mutants_from", "mutants_from VARCHAR"},
	{"trees", "trees INTEGER"},
	{"concurrency_note", "concurrency_note VARCHAR"},
	{"shared_dirs", "shared_dirs VARCHAR"},
	{"parent_sha256", "parent_sha256 VARCHAR"},
	{"mutants_graded", "mutants_graded INTEGER"},
	{"mutants_invalid", "mutants_invalid INTEGER"},
	{"mutants_timed_out", "mutants_timed_out INTEGER"},
	{"selection_ms", "selection_ms BIGINT"},
	{"generation_ms", "generation_ms BIGINT"},
	{"pool_ms", "pool_ms BIGINT"},
	{"dev_pass_ms", "dev_pass_ms BIGINT"},
	{"authored_pass_ms", "authored_pass_ms BIGINT"},
	{"critic_ms", "critic_ms BIGINT"},
	{"total_ms", "total_ms BIGINT"},
	{"mutant_ms_median", "mutant_ms_median BIGINT"},
	{"mutant_ms_max", "mutant_ms_max BIGINT"},
	{"challenger_jaccard", "challenger_jaccard DOUBLE"},
	{"challenger_kappa", "challenger_kappa DOUBLE"},
	{"challenger_sufficient", "challenger_sufficient BOOLEAN"},
	{"goals_derived", "goals_derived INTEGER"},
	{"goal_reused", "goal_reused BOOLEAN"},
	{"per_mutant", "per_mutant BOOLEAN"},
	{"tests_per_mutant_min", "tests_per_mutant_min INTEGER"},
	{"tests_per_mutant_median", "tests_per_mutant_median INTEGER"},
	{"tests_per_mutant_max", "tests_per_mutant_max INTEGER"},
	{"writer_mode", "writer_mode VARCHAR"},
	{"prompt_shape", "prompt_shape VARCHAR"},
	{"covering_tests", "covering_tests INTEGER"},
	{"import_only", "import_only BOOLEAN"},
	{"mutant_budget", "mutant_budget INTEGER"},
	{"mutant_budget_rule", "mutant_budget_rule VARCHAR"},
	{"complexity", "complexity INTEGER"},
	{"symbols", "symbols INTEGER"},
	{"symbols_probed", "symbols_probed INTEGER"},
	{"decisions", "decisions INTEGER"},
	{"decisions_probed", "decisions_probed INTEGER"},
	{"challenger_mutants", "challenger_mutants INTEGER"},
	{"challenger_survived_writer", "challenger_survived_writer INTEGER"},
	{"challenger_survived_shadow", "challenger_survived_shadow INTEGER"},
	{"challenger_union", "challenger_union INTEGER"},
	{"challenger_shared", "challenger_shared INTEGER"},
}

// scansMigrationCols is the same ledger at the SCAN grain. `scans` had no
// migration list at all until this change — every column it has ever had was
// in its original CREATE TABLE — so this list starts with the provenance and
// cost columns added here. A ledger an earlier corral created gets them
// added on open; a fresh one already has them from the CREATE below and runs
// zero ALTERs.
var scansMigrationCols = []struct{ name, ddl string }{
	{"corral_version", "corral_version VARCHAR"},
	{"host", "host VARCHAR"},
	{"cores", "cores INTEGER"},
	{"trees_requested", "trees_requested INTEGER"},
	{"total_ms", "total_ms BIGINT"},
	{"input_tokens", "input_tokens BIGINT"},
	{"output_tokens", "output_tokens BIGINT"},
	{"model_calls", "model_calls BIGINT"},
	{"source_pushed", "source_pushed BOOLEAN"},
	{"statement_sha256", "statement_sha256 VARCHAR"},
	{"selection_ms", "selection_ms BIGINT"},
	{"selection_reused_from", "selection_reused_from BIGINT"},
	{"rekor_log_index", "rekor_log_index BIGINT"},
	{"rekor_uuid", "rekor_uuid VARCHAR"},
}

// scanMutantsMigrationCols is the same ledger, at the mutant grain: the
// columns scan_mutants has needed beyond its original shape
// (scan_id/path/mutant_id/outcome/parent_sha256/proven), in the order they
// must be added. Both are also in the fresh CREATE TABLE below, so a new
// store runs zero ALTERs; this list is what brings a scan_mutants written by
// an earlier version of this package up to the current shape rather than
// failing every mutant INSERT against it.
var scanMutantsMigrationCols = []struct{ name, ddl string }{
	{"tests_run", "tests_run INTEGER"},
	{"selection_rule", "selection_rule VARCHAR"},
	{"duration_ms", "duration_ms BIGINT"},
	{"killed_by", "killed_by VARCHAR"},
	{"span_start", "span_start INTEGER"},
	{"span_end", "span_end INTEGER"},
	{"proven_by_authored_alone", "proven_by_authored_alone BOOLEAN"},
}

// Open opens (creating if absent) the scans/scan_files store at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("scanstore: open %q: %w", dsn, err)
	}

	// scans is one row per `certify --repo` invocation. It exists to carry
	// PROVENANCE: a disposition without its substrate, engine version and
	// model set is a claim without a warrant, and this table is what a
	// later analysis has to defend itself with.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scans (
		id BIGINT PRIMARY KEY, ts TIMESTAMP,
		owner VARCHAR, repo VARCHAR, commit VARCHAR,
		substrate VARCHAR, engine_version VARCHAR, model_set VARCHAR,
		top INTEGER, all_candidates BOOLEAN, diff_base VARCHAR,
		total_files INTEGER, candidates INTEGER, audited INTEGER,
		kill_rate DOUBLE, cache_hits INTEGER,
		preflight_ran BOOLEAN, preflight_note VARCHAR,
		started_at TIMESTAMP, finished_at TIMESTAMP,
		corral_version VARCHAR, host VARCHAR, cores INTEGER, trees_requested INTEGER,
		total_ms BIGINT, input_tokens BIGINT, output_tokens BIGINT, model_calls BIGINT,
		source_pushed BOOLEAN, statement_sha256 VARCHAR,
		selection_ms BIGINT, selection_reused_from BIGINT,
		rekor_log_index BIGINT, rekor_uuid VARCHAR
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scans table: %w", err)
	}

	if err := migrateScans(db); err != nil {
		db.Close()
		return nil, err
	}

	// scan_files is one row per file per scan. evidence is first-class and
	// NOT a detail: "paired" is a filename guess, "coverage" is an
	// instrument's report, "proven" is execution, and "" means no evidence
	// claim was ever made (a file the scan never ran anything against — see
	// cmd/corral/certify_repo_record.go's ungradableEvidence/
	// exclusionEvidence). A table that averages proof with guesswork is a
	// leaderboard nobody can defend — the CHECK constraints below exist so
	// a typo'd label (a future caller writing "prooven" or "n/a") fails
	// loud at INSERT time instead of silently entering a table that is
	// meant to be queried by exact string. They apply only to a table this
	// CREATE actually creates: a pre-existing store from before this
	// change keeps whatever it already had (DuckDB has no
	// `ADD CONSTRAINT` this package uses for the additive migrations
	// below), the same best-effort boundary migrateScanFiles already
	// accepts for newly added columns.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR CHECK (disposition IN ('audited', 'rejected')),
		reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN,
		preflight_state VARCHAR CHECK (preflight_state IN ('', 'executed', 'not-executed')),
		evidence VARCHAR CHECK (evidence IN ('', 'paired', 'coverage', 'proven')),
		detail VARCHAR,
		timed_out BOOLEAN,
		test_writer_failed BOOLEAN,
		proven_missed INTEGER,
		pool_test_unsound BOOLEAN,
		proven_mutant_ids VARCHAR,
		authored_test VARCHAR,
		cache_key VARCHAR,
		verdict_json VARCHAR,
		computed_at TIMESTAMP,
		models_by_role VARCHAR,
		mutants_total INTEGER,
		regions_total INTEGER,
		regions_probed INTEGER,
		dropped_regions VARCHAR,
		vacuous_findings INTEGER,
		status VARCHAR,
		authored_test_not_collected BOOLEAN,
		baseline_failed BOOLEAN,
		cache_hit BOOLEAN,
		reused_from_scan_id BIGINT,
		suite_baseline_ms BIGINT,
		test_selection VARCHAR,
		selected_tests INTEGER,
		suite_tests INTEGER,
		selection_fallback VARCHAR,
		uncovered BOOLEAN,
		mutants_from VARCHAR,
		trees INTEGER,
		concurrency_note VARCHAR,
		shared_dirs VARCHAR,
		parent_sha256 VARCHAR,
		mutants_graded INTEGER,
		mutants_invalid INTEGER,
		mutants_timed_out INTEGER,
		selection_ms BIGINT,
		generation_ms BIGINT,
		pool_ms BIGINT,
		dev_pass_ms BIGINT,
		authored_pass_ms BIGINT,
		critic_ms BIGINT,
		total_ms BIGINT,
		mutant_ms_median BIGINT,
		mutant_ms_max BIGINT,
		challenger_jaccard DOUBLE,
		challenger_kappa DOUBLE,
		challenger_sufficient BOOLEAN,
		goals_derived INTEGER,
		goal_reused BOOLEAN,
		per_mutant BOOLEAN,
		tests_per_mutant_min INTEGER,
		tests_per_mutant_median INTEGER,
		tests_per_mutant_max INTEGER,
		writer_mode VARCHAR,
		prompt_shape VARCHAR,
		covering_tests INTEGER,
		import_only BOOLEAN,
		mutant_budget INTEGER,
		mutant_budget_rule VARCHAR,
		complexity INTEGER,
		symbols INTEGER,
		symbols_probed INTEGER,
		decisions INTEGER,
		decisions_probed INTEGER,
		challenger_mutants INTEGER,
		challenger_survived_writer INTEGER,
		challenger_survived_shadow INTEGER,
		challenger_union INTEGER,
		challenger_shared INTEGER
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_files table: %w", err)
	}

	if err := migrateScanFiles(db); err != nil {
		db.Close()
		return nil, err
	}

	// scan_mutants is one row per mutant per file per scan — the grain a
	// per-file kill rate averages away. "Which generator produces mutants a
	// suite does not catch" is a question about mutants, and it cannot be
	// asked of a table whose finest row is a file.
	//
	// The mutant's SOURCE is deliberately absent. ParentSHA256 ties the row to
	// the exact bytes it was derived from, which is enough to group, count and
	// compare, without putting a tenant's code at rest in the warehouse.
	// Storing patches is a later, deliberate decision — it can be added, it
	// cannot be un-added.
	//
	// There is NO reuse marker of its own here, and that is worth stating
	// because it surprises: when a file's verdict is served from the verdict
	// cache, its mutants are re-recorded under the new scan_id exactly like
	// freshly-earned ones, so a naive leaderboard counts one measurement once
	// per scan forever. Excluding reused mutants requires joining back to
	// scan_files on (scan_id, path) and filtering on cache_hit — the flag
	// lives there, at the grain the cache actually operates on.
	//
	// The CHECK on outcome exists for the same reason scan_files has one on
	// evidence: this table is queried by exact string, and a typo'd label
	// should fail loud at INSERT rather than quietly enter a leaderboard.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_mutants (
		scan_id BIGINT, path VARCHAR, mutant_id VARCHAR,
		outcome VARCHAR CHECK (outcome IN ('killed', 'survived')),
		parent_sha256 VARCHAR,
		proven BOOLEAN,
		tests_run INTEGER,
		selection_rule VARCHAR,
		duration_ms BIGINT,
		killed_by VARCHAR,
		span_start INTEGER,
		span_end INTEGER,
		proven_by_authored_alone BOOLEAN
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_mutants table: %w", err)
	}

	if err := migrateScanMutants(db); err != nil {
		db.Close()
		return nil, err
	}

	// scan_model_calls is one row per (file, role): what that seat cost. The
	// scan header carries the run's totals, which answer "what did this
	// audit cost" and nothing else — "which seat was slow, and on which
	// file" is the operator's actual second question and the warehouse's
	// first GROUP BY, and it cannot be asked of a scan-wide total.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_model_calls (
		scan_id BIGINT, path VARCHAR, role VARCHAR, model VARCHAR,
		calls INTEGER, retries INTEGER,
		input_tokens BIGINT, output_tokens BIGINT,
		cached_input_tokens BIGINT, cache_write_input_tokens BIGINT, wall_ms BIGINT
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_model_calls table: %w", err)
	}

	if err := migrateScanModelCalls(db); err != nil {
		db.Close()
		return nil, err
	}

	// scan_events is the tape: an ordered log of what the pool did, at the
	// grain a phase boundary happens. Everything else in this ledger is a
	// SUMMARY — a rate, a count, a duration — and a summary cannot answer
	// "what was it doing for those 35 minutes". seq (not ts) is the ordering
	// key: two events inside one millisecond are ordinary, and a tape whose
	// order depends on clock granularity is not a tape.
	//
	// detail is VARCHAR holding JSON TEXT, deliberately, not DuckDB's JSON
	// type: the target of a push may be any DuckDB the operator owns,
	// including one with no extensions installed and no network to fetch
	// them, and a schema that cannot be created on the operator's own
	// machine is not a schema. Readers parse it; DuckDB's json functions
	// still work on a VARCHAR.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_events (
		scan_id BIGINT, path VARCHAR, seq BIGINT, ts TIMESTAMP,
		kind VARCHAR, actor VARCHAR, subject VARCHAR, model VARCHAR,
		duration_ms BIGINT, detail VARCHAR
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_events table: %w", err)
	}

	if err := migrateScanEvents(db); err != nil {
		db.Close()
		return nil, err
	}

	// goal_cache is content-addressed, not scan-scoped: one row per
	// (path, source_digest, model, engine_prompt_rev), reused across every
	// scan that asks the same question about the same bytes. No migration
	// list — this table did not exist before this change, so CREATE TABLE
	// IF NOT EXISTS on every Open is the whole story: a ledger opened
	// before this table existed gains it on the next Open, the same way
	// scan_events itself first appeared.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goal_cache (
		path VARCHAR, source_digest VARCHAR, model VARCHAR, engine_prompt_rev VARCHAR,
		goal VARCHAR, provenance VARCHAR, created_at TIMESTAMP,
		UNIQUE (path, source_digest, model, engine_prompt_rev)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create goal_cache table: %w", err)
	}

	// selection_cache is content-addressed like goal_cache, on
	// (tree_digest, cmd_digest, plugin, substrate) rather than a source
	// digest: the evidence ONE instrumented run of a project's suite
	// produces is a property of the WHOLE checkout (which tests execute
	// which files) and the exact instrumented command that produced it,
	// not of any one file. scan_id names the scan that EARNED this row —
	// the one that actually paid for the instrumented run — so a later
	// reuse can say "reused from scan N" rather than merely "reused from
	// somewhere". No migration list, for the same reason goal_cache has
	// none: this table did not exist before this change, so CREATE TABLE
	// IF NOT EXISTS on every Open is the whole story.
	//
	// substrate joined the key (and the CREATE TABLE, in place — this
	// column is not additive via migrateColumns, and the UNIQUE constraint
	// was widened directly) before this table ever shipped to a merged
	// branch: a jail run's "Ran=true" instrumented evidence is degraded in
	// ways specific to that sandbox (see the jail's own recipe doc), and
	// without substrate in the key a workspace run could be served a
	// jail-degraded row keyed on the identical tree — the #110 class of
	// bug, recurring one cache later. Because feat/tier-a-caches never
	// merged, there is no shipped row shape to migrate off of, so widening
	// the CREATE TABLE (and the UNIQUE constraint with it) directly is the
	// whole fix — a real migration, adding a column AFTER a UNIQUE already
	// shipped, would need DuckDB's ALTER TABLE ... ADD COLUMN plus
	// re-creating the constraint, which this branch never had to do.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS selection_cache (
		tree_digest VARCHAR, cmd_digest VARCHAR, plugin VARCHAR, substrate VARCHAR,
		raw BLOB, note VARCHAR, created_at TIMESTAMP, scan_id BIGINT,
		UNIQUE (tree_digest, cmd_digest, plugin, substrate)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create selection_cache table: %w", err)
	}

	// scans.id allocation: a CREATE SEQUENCE + nextval(), the same approach
	// internal/buildstore, internal/telemetry, internal/reference and
	// internal/repoindex already use for their own BIGINT PRIMARY KEYs on
	// this driver. Chosen over `SELECT COALESCE(MAX(id),0)+1` because that
	// form races under concurrent Record calls (two callers can read the
	// same MAX before either inserts); a DuckDB SEQUENCE hands out each
	// value atomically. Verified against this exact driver version
	// (github.com/marcboeker/go-duckdb/v2 v2.4.3, already a dependency) by
	// TestRecordRoundTripsEveryDisposition, which asserts the returned id
	// is nonzero and then reads the row back by that id.
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS scans_id START 1`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scans_id sequence: %w", err)
	}

	return &Store{db: db}, nil
}

// migrateColumns additively brings `table` up to `cols`. DuckDB has no
// `ADD COLUMN IF NOT EXISTS`, and this is a ledger — silently discarding
// every ALTER error would make a genuinely broken migration
// indistinguishable from an already-applied one. Instead: probe
// information_schema.columns for what already exists, add only what is
// missing, and surface any other ALTER failure as a real error. Idempotent
// across repeated opens: a table that already has every column runs zero
// ALTERs.
//
// One loop, four lists. The lists stay separate — they are separate ledgers
// of separate decisions, and a shared list would invite adding a column to
// the wrong table — but the loop over them was copied verbatim per table and
// had begun to drift only in its error strings.
func migrateColumns(db *sql.DB, table string, cols []struct{ name, ddl string }) error {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, table)
	if err != nil {
		return fmt.Errorf("scanstore: probe existing %s columns: %w", table, err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scanstore: scan existing %s column: %w", table, err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("scanstore: probe existing %s columns: %w", table, err)
	}
	rows.Close()

	for _, col := range cols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("scanstore: migrate %s: add column %s: %w", table, col.name, err)
		}
	}
	return nil
}

func migrateScans(db *sql.DB) error {
	return migrateColumns(db, "scans", scansMigrationCols)
}

func migrateScanFiles(db *sql.DB) error {
	return migrateColumns(db, "scan_files", scanFilesMigrationCols)
}

func migrateScanMutants(db *sql.DB) error {
	return migrateColumns(db, "scan_mutants", scanMutantsMigrationCols)
}

// scanModelCallsMigrationCols carries the columns scan_model_calls has grown
// since its original CREATE. cached_input_tokens is the first: a ledger an
// earlier corral wrote gets it added on open, and every row already in it
// keeps NULL — which is correct, not a gap. Those runs never asked a provider
// for a cached prompt and never read one back, so "not measured" is exactly
// what they mean.
//
// migrateScanEvents keeps an EMPTY list on purpose: nothing predates
// scan_events' CREATE, and the list is here so the next column added to it
// goes through the same additive path as every other column in this package
// rather than being appended to a CREATE TABLE an existing ledger will never
// re-run.
var scanModelCallsMigrationCols = []struct{ name, ddl string }{
	{"cached_input_tokens", "cached_input_tokens BIGINT"},
	{"cache_write_input_tokens", "cache_write_input_tokens BIGINT"},
}

var scanEventsMigrationCols = []struct{ name, ddl string }{}

func migrateScanModelCalls(db *sql.DB) error {
	return migrateColumns(db, "scan_model_calls", scanModelCallsMigrationCols)
}

func migrateScanEvents(db *sql.DB) error {
	return migrateColumns(db, "scan_events", scanEventsMigrationCols)
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// sanitizeKillRate converts a NaN-valued *float64 to nil before it ever
// reaches DuckDB. DuckDB sorts NaN as larger than any other DOUBLE value —
// measured against this exact driver, MAX(kill_rate) over a table
// containing NaN returns NaN (displacing a real 0.9), `kill_rate > 0.5`
// MATCHES the NaN row, and `ORDER BY kill_rate DESC LIMIT 1` surfaces the
// never-measured row FIRST. A caller that stores math.NaN() directly (e.g.
// reposcan.RepoReport.KillRate, which is deliberately NaN when Audited ==
// 0) would make the never-measured scan look like the ledger's
// BEST-scoring one — the exact inversion "no measurement was made" was
// supposed to prevent. This is the last line of defense: it runs
// regardless of whether the caller already converted NaN to nil itself.
// math.IsNaN is the check, not some proxy (e.g. Audited == 0) that could
// drift from the actual value stored.
func sanitizeKillRate(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) {
		return nil
	}
	return v
}

// fileKillRate is sanitizeKillRate plus the one row-level rule the value
// alone cannot express: an UNCOVERED file was never graded by anything — the
// selection evidence found no test that executes it — so whatever number the
// verdict carries is not a measurement of the suite's strength here. It is
// written NULL, exactly like a rejected file's, rather than a 0.0 that reads
// as "your tests caught nothing" about a question nobody asked. Enforced in
// the store, not left to each caller, because the caller getting it wrong is
// how a false accusation gets persisted.
// nullableString binds SQL NULL for an empty string rather than an empty
// VARCHAR. It matters for mutants_from specifically: NULL is the honest value
// for "this run generated its own mutants", and ” would be a set identifier
// that names nothing — indistinguishable, in a later query, from a recorded
// set whose hash was lost.
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullIfEmptyString binds SQL NULL for an empty string. "" and "this ledger
// does not say" are different answers, and an empty string is a VALUE: a
// query filtering `killed_by <> ”` and one filtering `killed_by IS NOT NULL`
// must agree, and they only can if nothing ever writes the empty string.
func nullIfEmptyString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullablePositive binds SQL NULL for a count that is only ever positive
// when something actually recorded it. A stored 0 is a NUMBER — a later
// query averages it, compares it and ranks on it — where NULL is the only
// encoding of "this ledger does not say".
func nullablePositive(v int) any {
	if v < 1 {
		return nil
	}
	return v
}

// nullableTrees binds SQL NULL for a concurrency that was never recorded.
// Trees < 1 is the one "not recorded" state (see advpool.Concurrency): the
// jail substrate builds no trees, a rejected file was never scored, and a
// verdict served from a pre-concurrency cache row carries none. A stored 0
// is a NUMBER — a later query would average it, compare it and rank on it —
// where NULL is the only encoding of "this ledger does not say". Same rule
// as mutants_from above, for the same reason.
func nullableTrees(v int) any { return nullablePositive(v) }

// nullMillis turns a nullable BIGINT column into the *int64 the File struct
// carries. It is the READ half of the rule the write half enforces by taking
// a pointer: a duration nothing measured is NULL in the column and nil in
// the struct, all the way out to the caller. Copying into a fresh variable
// (rather than taking &v.Int64) matters because v is reused by the row loop.
func nullMillis(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	ms := v.Int64
	return &ms
}

// nullCount is nullMillis for an INTEGER column read back as *int — the
// per-mutant test-count spread, where an absent measurement must stay absent
// rather than becoming a 0 that reads as "this mutant ran no tests".
func nullCount(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

// nullableIntPtr is the WRITE half of nullCount's read: a nil *int binds SQL
// NULL; a non-nil pointer binds the measured value. Shared by every nullable
// *int column this store writes (ModelCall.Retries, File.CoveringTests, ...)
// rather than each column inlining its own `any(...)` — the one place a
// column's nil-ness is decided cannot silently drift from the read side's
// nullCount that way. Named for the WRITE, not for any one caller: renamed
// from retriesParam, which named the first column to need it rather than
// the shape it converts — the trap DRY exists to catch, since a name that
// names its first caller invites a second caller to write a near-duplicate
// rather than reuse it.
func nullableIntPtr(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullableBoolPtr is nullableIntPtr's boolean twin: a nil *bool binds SQL
// NULL (a row written before File.ImportOnly existed, or one this write
// path did not set); a non-nil pointer binds the known true/false value.
// Shared by every nullable *bool column this store writes, for the same
// reason nullableIntPtr is: the one place a column's nil-ness is decided
// cannot silently drift from the read side's nullBoolPtr.
func nullableBoolPtr(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

// cachedTokensParam is nullableIntPtr for the cached-prompt count: NULL when
// nothing measured one, never a stored 0 a later query would average.
func cachedTokensParam(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func fileKillRate(f File) *float64 {
	if f.Uncovered {
		return nil
	}
	return sanitizeKillRate(f.KillRate)
}

// Record writes scan's header row and every file's disposition in one
// transaction — a half-written scan is worse than none, because a later
// report would present it as complete — and returns the assigned scan id.
func (s *Store) Record(ctx context.Context, scan Scan, files []File) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("scanstore: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO scans (
		id, ts, owner, repo, commit, substrate, engine_version, model_set,
		top, all_candidates, diff_base, total_files, candidates, audited,
		kill_rate, cache_hits, preflight_ran, preflight_note, started_at, finished_at,
		corral_version, host, cores, trees_requested,
		total_ms, input_tokens, output_tokens, model_calls,
		source_pushed, statement_sha256, selection_ms, selection_reused_from,
		rekor_log_index, rekor_uuid
	) VALUES (nextval('scans_id'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	RETURNING id`,
		time.Now().UTC(), scan.Owner, scan.Repo, scan.Commit, scan.Substrate, scan.EngineVersion, scan.ModelSet,
		scan.Top, scan.AllCandidates, scan.DiffBase, scan.TotalFiles, scan.Candidates, scan.Audited,
		sanitizeKillRate(scan.KillRate), scan.CacheHits, scan.PreflightRan, scan.PreflightNote, scan.StartedAt, scan.FinishedAt,
		scan.CorralVersion, scan.Host, scan.Cores, nullableTrees(scan.TreesRequested),
		scan.TotalMillis, scan.InputTokens, scan.OutputTokens, scan.ModelCalls,
		scan.SourcePushed, scan.StatementSHA256, scan.SelectionMillis, scan.SelectionReusedFrom,
		scan.RekorLogIndex, scan.RekorUUID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("scanstore: insert scan header: %w", err)
	}

	for _, f := range files {
		// KillRate stays *float64 all the way to the placeholder: passing a
		// nil *float64 as a driver arg binds SQL NULL, never 0.0. This is
		// the property TestRecordRoundTripsEveryDisposition pins for
		// rejected files.
		if _, err := tx.ExecContext(ctx, `INSERT INTO scan_files (
			scan_id, path, lang, disposition, reason,
			kill_rate, survivors, gradable, preflight_state, evidence, detail, timed_out, test_writer_failed, proven_missed, pool_test_unsound,
			proven_mutant_ids, authored_test, cache_key, verdict_json, computed_at,
			models_by_role, mutants_total, regions_total, regions_probed, dropped_regions, vacuous_findings, status,
			authored_test_not_collected, baseline_failed, cache_hit, reused_from_scan_id, suite_baseline_ms,
			test_selection, selected_tests, suite_tests, selection_fallback, uncovered, mutants_from,
			trees, concurrency_note, shared_dirs,
			parent_sha256, mutants_graded, mutants_invalid, mutants_timed_out,
			selection_ms, generation_ms, pool_ms, dev_pass_ms, authored_pass_ms, critic_ms, total_ms,
			mutant_ms_median, mutant_ms_max,
			challenger_jaccard, challenger_kappa, challenger_sufficient, goals_derived, goal_reused,
			per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max,
			writer_mode, prompt_shape, covering_tests, import_only,
			mutant_budget, mutant_budget_rule, complexity,
			symbols, symbols_probed, decisions, decisions_probed,
			challenger_mutants, challenger_survived_writer, challenger_survived_shadow, challenger_union, challenger_shared
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.Path, f.Lang, f.Disposition, f.Reason,
			fileKillRate(f), f.Survivors, f.Gradable, f.PreflightState, f.Evidence, f.Detail, f.TimedOut, f.TestWriterFailed, f.ProvenMissed, f.PoolTestUnsound,
			f.ProvenMutantIDs, f.AuthoredTest, f.CacheKey, f.VerdictJSON, f.ComputedAt,
			f.ModelsByRole, f.MutantsTotal, f.RegionsTotal, f.RegionsProbed, f.DroppedRegions, f.VacuousFindings, f.Status,
			f.AuthoredTestNotCollected, f.BaselineFailed, f.CacheHit, f.ReusedFromScanID, f.SuiteBaselineMillis,
			f.TestSelection, f.SelectedTests, f.SuiteTests, f.SelectionFallback, f.Uncovered, nullableString(f.MutantsFrom),
			nullableTrees(f.Trees), nullableString(f.ConcurrencyNote), nullableString(f.SharedDirs),
			f.ParentSHA256, f.MutantsGraded, f.MutantsInvalid, f.MutantsTimedOut,
			f.SelectionMillis, f.GenerationMillis, f.PoolMillis, f.DevPassMillis, f.AuthoredPassMillis, f.CriticMillis, f.TotalMillis,
			f.MutantMillisMedian, f.MutantMillisMax,
			f.ChallengerJaccard, f.ChallengerKappa, f.ChallengerSufficient, f.GoalsDerived, f.GoalReused,
			f.PerMutant, f.TestsPerMutantMin, f.TestsPerMutantMedian, f.TestsPerMutantMax,
			nullableString(f.WriterMode), nullableString(f.PromptShape), nullableIntPtr(f.CoveringTests), nullableBoolPtr(f.ImportOnly),
			nullableIntPtr(f.MutantBudget), nullableString(f.MutantBudgetRule), nullableIntPtr(f.Complexity),
			nullableIntPtr(f.Symbols), nullableIntPtr(f.SymbolsProbed), nullableIntPtr(f.Decisions), nullableIntPtr(f.DecisionsProbed),
			nullableIntPtr(f.ChallengerMutants), nullableIntPtr(f.ChallengerSurvivedWriter), nullableIntPtr(f.ChallengerSurvivedShadow), nullableIntPtr(f.ChallengerUnion), nullableIntPtr(f.ChallengerShared),
		); err != nil {
			return 0, fmt.Errorf("scanstore: insert scan_files row for %q: %w", f.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("scanstore: commit: %w", err)
	}
	return id, nil
}

// FilesForScan returns every scan_files row recorded for scanID, in
// insertion order — enforced with `ORDER BY rowid`, not left to whatever
// order DuckDB happens to return. DuckDB's rowid pseudocolumn tracks
// physical insertion order for a table this package only ever INSERTs
// into within one transaction per Record call (never UPDATEs or DELETEs a
// scan_files row), which is exactly this table's access pattern — an
// ORDER BY that relied on rowid surviving updates/deletes would not be
// safe here, but that case never arises. Confirmed against this exact
// driver (github.com/marcboeker/go-duckdb/v2): three sequential inserts
// read back via `SELECT rowid, ...` in insertion order (0, 1, 2).
// ScanRow is one `scans` header row as READ BACK — Scan plus the id the store
// assigned it, which a caller needs to then ask FilesForScan about.
type ScanRow struct {
	ID int64
	TS time.Time
	Scan
}

// Scans returns the most recent scan headers, newest first, capped at limit
// (<= 0 means a default of 20).
//
// This is the read side of a ledger that was, in practice, WRITE-ONLY: the
// store recorded every scan and every per-file disposition, and nothing could
// get them back out without a duckdb CLI (not installed on the production
// host) or a hand-written Go program. That is a cost with no product surface —
// and it bit exactly when it mattered, the day the ledger first held evidence
// worth reading. See `corral scans` (cmd/corral/scans.go) for the operator
// surface over this.
func (s *Store) Scans(ctx context.Context, limit int) ([]ScanRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+scanHeaderCols+`
		FROM scans ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("scanstore: list scans: %w", err)
	}
	defer rows.Close()

	var out []ScanRow
	for rows.Next() {
		r, err := scanScanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: iterate scans: %w", err)
	}
	return out, nil
}

// scanHeaderCols is the scans SELECT list, spelled ONCE. Two readers of this
// table (Scans and ScanByID) with two hand-maintained column lists is two
// chances for a column added to one to be silently absent from the other —
// and a reader that silently returns the zero value for a real stored number
// is the exact failure this ledger's nullable columns exist to prevent.
const scanHeaderCols = `id, ts, owner, repo, commit,
		substrate, engine_version, model_set, top, all_candidates, diff_base,
		total_files, candidates, audited, kill_rate, cache_hits,
		preflight_ran, preflight_note, started_at, finished_at,
		corral_version, host, cores, trees_requested,
		total_ms, input_tokens, output_tokens, model_calls,
		source_pushed, statement_sha256, selection_ms, selection_reused_from,
		rekor_log_index, rekor_uuid`

// scanScanRow decodes one scans row, in scanHeaderCols' order. Shared by both
// readers for the same reason the column list is.
func scanScanRow(rows *sql.Rows) (ScanRow, error) {
	var r ScanRow
	var ts, started, finished sql.NullTime
	var diffBase, preflightNote, modelSet, engineVersion, substrate sql.NullString
	// kill_rate is deliberately scanned into *float64: a scan that audited
	// nothing stored NULL rather than 0.0 (see Scan.KillRate's doc for the
	// DuckDB NaN-ordering trap that forced this), and it must read back as
	// "no measurement", never as a terrible score.
	// The scan-grain columns added at schema_version 2 all read back
	// nullable: a header written by an earlier corral has none of them,
	// trees_requested is stored NULL on the jail substrate, which builds no
	// trees at all, and selection_ms is NULL for a scan that instrumented
	// nothing.
	var corralVersion, host, statementSHA, rekorUUID sql.NullString
	var cores, treesRequested, totalMS, inputTokens, outputTokens, modelCalls sql.NullInt64
	var selectionMS, selectionReusedFrom, rekorLogIndex sql.NullInt64
	var sourcePushed sql.NullBool
	if err := rows.Scan(&r.ID, &ts, &r.Owner, &r.Repo, &r.Commit,
		&substrate, &engineVersion, &modelSet, &r.Top, &r.AllCandidates, &diffBase,
		&r.TotalFiles, &r.Candidates, &r.Audited, &r.KillRate, &r.CacheHits,
		&r.PreflightRan, &preflightNote, &started, &finished,
		&corralVersion, &host, &cores, &treesRequested,
		&totalMS, &inputTokens, &outputTokens, &modelCalls,
		&sourcePushed, &statementSHA, &selectionMS, &selectionReusedFrom,
		&rekorLogIndex, &rekorUUID); err != nil {
		return ScanRow{}, fmt.Errorf("scanstore: scan scans row: %w", err)
	}
	r.TS, r.StartedAt, r.FinishedAt = ts.Time, started.Time, finished.Time
	r.Substrate, r.EngineVersion, r.ModelSet = substrate.String, engineVersion.String, modelSet.String
	r.DiffBase, r.PreflightNote = diffBase.String, preflightNote.String
	r.CorralVersion, r.Host, r.StatementSHA256 = corralVersion.String, host.String, statementSHA.String
	r.Cores, r.TreesRequested = int(cores.Int64), int(treesRequested.Int64)
	r.TotalMillis, r.InputTokens = totalMS.Int64, inputTokens.Int64
	r.OutputTokens, r.ModelCalls = outputTokens.Int64, modelCalls.Int64
	r.SourcePushed = sourcePushed.Bool
	// A pointer, so "this scan ran no selection pass" survives the read as
	// nil rather than becoming a 0 nobody measured.
	if selectionMS.Valid {
		v := selectionMS.Int64
		r.SelectionMillis = &v
	}
	// Same discipline: "reused from scan N" must read back as a real id or
	// not at all, never a fabricated 0 that would misname scan zero as the
	// source.
	if selectionReusedFrom.Valid {
		v := selectionReusedFrom.Int64
		r.SelectionReusedFrom = &v
	}
	// Same discipline again: log index 0 is a real position (a log's very
	// first entry), so it must read back as a real pointer or not at all,
	// never a fabricated 0 standing in for "never uploaded".
	if rekorLogIndex.Valid {
		v := rekorLogIndex.Int64
		r.RekorLogIndex = &v
	}
	r.RekorUUID = rekorUUID.String
	return r, nil
}

// ScanByID returns ONE scan header. ok is false when no scan has that id,
// which is an ANSWER (the operator typed a number from a different ledger),
// not an error.
//
// It exists because the scan grain now carries facts a per-file readout has
// to name — selection_ms above all, the one phase that happens once for the
// whole scan — and reaching them through Scans(limit) would mean guessing a
// limit large enough to contain the row, which silently reports "no such
// scan" for anything older than the guess.
func (s *Store) ScanByID(ctx context.Context, id int64) (ScanRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+scanHeaderCols+`
		FROM scans WHERE id = ?`, id)
	if err != nil {
		return ScanRow{}, false, fmt.Errorf("scanstore: scan %d: %w", id, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return ScanRow{}, false, fmt.Errorf("scanstore: scan %d: %w", id, rerr)
		}
		return ScanRow{}, false, nil
	}
	r, err := scanScanRow(rows)
	if err != nil {
		return ScanRow{}, false, err
	}
	return r, true, nil
}

func (s *Store) FilesForScan(ctx context.Context, scanID int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, lang, disposition, reason,
		kill_rate, survivors, gradable, preflight_state, evidence, detail, timed_out, test_writer_failed, proven_missed, pool_test_unsound,
		proven_mutant_ids, authored_test,
		models_by_role, mutants_total, regions_total, regions_probed, dropped_regions, vacuous_findings, status,
		authored_test_not_collected, baseline_failed, cache_hit, reused_from_scan_id,
		suite_baseline_ms,
		test_selection, selected_tests, suite_tests, selection_fallback, uncovered, mutants_from,
		trees, concurrency_note, shared_dirs,
		parent_sha256, mutants_graded, mutants_invalid, mutants_timed_out,
		selection_ms, generation_ms, pool_ms, dev_pass_ms, authored_pass_ms, critic_ms, total_ms,
		mutant_ms_median, mutant_ms_max,
		challenger_jaccard, challenger_kappa, challenger_sufficient, goals_derived, goal_reused,
		per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max,
		writer_mode, prompt_shape, covering_tests, import_only,
		mutant_budget, mutant_budget_rule, complexity,
		symbols, symbols_probed, decisions, decisions_probed,
		challenger_mutants, challenger_survived_writer, challenger_survived_shadow, challenger_union, challenger_shared
		FROM scan_files WHERE scan_id = ? ORDER BY rowid`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: files for scan %d: %w", scanID, err)
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		var detail sql.NullString
		var timedOut, testWriterFailed, poolTestUnsound sql.NullBool
		var provenMissed sql.NullInt64
		var provenIDs, authoredTest sql.NullString
		// The eleven verdict columns all read back nullable: a row written
		// before this migration ran will not have them, and a rejected file
		// was never scored, so NULL is the honest value for its counts too.
		var writerMode sql.NullString
		var modelsByRole, droppedRegions, status sql.NullString
		var mutantsTotal, regionsTotal, regionsProbed, vacuousFindings sql.NullInt64
		var authoredTestNotCollected, baselineFailed, cacheHit sql.NullBool
		var reusedFromScanID sql.NullInt64
		// suite_baseline_ms reads back nullable for the same reason the
		// columns above do (pre-migration rows, and rejected files that were
		// never scored). It is read back at all because capacity planning is
		// meant to be a QUERY over this ledger — and this reader IS the query
		// surface: DuckDB is single-process on a file, so ad-hoc CLI SQL is
		// not a reliable fallback while a scan holds the handle open.
		var suiteBaselineMS sql.NullInt64
		// The five selection columns read back nullable for the same reason:
		// a row written before this migration ran has none of them, and a
		// rejected file never had a grading mode at all. NULL here means
		// "this ledger does not say" — never "graded by the whole suite",
		// which is a positive claim a pre-change row cannot make.
		var testSelection, selectionFallback sql.NullString
		var selectedTests, suiteTests sql.NullInt64
		var uncovered sql.NullBool
		// mutants_from is NULL on every row that generated its own mutants —
		// which is every row written before --mutants existed, and every
		// ordinary run since. Empty means "this row's exam was generated by
		// this run", never "the set is unknown".
		var mutantsFrom sql.NullString
		// trees/concurrency_note read back nullable for the same reason:
		// a row written before this migration ran has neither, and a
		// rejected file was never scored at all. NULL here means "this
		// ledger does not say" — never a claimed "1 tree", which a
		// pre-change row cannot assert.
		var trees sql.NullInt64
		var concurrencyNote, sharedDirs sql.NullString
		// The schema_version 2 grain columns. Every millisecond column is
		// read into a NullInt64 and copied out as a POINTER, never as an
		// int64: NULL means nothing timed that phase, and a 0 would be
		// averaged into the cost model as a phase that costs nothing. Same
		// rule for the two challenger coefficients and the per-mutant spread.
		var parentSHA sql.NullString
		var mutantsGraded, mutantsInvalid, mutantsTimedOut, goalsDerived sql.NullInt64
		var selectionMS, generationMS, poolMS, devPassMS, authoredPassMS, criticMS, totalMS sql.NullInt64
		var mutantMSMedian, mutantMSMax sql.NullInt64
		var challengerJaccard, challengerKappa sql.NullFloat64
		var challengerSufficient, perMutant, goalReused sql.NullBool
		var tpmMin, tpmMedian, tpmMax sql.NullInt64
		var promptShape sql.NullString
		// covering_tests reads back nullable for the same reason every other
		// evidence-shaped column here does: a row from a scan where the
		// evidence never measured this file (no evidence run, or a
		// pairing-only fallback) has none, and that is a different fact from
		// a measured zero.
		var coveringTests sql.NullInt64
		// import_only reads back nullable for a row written before this
		// column existed — see File.ImportOnly's own doc for why NULL is
		// distinct from a known false.
		var importOnly sql.NullBool
		var mutantBudget, complexity sql.NullInt64
		var mutantBudgetRule sql.NullString
		var symbols, symbolsProbed, decisions, decisionsProbed sql.NullInt64
		var chMutants, chSurvW, chSurvS, chUnion, chShared sql.NullInt64
		if err := rows.Scan(&f.Path, &f.Lang, &f.Disposition, &f.Reason,
			&f.KillRate, &f.Survivors, &f.Gradable, &f.PreflightState, &f.Evidence, &detail, &timedOut, &testWriterFailed, &provenMissed, &poolTestUnsound,
			&provenIDs, &authoredTest,
			&modelsByRole, &mutantsTotal, &regionsTotal, &regionsProbed, &droppedRegions, &vacuousFindings, &status,
			&authoredTestNotCollected, &baselineFailed, &cacheHit, &reusedFromScanID,
			&suiteBaselineMS,
			&testSelection, &selectedTests, &suiteTests, &selectionFallback, &uncovered, &mutantsFrom,
			&trees, &concurrencyNote, &sharedDirs,
			&parentSHA, &mutantsGraded, &mutantsInvalid, &mutantsTimedOut,
			&selectionMS, &generationMS, &poolMS, &devPassMS, &authoredPassMS, &criticMS, &totalMS,
			&mutantMSMedian, &mutantMSMax,
			&challengerJaccard, &challengerKappa, &challengerSufficient, &goalsDerived, &goalReused,
			&perMutant, &tpmMin, &tpmMedian, &tpmMax, &writerMode, &promptShape, &coveringTests, &importOnly,
			&mutantBudget, &mutantBudgetRule, &complexity,
			&symbols, &symbolsProbed, &decisions, &decisionsProbed,
			&chMutants, &chSurvW, &chSurvS, &chUnion, &chShared); err != nil {
			return nil, fmt.Errorf("scanstore: scan scan_files row: %w", err)
		}
		f.Detail = detail.String
		f.TimedOut = timedOut.Bool
		f.TestWriterFailed = testWriterFailed.Bool
		f.PoolTestUnsound = poolTestUnsound.Bool
		// NULL (a row written before this column existed, or a rejected file
		// that was never scored) reads back as 0 — the same "nothing to
		// report" value a fresh audited row with no proven gap would have,
		// which is fine: Survivors/TestWriterFailed already carry the
		// distinction a caller needs (see File.ProvenMissed's doc).
		f.ProvenMissed = int(provenMissed.Int64)
		// NULL for a row written before these columns existed, and for every
		// path that never authored a grading test. Empty is the honest value:
		// "no evidence recorded", never a fabricated attempt.
		f.ProvenMutantIDs = provenIDs.String
		f.AuthoredTest = authoredTest.String
		f.ModelsByRole = modelsByRole.String
		f.MutantsTotal = int(mutantsTotal.Int64)
		f.RegionsTotal = int(regionsTotal.Int64)
		f.RegionsProbed = int(regionsProbed.Int64)
		f.DroppedRegions = droppedRegions.String
		f.VacuousFindings = int(vacuousFindings.Int64)
		f.Status = status.String
		f.AuthoredTestNotCollected = authoredTestNotCollected.Bool
		f.BaselineFailed = baselineFailed.Bool
		f.CacheHit = cacheHit.Bool
		f.SuiteBaselineMillis = suiteBaselineMS.Int64
		f.TestSelection = testSelection.String
		f.SelectedTests = int(selectedTests.Int64)
		f.SuiteTests = int(suiteTests.Int64)
		f.SelectionFallback = selectionFallback.String
		f.WriterMode = writerMode.String
		f.Uncovered = uncovered.Bool
		f.Trees = int(trees.Int64)
		f.ConcurrencyNote = concurrencyNote.String
		f.SharedDirs = sharedDirs.String
		f.MutantsFrom = mutantsFrom.String
		// ReusedFromScanID stays *int64: NULL (never reused, or a
		// pre-migration row) must read back as nil, not a scan id of 0 — see
		// the field's own doc.
		if reusedFromScanID.Valid {
			v := reusedFromScanID.Int64
			f.ReusedFromScanID = &v
		}
		f.CoveringTests = nullCount(coveringTests)
		if importOnly.Valid {
			v := importOnly.Bool
			f.ImportOnly = &v
		}
		f.ParentSHA256 = parentSHA.String
		f.MutantsGraded = int(mutantsGraded.Int64)
		f.MutantsInvalid = int(mutantsInvalid.Int64)
		f.MutantsTimedOut = nullCount(mutantsTimedOut)
		f.GoalsDerived = int(goalsDerived.Int64)
		f.SelectionMillis = nullMillis(selectionMS)
		f.GenerationMillis = nullMillis(generationMS)
		f.PoolMillis = nullMillis(poolMS)
		f.DevPassMillis = nullMillis(devPassMS)
		f.AuthoredPassMillis = nullMillis(authoredPassMS)
		f.CriticMillis = nullMillis(criticMS)
		f.TotalMillis = nullMillis(totalMS)
		f.MutantMillisMedian = nullMillis(mutantMSMedian)
		f.MutantMillisMax = nullMillis(mutantMSMax)
		if challengerJaccard.Valid {
			v := challengerJaccard.Float64
			f.ChallengerJaccard = &v
		}
		if challengerKappa.Valid {
			v := challengerKappa.Float64
			f.ChallengerKappa = &v
		}
		if challengerSufficient.Valid {
			v := challengerSufficient.Bool
			f.ChallengerSufficient = &v
		}
		if goalReused.Valid {
			v := goalReused.Bool
			f.GoalReused = &v
		}
		f.PerMutant = perMutant.Bool
		f.TestsPerMutantMin = nullCount(tpmMin)
		f.TestsPerMutantMedian = nullCount(tpmMedian)
		f.TestsPerMutantMax = nullCount(tpmMax)
		f.PromptShape = promptShape.String
		f.MutantBudget = nullCount(mutantBudget)
		f.MutantBudgetRule = mutantBudgetRule.String
		f.Complexity = nullCount(complexity)
		f.Symbols, f.SymbolsProbed = nullCount(symbols), nullCount(symbolsProbed)
		f.Decisions, f.DecisionsProbed = nullCount(decisions), nullCount(decisionsProbed)
		f.ChallengerMutants, f.ChallengerSurvivedWriter, f.ChallengerSurvivedShadow = nullCount(chMutants), nullCount(chSurvW), nullCount(chSurvS)
		f.ChallengerUnion, f.ChallengerShared = nullCount(chUnion), nullCount(chShared)
		out = append(out, f)
	}
	return out, rows.Err()
}

// VerdictByCacheKey returns the most recently recorded verdict for (owner,
// cacheKey), if any.
//
// Owner scoping is in the WHERE clause, not applied in Go after fetching:
// this is the query that keeps one tenant's verdict from satisfying another
// tenant's audit, and a filter that runs after the rows are already in memory
// is one refactor away from being dropped.
//
// An empty owner is an ERROR, never a wildcard. The alternative is a shared
// bucket that every tenant with an unset owner reads and writes.
//
// Rows with no verdict JSON are skipped rather than returned as an empty hit:
// a row recorded before this column existed has nothing to reuse, and serving
// "" as a verdict would be worse than missing.
//
// "Most recent" orders by (s.ts, s.id), not s.ts alone. s.ts is a TIMESTAMP
// set from time.Now().UTC() at Record time, so two scans recorded within the
// same tick (reachable on a fast machine, or under batched recording) would
// tie on ts alone — and picking between two rows that could hold different
// verdicts for the same content address on an arbitrary tiebreak is exactly
// the ambiguity the package's fail-closed rule forbids. s.id is allocated
// from the scans_id DuckDB SEQUENCE (see Open's comment on it), so it is
// unique and monotonic with insertion order; ordering by (ts, id) makes
// "most recent" a TOTAL order, so the tie case cannot arise rather than
// being resolved arbitrarily — cheaper than falling back to a miss on tie,
// which would force a needless full re-audit over what is really just clock
// granularity.
func (s *Store) VerdictByCacheKey(ctx context.Context, owner, cacheKey string) (string, time.Time, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return "", time.Time{}, false, fmt.Errorf("scanstore: VerdictByCacheKey: empty owner")
	}
	if strings.TrimSpace(cacheKey) == "" {
		return "", time.Time{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT f.verdict_json, f.computed_at
		FROM scan_files f JOIN scans s ON s.id = f.scan_id
		WHERE s.owner = ? AND f.cache_key = ?
		  AND f.verdict_json IS NOT NULL AND f.verdict_json <> ''
		ORDER BY s.ts DESC, s.id DESC
		LIMIT 1`, owner, cacheKey)
	var js sql.NullString
	var at sql.NullTime
	switch err := row.Scan(&js, &at); {
	case errors.Is(err, sql.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, fmt.Errorf("scanstore: VerdictByCacheKey: %w", err)
	}
	if !js.Valid || js.String == "" {
		return "", time.Time{}, false, nil
	}
	return js.String, at.Time, true, nil
}

// Mutant is one mutant's fate in one scan.
type Mutant struct {
	ScanID       int64
	Path         string
	MutantID     string
	Outcome      string // "killed" | "survived"
	ParentSHA256 string
	// Proven is true when this survivor was subsequently killed by the pool's
	// AUTHORED test — a gap demonstrated by execution, not merely disclosed.
	// Survived-and-proven and survived-and-unadjudicated are different claims
	// and a leaderboard that conflates them is indefensible.
	Proven bool
	// TestsRun and SelectionRule are what this mutant was actually GRADED
	// by, when the run graded each mutant with the tests that reach its own
	// lines (advpool.MutantRef.TestsRun/.Rule). A survivor that faced 3
	// tests and one that faced 41 are different claims about the suite, and
	// a file-grain kill rate averages the difference away — so the record
	// carries it at the grain the grading happened. SelectionRule is a
	// lang.SpanRule* value ("lines" | "static" | "unreached" | "file"): a
	// scan that is mostly "static" or "unreached" narrowed almost nothing,
	// and the count of rules is what says so. Both are zero on a run that
	// graded every mutant with the file's one shared command.
	//
	// They describe the DEV pass — the exam this mutant's outcome above came
	// from. Proven describes the AUTHORED pass, which runs the pool's own
	// test and is never narrowed per mutant: a row reading tests_run 3 and
	// proven true means three tests failed to kill it and the authored test
	// then did, not that the authored test ran those three.
	TestsRun      int
	SelectionRule string
	// DurationMillis is how long grading THIS mutant took. *int64 because a
	// run that did not time its mutants must read back as "unknown", not as
	// a mutant that took no time: the dev pass is where the minutes go (35
	// of 43 on one reference file), and the whole point of this column is to
	// let a query say which mutants ate them.
	DurationMillis *int64
	// KilledBy is the first failing test id, best effort, when the language
	// plugin can parse one out of the runner's own output. "" when it
	// cannot — NEVER inferred: a wrong test id here would send a reader to
	// read the wrong test, which is worse than sending them nowhere.
	KilledBy string
	// SpanStart and SpanEnd are the mutated line range in the parent file
	// (advpool's lang.LineRange). They are what turns a mutant id into a
	// place: "which lines survive" is the question a reader actually has,
	// and an opaque id cannot answer it.
	//
	// Produced since 2026-09-04 from advpool.MutantRef.Span; every row
	// written before that stores SQL NULL, and so does a mutant whose
	// generator recorded no span. They are 1-based, so 0 is unambiguously
	// "not recorded" rather than a line.
	SpanStart int
	SpanEnd   int
	// ProvenByAuthoredAlone marks a survivor the pool's AUTHORED test killed
	// where the dev suite's own tests never did — the strict subset of
	// Proven that is a demonstrated gap rather than a demonstrated kill.
	// Distinct from Proven so a leaderboard can count the strong claim
	// without recomputing the difference from two tables.
	ProvenByAuthoredAlone bool
}

// ModelCall is what ONE role's seat cost on ONE file: the money grain. The
// scan header carries the run's totals, and a total cannot answer "which
// seat was slow" — the operator's second question, and the warehouse's first
// GROUP BY.
type ModelCall struct {
	ScanID int64
	Path   string
	Role   string
	Model  string
	Calls  int
	// Retries are calls that had to be made AGAIN — a compile-gate rejection,
	// a malformed response. They are counted separately from Calls because a
	// seat that needs four attempts per mutant is a different (and more
	// expensive) failure from one that needs four mutants.
	//
	// NULLABLE, not 0-when-unknown: nothing in agentbackend has a retry loop
	// to observe today (checked before this column was ever written to), so
	// every row this codebase produces has retries UNMEASURED. A stored 0
	// is a NUMBER a later query averages and ranks on; NULL is the only
	// honest encoding of "this ledger does not say" — same rule as
	// nullablePositive/nullCount elsewhere in this file.
	Retries     *int
	InputTokens int64
	// CachedInputTokens is how many of InputTokens the provider served from
	// its own prompt cache. NULLABLE for the same reason Retries is, but for
	// a different cause: a provider that says nothing about caching has
	// reported nothing, not a miss, and a stored 0 is a number a later query
	// would average as a measured zero. See
	// agentbackend.Usage.CachedInputTokens.
	CachedInputTokens *int64
	// CacheWriteInputTokens is what filling that cache cost — billed at 1.25x
	// an ordinary input token, so the saving above has a price and both
	// belong in the same row. Nullable for the same reason, and independently
	// of the read count.
	CacheWriteInputTokens *int64
	OutputTokens          int64
	WallMillis            int64
}

// Event is one entry on the tape: an ordered log of what the pool did, at
// the grain a phase boundary happens. Everything else in this ledger is a
// SUMMARY, and a summary cannot answer "what was it doing for those 35
// minutes".
type Event struct {
	ScanID int64
	Path   string
	// Seq, not TS, is the ordering key. Two events inside one millisecond are
	// ordinary, and a tape whose order depends on clock granularity is not a
	// tape.
	Seq     int64
	TS      time.Time
	Kind    string
	Actor   string
	Subject string
	Model   string
	// DurationMillis is set on the events that HAVE a duration (a completed
	// phase, a returned model call) and nil on the ones that are a moment
	// (a phase start). Never 0 for the latter — see nullMillis.
	DurationMillis *int64
	// Detail is JSON TEXT, in a VARCHAR column. See the scan_events CREATE
	// TABLE in Open for why the column is not DuckDB's JSON type.
	Detail string
}

// RecordMutants appends mutant rows. An empty slice is a no-op, not an error:
// a file whose baseline failed produced no mutants, and that is a normal
// outcome the caller should not have to special-case.
func (s *Store) RecordMutants(ctx context.Context, ms []Mutant) error {
	if len(ms) == 0 {
		return nil
	}
	for _, m := range ms {
		if m.Outcome != "killed" && m.Outcome != "survived" {
			return fmt.Errorf("scanstore: RecordMutants: %s/%s: unknown outcome %q", m.Path, m.MutantID, m.Outcome)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scanstore: RecordMutants: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range ms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scan_mutants (scan_id, path, mutant_id, outcome, parent_sha256, proven, tests_run, selection_rule,
				duration_ms, killed_by, span_start, span_end, proven_by_authored_alone)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ScanID, m.Path, m.MutantID, m.Outcome, m.ParentSHA256, m.Proven, m.TestsRun, m.SelectionRule,
			// Line numbers are 1-BASED, so 0 is the one "not recorded" state
			// — and today it is the only state: advpool.MutantRef carries no
			// span, so nothing produces one. Line 0 does not exist, and a
			// reader jumping to it would be sent to the top of the file.
			// killed_by is NULL, never '': a mutant killed by a run whose
			// output nothing could parse — or by a TIMEOUT, where no test
			// reported anything at all — has no killer to name, and an empty
			// string is a VALUE a query counts as "we know who caught it".
			// Both producers' comments already said NULL; only the bind did
			// not.
			m.DurationMillis, nullIfEmptyString(m.KilledBy), nullablePositive(m.SpanStart), nullablePositive(m.SpanEnd), m.ProvenByAuthoredAlone,
		); err != nil {
			return fmt.Errorf("scanstore: RecordMutants: insert %s/%s: %w", m.Path, m.MutantID, err)
		}
	}
	return tx.Commit()
}

// MutantsForScan returns every mutant row for a scan, for round-trip tests and
// for the CLI reader.
func (s *Store) MutantsForScan(ctx context.Context, scanID int64) ([]Mutant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scan_id, path, mutant_id, outcome, parent_sha256, proven, tests_run, selection_rule,
			duration_ms, killed_by, span_start, span_end, proven_by_authored_alone
		 FROM scan_mutants WHERE scan_id = ? ORDER BY path, mutant_id`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: MutantsForScan: %w", err)
	}
	defer rows.Close()
	var out []Mutant
	for rows.Next() {
		var m Mutant
		var parent, rule sql.NullString
		// NULL-tolerant on purpose: rows written before these columns
		// existed (and rows from a run that did not grade per mutant) read
		// back as zero rather than as a scan error.
		var testsRun sql.NullInt64
		var durationMS, spanStart, spanEnd sql.NullInt64
		var killedBy sql.NullString
		var provenByAuthoredAlone sql.NullBool
		if err := rows.Scan(&m.ScanID, &m.Path, &m.MutantID, &m.Outcome, &parent, &m.Proven, &testsRun, &rule,
			&durationMS, &killedBy, &spanStart, &spanEnd, &provenByAuthoredAlone); err != nil {
			return nil, fmt.Errorf("scanstore: MutantsForScan: scan row: %w", err)
		}
		m.ParentSHA256 = parent.String
		m.TestsRun = int(testsRun.Int64)
		m.SelectionRule = rule.String
		m.DurationMillis = nullMillis(durationMS)
		m.KilledBy = killedBy.String
		m.SpanStart, m.SpanEnd = int(spanStart.Int64), int(spanEnd.Int64)
		m.ProvenByAuthoredAlone = provenByAuthoredAlone.Bool
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: MutantsForScan: %w", err)
	}
	return out, nil
}

// RecordModelCalls appends per-(file, role) usage rows. An empty slice is a
// no-op, not an error: a scan that reused every verdict from the cache made
// no model calls at all, and that is a normal outcome the caller should not
// have to special-case.
func (s *Store) RecordModelCalls(ctx context.Context, cs []ModelCall) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scanstore: RecordModelCalls: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range cs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scan_model_calls (scan_id, path, role, model, calls, retries, input_tokens, output_tokens, cached_input_tokens, cache_write_input_tokens, wall_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ScanID, c.Path, c.Role, c.Model, c.Calls, nullableIntPtr(c.Retries),
			c.InputTokens, c.OutputTokens,
			cachedTokensParam(c.CachedInputTokens), cachedTokensParam(c.CacheWriteInputTokens), c.WallMillis,
		); err != nil {
			return fmt.Errorf("scanstore: RecordModelCalls: insert %s/%s: %w", c.Path, c.Role, err)
		}
	}
	return tx.Commit()
}

// ModelCallsForScan returns every model-call row for a scan, ordered by
// (path, role) so a reader and a round-trip test see a stable order rather
// than whatever the storage layer happens to return.
func (s *Store) ModelCallsForScan(ctx context.Context, scanID int64) ([]ModelCall, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scan_id, path, role, model, calls, retries, input_tokens, output_tokens, cached_input_tokens, cache_write_input_tokens, wall_ms
		 FROM scan_model_calls WHERE scan_id = ? ORDER BY path, role`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: ModelCallsForScan: %w", err)
	}
	defer rows.Close()
	var out []ModelCall
	for rows.Next() {
		var c ModelCall
		var path, role, model sql.NullString
		var calls, retries, in, outTok, cached, written, wall sql.NullInt64
		if err := rows.Scan(&c.ScanID, &path, &role, &model, &calls, &retries, &in, &outTok, &cached, &written, &wall); err != nil {
			return nil, fmt.Errorf("scanstore: ModelCallsForScan: scan row: %w", err)
		}
		c.Path, c.Role, c.Model = path.String, role.String, model.String
		c.Calls = int(calls.Int64)
		c.Retries = nullCount(retries)
		if cached.Valid {
			v := cached.Int64
			c.CachedInputTokens = &v
		}
		if written.Valid {
			v := written.Int64
			c.CacheWriteInputTokens = &v
		}
		c.InputTokens, c.OutputTokens, c.WallMillis = in.Int64, outTok.Int64, wall.Int64
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: ModelCallsForScan: %w", err)
	}
	return out, nil
}

// RecordEvents appends tape entries. An empty slice is a no-op, for the same
// reason RecordMutants' is: a scan that graded nothing emitted nothing.
func (s *Store) RecordEvents(ctx context.Context, es []Event) error {
	if len(es) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scanstore: RecordEvents: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range es {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scan_events (scan_id, path, seq, ts, kind, actor, subject, model, duration_ms, detail)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ScanID, e.Path, e.Seq, e.TS, e.Kind, e.Actor, e.Subject, e.Model, e.DurationMillis, e.Detail,
		); err != nil {
			return fmt.Errorf("scanstore: RecordEvents: insert %s#%d: %w", e.Path, e.Seq, err)
		}
	}
	return tx.Commit()
}

// EventsForScan returns a scan's tape in SEQ order — see Event.Seq for why
// the sequence, not the timestamp, is what orders it.
func (s *Store) EventsForScan(ctx context.Context, scanID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scan_id, path, seq, ts, kind, actor, subject, model, duration_ms, detail
		 FROM scan_events WHERE scan_id = ? ORDER BY seq, path`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: EventsForScan: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var path, kind, actor, subject, model, detail sql.NullString
		var seq, durationMS sql.NullInt64
		var ts sql.NullTime
		if err := rows.Scan(&e.ScanID, &path, &seq, &ts, &kind, &actor, &subject, &model, &durationMS, &detail); err != nil {
			return nil, fmt.Errorf("scanstore: EventsForScan: scan row: %w", err)
		}
		e.Path, e.Seq, e.TS = path.String, seq.Int64, ts.Time.UTC()
		e.Kind, e.Actor, e.Subject, e.Model = kind.String, actor.String, subject.String, model.String
		e.DurationMillis = nullMillis(durationMS)
		e.Detail = detail.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: EventsForScan: %w", err)
	}
	return out, nil
}

// SetStatementSHA256 stamps the signed statement's hash onto a scan header
// after the fact.
//
// It exists because of an ordering the scan cannot avoid: the statement has
// to NAME the scan id, so it is written after the header row, and the header
// row therefore has nothing to write in this column at INSERT time. Without
// this the local ledger's statement_sha256 was empty on every run that used
// --attest — while the pushed warehouse row carried the hash — which is
// exactly the asymmetry the column was added to remove.
//
// This is the ONE UPDATE this package performs. The ledger is otherwise
// append-only, and that is deliberate; the exception is narrow (one column,
// one row, a value that was unknowable when the row was written) and it
// cannot rewrite a measurement.
func (s *Store) SetStatementSHA256(ctx context.Context, id int64, sha string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE scans SET statement_sha256 = ? WHERE id = ?`, sha, id); err != nil {
		return fmt.Errorf("scanstore: SetStatementSHA256 for scan %d: %w", id, err)
	}
	return nil
}

// SetSourcePushed stamps whether source bytes left the box on one scan,
// mirroring SetStatementSHA256: the push runs after the scan row exists and
// can fail, so Record writes false and the caller stamps true on success.
func (s *Store) SetSourcePushed(ctx context.Context, id int64, pushed bool) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE scans SET source_pushed = ? WHERE id = ?`, pushed, id); err != nil {
		return fmt.Errorf("scanstore: SetSourcePushed for scan %d: %w", id, err)
	}
	return nil
}

// SetRekorReceipt stamps the Rekor upload receipt onto one scan header,
// mirroring SetStatementSHA256: --transparency's upload happens after the
// scan row already exists (it needs the --attest statement, which needs the
// scan id), so the receipt has to be stamped on rather than written at
// Record time. logIndex is a pointer so the caller can never accidentally
// stamp a fabricated 0 — pass a real address or do not call this at all.
func (s *Store) SetRekorReceipt(ctx context.Context, id int64, logIndex *int64, uuid string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE scans SET rekor_log_index = ?, rekor_uuid = ? WHERE id = ?`, logIndex, uuid, id); err != nil {
		return fmt.Errorf("scanstore: SetRekorReceipt for scan %d: %w", id, err)
	}
	return nil
}
