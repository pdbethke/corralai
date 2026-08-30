// SPDX-License-Identifier: Elastic-2.0

// Package auditpush appends a scan's verdict to a DuckDB the OPERATOR owns —
// a local file, or their MotherDuck database.
//
// It is deliberately not a service. corral has no hosted tier and collects no
// telemetry: the key is theirs, the runner is theirs, and the warehouse is
// theirs too. That is also why the target is any DuckDB rather than MotherDuck
// specifically — `md:<db>` is one destination, a path on disk is another, and
// neither is a lock-in.
//
// What it exists for is the question a single pull request cannot answer.
// One kill rate is a sample; we have watched the same unchanged diff score 0.85
// and then 0.90. Forty of them are a distribution, and "this file has drifted
// from 0.9 to 0.6 over two months" is a claim no individual run can support.
//
// Two rules the schema enforces rather than documents:
//
//   - APPEND ONLY. A verified receipt that can be UPDATEd is not a receipt.
//     Rows are inserted, never modified, and — whenever --attest ran too —
//     each carries the sha256 of the signed statement it came from, so a row
//     traces back to something a third party can verify. Without --attest
//     there is no statement to point to, and a row's statement_sha256 is
//     honestly empty rather than fabricated; scan_id is always the ledger's
//     row id, or 0 when --record was not given.
//   - THE QUALIFIERS TRAVEL WITH THE NUMBERS. proven_missed = 0 means "nothing
//     was proven" rather than "the suite is clean" whenever the writer failed
//     or its test never graded. Aggregation is exactly where that distinction
//     gets dropped and a zero silently becomes good news, so the columns sit
//     beside the number and every documented query carries them.
package auditpush

import (
	"fmt"
	"strings"
)

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
	Lang            string
	MutantsPlanted  int
	ModelsByRole    string // JSON, so a new role does not need a migration
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          bool
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
	RegionsTotal    int
	RegionsProbed   int
	DroppedRegions  string
	VacuousFindings int
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
	GoalsDerived         int
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
	// PromptShape mirrors scanstore.File.PromptShape / advpool.Verdict.PromptShape:
	// "chunk" when every mutant-generator shard on this file's run saw only
	// its own symbols' bodies plus the file's preamble, "file" when even one
	// shard fell back to the whole file (including every unsharded run).
	// "" for a row from before this disclosure existed — never fabricated.
	PromptShape string
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
// smallest, the middle and the largest. This package's own copy of the
// pool's spread — auditpush is a leaf writer and imports no engine package
// — reached only through a pointer so an unmeasured spread is absent rather
// than three zeros.
type TestsPerMutantSpread struct{ Min, Median, Max int }

// Push appends file rows to target, creating the tables if they are not
// there.
//
// DEPRECATED, and kept because it is the shape every pre-bundle caller was
// written against: it pushes ONE of the five grains. New callers build a
// Bundle and call PushBundle, which is what this now delegates to (with an
// empty scan row and no mutants, calls or events).
//
// target is a DuckDB path or `md:<db>`. For MotherDuck the caller must have set
// motherduck_token in the environment — the same contract fleet sync uses, and
// the reason this takes no credential of its own: corral never holds one.
//
// link is variadic so Push(target, rows) keeps working for every caller that
// has no statement to link rows to — the test-only/legacy path, and any
// caller pushing rows without --attest. MORE THAN ONE link is an error, not
// a silent "first one wins": the dropped link is precisely the traceability
// a row would then claim and not have.
func Push(target string, rows []Row, link ...Link) (int, error) {
	if len(link) > 1 {
		return 0, fmt.Errorf("auditpush: Push takes at most one Link, got %d", len(link))
	}
	if len(rows) == 0 {
		if strings.TrimSpace(target) == "" {
			return 0, fmt.Errorf("auditpush: no target")
		}
		return 0, nil
	}
	var lk Link
	if len(link) == 1 {
		lk = link[0]
	}
	c, err := PushBundle(target, Bundle{Files: rows, Link: lk})
	return c.Files, err
}
