// SPDX-License-Identifier: Elastic-2.0

package ledger

import "time"

// Row is one audited file, as it lands in the warehouse.
type Row struct {
	Repo   string
	Commit string
	Path   string
	// KillRate is a POINTER so an UNCOVERED file writes SQL NULL rather than
	// a 0.0 the report itself refuses to print. A warehouse that stores the
	// fabricated zero is where the withheld number comes back as fact.
	KillRate         *float64
	Survivors        int
	ProvenMissed     int
	TimedOut         bool
	TestWriterFailed bool
	PoolTestUnsound  bool
	// Scope travels on every row, denormalized on purpose: a query that reads
	// one row must be able to see how much of the repo was looked at. "3 files
	// clean" reads very differently out of 4 than out of 400, and a join to
	// find that out is a join people skip.
	Audited    int
	Candidates int
	// Comparability. Without these a cross-project row is unusable: a kill
	// rate on a dense 200-line function and one on a 12-line accessor are not
	// the same measurement, and a reader cannot tell a hard file from a weak
	// suite.
	Lang           string
	MutantsPlanted int
	ModelsByRole   string // JSON, so a new role does not need a migration
	// SeatTools is the roster's agent seats' tool and version, as JSON keyed
	// by role like ModelsByRole (agentbackend.SeatTool); "" when no seat was
	// an agent. A per-model reader needs it beside the roster: an unpinned
	// agent seat names a tool, not a model (agentbackend.RecordedSeatNamesAModel).
	SeatTools       string `json:",omitempty"`
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          *bool      // nil = not recorded; see ScanRow.Passed
	StartedAt       *time.Time // the scan's own start; see ScanRow.StartedAt
	// StatementSHA256 ties this row to the signed in-toto statement the run
	// published. It is what makes the table evidence rather than self-report:
	// any row can be traced to an attestation a third party verifies.
	StatementSHA256 string
	RunURL          string
	// Which measurement KillRate IS — the same five facts the scan ledger
	// records. Without them a cross-repo query averages selection rates and
	// whole-suite rates together, which is two questions in one number.
	TestSelection     string
	SelectedTests     int
	SuiteTests        int
	SelectionFallback string
	// WriterMode is HOW the writer seat attacked this file's survivors:
	// "per-survivor" or "batched". EMPTY reaches the warehouse as NULL, never
	// as either spelling — a run that named no mode, or a row pushed by a
	// corral from before the mode existed, must be excludable from a query
	// that groups by it. The two are not the same measurement.
	WriterMode string
	Uncovered  bool
	// And at which GRAIN it was measured. PerMutant says each mutant was
	// graded by the tests that reach its own lines, which makes SelectedTests
	// the file's UNION and no mutant's denominator — so the spread travels
	// with it, or a cross-repo average of kill_rate silently mixes a rate
	// earned over 3 tests per mutant with one earned over 620.
	//
	// The three columns are written as SQL NULL, not 0, when no spread was
	// measured — an ordinary shared-command run, or a per-mutant run whose
	// every mutant was rejected by the compile gate before anything could be
	// graded. A stored 0-to-0 range is a measurement nobody made, and the
	// whole point of these columns is that a number in this table was
	// measured. nil is that absence, carried by the type rather than by a
	// caller remembering to leave three ints alone.
	PerMutant      bool
	TestsPerMutant *TestsPerMutantSpread
	// Trees and ConcurrencyNote disclose how many private trees scored this
	// file at once, or — when it only got one — why. The same fact the
	// report line and the signed attestation carry, denormalized onto this
	// row so a cross-repo query does not need to reconstruct it. Trees < 1
	// means nothing measured it and is written SQL NULL, never 0.
	Trees           int
	ConcurrencyNote string
	// SharedDirs is the comma-joined list of dependency directories that were
	// symlinked into every tree rather than copied — the one thing the trees
	// did NOT hold privately. SQL NULL, not "", when nothing was shared.
	SharedDirs string
	// ScanID is the local scan ledger's row id this row was pushed alongside
	// (see scanstore.Store.Record), or 0 when the ledger was not written.
	// It is the join key back to scan_files/scan_mutants, and — together
	// with StatementSHA256 — the other half of the link a signed statement
	// makes to this row: the statement names ScanID and the hash of the
	// rows it was written with, and this row names the statement's own
	// hash. Two pointers, not one, so either can be checked against the
	// other.
	ScanID int64

	// ---- Every file, at every disposition (schema_version 2) ----
	//
	// Until this version the warehouse held only the files a scan AUDITED.
	// A table of audited files cannot answer the question an operator
	// actually has — "is this repo covered?" — because the files corral
	// refused are exactly the ones nobody is looking at. Disposition and
	// Reason are what make a rejected file a row rather than an absence.
	Disposition    string // "audited" | "rejected"
	Reason         string // populated when Disposition == "rejected"
	PreflightState string
	Evidence       string // "" | "paired" | "coverage" | "proven"
	Detail         string
	Status         string // advpool's "certified" | "needs-review"
	CacheHit       bool
	// ReusedFromScanID is nil, never 0, when this row was measured fresh: 0
	// would be a foreign key to nothing.
	ReusedFromScanID *int64
	CacheKey         string
	// ParentSHA256 is the sha256 of the audited file's own bytes — the
	// VALIDITY key. A verdict is about bytes, not about a commit, so
	// "is this verdict still current for HEAD?" is
	// `parent_sha256 == sha256(HEAD:path)`: answerable by a reader holding
	// the checkout, and by the corral_seal view, with no re-audit.
	ParentSHA256 string
	// The denominators behind the rate, split by what happened to each
	// mutant. corral_mutants carries all four outcomes at the mutant grain;
	// these are the same facts summarised per file so the common query does
	// not need the join.
	MutantsGraded  int
	MutantsInvalid int
	// MutantsTimedOut is *int because nothing produces it yet: no verdict
	// field counts mutants that hit their deadline. A stored 0 would be the
	// positive claim "none timed out" on every row in the warehouse — a
	// measurement nobody made — so it is written SQL NULL until there is
	// something to write.
	MutantsTimedOut *int
	// MutantsDistinct / MutantsDistinctSurvived are the sample a 95% interval
	// is taken over: distinct measured edits, and how many survived
	// (advpool.Verdict.MutantsDistinct). MutantsGraded counts duplicates, so
	// a band over it is narrower than the evidence. *int because rows pushed
	// before this was recorded have none, and a stored 0 would claim one.
	MutantsDistinct         *int `json:",omitempty"`
	MutantsDistinctSurvived *int `json:",omitempty"`
	RegionsTotal            int
	RegionsProbed           int
	DroppedRegions          string
	VacuousFindings         int
	// CriticIncomplete qualifies VacuousFindings: the critic's review was cut
	// short, so the count is a lower bound (scanstore.File's field).
	CriticIncomplete bool `json:",omitempty"`
	// AuthoredTestNotCollected and BaselineFailed are the two qualifiers
	// that turn a clean-looking number into a meaningless one. They travel
	// with the number, on the row, for the reason this package's doc gives:
	// aggregation is exactly where a qualifier gets dropped.
	AuthoredTestNotCollected bool
	BaselineFailed           bool
	// SuiteBaselineMillis is the compliant suite's own wall clock — the one
	// input to the audit cost model. *int64, and NULL for a file nothing
	// ran: a stored 0 would be averaged in as a suite that takes no time.
	SuiteBaselineMillis *int64
	ProvenMutantIDs     string
	// The challenger seat's agreement with the primary writer. All three are
	// pointers: "the challenger did not run" and "the challenger agreed on
	// nothing" are different claims and 0.0 cannot tell them apart.
	ChallengerJaccard    *float64
	ChallengerKappa      *float64
	ChallengerSufficient *bool
	// The pair's counts — see scanstore.File.ChallengerMutants. NULL when
	// no pair was measured; recorded whether or not the coefficient was.
	ChallengerMutants        *int
	ChallengerSurvivedWriter *int
	ChallengerSurvivedShadow *int
	ChallengerUnion          *int
	ChallengerShared         *int
	// PriorsApplied / PriorDigest mirror scanstore.File's.
	PriorsApplied *int
	PriorDigest   string
	// ComputedAt is when this file's verdict was earned (a reused verdict
	// keeps the time it was first computed, not the time it was served);
	// MutantsFrom is the sha256 of the recorded mutant set a --mutants run
	// replanted, empty for a freshly generated exam. Both mirror
	// scanstore.File's, carried since the entry became the only record.
	ComputedAt   *time.Time
	MutantsFrom  string
	GoalsDerived int
	// GoalReused mirrors scanstore.File.GoalReused: *bool, and true is the
	// only value ever written — "not reused" and "the goal cache was never
	// asked about this file" are different claims a stored false cannot
	// tell apart, so both read back NULL here.
	GoalReused *bool
	// The per-phase clock. NULL until the phase is actually timed — see the
	// same fields on scanstore.File. A 0 here would report a phase that
	// costs nothing into the page whose entire purpose is the cost model.
	SelectionMillis    *int64
	GenerationMillis   *int64
	PoolMillis         *int64
	DevPassMillis      *int64
	AuthoredPassMillis *int64
	CriticMillis       *int64
	TotalMillis        *int64
	MutantMillisMedian *int64
	MutantMillisMax    *int64
	// AuthoredTest and VerdictJSON are SOURCE. They are written only when
	// the bundle says --push-source was given; otherwise the columns are
	// SQL NULL no matter what this struct carries. Enforced by the writer,
	// not by the caller remembering to blank them — see PushBundle.
	AuthoredTest string
	VerdictJSON  string
	// AuthoredLanding is scanstore.File's: the file AuthoredTest must be
	// appended to (a Rust corral_tests block compiles only there). A path,
	// not source, so it is written whether or not source is pushed. Omitted
	// when empty, so every row written before it hashes as it did.
	AuthoredLanding string `json:",omitempty"`
	// BaselineFlaky is scanstore.File's: the tests a passing baseline passed
	// only on a retry, one per line. A measurement, not source, so it is
	// written whether or not source is pushed — the verdict document that
	// also holds it is withheld without --push-source. Omitted when empty, so
	// every row written before it hashes as it did.
	BaselineFlaky string `json:",omitempty"`
	// PromptShape mirrors scanstore.File.PromptShape / advpool.Verdict.PromptShape:
	// "chunk" when every mutant-generator shard on this file's run saw only
	// its own symbols' bodies plus the file's preamble, "file" when even one
	// shard fell back to the whole file (including every unsharded run).
	// "" for a row from before this disclosure existed — never fabricated.
	PromptShape string
	// MutantBudget / MutantBudgetRule / Complexity mirror
	// scanstore.File's: how many faults the seats were asked for, by which
	// rule, and the file's summed complexity. NULL for a row from before
	// these columns or a run that generated nothing — never a fabricated 0.
	MutantBudget     *int
	MutantBudgetRule string
	Complexity       *int
	// Symbols/SymbolsProbed/Decisions/DecisionsProbed mirror
	// scanstore.File's: the exam's reach. NULL when unmeasured.
	Symbols         *int
	SymbolsProbed   *int
	Decisions       *int
	DecisionsProbed *int
	// CoveringTests mirrors scanstore.File.CoveringTests: the number of
	// tests the selection evidence showed execute this file. *int, and NULL
	// for a row from a scan where the evidence never measured this file
	// (no evidence collected, or a pairing-only fallback) — never confused
	// with a measured, genuine zero.
	CoveringTests *int
	// CoveringUnits mirrors scanstore.File.CoveringUnits: the number of test
	// UNITS the unit evidence pass showed execute this file, in its own
	// column so a unit count never redefines CoveringTests. NULL for a row
	// whose evidence was not a unit pass, or that never measured this file.
	// omitempty, so every row without it — every Python row, every row
	// written before it existed — marshals to the bytes it always did. An
	// additive field needs no new ledger format: from corral-ledger-3 an
	// entry's hash is over its bytes as written (EntryHash, Entry.Raw), and
	// ParseEntry's json.Unmarshal ignores a key an older reader does not
	// know (TestAnEntryCarryingCoveringUnitsStillVerifies).
	CoveringUnits *int `json:",omitempty"`
	// ImportOnly mirrors scanstore.File.ImportOnly: refines Uncovered — nil
	// for a row pushed before this column existed, *true when the file was
	// executed at import time and never by a test directly (see
	// reposcan.ReasonImportOnly), *false when Uncovered is a genuine
	// "nothing executed this at all" finding. A reader (corral seal's
	// UncoveredPaths) that prints the word UNCOVERED off Uncovered alone,
	// without checking this FIRST, reintroduces the exact false claim
	// reposcan.ReasonImportOnly exists to correct.
	ImportOnly *bool
}

