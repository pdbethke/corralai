// SPDX-License-Identifier: Elastic-2.0

package ledger

import "time"

// ScanRow is the run itself: one row per `certify --repo` invocation. It is
// the header the other four grains hang from, and it exists because a
// per-file number without the box, the version and the substrate that
// produced it is a claim with no warrant.
//
// JOIN ON ScanUID. It is one globally unique column, present on all five
// grains, and it is the answer to everything this comment used to describe as
// something a reader had to know beforehand.
//
// The legacy key is (Repo, RunURL, ScanID), unique PER WRITER, so twenty
// runners pushing at once need no coordination and can never collide. It has
// two sharp edges, and both have cost real readers real numbers:
//
//   - ScanID ALONE is not a key. It is a per-ledger sequence that restarts at
//     1 in every local --record ledger, so the same small integers recur
//     across every ledger that ever pushed to a warehouse. A join on it unions
//     unrelated scans SILENTLY. Observed: a two-file scan that pushed 76
//     mutants reported 170, having absorbed another ledger's scan 1.
//   - RunURL is EMPTY off CI (it comes from GITHUB_SERVER_URL/REPOSITORY/
//     RUN_ID), so for a local run the composite degenerates to (Repo, ScanID),
//     and two local ledgers that audited the same repository can collide for
//     real. A local run with no --record has ScanID 0 too, so every such push
//     to one repo lands under ("repo", "", 0).
//
// None of that corrupts the ledger — these tables are APPEND-ONLY, every row
// carries ts, and every reader that orders by ts (corral_seal included) is
// unaffected. What the repeated key costs is JOINABILITY, which is exactly
// what ScanUID restores: derived per push from the scan's own identity, so it
// is distinct whenever two runs differ in anything, including both cases
// above, and it needs no --record to exist.
type ScanRow struct {
	// ScanUID is the globally unique identity of this scan, filled in by
	// PushBundle (never by a caller) so that no push can forget it. See
	// scanUID for what it is derived from and why.
	ScanUID string
	Repo    string
	RunURL  string
	ScanID  int64
	Commit  string
	// CorralVersion is the same string `corral version` prints.
	CorralVersion string
	Substrate     string
	Host          string
	Cores         int
	// TreesRequested is what the scan ASKED each file's pool for on the
	// workspace substrate — an intention, not a result (the per-file probe
	// decides what it got, and corral_audits.trees records that). 0 on the
	// jail, which builds no trees, and written SQL NULL there.
	TreesRequested int
	DiffBase       string
	// Scope is the --path subtree a scan was bounded to, "" for the whole
	// repository. Without it an entry for one crate of an 80-crate
	// workspace reads its file counts and "100% of candidates" as if they
	// described the repository named in Repo.
	Scope      string
	Candidates int
	Audited    int
	// Passed is NULLABLE: nil is "no threshold was set, so nothing was
	// passed or failed" — never a fabricated true.
	Passed *bool
	// StartedAt is when the SCAN started, from the local ledger — not when
	// this push ran. The seal view picked "latest per path" by ts, which is
	// push time, so re-pushing an old scan made its superseded verdict
	// current. nil for rows written before this column existed.
	StartedAt *time.Time
	// TotalMillis is the scan's own wall clock. *int64 so a run that did not
	// time itself is NULL rather than a scan that took no time.
	TotalMillis *int64
	// SelectionMillis is the scan's ONE instrumented coverage run — the pass
	// that decides which tests execute which file. It belongs at the SCAN
	// grain because that is where it happens: corral_audits.selection_ms
	// carries the same run on every file of the scan so a per-file readout
	// can name each phase, and summing THAT column would count one run once
	// per file. This is the column a cost query adds.
	//
	// NULL — never 0 — for a scan that instrumented nothing (`--whole-suite`,
	// an unsupported language, a runner that could not be built).
	SelectionMillis *int64
	// SelectionReused is true when this scan reused a PRIOR scan's
	// instrumented coverage evidence (see scanstore.Scan.SelectionReused);
	// the only column that tells a reused pass from one that never ran,
	// since both leave SelectionMillis NULL.
	SelectionReused bool
	// InputTokens / OutputTokens are omitted, never written 0, when none
	// of the scan's calls was metered (ScanTokensUnmetered — the rule the
	// warehouse's NULL follows): buildBundle leaves them zero there and
	// omitempty keeps the ledger entry from stating a measured-looking 0.
	// A scan that made no calls at all omits them too, which reads the same
	// as before: there were no tokens to measure.
	InputTokens  int64 `json:",omitempty"`
	OutputTokens int64 `json:",omitempty"`
	ModelCalls   int64
	// SourcePushed records whether THIS run carried source bytes to the
	// warehouse. A custody fact belongs in the record: "did our code leave
	// the box on that run" must be answerable from the table, not from
	// whoever remembers the argv.
	SourcePushed    bool
	StatementSHA256 string
	// RekorLogIndex and RekorUUID are the same --transparency receipt the
	// local ledger's scans.rekor_log_index/rekor_uuid carry (see
	// scanstore.Scan's doc for why LogIndex is a pointer: 0 is a real log
	// position, so an un-uploaded scan must read back nil, never a
	// fabricated 0).
	RekorLogIndex *int64
	RekorUUID     string
	// PushedBy names WHICH VERB wrote this row: PushedByCertify for the
	// run's own entry. A writer that is not the run (an adjudication, a
	// review verdict written beside it) names itself here, so a reader can
	// tell whose row it is holding.
	PushedBy string
	// EntryHash is the ledger entry this scan was written as, when the
	// push knows it — `corral ledger push` always does, and a run's own
	// --push does once its entry is placed. It is what lets a second push
	// of the same directory skip what the warehouse already holds.
	EntryHash string
	// The scan-grain facts that lived only in the retired local DuckDB
	// record (scanstore.Scan) until the ledger entry became THE record:
	// what the scan was asked to do (Top, AllCandidates, TotalFiles, the
	// engine and model roster it ran with), whether its coverage preflight
	// ran, and when it finished. Without them a ledger entry could not
	// answer "was this a --top 3 run" — the question the retired store's
	// `scans list` answered.
	EngineVersion string
	ModelSet      string
	Top           int
	AllCandidates bool
	TotalFiles    int
	PreflightRan  bool
	PreflightNote string
	FinishedAt    *time.Time
	// Identity is the audited party — who made the commit (see Identity).
	// Empty on rows written before it was recorded, or when the checkout
	// could not say.
	Identity Identity
	// TreeDirty is whether the audited tree differed from HEAD — uncommitted
	// or untracked changes when the run began; nil when unknown or when the
	// recorded commit is not HEAD. It is defined against HEAD because that is
	// what git can measure, so it says something about Commit only when
	// Commit IS HEAD (cmd/corral treeDirtyAt compares full SHAs): a run that
	// records another revision would otherwise carry a "clean" that described
	// a different tree. A pointer on purpose: nil — "git could not say", "not
	// the recorded revision", and every row written before this field — is
	// not the same claim as false.
	TreeDirty *bool `json:",omitempty"`
	// RepoIdentity is how Repo was established: "git" (derived from the
	// checkout's origin), "operator" (typed by the operator — certify
	// --local's --repo/--commit — and not checked against git) or "none" (it
	// could not be, and Repo is a stand-in or empty). It is NOT Identity above — that is the audited PARTY, this
	// is the provenance of the repo NAME. Empty on rows that never recorded it.
	RepoIdentity string `json:",omitempty"`
	// SandboxBackend and SandboxImage say which jail ran the scan's commands
	// (sandbox.Identity): the backend ("bwrap", "container", "sandbox-exec")
	// and, for a container, "<ref>@<image id>", the toolchain the tests ran
	// with. Substrate says only "jail", which cannot answer "which one?".
	// Empty under --substrate workspace, which runs no sandbox, and on every
	// row written before them.
	SandboxBackend string `json:",omitempty"`
	SandboxImage   string `json:",omitempty"`
}

