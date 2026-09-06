// SPDX-License-Identifier: Elastic-2.0

package scanstore

import "time"

// The row types below are the in-memory shape `certify --repo` builds for
// one scan before mapping it to the ledger entry (auditpush.Bundle, via
// cmd/corral/certify_repo_bundle.go): the header, one File per walked
// file, one Mutant per mutant graded, one ModelCall per (file, role), one
// Event per driver beat. They were once also the rows of a local DuckDB
// record; that record is gone, and these are kept as the one set of names
// for one set of facts, checked field for field against the bundle's
// (TestBundleIsTheLedgerRowForRow).

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
	// SelectionReused is true when this scan reused a PRIOR scan's
	// instrumented coverage evidence, because its tree, instrumented command,
	// language plugin and substrate were all byte-identical to that scan's —
	// see internal/reposcan.TreeDigest and the selection_cache table. A
	// reused scan and a scan that instrumented nothing both leave
	// SelectionMillis nil, and this is the ONLY column that tells them
	// apart.
	SelectionReused bool
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
	// Retracted is set on a scan a later ledger entry retracted, with the
	// retractor's reason. The scan is still listed — it happened — and is
	// no longer the record: the view, the prior and the verdict cache skip
	// it. A reader must be able to see both facts at once.
	Retracted       bool
	RetractedReason string
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
	// PriorsApplied / PriorDigest: the run was told about this many edits
	// earlier runs tried on the same bytes — a different exam. NULL / ""
	// when no prior was given.
	PriorsApplied *int
	PriorDigest   string
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

// ScanRow is a Scan as a reader hands it back: its id — the entry's
// position in the ledger chain, oldest first — and when it was placed.
type ScanRow struct {
	ID int64
	TS time.Time
	Scan
}

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
	// Shape is the kind of fault (adequacy.ShapeOf, from the hunk) and
	// GeneratorModel the seat that planted it — together the grain that
	// makes "which shapes does this model plant, which does this suite let
	// through" a query. NULL on rows from before these columns.
	Shape          string
	GeneratorModel string
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