// Link identifies the ledger scan and signed statement a pushed row belongs
// to — the fields stampLink (internal/auditpush/bundle.go) writes onto every
// row of every table from a single source, so a row and the statement it
// names can never disagree about which run produced them. (It used to name
// cmd/corral's pushAuditRows, which was replaced by the bundle path in the
// same change that made the warehouse hold every disposition rather than only
// the audited files.)
type Link struct {
	// ScanID is written onto Row.ScanID — and onto the scan, mutant,
	// model-call and event rows too. Zero writes nothing: it is the absence
	// of a ledger id, not an id, and the legacy Push path sets the field on
	// its rows directly. See stampLink.
	ScanID int64
	// StatementSHA256 is written onto Row.StatementSHA256.
	StatementSHA256 string
	// Require, when true, refuses to push any row whose StatementSHA256 is
	// empty. The certify --repo path sets this whenever --attest produced a
	// statement, so a row that would otherwise claim traceability actually
	// has it; without --attest there is no statement to point to, Require
	// is false, and a row with statement_sha256 = '' pushes honestly.
	Require bool
}

// TestsPerMutantSpread is how many tests each graded mutant ran: the
// smallest, the middle and the largest. Reached only through a pointer so an
// unmeasured spread is absent rather than three zeros.
type TestsPerMutantSpread struct{ Min, Median, Max int }