// PushedByCertify is the ScanRow.PushedBy of a run's own entry.
const PushedByCertify = "certify"

// MutantRow is one mutant's fate. The warehouse table is new, so unlike the
// local scan_mutants (whose CHECK predates this schema and cannot be altered
// in place) it admits all four outcomes: killed, survived, invalid,
// timed_out. A kill rate computed over a denominator that silently excludes
// the invalid and timed-out mutants is a different number from one that
// discloses them.
type MutantRow struct {
	Repo         string
	RunURL       string
	ScanID       int64
	Path         string
	MutantID     string
	ParentSHA256 string
	Outcome      string
	// InvalidReason says WHY a mutant never graded (it did not compile, the
	// gate rejected it). Without it, "invalid" is a count with no diagnosis
	// and a generator that produces garbage looks the same as one that
	// produces hard mutants.
	InvalidReason string
	Proven        bool
	// ProvenByAuthoredAlone is the strict subset of Proven that is a
	// demonstrated GAP: the authored test killed it and the dev suite never
	// did.
	ProvenByAuthoredAlone bool
	TestsRun              int
	SelectionRule         string
	// DurationMillis is how long grading this mutant took. NULL, never 0,
	// when nothing timed it.
	DurationMillis *int64
	// KilledBy is the first failing test id, best effort, when the language
	// plugin can parse one out of the runner's output. "" when it cannot —
	// never inferred.
	KilledBy string
	// SpanStart and SpanEnd are the mutated line range, from
	// advpool.MutantRef.Span since 2026-09-04 (NULL on rows pushed before
	// that, or a mutant whose generator recorded none). They are 1-BASED, so
	// 0 is unambiguously "not recorded" and is written SQL NULL — line 0
	// does not exist, and a reader jumping to it would be sent to the top of
	// the file.
	SpanStart int
	SpanEnd   int
	// Shape and GeneratorModel mirror scanstore.Mutant's: the kind of
	// fault (from the hunk) and the seat that planted it.
	Shape          string
	GeneratorModel string
	// Code is the mutant's source. It IS the audited code, so it is written
	// only when the bundle says --push-source was given.
	Code            string
	StatementSHA256 string
}

// ModelCallRow is what one role's seat cost on one file — the money grain.
// The scan header's totals answer "what did this audit cost"; only this
// grain answers "which seat was slow, and on which file".
type ModelCallRow struct {
	Repo   string
	RunURL string
	ScanID int64
	Path   string
	Role   string
	Model  string
	Calls  int
	// Retries is nullable: nil means "not measured" (every producer today,
	// since agentbackend has no retry loop to observe), never a stored 0
	// that would read as "measured: zero retries".
	Retries     *int
	InputTokens int64
	// CachedInputTokens is how many of InputTokens the provider served from
	// its own prompt cache. Nullable for the same NULL-not-zero reason
	// Retries is: silence from a provider is not a measured miss. See
	// scanstore.ModelCall.CachedInputTokens.
	CachedInputTokens *int64
	// CacheWriteInputTokens is the price of the saving above: Anthropic bills
	// a cache write at 1.25x an ordinary input token. Nullable, and nil
	// independently of the read count.
	CacheWriteInputTokens *int64
	OutputTokens          int64
	// UnmeteredCalls is how many of Calls reported no token usage (an agent
	// seat on the `text` wire); InputTokens/OutputTokens cover only the rest.
	// When it equals Calls the warehouse writes the token columns NULL — see
	// TokensUnmetered.
	UnmeteredCalls int `json:",omitempty"`
	WallMillis     int64

	StatementSHA256 string
}

// EventRow is one entry on the tape. Seq, not TS, is the ordering key: two
// events inside one millisecond are ordinary, and a tape whose order depends
// on clock granularity is not a tape.
type EventRow struct {
	Repo   string
	RunURL string
	ScanID int64
	Path   string
	// TS is when the BEAT happened, carried from the ledger's own event row
	// — not when the push ran. Every row of a push used to share one `now`,
	// which threw the tape's clock away at the door: the warehouse could
	// order beats by Seq but could never say when in the run one fell, or
	// how long the gap between two of them was.
	TS      time.Time
	Seq     int64
	Kind    string
	Actor   string
	Subject string
	Model   string
	// DurationMillis is set on the events that HAVE a duration (a completed
	// phase, a returned call) and nil on the ones that are a moment.
	DurationMillis *int64
	// Detail is JSON TEXT in a VARCHAR column — see the corral_events DDL
	// below for why the column is not DuckDB's JSON type.
	Detail string

	StatementSHA256 string
}

// SeatRow is one seat's execution-proven contribution to one run: the
// per-seat scorecard (catches, opportunities, plants, survivors, critic
// flags). Every field of advpool.BugCatchObservation by the same Go name,
// plus the subject the run audited (Repo, Commit, Path). auditpush cannot
// name the producer type in a signature without inverting the layering, so
// the parity is enforced by TestSeatRowCarriesEveryObservationField in
// cmd/corral, which fails when a producer field has no home here. Counts are
// not omitempty: a seat that caught nothing measured zero, and the entry
// says so.
type SeatRow struct {
	Repo   string
	Commit string
	Path   string

	Model            string
	Role             string
	Catches          int
	Opportunities    int
	SoundTests       int
	AuthoredTests    int
	CriticFlags      int
	MutantsPlanted   int
	MutantsSurvived  int
	Shard            int
	Region           string
	RegionComplexity int
	RegionLines      int
	TestComplexity   int
	ParseRetries     int
	Dropped          bool
	Shadow           bool
	Lang             string
	ShadowDrawn      bool
	SeatTool         string
	ModelUnresolved  bool
}

// MutantAttemptRow is one seat's outcome for one mutant: the paired
// observation the correlation statistic needs. Every field of
// advpool.MutantAttempt by the same Go name, plus Repo and Commit. Path is
// the producer's own (the mutant's file), not a run-level value.
type MutantAttemptRow struct {
	Repo   string
	Commit string

	Path     string
	MutantID string
	Model    string
	Role     string
	Shadow   bool
	Outcome  string
}

// CriticFindingRow is one test-critic finding and the execution-checked
// outcome of it. Every field of advpool.CriticFindingObservation by the same
// Go name, plus the subject. Evidence quotes code, so it is in the custody
// set BlankUnpushedSource withholds. It is also NEVER in a statement's
// telemetrySha256, whatever the bundle's custody (see
// certify.AuditStatement.TelemetrySHA256 and cmd/corral telemetrySHA256):
// one statement must verify against both the push and the ledger entry.
type CriticFindingRow struct {
	Repo   string
	Commit string
	Path   string

	QueueFindingID  int64
	Model           string
	TargetTest      string
	TestFile        string
	TestSelector    string
	Scope           string
	Evidence        string
	Severity        string
	Adjudication    string
	Source          string
	SeatTool        string
	ModelUnresolved bool
}

// Bundle is one scan at all five grains: the thing a push actually is. It
// exists as one value, rather than five calls, because the five tables must
// land together — a scan row with no mutants reads, to the next query, as a
// scan that produced none.
type Bundle struct {
	Scan    ScanRow
	Files   []Row
	Mutants []MutantRow
	Calls   []ModelCallRow
	Events  []EventRow
	// Seats, MutantAttempts and CriticFindings are what a run measured about
	// its seats (see SeatRow). They live in the bundle, and so in the signed
	// ledger entry, but NO warehouse table holds them yet (Phase 4):
	// PushBundle ignores them, and the warehouse-rows hash leaves them out
	// (cmd/corral prepareRowsForHash) because a verifier reads the rows back
	// from the warehouse and could never reproduce a hash that covered rows
	// the warehouse never received. omitempty, so a bundle that carries none
	// marshals byte-for-byte as it did before they existed. When a statement
	// is written, it additionally binds them through its telemetrySha256
	// (cmd/corral telemetrySHA256).
	Seats          []SeatRow          `json:",omitempty"`
	MutantAttempts []MutantAttemptRow `json:",omitempty"`
	CriticFindings []CriticFindingRow `json:",omitempty"`
	// Link is stamped onto every row of every table by PushBundle, so a row
	// and the statement it names can never disagree about which run produced
	// them.
	Link Link
	// SourcePushed is the custody switch (`--push-source`). When false, the
	// three source-bearing columns are written SQL NULL no matter what the
	// rows carry: the writer enforces it, not the caller.
	SourcePushed bool
}

// Counts is what a push actually wrote, per table. One integer would have to
// be a sum, and a sum cannot tell "3 mutants" from "3 events" — which is the
// difference between a scan that graded something and one that did not.
type Counts struct {
	Scans   int
	Files   int
	Mutants int
	Calls   int
	Events  int
	// Entry and KeyID describe a LEDGER-DIRECTORY push only: the file name
	// the entry was placed under and the keyid that signed it ("" when it
	// was written unsigned). Both are "" for a warehouse push, which places
	// no file. Carried here, from the write itself, so a caller that names
	// the entry it wrote never has to re-read the directory to find it — a
	// re-read names whichever entry is newest, and another writer's may be.
	Entry string
	KeyID string
}

// Total is every row the push wrote, for a caller that only wants one number.
func (c Counts) Total() int { return c.Scans + c.Files + c.Mutants + c.Calls + c.Events }
