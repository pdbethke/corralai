// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/matrix"
	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// Scorer wraps adequacy.Score in the jail: the brain-side, sandboxed judge
// that actually RUNS a candidate test against the compliant code plus a set
// of mutants and reports the kill rate. The driver NEVER derives
// DevKillRate/ProvenMissed from a worker's self-report — only from this
// (soundness #1: "a judge may not certify herself").
type Scorer interface {
	Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (killRate float64, survivors []adequacy.Mutant, err error)

	// ScoreReport is Score's richer sibling: it returns the full adequacy.Report
	// (CompliantPass + the Killed/Survived mutant IDs), so a caller can tell a
	// baseline that could not pass (CompliantPass=false) from a genuine zero-kill
	// (CompliantPass=true, len(Killed)==0). The matrix needs this distinction.
	ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
}

// AuthoredScorer is an optional Scorer extension for scoring a POOL-authored
// test — the test-writer's output — as opposed to the dev suite's own.
// tickPoolAdequacy prefers this over ScoreReport when a Scorer implements it
// (the same optional-extension pattern verboseJail already uses in gate.go).
//
// The distinction matters only in repo-aware mode: JailScorer.ScoreReport's
// workspace-building deliberately does NOT overlay `test` there (correct for
// the dev test, which is already on disk — overlaying would shadow the real
// suite), but an AUTHORED test is a brand-new file the repo does not already
// contain, so silently dropping it means the pool re-scores the DEV suite
// against its own already-known survivors — every repo-aware run then
// computes ProvenMissed=0 unconditionally, regardless of what the pool's test
// actually proved. See JailScorer.ScoreAuthoredReport's doc for the full
// history. A Scorer that does not implement this (only test fakes, today —
// JailScorer, the only production implementation, always does) falls back to
// ScoreReport via scoreAuthored below, which is fine in single-file mode.
type AuthoredScorer interface {
	ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
}

// scoreAuthored scores a pool-authored test through AuthoredScorer when the
// wired Scorer implements it, else falls back to the plain ScoreReport —
// see AuthoredScorer's doc for why the two differ and when the fallback is
// safe.
func scoreAuthored(ctx context.Context, scorer Scorer, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	if as, ok := scorer.(AuthoredScorer); ok {
		return as.ScoreAuthoredReport(ctx, codePath, code, test, mutants, testCmd)
	}
	return scorer.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
}

// PerMutantScorer is the optional RICHER contract: a Scorer that can grade
// each mutant with its OWN command — the tests that actually reach the lines
// that mutant changed — instead of the one command the whole file shares.
// JailScorer implements it; the test fakes need not, and the driver
// type-asserts, so a Scorer that cannot do this keeps behaving exactly as it
// did (see scoreDevReport/scoreDevSurvivors).
//
// It is a separate interface rather than two more methods on Scorer for that
// reason alone: Scorer is implemented by every fake in this package's tests,
// and widening it would force eleven doubles to grow a method none of them
// has anything to say about.
type PerMutantScorer interface {
	ScoreReportFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (adequacy.Report, error)
	ScoreFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (float64, []adequacy.Mutant, error)
}

// scoreDevReport grades mutants the way the run's DEV pass grades them: per
// mutant when the scorer can (PerMutantScorer) and the run has line evidence
// to narrow by (DevCommandFor non-nil), else with the run's single shared
// command. Both branches issue DevCommand(rs) as the baseline/compile-gate
// command, so the fallback is the pre-per-mutant path unchanged.
//
// The bool says whether the per-mutant closure was actually HANDED to the
// scorer. That is the fact the verdict's disclosure must rest on, and it is
// knowable only here: the returned Report's PerMutant map is empty whenever
// no mutant reached grading (every one rejected by the compile gate), which
// is a statement about the exam, not about which command the run chose.
func scoreDevReport(ctx context.Context, scorer Scorer, rs RunSpec, mutants []adequacy.Mutant) (adequacy.Report, bool, error) {
	cmd := DevCommand(rs)
	if pm, ok := scorer.(PerMutantScorer); ok {
		if cmdFor := DevCommandFor(rs); cmdFor != nil {
			rep, err := pm.ScoreReportFor(ctx, rs.CodePath, rs.Code, rs.DevTestCode, mutants, cmd, cmdFor)
			return rep, true, err
		}
	}
	rep, err := scorer.ScoreReport(ctx, rs.CodePath, rs.Code, rs.DevTestCode, mutants, cmd)
	return rep, false, err
}

// scoreDevSurvivors is scoreDevReport's Score-shaped sibling, for the shadow
// mutant-generator pass. The challenger's mutants must face the SAME exam the
// primary's did — the pass is a controlled head-to-head — so it narrows by
// the same closure, not merely by the same shared command.
func scoreDevSurvivors(ctx context.Context, scorer Scorer, rs RunSpec, mutants []adequacy.Mutant) (float64, []adequacy.Mutant, error) {
	cmd := DevCommand(rs)
	if pm, ok := scorer.(PerMutantScorer); ok {
		if cmdFor := DevCommandFor(rs); cmdFor != nil {
			return pm.ScoreFor(ctx, rs.CodePath, rs.Code, rs.DevTestCode, mutants, cmd, cmdFor)
		}
	}
	return scorer.Score(ctx, rs.CodePath, rs.Code, rs.DevTestCode, mutants, cmd)
}

// Validator is brain-side artifact validation of a worker's structured
// result, run before the driver trusts it enough to score or promote on it.
type Validator interface {
	CompileTest(ctx context.Context, codePath, code, test string) error
	ParseMutants(raw, original string) ([]adequacy.Mutant, error) // = testgen.ParseMutantsOutput (applies each SEARCH/REPLACE hunk to `original`)
	ParseTest(raw string) string                                  // = testgen.ParseTestOutput (strip fences/prose from a worker's raw test)
}

// Verdict status values. Never auto-certified: a blocking finding or a
// below-threshold DevKillRate always routes to needs-review.
const (
	StatusCertified   = "certified"
	StatusNeedsReview = "needs-review"
)

// Leaderboard outcome values fed by the driver after a terminal, signed
// verdict — never derived from a worker's self-report (soundness #5).
const (
	OutcomePass = "pass"
	OutcomeFail = "fail"
)

// Signer wraps the certify chain (certify.BuildLedger/BuildAttestation/
// SignDSSE + buildstore — the real impl is Task 5.1): it signs a terminal
// Verdict as a tamper-evident record, subject = repo@commit, byproducts =
// the Verdict fields (incl ModelsByRole). Here it is an interface so the
// driver stays pure/unit-testable with a fake; the resulting record
// verifies with `corral certify verify`.
type Signer interface {
	SignVerdict(ctx context.Context, v Verdict) (recordID int64, head string, err error)
}

// LeaderboardSink is the gate-earned fitness feed: one (model, role, outcome)
// observation per role, fed ONLY after a terminal Verdict has been scored by
// the deterministic gate (Scorer, never a worker's self-report) AND signed
// (soundness #5 — "a judge may not certify herself" extends to the fitness
// signal too: a model cannot earn leaderboard credit from its own claim).
type LeaderboardSink interface {
	Record(model, role, outcome string)
}

// BugCatchObservation is one seat's execution-proven contribution from a
// single converged run (see internal/bugcatch). Catches come ONLY from
// ProvenMissed — no claim/self-report path may ever populate it.
type BugCatchObservation struct {
	Model, Role                                  string
	Catches, Opportunities                       int
	SoundTests, AuthoredTests                    int
	CriticFlags, MutantsPlanted, MutantsSurvived int
	// Per-shard generator dimensions (zero for the single-seat roles).
	Shard            int
	Region           string
	RegionComplexity int
	RegionLines      int
	TestComplexity   int
	ParseRetries     int
	Dropped          bool
	Shadow           bool // set by Task 6; a shadow seat NEVER gates
}

// BugCatchSink is the optional per-run bug-catching feed (nil ⇒ no-op),
// mirroring LeaderboardSink but fed on EVERY converged run (certified AND
// needs-review) — a catch or a miss is meaningful regardless of the overall
// verdict, unlike leaderboard fitness which is gated on certification.
type BugCatchSink interface {
	Record(recordID int64, recordHead string, obs []BugCatchObservation)
}

// MutantAttempt is one SEAT's outcome for one mutant. Two attempts sharing a
// mutant id and differing in Model are the paired observation the correlation
// statistic needs.
type MutantAttempt struct {
	Path     string
	MutantID string
	Model    string
	Role     string // RoleTestWriter | RoleTestWriterShadow
	Shadow   bool
	Outcome  string // "killed" | "survived"
}

// MutantAttemptSink is the optional per-run feed of per-seat mutant outcomes
// (nil ⇒ no-op), mirroring BugCatchSink. advpool deliberately does NOT import
// internal/scanstore: the composition root adapts this to
// scanstore.RecordMutantAttempts, exactly as it does for the other sinks.
//
// This is a MEASUREMENT feed. Nothing written here may reach the Verdict, the
// aggregate, or the signed record.
type MutantAttemptSink interface {
	Record(recordID int64, recordHead string, attempts []MutantAttempt)
}

// CriticFindingObservation is one test-critic finding's execution-checked
// outcome from a single converged run: whether the flagged test, run ALONE
// against the run's own mutants, actually killed anything. Populated ONLY by
// the driver's conservative auto-refute (never a worker's self-report) —
// soundness #1 extends to critic findings too ("a judge may not certify
// herself" also means a claim about a test is not itself proof).
type CriticFindingObservation struct {
	QueueFindingID                     int64
	Model                              string
	TargetTest, TestFile, TestSelector string
	Scope                              string // normalized (NormalizeScope)
	Evidence, Severity                 string
	Adjudication                       string // auto verdict: refuted|unadjudicated
	Source                             string // "auto"
}

// CriticFindingSink is the optional per-run critic-finding feed (nil ⇒
// no-op), mirroring BugCatchSink: fed on every terminal verdict once
// RecordID/RecordHead are set, so every row carries a linkable record.
type CriticFindingSink interface {
	Record(recordID int64, recordHead string, obs []CriticFindingObservation)
}

// TestEnumerator runs a language plugin's list command in the run's jail
// workspace and returns its stdout — the seam tickMatrix needs to turn a
// suite file into individual selectors before scoring each one. Optional on
// the Driver (nil ⇒ the matrix phase is skipped even when RunSpec.Matrix is
// set — matrix is opt-in on TWO axes: the run must ask for it AND the driver
// must be wired with a way to enumerate).
type TestEnumerator interface {
	Enumerate(ctx context.Context, codePath, code, test string, listCmd []string) (stdout string, err error)
}

// MatrixObservation is one test's execution-proven adequacy from the
// tests×mutants matrix: how many of the run's mutants it killed ALONE, and
// whether that makes it a delete-candidate (scored, ran against a non-empty
// mutant set, killed nothing).
type MatrixObservation struct {
	TestSelector, TestFile string
	Kills, MutantsTotal    int
	DeleteCandidate        bool
}

// MatrixSink is the optional per-run tests×mutants matrix feed (nil ⇒
// no-op), mirroring CriticFindingSink/BugCatchSink: fed on every terminal
// verdict once RecordID/RecordHead are set, only when the matrix actually ran.
type MatrixSink interface {
	Record(recordID int64, recordHead string, obs []MatrixObservation)
}

// EventSink receives the pool's reasoning milestones as replay/telemetry
// events. Optional (nil ⇒ no-op), like Signer/Leaderboard — the pure Driver
// takes no telemetry dependency; the brain wires this to its telemetry store
// keyed on the run's missionID. Kinds: pool_subject, pool_dev_adequacy
// (dev_pass's phase boundary — carries duration_ms once the dev pass has
// closed), pool_verdict, plus the phase-boundary beats phase_pool,
// phase_generation, phase_authored_pass and phase_critic, each carrying
// detail.duration_ms and each fired only when that phase actually ran (a
// phase that never started emits nothing — see Timing's own doc). detail
// carries the real values/evidence, never a summary.
type EventSink interface {
	Emit(missionID int64, kind, subject string, detail map[string]any)
}

// MutantRef identifies a mutant without carrying its SOURCE.
//
// The ledger stores parent_sha256 and an id, never a patch: that is enough to
// group, count and compare, and it keeps a tenant's code out of the warehouse.
// This type exists so that choice is enforced by the TYPE rather than by every
// future caller remembering to strip a field — Verdict is marshalled whole
// (CertSigner.SignVerdict, and scan_files.verdict_json is documented as "stored
// whole"), so any field reachable from here reaches the warehouse eventually.
type MutantRef struct {
	ID           string
	ParentSHA256 string
	// TestsRun and Rule are what this mutant was actually GRADED by, when the
	// run graded each mutant with its own command (see PerMutantScorer). A
	// kill rate averaged over mutants graded by different test sets is not
	// one measurement unless the record says which set each one faced, so the
	// disclosure rides at the same grain as the grading. Both are zero on a
	// run that graded every mutant with the file's shared command.
	TestsRun int
	Rule     string // lang.SpanRule*; "" when the run was not graded per mutant
	// Duration is how long the suite run that GRADED this mutant took
	// (adequacy.MutantGrading.Duration). The dev pass is where an audit's
	// minutes go — 35 of 43 on one reference file — and a file-grain total
	// cannot say whether that was one pathological mutant or forty ordinary
	// ones. Zero when the run was not timed, and stored as SQL NULL rather
	// than 0 for exactly that reason (see scanstore.Mutant.DurationMillis).
	//
	// `json:"-"`: the wire form is duration_ms, written by MutantRef's own
	// MarshalJSON below — the same reason Verdict.BaselineDuration carries
	// this tag.
	Duration time.Duration `json:"-"`
	// KilledBy is the id of the first test that FAILED on this mutant, when
	// the language's runner said so in words corral can parse
	// (adequacy.MutantGrading.KilledBy). It answers "which test was awake",
	// which the ledger could never say: a kill was recorded as a bare fact.
	// Empty on a survivor (nothing caught it), on a timeout-kill (no test
	// reported anything), and for every language whose runner output corral
	// declines to parse — stored as SQL NULL rather than "" for exactly that
	// reason, and never inferred from anything but the output.
	KilledBy string
}

// toMutantRefs strips MUTANT SOURCE down to the reference scan_mutants needs:
// id and parent hash. See MutantRef's own doc for why this must happen by
// TYPE, not by caller discipline — anything reachable from a Verdict field
// eventually reaches the warehouse.
func toMutantRefs(ms []adequacy.Mutant) []MutantRef {
	return toMutantRefsWith(ms, nil)
}

// toMutantRefsWith is toMutantRefs carrying the per-mutant grading a
// per-mutant run produced (adequacy.Report.PerMutant, keyed by mutant ID). A
// nil map is the ordinary run: every ref's TestsRun/Rule stay zero, which is
// exactly what toMutantRefs produced before per-mutant grading existed.
func toMutantRefsWith(ms []adequacy.Mutant, grading map[string]adequacy.MutantGrading) []MutantRef {
	refs := make([]MutantRef, len(ms))
	for i, m := range ms {
		refs[i] = MutantRef{ID: m.ID, ParentSHA256: m.ParentSHA256}
		if g, ok := grading[m.ID]; ok {
			refs[i].TestsRun = g.TestsRun
			refs[i].Rule = g.Rule
			refs[i].Duration = g.Duration
			refs[i].KilledBy = g.KilledBy
		}
	}
	return refs
}

// TestSelection discloses which measurement a Verdict's kill rate is: the
// whole suite (Method "") or the narrowed set of tests that evidence showed
// executed the file under audit (Method set, Selected of Of). Fallback
// carries why a selector could not narrow (no selector for the language,
// stale/missing evidence) even though Method is set.
type TestSelection struct {
	Method   string `json:"method"` // "" = whole suite
	Selected int    `json:"selected"`
	Of       int    `json:"of"`
	Fallback string `json:"fallback"`
	// PerMutant is true when the run graded each mutant with the tests that
	// reach its own span rather than with the file's shared selection — a
	// different measurement from the one Method/Selected describe, so it is
	// disclosed rather than folded into them.
	PerMutant bool `json:"per_mutant,omitempty"`
	// TestsPerMutant is the spread of how many tests each graded mutant
	// actually ran, over the mutants whose grading recorded a count. It is
	// the honest summary of how much the narrowing narrowed: a Min equal to
	// the Max means every mutant faced the same set after all.
	//
	// A POINTER so that "no spread was measured" is expressible. A Verdict is
	// marshalled whole into the signed statement, the ledger and the
	// warehouse, and a struct field with `omitempty` never omits: every
	// whole-suite verdict used to sign {0,0,0}, a range nobody measured and
	// indistinguishable from a real one. nil is the only honest value when
	// no mutant's grading recorded a count — which includes every run that
	// did not grade per mutant, and a per-mutant run whose whole exam was
	// rejected by the compile gate.
	TestsPerMutant *TestsPerMutantSpread `json:"tests_per_mutant,omitempty"`
	// Rules counts the mutants by WHY they got the command they got
	// (lang.SpanRule*). A run that is mostly "static" or "unreached" narrowed
	// almost nothing, and the count is what says so.
	Rules map[string]int `json:"rules,omitempty"`
	// AuthoredAlone: the authored pass proved each survivor with the
	// authored test ALONE (see JailScorer.ScoreAuthoredReport). A proven
	// count under this flag can only be the authored test's own doing;
	// without it, any test in the shared command could have supplied the
	// kill — a different, weaker claim.
	AuthoredAlone bool `json:"authored_alone,omitempty"`
}

// Concurrency discloses how many private trees the workspace substrate's
// concurrency probe granted this file — how many mutants were scored AT ONCE
// — or, when it granted only one, WHY: the substrate builds no trees at all
// (the jail path), the budget only bought one, or the probe downgraded a
// suite that failed under concurrency (Note carries the reason, verbatim,
// so the screen and the record never disagree — see
// cmd/corral/certify_repo.go's noteConcurrency).
//
// Trees < 1 is the single explicit "NOT RECORDED" state, and every reader
// treats it as one: the report prints no concurrency line, the attestation
// signs no trees key, the ledger and the warehouse store SQL NULL, and
// `corral scans show` prints "not recorded". It is what a jail-substrate run
// carries (that substrate builds no trees) and what a verdict served from a
// cache row written before this column existed carries. A measured ONE tree
// is Trees 1 and is disclosed like any other measurement — the absence of a
// measurement is never rounded up to it.
type Concurrency struct {
	Trees int    `json:"trees"`
	Note  string `json:"note,omitempty"`
	// Shared names the dependency directories that were SYMLINKED into every
	// tree rather than copied (adequacy.Disclosure.Shared). They are the one
	// thing the trees do NOT hold privately — a channel between them, and a
	// path back into the operator's real checkout — so a reader told "6
	// trees" has to be told what those 6 had in common. Empty when the run
	// shared nothing, which includes every pool of one.
	Shared []string `json:"shared,omitempty"`
}

// TestsPerMutantSpread is how many tests the graded mutants each ran: the
// smallest, the middle and the largest. Named (and always reached through a
// pointer) so that an unmeasured spread is ABSENT everywhere it travels
// rather than three zeros that read as a measurement.
type TestsPerMutantSpread struct {
	Min    int `json:"min"`
	Median int `json:"median"`
	Max    int `json:"max"`
}

// Verdict is one run's final, gated outcome.
type Verdict struct {
	Repo, Commit string
	Lang         string  // the run's resolved language plugin name (e.g. "go", "python")
	DevKillRate  float64 // the headline: the DEV suite's kill-rate, from Scorer — never a self-report
	// BaselineDuration is the dev suite's compliant (unmutated) wall-clock
	// runtime — the single input to the audit cost model (O(mutants x the
	// TARGET's suite runtime)). See adequacy.Report.BaselineDuration.
	//
	// `json:"-"`: the wire form is baseline_duration_ms, written by
	// verdictWire in verdict_wire.go (the same reason MutantDurationMedian
	// and MutantDurationMax carry this tag — see that type's doc).
	BaselineDuration time.Duration `json:"-"`
	MutantsTotal     int           // mutants actually GRADED (compile-gate rejects excluded)
	// MutantsInvalid counts mutants that failed the language's own compile
	// check and were never run. Surfaced rather than dropped: a run where the
	// generator produced mostly unbuildable mutants graded a much smaller exam
	// than its mutant budget suggests, and the operator must be able to see it.
	MutantsInvalid int
	Survivors      int // mutants the dev's own tests did NOT kill
	ProvenMissed   int // survivors the pool's authored test then killed — real, catchable gaps
	// ProvenMutantIDs is the EVIDENCE behind ProvenMissed: which survivors the
	// authored test actually killed, derived from the same scoring report so
	// the two can never disagree. Empty on every path that did not grade
	// (TestWriterFailed, PoolTestUnsound) AND on a genuine "tried and missed" —
	// those are told apart by those flags, not by this being empty.
	ProvenMutantIDs []string
	// AuthoredTest is the pool's compiling authored test, retained as evidence.
	// A count is not evidence: on 2026-07-31 a paid audit produced a sound,
	// collected, genuinely-grading test that killed 0 of 10 survivors, and the
	// whole surviving record of the attempt was the integer 0 — `certify
	// --repo` has no tape flag, so diagnosing it meant paying to re-run. "" when
	// the writer never produced a compiling test.
	AuthoredTest string
	// WriterMode is HOW the writer seat attacked this file's survivors —
	// "per-survivor" (one call, one repair budget and one proof PER survivor)
	// or "batched" (one call carrying every survivor as a diff). Empty on a
	// run whose caller never named a mode, and on every verdict earned before
	// the mode existed; a reader must render that as "not recorded", never as
	// either mode.
	//
	// DISCLOSURE, not decoration. Nothing about what a kill is or what
	// "proven" means changes between the modes — a survivor is proven iff an
	// authored test kills it alone and passes on the original, in both — but
	// the two cost differently and attempt differently, and two verdicts
	// earned under different modes are not the same measurement. The record
	// says which one it is rather than leaving a reader to infer it from a
	// call count.
	WriterMode string `json:"writer_mode,omitempty"`
	// AuthoredExtra holds PROVEN authored tests that could not be merged into
	// AuthoredTest — a per-survivor run whose parts collide on a helper the
	// concatenator will not rename (see lang.TestConcatenator), or a language
	// with no concatenator at all.
	//
	// They are carried, not dropped, because each one is a real
	// execution-proven artifact: a test corral wrote, compiled and ran to
	// kill a specific survivor. Losing it to make a tidy single file would
	// throw away exactly the evidence ProvenMissed is a count OF. Empty on a
	// batched run, which has only ever had one file.
	AuthoredExtra []golang.AuthoredPart `json:"authored_extra,omitempty"`
	// WriterSeatsUngraded is how many of a per-survivor run's writer seats
	// never produced a test that genuinely graded — no compiling test within
	// their own budget, or one that compiled and then failed on the
	// unmutated code / never reached the file.
	//
	// It exists because the fan-out made PARTIAL failure the common case, and
	// the two flags beside it cannot express it: TestWriterFailed means
	// NOTHING compiled anywhere and PoolTestUnsound means nothing graded
	// anywhere, so a file where twenty-one of twenty-four seats graded
	// carries neither — and its ProvenMissed then reads like a count over all
	// twenty-four. Three seats unattempted changes what a 5 means.
	//
	// 0 is omitted, and is also what a BATCHED run reports: that mode has one
	// seat for the whole file, and its total failure is already
	// TestWriterFailed or PoolTestUnsound. It rides in verdict_json only —
	// there is deliberately NO ledger column for it this round, so a query
	// that wants it must read the JSON rather than find a half-populated
	// column.
	WriterSeatsUngraded int `json:"writer_seats_ungraded,omitempty"`
	// WriterAttempts is the spread of how many attempts (the first try plus
	// every compile/compliant-failure repair) each PER-SURVIVOR writer seat
	// took before it went terminal — the same shape as TestsPerMutant, over
	// a different population. It exists so the next optimisation can tell
	// apart a writer phase that cost what it cost because of RETRIES (a high
	// median/max here) from one that cost what it cost because each seat's
	// single attempt was itself slow (AuthoredPass large, this spread flat
	// at 1). Nil on a batched run (one seat, one repair budget for the whole
	// file — a single count would not be a spread) and on a run that never
	// reached the writer at all.
	WriterAttempts *TestsPerMutantSpread `json:"writer_attempts,omitempty"`
	RegionsTotal   int                   // mutant-generator seats the run dispatched
	RegionsProbed  int                   // seats that returned usable mutants
	DroppedRegions []string              // seats abandoned after MaxShardRetries — the coverage shortfall
	// DuplicateMutants is how many generated mutants were byte-identical
	// edits of another and were collapsed before scoring — pure wasted suite
	// runs, removed. Disclosed rather than dropped: a denominator that
	// shrank silently is one nobody can reconcile against the generator's
	// own output. Omitted from JSON when zero, which is the common case.
	DuplicateMutants int             `json:"duplicate_mutants,omitempty"`
	VacuousFindings  []queue.Finding // test-critic's designed-to-pass/vacuous flags
	ModelsByRole     map[string]string
	Status           string // certified | needs-review
	// TestWriterFailed is true when the pool exhausted MaxTestWriterAttempts
	// without producing a compiling killing test. HONESTY NOTE: when this is
	// true, ProvenMissed==0 does NOT mean "no real gaps" — it means "gaps
	// found (Survivors > 0), killing test not authored." A testWriterFailed
	// run is never certified (aggregate forces needs-review whenever
	// Survivors > 0 and ProvenMissed < Survivors — see aggregate).
	TestWriterFailed bool
	// PoolTestUnsound is true when the pool's authored test DID compile (so
	// TestWriterFailed is false) but, when actually run against the
	// survivors, its own report did not genuinely grade: it failed on the
	// unmutated compliant code (CompliantPass false — an ordinary outcome
	// for an LLM-written test, since the compile gate only checks syntax,
	// never "passes"), or the canary was not killed (CanaryKilled false —
	// the test never reads the file), or nothing was scored (Total == 0).
	// HONESTY NOTE: this is a DIFFERENT diagnosis from TestWriterFailed (a
	// compiling test WAS produced here), but the same rule applies —
	// ProvenMissed==0 when this is true does NOT mean "no real gaps," and
	// it must never be read as "the pool's test proved every survivor
	// either" (the inverse false claim: fabricating proof from a run that
	// graded nothing). A poolTestUnsound run is never certified — see
	// aggregate.
	PoolTestUnsound bool
	// AuthoredTestNotCollected narrows PoolTestUnsound: the run proved the
	// test command never reached the authored test's file at all. See
	// runState.authoredTestNotCollected.
	AuthoredTestNotCollected bool
	// BaselineFailed is true when the dev suite did not pass on the UNMUTATED
	// compliant code inside the jail — a build/environment failure, not a
	// test-quality verdict. When true, DevKillRate (0) and Survivors (0) are
	// meaningless: nothing was graded. The status is still needs-review
	// (fail-closed), but the readout says "could not grade" rather than
	// reporting a fabricated kill tally. See runState.baselineFailed.
	BaselineFailed bool
	// BaselineOutput is the failing baseline's OWN output, when BaselineFailed
	// is set. The refusal to grade is correct; silence about WHY is not — this
	// is what turns "a build/environment issue" into something an operator can
	// actually fix. Empty when the baseline passed, or when the runner produced
	// no output at all.
	BaselineOutput string
	// SuiteIgnoresFile is true when the dev suite passed on deliberately
	// invalid source — it provably never compiles or imports the file under
	// audit, so DevKillRate is meaningless. DISTINCT from BaselineFailed:
	// the suite is fine, the check command points somewhere else.
	SuiteIgnoresFile bool
	// TestSelection says WHICH measurement this kill rate is: the tests
	// that executed the file (Method set, Selected of Of), or the whole
	// suite (Method "", and Fallback says why under selection). Two
	// verdicts with different Methods are not comparable.
	TestSelection TestSelection
	// Concurrency discloses how many private trees scored this file at
	// once, or why it only got one. See the Concurrency type's doc.
	Concurrency Concurrency
	// Uncovered: the evidence run found no test executing this file — TRUE
	// for BOTH shapes ReasonImportOnly's own doc distinguishes (genuinely
	// dead, or executed only at import time), because BOTH withhold the
	// same way: the dev kill rate is WITHHELD by every reader (report,
	// ledger, gate) regardless of which shape it is — the survivors are
	// real, the 0.00 is not a measurement either way. Every existing
	// consumer of this flag (gating, NULL-ing the ledger's kill_rate,
	// GradedFiles' denominator) is therefore UNCHANGED by ImportOnly's
	// addition below; ImportOnly only ever REFINES which text a reader
	// prints, never whether a rate is withheld.
	Uncovered bool
	// ImportOnly is Uncovered's refinement: true when the file was ALSO
	// executed — coverage recorded real lines for it, at import/module-load
	// time (a package __init__.py, a module-level constant) — just never by
	// a test directly (pytest-cov attributes import-time execution to no
	// test's own dynamic context). Always false when Uncovered is false;
	// when Uncovered is true, distinguishes "genuinely nothing executed
	// this" (ImportOnly false — the ORIGINAL uncovered claim) from
	// "executed, just not by a test" (ImportOnly true — a DIFFERENT, honest
	// claim: reporting the latter as plain "UNCOVERED — no test executes
	// this file" is false in the sense a reader checks it against, and hits
	// on essentially every Python repo's __init__.py). Every reader that
	// PRINTS the word UNCOVERED must check this first and substitute
	// reposcan.ReasonImportOnly's exact text instead — see printWeakFile,
	// the live per-file note, and printRepoReport's NO-GRADED-FILE/
	// KILL-RATE-BREACH sections. Every reader that only GATES on Uncovered
	// (withhold the rate, fail --min-kill-rate, exclude from GradedFiles)
	// needs no change at all: ImportOnly implies Uncovered, so the
	// existing `if v.Uncovered` checks already catch it.
	ImportOnly bool
	RecordID   int64  // the signed build-record id (0 if signing skipped/failed)
	RecordHead string // the record's ledger head
	// TimedOut is true when this verdict came from RunDeadline's backstop
	// (see timeoutVerdict) rather than the pool actually converging — the
	// test-writer/shadow/critic/aggregate steps never finished. Status is
	// unconditionally StatusNeedsReview either way (a timed-out run is never
	// certified); this says WHY in a way that must ride into any report or
	// ledger row built from the verdict, so "measured, but the pool did not
	// finish" reads differently from a clean below-threshold audit — a claim
	// carries how it was earned.
	TimedOut bool
	// DevScored mirrors runState.devScored: true once the dev suite's OWN
	// kill-rate was actually measured against real mutants in the real jail.
	// On a TimedOut verdict this is the ONLY thing that makes
	// DevKillRate/Survivors/MutantsTotal real numbers rather than zero
	// values nothing ever computed. A caller must treat a TimedOut verdict
	// with DevScored==false as UNGRADABLE — never print or bank its
	// (fabricated) 0.00 kill rate as a measurement. See timeoutVerdict and
	// cmd/corral/certify_local_drive.go's bankableTimeoutVerdict.
	DevScored bool
	// PoolScored mirrors runState.poolScored, and is to ProvenMissed exactly
	// what DevScored is to DevKillRate: the only thing that separates a
	// MEASURED zero from a zero nobody computed.
	//
	// It exists because its absence threw real evidence away. A run reaches
	// tickPoolAdequacy, converges its pool score — a genuine, execution-proven
	// ProvenMissed — and only THEN stalls waiting on the test-critic, so
	// RunDeadline fires and timeoutVerdict banks it. Without this flag a
	// reader could not tell that verdict apart from one that timed out before
	// the writer ever ran, so the renderer took the pessimistic branch and
	// printed "(not run — pool did not converge)" over a number the jail had
	// actually earned. Understating a gap is the safe direction, but throwing
	// away the strongest evidence corral produces is not a good outcome.
	//
	// A caller must treat ProvenMissed on a TimedOut verdict as real ONLY
	// when this is true — and, exactly as with DevScored, must never render
	// its zero as "nothing was missed" when it is false.
	PoolScored bool
	// DevKilledMutants and DevSurvivedMutants are the mutant-level EVIDENCE
	// behind DevKillRate and Survivors: which mutants the DEV suite's own
	// tests killed, and which it did not, each carrying ParentSHA256 — the
	// per-mutant grain scan_mutants records, that a per-file kill rate
	// averages away. "Which generator produces mutants a suite does not
	// catch" is a question about mutants, and cannot be asked of
	// DevKillRate/Survivors alone. Set on every scored run (mirrors
	// BaselineDuration), empty on the could-not-grade paths where no mutant
	// was ever run.
	DevKilledMutants   []MutantRef
	DevSurvivedMutants []MutantRef
	// Timing is where this run's wall clock went, phase by phase — see the
	// Timing type. Every phase that did not run reads zero, which every
	// reader renders as "—" and the ledger stores as NULL.
	Timing Timing `json:"timing"`
	// ModelCalls is what this file's audit cost, broken out by role — the
	// money half of Timing's time half. One entry per role that made at
	// least one call; a role with zero calls (an unassigned optional seat,
	// or `--critic-model off`) has NO entry — never a zero-valued one, which
	// a reader could mistake for "ran and cost nothing". Built from each
	// role's own agentbackend.UsageMeter (see auditRoles.meters in
	// cmd/corral) and carried here so the ledger (scanstore.ModelCall) and
	// the warehouse can be built from the SAME numbers the run measured,
	// rather than re-deriving them.
	ModelCalls []ModelCall `json:"model_calls,omitempty"`
	// MutantDurationMedian and MutantDurationMax summarize how long grading
	// ONE mutant took, over the mutants that were actually graded
	// (compile-gate rejects have no duration and are excluded — see
	// adequacy.Report). They sit beside Timing rather than inside it because
	// they are not a phase: they are the SHAPE of the dev pass, and the
	// question "is this file slow because of one mutant or all of them" has
	// no other answer at the file grain. Zero when nothing timed a mutant.
	//
	// NOT a decomposition of Timing.DevPass, and the arithmetic does not
	// close: at mutant concurrency N > 1 each duration is a CONTENDED wall
	// clock measured while N-1 siblings ran beside it, so median x graded
	// routinely EXCEEDS the dev pass it happened inside. Read them as the
	// per-mutant cost distribution, never as a budget that must sum.
	//
	// `json:"-"` on both, and NOT because they are omitted: Verdict's own
	// MarshalJSON/UnmarshalJSON write them as mutant_duration_median_ms /
	// _max_ms (see verdict_wire.go). Left to encoding/json's default they
	// went out as raw NANOSECONDS under their Go names — a number no reader
	// of verdict_json outside Go could interpret, sitting beside a Timing
	// that spells its milliseconds out for exactly that reason. The tag is
	// what keeps the default rendering from appearing alongside the real one.
	MutantDurationMedian time.Duration `json:"-"`
	MutantDurationMax    time.Duration `json:"-"`
	// ChallengerAgreement is the primary writer's agreement with the
	// challenger writer over the SAME survivor set — Jaccard-over-survivors
	// and Cohen's kappa, from internal/modelcorr. nil whenever no comparable
	// pair exists: no challenger was configured, either seat's own kill
	// vector was never measured (see runState.primaryWriterMeasured /
	// shadowWriterMeasured), every comparable seat was salvaged (RULING P11
	// — a salvaged proof came from a deselected remainder the challenger got
	// no equivalent of; batched refuses the whole file, per-survivor drops
	// the salvaged seats' own survivors), or the two seats' measured sets do
	// not overlap at all.
	//
	// THE PAIR COVERS ONLY THE SURVIVORS BOTH SEATS GENUINELY ATTEMPTED, not
	// every survivor the file had. Under WriterModePerSurvivor each survivor
	// gets its own seat on each side, and a seat that exhausted its budget
	// without a grading test measured NOTHING about that survivor — for
	// either writer. Counting it would put a survivor neither side attempted
	// into the vectors as `false` on both, which Compare reads as a shared
	// blind spot, inflating SharedSurvivors and therefore Jaccard by however
	// many seats failed. Pair.Mutants is the size of the ACTUAL overlap, and
	// Pair.Sufficient follows it — a narrow overlap reports itself as
	// under-powered rather than as a confident measurement of the wrong
	// thing. In batched mode one seat per writer faces every survivor, so the
	// overlap is the whole set and nothing changes.
	// Non-nil does NOT mean the coefficients are meaningful on their own —
	// callers must still check Pair.Sufficient before reading Jaccard and
	// Pair.KappaDefined before reading Kappa, exactly as modelcorr documents.
	ChallengerAgreement *modelcorr.Pair
	// PromptShape discloses what a mutant-generator shard actually SAW:
	// "chunk" when every shard of this run showed only its own symbols'
	// bodies (see advpool's shardCode), "file" when even one shard fell back
	// to the whole file (a shard whose symbols never resolved to a signature,
	// or whose extractor never populated Lines), and "" — never fabricated —
	// on a run that predates this disclosure (an unsharded whole-file run
	// also reads "file": the model saw the whole file either way, which is
	// the same claim). A caller must never guess this from RegionsTotal or
	// any other field; it is set once, here, from the SAME shard-by-shard
	// decision the render actually made.
	PromptShape string `json:"prompt_shape,omitempty"`
}

// RunState is the observable status of one run: Converged is true once the run
// has a terminal Verdict, and Verdict is non-nil exactly when Converged is true.
type RunState struct {
	Converged bool
	Verdict   *Verdict
	// AuthoredTest is the pool's compiling killing test, when one was authored
	// (the test-writer ran because the dev suite left survivors). Empty when a
	// perfect dev suite made the test-writer moot. NOT part of the signed
	// Verdict — evidence handed back to the dev, not certified state.
	AuthoredTest string
	// AuthoredExtra is every proven authored test that could NOT be merged
	// into AuthoredTest — see Verdict.AuthoredExtra. Exposed here beside
	// AuthoredTest because a caller that prints one must print the other:
	// they are one set of proofs, split only by what the language's
	// concatenator could fold.
	AuthoredExtra []golang.AuthoredPart
	// Matrix is the tests×mutants matrix result (swarm slice 5), when the run
	// opted in (RunSpec.Matrix) and a Driver.Enumerator was wired. nil when
	// the matrix phase never ran (opted out, no Enumerator, or it hasn't
	// reached that tick yet) — a caller (the --local verdict summary, the
	// brain's matrix sink caller) must treat nil as "no matrix data",
	// never as "zero tests scored". Exported here (mirroring AuthoredTest)
	// so a caller outside the package can read the driver's internal
	// runState.matrix without a package-private accessor.
	Matrix *matrix.Result
}

// SCOPE, because reviewers read this guard as broader than it is: it compares
// the CRITIC against the WRITER and nothing else. Two facts about what it
// deliberately does NOT do:
//
//   - The mutant-generator MAY share a model with the test-writer. That
//     correlation is benign in direction: a writer sharing lineage with the
//     generator is BETTER at killing its mutants, which yields MORE proven gaps
//     and MORE needs-review. It cannot manufacture a false clean, which is the
//     only direction worth a refusal.
//
//   - It compares model NAMES, not provenance: "gemini-3.7-flash" vs
//     "gemini-3.7-pro" satisfies THIS function while sharing training lineage.
//     But corral is not blind to that, and a reviewer reading only this
//     function has twice concluded it is. Same-vendor is DETECTED and reported
//     in two places — certify_local.go warns at seat resolution ("different
//     models from the SAME provider … an independent MODEL but not an
//     independent VENDOR") and certify_adversarial.go prints it on the verdict
//     itself ("every graded seat is %s — the same lineage planted the faults
//     and graded the tests"). What it does not do is REFUSE.
//
//     That is the actual open question, and it is a product decision rather
//     than a missing check: refusing would make a single-vendor operator unable
//     to run at all, which is why it warns. internal/modelcorr separately
//     MEASURES seat agreement (Jaccard over survivor sets) and also does not
//     gate. Anyone changing this should change the warning into a refusal
//     deliberately, not "add vendor awareness" — that already exists.
//
// CheckDecorrelation rejects an assignment where test-critic and test-writer
// share a model. A test-critic judging tests written by its own model (or a
// copy of it) is not an independent check — it is the same failure mode
// grading its own homework. Enforced at driver construction (soundness #4).
func CheckDecorrelation(assign RoleAssignment) error {
	critic, writer := assign[RoleTestCritic], assign[RoleTestWriter]
	if critic != "" && critic == writer {
		return fmt.Errorf("advpool: decorrelation guard: test-critic and test-writer must not share model %q", critic)
	}
	return nil
}

// shardStat is one generator seat's recorded outcome. Region complexity is the
// DIFFICULTY CONTROL: raw yield cannot distinguish a weak model from an easy
// region, so effectiveness is read CONDITIONED on complexity, never pooled
// across it.
type shardStat struct {
	region       string
	complexity   int
	lines        int
	mutants      int
	parseRetries int
	dropped      bool
	// survived is set only on a shadowStats entry (the challenger's scored
	// outcome for this region) — the primary's survivor count is NOT tracked
	// per shard here; see the survivorIdx placement note in
	// bugCatchObservations for why the primary's is recorded differently.
	survived int
	// measured is set only on a shadowStats entry, and only once the
	// challenger seat actually PRODUCED an observation for this region —
	// either a scored mutant set or a real parse failure. It stays false when
	// the seat never finished, when its scoring errored, or when the shadow
	// budget guard skipped it. An unmeasured seat emits NO bugcatch row: a
	// zero-mutant row for a seat that never ran would be recorded as the
	// challenger producing nothing, which is a fabricated comparison, not a
	// measurement. The telemetry event is still emitted for it, carrying
	// measured=false, so the skip is visible rather than silent.
	measured bool
}

// runState is one run's mutable progress, tracked across ticks. The
// test-writer task's id (not its key) is tracked explicitly because
// SupersedeTask auto-uniquifies the replacement's key when it reuses the
// original — see the comment in Tick.
// writerAttempt is ONE survivor's writer seat under WriterModePerSurvivor:
// the live task, the test it last returned, how many repairs it has spent out
// of its own budget, and whether it ended up proving its survivor.
//
// The budget is PER SURVIVOR and equal to the per-file budget the batched mode
// gives the whole set (MaxTestWriterAttempts). That is the point of the
// fan-out: under batching one unbuildable test consumed the file's entire
// retry budget and every survivor lost its chance with it. Here a survivor's
// failures are charged to that survivor alone.
//
// `done` is the state machine's completion flag, and it is set on THREE
// terminal outcomes, not one: proven, exhausted (no compiling or grading test
// within the budget), or salvaged. The tick completes when every attempt is
// done — never when one is, and never on a count of ticks.
type writerAttempt struct {
	mutant  adequacy.Mutant
	taskID  int64
	test    string
	repairs int
	// attempts is EVERY model result this seat has consumed, success or
	// failure — unlike repairs, which counts only reissues and is
	// incremented on the final, non-reissued try by ONE of the two
	// exhaustion paths and not the other (see advanceWriterAttempt), making
	// it an unreliable stand-in for "how many tries did this seat take".
	// attempts is the honest count Verdict.WriterAttempts is built from.
	attempts int
	done     bool
	proven   bool
	// measured is this seat's own primaryWriterMeasured: its test genuinely
	// graded against its survivor (baseline passed, canary killed, something
	// scored). UNMEASURED IS NOT ZERO holds per seat exactly as it holds per
	// run — a seat whose test never ran must not contribute a "caught
	// nothing" observation.
	measured bool
	// salvaged records that this seat's proof came from a DESELECTED
	// re-score rather than a clean run. It rides up to run.writerSalvaged
	// because the challenger gets no equivalent rescue, so a salvaged proof
	// anywhere in the file confounds the head-to-head (RULING P11).
	salvaged bool
	// compiled is whether this seat ever produced a test that built at all.
	// It separates the two honest failures: testWriterFailed (nothing
	// compiled) from poolTestUnsound (something compiled and never graded).
	compiled bool
}

type runState struct {
	rs   RunSpec
	sigs []repoindex.Signature

	devScored    bool
	devKillRate  float64
	mutantsTotal int
	// mutantsInvalid counts mutants the language's compile gate rejected. They
	// are evidence about the GENERATOR, not the suite, so they are excluded
	// from mutantsTotal and reported separately rather than hidden.
	mutantsInvalid int
	// invalidReasons maps a rejected mutant's ID to what the compile checker
	// printed. Kept so the run can SAY why the exam shrank — a bare count
	// cannot distinguish a generator dropping a used import from one changing a
	// signature, and cannot be fed back to the model that made the mistake.
	invalidReasons map[string]string
	devSurvivors   []adequacy.Mutant
	// devKilled is the mutant-level counterpart to devSurvivors: the mutants
	// the dev suite's OWN tests killed (rep.Killed), reduced to MutantRef (id
	// + ParentSHA256, no source) at the point it's built, since nothing else
	// downstream needs Code for a KILLED mutant the way renderTestWriter etc.
	// need it for survivors. Set alongside devSurvivors in applyDevScore,
	// carried onto Verdict.DevKilledMutants — see that field's doc for why.
	devKilled []MutantRef
	// perMutant is adequacy.Report.PerMutant from the dev pass: what each
	// mutant was actually graded with, keyed by mutant ID. nil unless the run
	// graded per mutant (see PerMutantScorer), which is also the signal the
	// aggregate reads to decide whether TestSelection discloses per-mutant
	// stats at all — an empty exam must not read as "not graded per mutant".
	perMutant map[string]adequacy.MutantGrading
	// perMutantGraded is whether the dev pass actually handed the per-mutant
	// closure to a scorer that took it. It is the disclosure's source of
	// truth, and it is deliberately NOT `perMutant != nil`: adequacy.Score
	// leaves PerMutant empty when no mutant reached grading at all (every one
	// rejected by the compile gate), and such a run must still sign as the
	// per-mutant measurement it was rather than as a whole-selection one.
	perMutantGraded bool
	// baselineFailed is true when the dev suite did not pass on the UNMUTATED
	// compliant code inside the jail (adequacy.Report.CompliantPass=false) — a
	// build/environment failure (bad toolchain, missing dep, a shell-mangled
	// test command), NOT a test-quality verdict. When set, devKillRate is a
	// meaningless 0 and devSurvivors is empty because NOTHING was actually
	// graded, so the readout must say "could not grade", never fabricate a tally.
	baselineFailed bool
	// baselineOutput is the runner's OWN output from that failing baseline
	// (adequacy.Report.BaselineOutput). Without it, baselineFailed says only
	// THAT the suite did not pass, which is the least debuggable outcome an
	// audit can produce: the operator is told their environment is broken and
	// given nothing to act on. Carried on the verdict so the readout can print
	// the suite's own words — see renderAdvVerdict. Only meaningful when
	// baselineFailed is true.
	baselineOutput string
	// baselineDuration is how long the dev suite's compliant (unmutated) run
	// took (adequacy.Report.BaselineDuration) — set on every scored run,
	// including a failed baseline, since a suite that took 90s to fail is the
	// case an operator most needs the number for. See Verdict.BaselineDuration.
	baselineDuration time.Duration
	// suiteIgnoresFile is true when the dev suite PASSED its baseline but also
	// passed on deliberately invalid source (adequacy.Report.CanaryKilled=false):
	// the check command provably never compiles or imports the file under audit,
	// so devKillRate is a meaningless 0 and no mutant was ever graded. Kept
	// SEPARATE from baselineFailed on purpose — "your suite is broken or your
	// environment is wrong" and "your suite is fine but it is pointed somewhere
	// else" send an operator to completely different places.
	suiteIgnoresFile bool
	// mutants is the FULL merged mutant set (every shard, pre-scoring) that
	// tickDevAdequacy graded the dev suite against — retained (not just its
	// count/survivors) so tickAggregate's critic auto-refute step can
	// re-score a single flagged test against the SAME exam later.
	mutants []adequacy.Mutant

	testWriterTaskID int64

	// writerMode is the resolved mode for THIS run — RunSpec.WriterMode with
	// the empty value already folded to WriterModeBatched, so nothing below
	// has to remember which spelling means the historical shape.
	writerMode string
	// writerAttempts is the per-survivor writer state, keyed by mutant id,
	// and writerOrder fixes the iteration order to the survivor order the dev
	// pass produced. Both are empty in batched mode, where the whole file
	// shares run.testWriterTaskID and run.testWriterAttempts.
	//
	// A MAP KEYED BY MUTANT, not a slice indexed by position: a seat is
	// superseded on every repair, so its task id changes while its survivor
	// does not, and the survivor is the only stable identity in the whole
	// loop. The separate order slice exists because map iteration is random
	// and this state produces a signed count, an ordered id list and a
	// concatenated file.
	writerAttempts map[string]*writerAttempt
	writerOrder    []string
	// authoredExtra holds proven parts the language's concatenator refused to
	// merge — carried onto the verdict rather than dropped. See
	// Verdict.AuthoredExtra.
	authoredExtra []golang.AuthoredPart

	poolScored   bool
	provenMissed int

	// matrix is the tests×mutants matrix result (nil until tickMatrix runs,
	// and forever nil when RunSpec.Matrix is false or no Enumerator is
	// wired). matrixDone guards tickMatrix to run AT MOST ONCE per run — the
	// matrix is O(tests × mutants) jail runs, the most expensive phase in the
	// pipeline, and Tick may be called many times before convergence.
	matrix     *matrix.Result
	matrixDone bool

	// shadowWriter* hold the CHALLENGER writer seat's outcome. They are
	// deliberately separate from devKilled/devSurvivors: nothing here may
	// reach aggregate(), the Verdict, or the signed record. Verdict is hashed
	// WHOLE by CertSigner.SignVerdict, so a single leaked outcome field would
	// change every previously signed record's digest — see
	// writer_shadow_test.go.
	//
	// shadowWriterKilled is the challenger writer's PROVEN-KILL set over
	// run.devSurvivors — the mutant-level counterpart of provenIDs, which is
	// the PRIMARY writer's vector over that same set (RULING P9). The two pair
	// one-for-one by MutantRef.ID: both are produced by provenMutantIDs, both
	// over run.devSurvivors, so the head-to-head compares the two WRITERS.
	// Deliberately NOT paired against devKilled, which is the DEV SUITE's
	// vector over every mutant and answers a different question.
	shadowWriterKilled   []MutantRef
	shadowWriterMeasured bool
	// shadowWriterCompileRetries is the challenger's OWN compile-retry budget
	// in BATCHED mode. Sharing testWriterAttempts would let a failing
	// measurement seat exhaust the graded seat's retries and change the
	// verdict. (Under WriterModePerSurvivor each challenger seat carries its
	// own budget on its writerAttempt, exactly as the primary's does.)
	shadowWriterCompileRetries int
	// shadowWriterTaskID is the live challenger writer task's id, tracked the
	// same way testWriterTaskID is and for the same reason: SupersedeTask
	// auto-uniquifies a replacement that reuses the old key, so the seat can
	// never be re-looked-up by RoleTestWriterShadow's key after a retry.
	// 0 when the challenger is off or was never enqueued.
	shadowWriterTaskID int64
	// shadowWriterAttempts / shadowWriterOrder are the CHALLENGER's
	// per-survivor seats under WriterModePerSurvivor, mirroring
	// writerAttempts / writerOrder. They are separate state, not a shared
	// map, for the same reason every other shadow field is separate: nothing
	// here may reach aggregate(), the Verdict or the signed record.
	shadowWriterAttempts map[string]*writerAttempt
	shadowWriterOrder    []string
	// shadowWriterDone is set once every challenger seat is terminal, so the
	// pass stops re-entering after it has recorded what it could.
	shadowWriterDone bool
	// shadowWriterSpent is the CUMULATIVE wall-clock this run has credited
	// back to the deadline clock for challenger-writer work. runShadowWriterPass
	// may be entered on several ticks (unlike runShadowPass, which runs once),
	// so the credit is capped in aggregate at ShadowTimeBudget rather than
	// per-call — otherwise repeated entries could extend the primary's
	// deadline without bound.
	shadowWriterSpent time.Duration

	// testWriterAttempts counts compile-failure reopens of the test-writer
	// task, guarded against MaxTestWriterAttempts in tickPoolAdequacy. Once
	// exhausted, testWriterFailed is set and the run converges instead of
	// reopening again.
	testWriterAttempts int
	// testWriterFailed is set when the test-writer exhausted
	// MaxTestWriterAttempts without producing a compiling test. The run still
	// converges (poolScored=true, provenMissed=0), but ProvenMissed=0 here does
	// NOT mean "no real gaps" — it means "gaps found (Survivors > 0), killing
	// test not authored." Carried onto the signed Verdict (TestWriterFailed) so
	// the CLI/cockpit can say so honestly instead of implying a clean suite.
	testWriterFailed bool
	// poolTestUnsound is set when the pool's authored test compiled (so
	// testWriterFailed is false) but its scoring report did not genuinely
	// grade (CompliantPass/CanaryKilled false, or Total 0) — see
	// Verdict.PoolTestUnsound's doc for the full honesty rule this carries
	// onto the signed verdict.
	poolTestUnsound bool
	// authoredTestNotCollected narrows poolTestUnsound to its most common
	// cause: the test command never collected the authored test's own file.
	// Carried separately because the two fixes are different -- "your test
	// asserts something untrue" vs "your command does not run that file" --
	// and the second is nearly always a command pinned to a single path while
	// the authored test is a NEW file beside the developer's.
	authoredTestNotCollected bool

	// shardRetries counts parse failures per mutant-generator task KEY (never
	// its id). Keying by key is deliberate: a lease-expiry re-claim and a
	// parse-failure reopen must draw on the SAME budget, or a shard could
	// retry forever by alternating failure modes.
	shardRetries map[string]int
	// droppedKeys is the set of mutant-generator task keys already recorded in
	// droppedRegions. tickDevAdequacy re-runs its whole scan on EVERY tick
	// until the run is devScored (Tick re-calls it unconditionally), and two
	// paths return an error AFTER a drop is recorded (the all-regions-failed
	// guard, and a transient Scorer error) — both are ordinary, expected
	// re-entry, not a fresh drop. Without this set, re-entry would re-append
	// the same region to droppedRegions on every subsequent tick, corrupting
	// the signed counts (unbounded slice growth, a shortfall message whose
	// count inflates forever). Keyed by task key, same as shardRetries.
	droppedKeys map[string]bool
	// droppedRegions names the shards abandoned after exhausting their retry
	// budget — the coverage shortfall, carried into the signed verdict so a
	// partial audit is provably partial rather than silently partial. Each
	// entry is recorded exactly once, guarded by droppedKeys.
	droppedRegions []string
	// dupMutants is how many generated mutants were byte-identical edits of
	// another and were collapsed before scoring (see adequacy.DedupeMutants).
	// Disclosed on the verdict so the graded denominator can be reconciled
	// against what the generator actually emitted.
	dupMutants   int
	regionsTotal int
	// regionsProbed counts the regions that actually contributed at least one
	// mutant to the union scored against the dev suite — NOT regionsTotal
	// minus len(droppedRegions), which would over-report a shard that parsed
	// cleanly but produced zero mutants as "probed" when it never contributed
	// anything to the exam. Recomputed fresh on every tickDevAdequacy pass
	// (deterministic over the same task results), so re-entry is safe.
	regionsProbed int
	// shardSymbols maps a mutant-generator task key to the qualified symbols
	// that shard was aimed at (Shard.Symbols), captured once at StartRun. Used
	// so a dropped region is recorded in droppedRegions by the SYMBOLS it left
	// unprobed (e.g. "A, B") rather than the task-UI title string ("Generate
	// mutants for A, B") — a signed verdict is evidence, not a task list, and
	// should read like the former. Empty/absent for an unsharded run's single
	// bare-keyed task, which falls back to its Title.
	shardSymbols map[string][]string
	// promptShape is Verdict.PromptShape, computed once at StartRun from the
	// SAME ShardSymbols call BuildDAG made internally (mirroring
	// shardSymbols/shardStats): "chunk" when every shard's signatures were
	// signaturesChunkable, "file" when any fell back or the run had no
	// shards at all (PresetMutants replays a fixed exam and generates
	// nothing, so it stays "").
	promptShape string

	// shardStats is per-shard generation outcome, keyed by shard index — the
	// metrics substrate. Recorded per shard and NEVER summed: summing collapses
	// N seats into one row and makes an underperforming seat invisible by
	// construction.
	shardStats map[int]shardStat

	// shadowStats mirrors shardStats but for the CHALLENGER seats (Task 6):
	// keyed by the SAME shard index, seeded with the SAME region/complexity/
	// lines in StartRun — the whole point of a shadow run is that both models
	// are graded on identical regions, not a second independent partition.
	// Populated by the shadow pass in tickDevAdequacy; empty when
	// rs.ShadowModel == "" (no change to any pre-existing run's behavior).
	shadowStats map[int]shardStat

	// testComplexity is the dev suite's complexity — the SECOND conditioning
	// axis (a model that only wins against naive suites is a different
	// proposition from one that wins against rigorous ones).
	//
	// FILE-granular by necessity: attributing a specific test to a specific
	// region requires knowing which tests exercise which code, which is exactly
	// what the slice-5 tests-x-mutants matrix establishes by execution. Any
	// per-region test-complexity claim would be unproven until then.
	testComplexity int

	// provenIDs are the ids of the survivors the authored test actually
	// killed — the evidence behind provenMissed, carried onto the Verdict and
	// into the scan ledger so a later query can tell WHICH gaps were proven
	// (and, on a "tried and missed", that the attempt genuinely happened and
	// caught nothing). Empty on every path that did not grade.
	provenIDs []string
	// primaryWriterMeasured is true only once the PRIMARY writer's suite has
	// genuinely graded against run.devSurvivors — the positive counterpart of
	// shadowWriterMeasured, and for exactly the same reason: UNMEASURED IS NOT
	// ZERO.
	//
	// run.provenIDs is nil on every path that did not grade, and two of those
	// paths reach a signed verdict: testWriterFailed (no compiling test after
	// MaxTestWriterAttempts) and poolTestUnsound (a suite that compiled but
	// whose canary survived / whose file the command never reached / that
	// scored nothing). Reading provenIDs on either path writes EVERY survivor
	// as `survived` for the primary and manufactures a total blind spot for a
	// seat that never ran the code — the same fabrication the challenger's
	// `measured` flag has always refused.
	//
	// A POSITIVE flag, deliberately, rather than a growing list of negatives:
	// a new non-grading path added later is unmeasured by DEFAULT and cannot
	// silently start fabricating a vector because nobody remembered to extend
	// the exclusion list.
	primaryWriterMeasured bool
	// writerSalvaged is true when ANY of the primary writer's provenIDs came
	// from a DESELECTED re-score rather than a clean run. The challenger seat
	// has no equivalent rescue, so a salvaged proof is confounded in the
	// primary's favour and must not enter a comparison.
	//
	// FILE-WIDE, which is the right grain only in batched mode — one seat
	// there covers every survivor. Under the fan-out consult
	// primarySeatSalvaged instead: this flag is true as soon as one of a
	// file's many seats needed the rescue, and treating that as a fact about
	// the file discards every clean pair beside it.
	writerSalvaged bool
	// authoredTest is the pool's compiling killing test (the test-writer's
	// cleaned source), surfaced via RunState so `corral certify --adversarial`
	// can hand it back to the dev ("add this test; it catches the gap your suite
	// missed"). Evidence, deliberately NOT folded into the signed Verdict digest
	// — kept as run status, per the reasoning-trace design's non-goals.
	authoredTest string

	// testWriterMoot is set when a perfect dev suite (0 survivors) skipped the
	// test-writer entirely: the assigned model never ran, so it must NOT be fed
	// to the leaderboard as a failure for a task it never attempted.
	testWriterMoot bool

	verdict *Verdict

	// mutantDurationMedian/Max are how long grading ONE mutant took, from
	// the dev pass's own report (adequacy.Report). They summarize the dev
	// pass's shape at the file grain — one slow mutant or forty ordinary
	// ones — which the per-mutant rows can answer only in aggregate.
	mutantDurationMedian time.Duration
	mutantDurationMax    time.Duration

	// timing is what this run has spent so far, phase by phase (see Timing).
	// Filled in at the DAG's own boundaries — the same points emit() already
	// fires at — and copied onto both the converged verdict and the
	// timed-out one, because a run that stalled still spent what it spent.
	timing Timing
	// phaseStart is when each still-open phase began, keyed by the phase
	// names below. A phase with no entry never started, which is how a
	// duration stays ABSENT (and the ledger stores NULL) instead of being
	// computed from a zero time.Time.
	phaseStart map[string]time.Time

	// startedAt is the run's start time (from Driver.Now, set in StartRun),
	// used by the RunDeadline backstop below. It is advanced by exactly the
	// wall-clock time runShadowPass spends, so the deadline measures PRIMARY
	// elapsed time only — shadow measurement can never push a would-be
	// certified run into a needs-review timeout. Only the tick goroutine
	// touches it.
	startedAt time.Time
}

// Driver runs one or more adversarial-pool runs' tick state machines over
// injected effects: a real *queue.Store (cheap local SQLite — the same
// substrate the mission engine drives directly, see internal/mission.Engine)
// plus a Scorer and Validator. It has no jail/brain/LLM wiring of its own —
// that's Task 4.3 (real Scorer/Validator) and 5.1 (brain integration); this
// driver is pure and fully unit-testable with fakes.
type Driver struct {
	Q         *queue.Store
	Scorer    Scorer
	Validator Validator
	Assign    RoleAssignment

	// Signer and Leaderboard are the terminal-verdict effects (Task 4.3):
	// optional (nil = skipped) so every pre-existing Driver test keeps
	// working unwired. Phase 5 wires the real certify-chain Signer and
	// leaderboard PerformanceTracker sink. When set, Signer.SignVerdict is
	// called on every terminal verdict (certified or needs-review — a
	// needs-review run may still get a signed needs-review record, just
	// never a "certified"-status one past the gate); Leaderboard.Record is
	// only ever called AFTER SignVerdict returns successfully, never before
	// the deterministic score + sign (soundness #5).
	Signer      Signer
	Leaderboard LeaderboardSink

	// BugCatch is the optional per-run bug-catching feed (nil = no-op),
	// fed AFTER Signer (once RecordID/RecordHead are set) on every terminal
	// verdict — certified AND needs-review, unlike Leaderboard which only
	// fires on certified. See bugCatchObservations.
	BugCatch BugCatchSink

	// CriticFindings is the optional per-run critic-finding auto-adjudication
	// feed (nil = no-op), mirroring BugCatch: fed AFTER Signer (once
	// RecordID/RecordHead are set) on every terminal verdict. See
	// tickAggregate's auto-refute step.
	CriticFindings CriticFindingSink

	// MutantAttempts is the optional per-run feed of BOTH writer seats'
	// per-mutant outcomes (nil = no-op), mirroring BugCatch/CriticFindings.
	// Fed pair-or-nothing by recordMutantAttempts: see its doc for the full
	// gating (challenger configured AND measured, per-seat graded, and the
	// primary's proof for that survivor not salvaged).
	MutantAttempts MutantAttemptSink

	// Enumerator is the optional jail-backed test-list seam (nil = the matrix
	// phase is always skipped, regardless of any run's RunSpec.Matrix). When
	// set AND a run opts in, tickMatrix uses it to enumerate the dev suite's
	// individual tests before scoring each against the run's mutants.
	Enumerator TestEnumerator

	// Matrix is the optional per-run tests×mutants matrix feed (nil = no-op),
	// fed AFTER Signer (once RecordID/RecordHead are set), mirroring
	// CriticFindings/BugCatch — same RecordID!=0 guard, same reasoning.
	Matrix MatrixSink

	// MatrixWorkers bounds the matrix phase's concurrent jail scoring calls.
	// <= 0 defaults to matrixDefaultWorkers. Each ScoreReport/Enumerate call
	// runs in its OWN disposable os.MkdirTemp workspace with no shared
	// mutable state (confirmed against bwrapJail — see jail.go), so scoring
	// concurrently is safe the same way --swarm's concurrent workers are.
	MatrixWorkers int

	// Events is the optional reasoning-event sink (nil = no-op), mirroring
	// Signer/Leaderboard: every pre-existing Driver test keeps working
	// unwired. When set, the driver emits every beat named on EventSink's own
	// doc via the d.emit helper.
	Events EventSink

	// MutantSink, when set, is handed the exact mutant set the dev pass
	// GRADED — one call per run, right after that pass scores, with the
	// mutants the compile gate rejected already removed. nil (the brain's
	// position, and every pre-existing caller's) records nothing.
	//
	// Only the GRADED set is offered, deliberately: the point of recording a
	// set is to be able to REPLAY it, and a replay of a mutant that the
	// language's own compile check refuses would grade nothing while
	// pretending to be the same exam. What gets written back has to be what
	// was actually measured.
	//
	// Fed the preset mutants unchanged on a replayed run, so re-recording a
	// replay is a fixed point rather than a slow drift.
	MutantSink func(codePath string, ms []adequacy.Mutant)

	// Threshold is the minimum DevKillRate a run may be auto-certified at;
	// below it (or with any blocking finding open) the run is routed to
	// needs-review — the human gate never auto-certifies a weak dev suite.
	Threshold float64

	// BlockSeverity is the lowest open-finding severity that forces
	// needs-review at aggregate time (mirrors mission.Engine's
	// ConvergeBlockSeverity). "" disables the findings gate. Default "high".
	BlockSeverity string

	// NoProgressTicks is the give-up backstop: Tick returns an error once a
	// run has shown no forward progress for this many consecutive ticks
	// while nothing is claimed. 0 disables it. Default 240 (mirrors
	// mission.Engine.NoProgressTicks).
	NoProgressTicks int

	// Now returns the current time; injected so the deadline logic below is
	// pure/unit-testable with a fake clock (the driver/store convention
	// forbids a bare time.Now() call in the pure logic). Defaulted to
	// time.Now in NewDriver when left nil.
	Now func() time.Time

	// RunDeadline is the wall-clock backstop checkNoProgress can't be: it
	// explicitly stands down while any task is StatusClaimed ("slow is not
	// stuck"), so a claimed-but-wedged task would otherwise stall a run
	// forever. 0 disables it. When a run's wall-clock age (Now() minus
	// startedAt) exceeds RunDeadline before convergence, Tick converges the
	// run to a signed needs-review verdict noting the timeout — honest and
	// terminal, so the CLI gets an answer and the single active slot frees.
	// A sane non-zero default is set in the brain wiring (StartAdversarialPool).
	RunDeadline time.Duration

	// mu guards the runs map lookups and each run's verdict pointer against
	// concurrent RunStatus callers (the get_adversarial_run MCP handler runs
	// on a different goroutine than the tick loop). It is NEVER held across
	// slow work (Q.Enqueue/Q.List/Scorer.Score/tick helpers) — only around a
	// map op or the verdict read/write. noProgress/lastFingerprint are not
	// guarded: only the single tick goroutine touches them.
	mu sync.Mutex

	runs            map[int64]*runState
	noProgress      map[int64]int
	lastFingerprint map[int64]string
}

// NewDriver constructs a Driver for the given assignment, rejecting a
// decorrelated (test-critic == test-writer model) assignment up front so no
// run can ever be started under it.
func NewDriver(q *queue.Store, scorer Scorer, validator Validator, assign RoleAssignment, threshold float64) (*Driver, error) {
	if err := CheckDecorrelation(assign); err != nil {
		return nil, err
	}
	if threshold <= 0 {
		return nil, fmt.Errorf("advpool: threshold must be > 0 (got %v) — a non-positive threshold would auto-certify any dev suite, defeating the human gate", threshold)
	}
	d := &Driver{
		Q: q, Scorer: scorer, Validator: validator, Assign: assign,
		Threshold:       threshold,
		BlockSeverity:   "high",
		NoProgressTicks: 240,
		runs:            map[int64]*runState{},
		noProgress:      map[int64]int{},
		lastFingerprint: map[int64]string{},
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d, nil
}

// emit forwards a reasoning-milestone event to d.Events, no-op when it is
// nil (the default — every pre-existing Driver test keeps working unwired).
func (d *Driver) emit(missionID int64, kind, subject string, detail map[string]any) {
	if d.Events != nil {
		d.Events.Emit(missionID, kind, subject, detail)
	}
}

// survivorIDs returns the Mutant.ID of each survivor, for the
// pool_dev_adequacy event — NOT the mutant source, which is recoverable from
// the mutant-generator task's Result if ever needed.
func survivorIDs(survivors []adequacy.Mutant) []string {
	ids := make([]string, len(survivors))
	for i, m := range survivors {
		ids[i] = m.ID
	}
	return ids
}

// StartRun enqueues a run's DAG (BuildDAG(rs, d.Assign, sigs)) under missionID
// and begins tracking its progress. missionID is caller-supplied (Phase 5
// wires it to a real mission.Store id); the driver has no mission package of
// its own, mirroring the RepoOps/Indexer decoupling pattern elsewhere in the
// codebase.
// The phase names runState.phaseStart is keyed by. Constants rather than
// string literals at four call sites: a phase whose start is filed under a
// typo never ends, and its duration is silently absent — which is
// indistinguishable, in the record, from a phase that never ran.
const (
	phaseGeneration = "generation"
	phaseDevPass    = "dev-pass"
	phaseAuthored   = "authored-pass"
	phaseCritic     = "critic"
)

// now is d.Now with a fallback. Every production Driver comes from
// NewDriver, which fills the field in — but a hand-built Driver (several
// tests) has a nil Now, and a timing read must never be the thing that
// panics a verdict path.
func (d *Driver) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// beginPhase records that a phase started NOW, unless it already has. Every
// tick* function is re-entered on later ticks, so an unguarded write would
// keep resetting the start and report only the final tick's slice of a phase
// that took an hour.
func (d *Driver) beginPhase(run *runState, phase string) {
	if run.phaseStart == nil {
		run.phaseStart = map[string]time.Time{}
	}
	if _, open := run.phaseStart[phase]; !open {
		run.phaseStart[phase] = d.now()
	}
}

// endPhase closes a phase and returns what it spent, or 0 if it never
// started. Zero is the honest answer for a phase that did not happen, and
// every reader treats it as "not measured" rather than "free".
func (d *Driver) endPhase(run *runState, phase string) time.Duration {
	start, open := run.phaseStart[phase]
	if !open {
		return 0
	}
	delete(run.phaseStart, phase)
	return d.now().Sub(start)
}

// attributeOpenPhases closes every phase still open in run.phaseStart and
// credits it with the wall clock it spent, for a run that stops WITHOUT ever
// reaching the ordinary tick-boundary code that closes a phase the moment it
// converges (see the phaseAuthored/phaseCritic closes in Tick and
// tickPoolAdequacy).
//
// This exists because a phase that RAN must never render as one that did
// not: issue #201 measured a per-survivor writer fan-out running for roughly
// an hour (compiling, failing on the unmutated code, reissued) while
// timeoutVerdict left Timing.AuthoredPass at its zero value because
// beginPhase had opened it but nothing had ever called endPhase — the exact
// rendering timingLine reserves for "did not run". A phase genuinely never
// reached (phaseStart never carries its key — e.g. the critic when the
// writer stalled before promoting it) is untouched here and correctly stays
// at zero.
func (d *Driver) attributeOpenPhases(run *runState) {
	if a := d.endPhase(run, phaseAuthored); a > 0 {
		run.timing.AuthoredPass += a
	}
	if c := d.endPhase(run, phaseCritic); c > 0 {
		run.timing.Critic += c
	}
}

func (d *Driver) StartRun(missionID int64, rs RunSpec, sigs []repoindex.Signature) error {
	d.mu.Lock()
	_, exists := d.runs[missionID]
	d.mu.Unlock()
	if exists {
		return fmt.Errorf("advpool: run already started for mission %d", missionID)
	}
	specs := BuildDAG(rs, d.Assign, sigs)
	if err := d.Q.Enqueue(missionID, specs); err != nil {
		return fmt.Errorf("advpool: enqueue: %w", err)
	}
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return fmt.Errorf("advpool: list after enqueue: %w", err)
	}
	var twID int64
	for _, t := range tasks {
		if t.Key == RoleTestWriter {
			twID = t.ID
		}
	}
	if twID == 0 {
		return fmt.Errorf("advpool: test-writer task not found after enqueue")
	}
	// Capture the shard→symbols map once, from the SAME ShardSymbols call
	// BuildDAG makes internally, so a dropped shard's coverage-shortfall entry
	// can name the functions it left unprobed (M-2) instead of the task-UI
	// title string.
	// A preset run has no generator seats (see BuildDAG), so it has no shards
	// either: the shard bookkeeping below describes regions that were probed
	// by a model, and a replayed set was not probed by this run at all.
	// Seeding it anyway would emit per-shard telemetry for seats that never
	// existed — a positive claim about work nobody did.
	var shards []Shard
	if rs.PresetMutants == nil {
		shards = ShardSymbols(sigs, rs.MaxShards)
	}
	shardSymbols := make(map[string][]string, len(shards))
	// shardStats seeds the metrics substrate with each shard's difficulty
	// control (region/complexity/lines), computed once here from the SAME
	// ShardSymbols call BuildDAG used — never a second source of truth for
	// what a region contains.
	stats := make(map[int]shardStat, len(shards))
	// shadowStats is seeded with the SAME region/complexity/lines as stats —
	// the challenger is graded on IDENTICAL regions, not a second partition,
	// which is the entire point of a shadow run (see RoleMutantGeneratorShadow).
	// Left nil (never seeded) when no shadow model is configured, so an
	// ordinary sharded run's bugCatchObservations emits zero shadow rows —
	// exactly its pre-Task-6 behavior.
	var shadowStats map[int]shardStat
	if strings.TrimSpace(rs.ShadowModel) != "" {
		shadowStats = make(map[int]shardStat, len(shards))
	}
	for _, sh := range shards {
		shardSymbols[ShardTaskKey(sh.Index)] = sh.Symbols
		seed := shardStat{
			region: strings.Join(sh.Symbols, ", "), complexity: sh.Complexity, lines: sh.Lines,
		}
		stats[sh.Index] = seed
		if shadowStats != nil {
			shadowStats[sh.Index] = seed
		}
	}

	// promptShape is the disclosure a signed verdict carries about what a
	// generator shard actually SAW — "chunk" only when EVERY shard's own
	// signatures resolved to a real Lines span (ShardIsChunked, the same
	// rule renderMutantGeneratorShard applied when it actually rendered),
	// "file" the moment any shard fell back, and "" (never fabricated) for
	// a preset run that never dispatched a generator seat at all.
	promptShape := ""
	if rs.PresetMutants == nil {
		promptShape = "file"
		if len(shards) > 0 {
			promptShape = "chunk"
			for _, sh := range shards {
				if !ShardIsChunked(sigs, sh) {
					promptShape = "file"
					break
				}
			}
		}
	}

	// testComplexity is the dev suite's own complexity — see the runState
	// field comment. A parse failure here (an unsupported/unparseable dev
	// test) is not fatal to the run: the conditioning axis is best-effort
	// telemetry, not a gate, so it is simply left at its zero value.
	testComplexity := 0
	if testSigs, terr := repoindex.ExtractSignatures(rs.DevTestCode, rs.Lang); terr == nil {
		for _, s := range testSigs {
			testComplexity += s.Complexity
		}
	}

	d.mu.Lock()
	run := &runState{
		rs: rs, sigs: sigs, testWriterTaskID: twID, startedAt: d.Now(),
		shardRetries:   map[string]int{},
		droppedKeys:    map[string]bool{},
		shardSymbols:   shardSymbols,
		promptShape:    promptShape,
		shardStats:     stats,
		shadowStats:    shadowStats,
		testComplexity: testComplexity,
		phaseStart:     map[string]time.Time{},
		writerMode:     resolvedWriterMode(rs),
		writerAttempts: map[string]*writerAttempt{},
	}
	// GENERATION starts the moment its seats are enqueued and claimable —
	// the model's thinking time is the phase, and the driver only ever sees
	// the result. A preset (--mutants) run dispatches no generator at all,
	// so the phase never starts and its duration stays absent rather than
	// being recorded as an instant generation that never happened.
	if rs.PresetMutants == nil {
		run.phaseStart[phaseGeneration] = run.startedAt
	}
	d.runs[missionID] = run
	d.mu.Unlock()
	d.emit(missionID, "pool_subject", rs.CodePath, map[string]any{
		"goal": rs.Goal, "code": rs.Code, "dev_test_code": rs.DevTestCode,
		"code_path": rs.CodePath, "dev_test_path": rs.DevTestPath,
	})
	// PHASE_POOL: the workspace substrate's copy of the checkout plus its
	// concurrency probe (RunSpec.PoolDuration) — the driver never measures
	// this itself (it happens before the driver is even constructed, see
	// RunSpec.PoolDuration's doc), so it is reported here as the one
	// phase-boundary event for it. Zero (jail substrate, or a pool of one)
	// means the phase did not run, and emitting nothing is the honest
	// disclosure — never a false "ran in 0ms".
	if rs.PoolDuration > 0 {
		d.emit(missionID, "phase_pool", rs.CodePath, map[string]any{
			"duration_ms": rs.PoolDuration.Milliseconds(),
		})
	}
	return nil
}

// RunStatus reports whether missionID's run has converged, and its Verdict if
// so. found is false when the driver has no such run. A run is retained in
// d.runs after convergence (never deleted), so a converged verdict stays
// queryable after the runtime frees the active slot — which is exactly when a
// caller polls for it. Safe to call concurrently with Tick (guarded by d.mu).
// resolvedWriterMode folds RunSpec.WriterMode's empty value to the historical
// batched shape, in ONE place, so no branch below has to re-decide what an
// unset mode means. The CLI resolves its own default to "per-survivor" before
// building a RunSpec (see ResolveWriterMode); a caller that never named a mode
// keeps exactly the run it has always had.
func resolvedWriterMode(rs RunSpec) string {
	if strings.TrimSpace(rs.WriterMode) == WriterModePerSurvivor {
		return WriterModePerSurvivor
	}
	return WriterModeBatched
}

func (d *Driver) RunStatus(missionID int64) (RunState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	run, ok := d.runs[missionID]
	if !ok {
		return RunState{}, false
	}
	return RunState{Converged: run.verdict != nil, Verdict: run.verdict,
		AuthoredTest: run.authoredTest, AuthoredExtra: run.authoredExtra, Matrix: run.matrix}, true
}

// Tick advances one run given the current task states. It returns a non-nil
// Verdict once the run has converged (test-critic done AND pool-adequacy
// scored); otherwise it returns (nil, nil) and progress is left for the next
// tick. It is pure over the injected Scorer/Validator/queue.Store — no
// hidden clock, no goroutines.
//
// The tick logic mirrors the mission-engine promote/gate pattern
// (internal/mission.Engine.Tick), re-pointed at the pool's three-role DAG:
//  1. PromoteReady.
//  2. mutant-generator done -> parse + score the DEV's own tests -> promote
//     test-writer re-rendered with the real survivors.
//  3. test-writer done -> validate (compiles) + score the pool's test against
//     the survivors -> ProvenMissed.
//  4. test-critic done AND pool-adequacy done -> aggregate -> Verdict, gated
//     by the human gate (blocking finding or below-threshold DevKillRate).
//  5. No-progress backstop: a stalled run fails.
func (d *Driver) Tick(ctx context.Context, missionID int64) (*Verdict, error) {
	d.mu.Lock()
	run, ok := d.runs[missionID]
	var existing *Verdict
	if ok {
		existing = run.verdict
	}
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("advpool: unknown run for mission %d (call StartRun first)", missionID)
	}
	if existing != nil {
		return existing, nil
	}

	// Wall-clock deadline: the backstop checkNoProgress can't be, since it
	// stands down whenever any task is claimed ("slow is not stuck"). On
	// exceed, converge to a signed needs-review verdict now — honest and
	// terminal — so the CLI gets an answer and the slot frees.
	if d.RunDeadline > 0 && d.Now().Sub(run.startedAt) > d.RunDeadline {
		v := d.timeoutVerdict(run)
		if d.Signer != nil {
			recordID, head, serr := d.Signer.SignVerdict(ctx, v)
			if serr != nil {
				return nil, fmt.Errorf("advpool: sign timeout verdict: %w", serr)
			}
			v.RecordID, v.RecordHead = recordID, head
		}
		d.emit(missionID, "pool_verdict", v.Commit, map[string]any{
			"status": v.Status, "dev_kill_rate": v.DevKillRate, "mutants_total": v.MutantsTotal,
			"survivors": v.Survivors, "proven_missed": v.ProvenMissed, "models_by_role": v.ModelsByRole,
			"record_id": v.RecordID, "record_head": v.RecordHead,
		})
		d.mu.Lock()
		run.verdict = &v
		d.mu.Unlock()
		return &v, nil
	}

	if _, err := d.Q.PromoteReady(missionID); err != nil {
		return nil, fmt.Errorf("advpool: promote: %w", err)
	}

	if !run.devScored {
		if err := d.tickDevAdequacy(ctx, missionID, run); err != nil {
			return nil, err
		}
	}

	if run.devScored && !run.poolScored {
		if err := d.tickPoolAdequacy(ctx, missionID, run); err != nil {
			return nil, err
		}
		if run.poolScored {
			// Closed HERE rather than inside tickPoolAdequacy: that function
			// sets poolScored at five different points (unsound, writer
			// exhausted, graded, and the two early converge paths), and a
			// phase closed at four of five is a phase that silently reports
			// nothing on the fifth.
			if a := d.endPhase(run, phaseAuthored); a > 0 {
				run.timing.AuthoredPass = a
				d.emit(missionID, "phase_authored_pass", run.rs.CodePath, map[string]any{
					"duration_ms": a.Milliseconds(),
				})
			}
			// And the CRITIC's clock starts: the critic seat runs in
			// parallel with everything above, so what it COSTS this run is
			// the time the run now waits on it before it can aggregate. A
			// run with no critic assigned (`--critic-model off`) seeds no
			// critic task, so the phase never starts and reads as absent
			// rather than as a critic that answered instantly.
			if strings.TrimSpace(d.Assign[RoleTestCritic]) != "" {
				d.beginPhase(run, phaseCritic)
			}
		}
	}

	if run.poolScored && !run.matrixDone {
		d.tickMatrix(ctx, run)
	}

	if run.poolScored {
		v, err := d.tickAggregate(ctx, missionID, run)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
	}

	if err := d.checkNoProgress(missionID); err != nil {
		return nil, err
	}
	return nil, nil
}

// applyDevScore grades the dev suite against `mutants` and records the result
// on run. It reads ScoreReport (not Score) so the two could-not-grade cases
// arrive as their own flags instead of being inferred from the tally: the old
// inference (killRate==0 && len(survivors)==0) cannot tell a failed baseline
// from a suite that never reads the file, because BOTH return early with an
// empty report — and conflating them sends an operator to debug the wrong
// thing entirely.
func applyDevScore(ctx context.Context, run *runState, scorer Scorer, mutants []adequacy.Mutant, sink func(string, []adequacy.Mutant)) error {
	// DevCommand, not rs.TestCmd: the run's command narrowed to the tests
	// that execute this file. The scorer no longer does it — see DevCommand
	// for why the caller has to be the one that means it.
	//
	// Per mutant when the scorer can and the run has line evidence to narrow
	// by — see scoreDevReport, which owns that choice for both this pass and
	// the shadow one, so the two can never disagree about what exam was set.
	rep, perMutant, serr := scoreDevReport(ctx, scorer, run.rs, mutants)
	if serr != nil {
		return fmt.Errorf("advpool: score dev tests: %w", serr)
	}
	// Whether the run was GRADED per mutant — recorded from the call, not
	// inferred from rep.PerMutant, which is empty on a run whose every mutant
	// the compile gate rejected. Inferring it there would sign such a run as
	// an ordinary whole-selection measurement, which is not what it was.
	run.perMutantGraded = perMutant
	run.baselineFailed = !rep.CompliantPass
	run.baselineDuration = rep.BaselineDuration
	// Keep the runner's own words on a FAILING baseline. certify --repo has
	// printed these since the day two paid audits dead-ended here with nothing
	// to go on; certify --local computed the same string and discarded it, so
	// the single most common first-run failure was undiagnosable.
	if !rep.CompliantPass {
		run.baselineOutput = rep.BaselineOutput
	}
	// Gated on CompliantPass: when the baseline itself failed, adequacy.Score
	// returns BEFORE running the canary, so CanaryKilled is false for a reason
	// that is not "the suite ignores this file". Only a suite that PASSED its
	// baseline and then also passed on invalid source has proven it never
	// compiles or imports the file.
	run.suiteIgnoresFile = rep.CompliantPass && !rep.CanaryKilled
	run.devScored = true
	run.devKillRate = rep.KillRate()
	// The GRADED count: emitted minus what the compile gate rejected. Reporting
	// the emitted count would print "killed 10 of 13" beside a rate computed
	// over 9 — the same inflation the gate removes, moved into the summary line.
	//
	// Derived from the mutant SLICE (minus invalids), not from rep.Total, to
	// preserve the driver's standing invariant that its mutant total comes from
	// the exam it assembled rather than from a scorer's self-report — the same
	// soundness-#1 reflex that keeps DevKillRate off a worker's word.
	run.mutantsInvalid = len(rep.Invalid)
	run.invalidReasons = rep.InvalidReasons
	run.mutantsTotal = len(mutants) - run.mutantsInvalid
	// What each mutant was actually graded with, kept so the verdict's mutant
	// refs can carry it. Empty both on a run that graded every mutant with
	// the same command AND on a per-mutant run with nothing left to grade —
	// run.perMutantGraded above, not this map, is what tells those apart.
	run.perMutant = rep.PerMutant
	run.mutantDurationMedian = rep.MutantDurationMedian
	run.mutantDurationMax = rep.MutantDurationMax
	run.devSurvivors = survivorsFrom(rep, mutants)
	run.devKilled = toMutantRefsWith(killedFrom(rep, mutants), rep.PerMutant)
	run.mutants = mutants
	// Handed the GRADED set, once, at the only point in the run where it is
	// both complete and known-gradable. Placed here rather than at the
	// caller so the recorded set can never be assembled from a different
	// slice than the one the kill rate was computed over.
	if sink != nil {
		invalid := make(map[string]bool, len(rep.Invalid))
		for _, id := range rep.Invalid {
			invalid[id] = true
		}
		graded := make([]adequacy.Mutant, 0, len(mutants))
		for _, m := range mutants {
			if !invalid[m.ID] {
				graded = append(graded, m)
			}
		}
		sink(run.rs.CodePath, graded)
	}
	return nil
}

// survivorsFrom maps a report's surviving mutant IDs back to the mutants they
// name, preserving the report's order. IDs with no matching mutant are
// dropped: a survivor the caller cannot produce the code for is not evidence.
func survivorsFrom(rep adequacy.Report, mutants []adequacy.Mutant) []adequacy.Mutant {
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	survivors := make([]adequacy.Mutant, 0, len(rep.Survived))
	for _, id := range rep.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}
	return survivors
}

// killedFrom is survivorsFrom's counterpart: it maps a report's KILLED mutant
// ids back to the mutants they name, preserving the report's order. Together
// with survivorsFrom this is the mutant-level evidence behind
// Verdict.DevKilledMutants/DevSurvivedMutants — a per-file kill rate averages
// this away; scan_mutants needs it at the mutant grain.
func killedFrom(rep adequacy.Report, mutants []adequacy.Mutant) []adequacy.Mutant {
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	killed := make([]adequacy.Mutant, 0, len(rep.Killed))
	for _, id := range rep.Killed {
		if m, ok := byID[id]; ok {
			killed = append(killed, m)
		}
	}
	return killed
}

// salvageByDeselect tries to rescue a partially-broken authored test: read the
// runner's own failing-test selectors out of the clean-code failure, deselect
// exactly those, and re-score with what remains.
//
// Returns ok=false — and changes nothing — unless the remainder is genuinely
// sound (CompliantPass, CanaryKilled, Total > 0) AND proves at least one
// survivor. Both halves matter. Without the soundness gate this would be the
// fabrication path again; without the "proves something" gate it would consume
// a run's one chance at salvage to record a zero, displacing a retry that
// might have done better.
//
// Requires the language plugin to implement lang.FailureDeselector and the
// Scorer to be able to report the failure output. Anything missing means no
// salvage, never a guess: a wrong selector would deselect the wrong test and
// silently narrow the exam, making the run look healthier while proving less.
// survivors is the set this salvage is over: the whole file's survivors in
// batched mode, and exactly ONE survivor under WriterModePerSurvivor — the
// deselect is arithmetic over the runner's output either way, and the scope
// is the caller's to state rather than this function's to assume.
func (d *Driver) salvageByDeselect(ctx context.Context, run *runState, writerTest string, rep adequacy.Report, survivors []adequacy.Mutant) (proven int, ids []string, deselected int, ok bool) {
	fd, canDeselect := langFor(run.rs).(golang.FailureDeselector)
	if !canDeselect {
		return 0, nil, 0, false
	}
	failure := compliantFailureOutput(ctx, d.Scorer, run, writerTest)
	failing := fd.FailedTests(failure)
	if len(failing) == 0 {
		return 0, nil, 0, false
	}
	args := fd.DeselectArgs(failing)
	if len(args) == 0 {
		return 0, nil, 0, false
	}

	cmd := strings.TrimSpace(run.rs.TestCmd + " " + strings.Join(args, " "))
	salvaged, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, writerTest, survivors, cmd)
	if serr != nil {
		// A failed salvage attempt is not a failed run: fall back to the
		// retry path rather than losing the dev-adequacy result already
		// computed.
		return 0, nil, 0, false
	}
	if !salvaged.CompliantPass || !salvaged.CanaryKilled || salvaged.Total == 0 {
		return 0, nil, 0, false
	}
	killed := provenMutantIDs(salvaged, survivors)
	if len(killed) == 0 {
		return 0, nil, 0, false
	}
	return len(killed), killed, len(failing), true
}

// CompliantFailureExplainer is the optional Scorer extension that reports WHY
// an authored test failed against the unmutated code — the runner's own
// output, the thing that makes the retry corrective rather than blind. A
// Scorer that does not implement it simply yields no detail: the retry still
// happens, because "your test failed on the correct code" is itself
// information the writer never previously received, but it is far weaker than
// handing over the actual failing assertion.
type CompliantFailureExplainer interface {
	CompliantFailure(ctx context.Context, codePath, code, test, testCmd string) string
}

// compliantFailureOutput asks the Scorer for the clean-code failure detail,
// tolerating a Scorer that cannot supply it.
func compliantFailureOutput(ctx context.Context, s Scorer, run *runState, test string) string {
	ex, ok := s.(CompliantFailureExplainer)
	if !ok {
		return ""
	}
	return ex.CompliantFailure(ctx, run.rs.CodePath, run.rs.Code, test, run.rs.TestCmd)
}

// provenMutantIDs returns the ids of the survivors the POOL's authored test
// actually killed: everything in `mutants` that is NOT in rep.Survived, in the
// input's own order (deterministic, so a ledger row is stable across runs).
//
// This is the EVIDENCE behind ProvenMissed. The count on its own cannot
// distinguish "killed these three, missed those seven" from a bare 0, which is
// what made a real tried-and-missed on pallets/flask (2026-07-31) impossible
// to diagnose without paying for another run: `certify --repo` has no tape
// flag, so the integer was the entire surviving record of the attempt.
func provenMutantIDs(rep adequacy.Report, mutants []adequacy.Mutant) []string {
	// POSITIVE EVIDENCE ONLY. This used to return every survivor that was
	// NOT in rep.Survived — a subtraction that counted as "proven" any mutant
	// the report had merely failed to grade: one whose grading command fails
	// on the compliant code (rep.Unmeasured), or one the compile gate
	// rejected. A gap is proven when the authored test KILLED the mutant by
	// execution, and that is the only list this reads.
	killedIDs := make(map[string]struct{}, len(rep.Killed))
	for _, id := range rep.Killed {
		killedIDs[id] = struct{}{}
	}
	proven := make([]string, 0, len(mutants))
	for _, m := range mutants {
		if _, ok := killedIDs[m.ID]; ok {
			proven = append(proven, m.ID)
		}
	}
	return proven
}

// tickDevAdequacy is step 2: once mutant-generator is done, parse its
// mutants, score the dev's own tests against them (brain-side, via Scorer —
// never the worker's self-report), and promote test-writer re-rendered with
// the real survivors.
func (d *Driver) tickDevAdequacy(ctx context.Context, missionID int64, run *runState) error {
	// The PRESET branch comes first, and it has to: everything below is the
	// machinery of turning generator TASKS into mutants, and the very next
	// statement returns early when there are none — which on a preset run is
	// always, by construction (BuildDAG seeds no generator seat). Put after
	// that gate, a replayed run would return nil on every tick, never score,
	// and hang until the deadline signed a needs-review verdict nobody
	// measured. So the preset path assembles `mutants`/`probed` itself and
	// then joins the SAME scoring path an ordinary run takes — one dev pass,
	// one place kill rate and survivors are computed, no second copy to drift.
	var mutants []adequacy.Mutant
	probed := 0
	regionsTotal := 0

	if run.rs.PresetMutants != nil {
		// A replayed set IS the exam, already assembled. regionsTotal and
		// probed both stay 0 on purpose: they count mutant-generator SEATS
		// this run dispatched and the ones that came back usable, and a
		// preset run dispatched none. Reporting a probe count here (the
		// mutant count, say) would assert regions were attacked by this run
		// when the attacking happened in some earlier run this one is only
		// replaying — and the coverage-shortfall readout, which is gated on
		// regionsTotal > 0, correctly stays silent on 0/0 rather than
		// accusing a replay of dropping regions it never had.
		mutants = run.rs.PresetMutants
	} else {
		mgs, err := d.tasksByRole(missionID, RoleMutantGenerator)
		if err != nil {
			return err
		}
		if len(mgs) == 0 {
			return nil
		}
		// EVERY shard must be terminal before the dev's tests are scored. Scoring a
		// partial mutant set would grade the suite against a smaller exam than the
		// run claims to have set — the kill-rate would be real but would not mean
		// what the verdict says it means.
		for i := range mgs {
			if mgs[i].Status != queue.StatusDone {
				return nil
			}
		}

		// Union every shard's mutants. IDs are prefixed with the shard index so two
		// shards returning "m1" cannot collide, and so each survivor names the
		// region it came from (including in the test-writer's prompt). An UNSHARDED
		// run keeps its original, unprefixed IDs.
		//
		// This whole scan re-runs on EVERY tick until run.devScored is true (Tick
		// re-calls tickDevAdequacy unconditionally each pass), and two paths below
		// return an error AFTER a region has already been dropped and recorded:
		// the all-regions-failed guard just past this loop, and a transient
		// Scorer.Score error further down (the scorer runs a suite in a sandbox —
		// a transient failure there is exactly the condition dropping exists to
		// survive). Both are ordinary re-entry, not a fresh event, so every
		// mutation to run state in this loop must be idempotent per shard key:
		// already-dropped regions are skipped via droppedKeys rather than
		// re-counted or re-appended, and regionsProbed is recomputed fresh from
		// this pass's results rather than accumulated.
		for i := range mgs {
			key := mgs[i].Key
			if run.droppedKeys[key] {
				// Already exhausted its retry budget and recorded as dropped on a
				// prior pass: this task's Result hasn't changed (it was never
				// reopened past the budget), so re-parsing it would only rediscover
				// the same failure. Skip it silently — the drop is already honestly
				// recorded, and re-running the drop bookkeeping here is what
				// corrupts the signed counts.
				continue
			}
			shardIdx, sharded := ShardIndexFromKey(key)
			parsed, perr := d.Validator.ParseMutants(mgs[i].Result, run.rs.Code)
			if perr != nil {
				run.shardRetries[key]++
				if run.shardRetries[key] <= MaxShardRetries {
					if _, rerr := d.Q.ReopenTask(mgs[i].ID); rerr != nil {
						return fmt.Errorf("advpool: reopen %s after parse failure: %w", key, rerr)
					}
					return fmt.Errorf("advpool: %s result unparseable, reissued for retry (%d/%d): %w",
						key, run.shardRetries[key], MaxShardRetries, perr)
				}
				// Budget exhausted: DROP this region and proceed on the shards that
				// parsed. Recorded exactly once (droppedKeys), never swallowed.
				log.Printf("advpool: run %d: dropping region %s after %d unparseable results — its functions go unprobed",
					missionID, key, run.shardRetries[key])
				label := mgs[i].Title
				if symbols := run.shardSymbols[key]; len(symbols) > 0 {
					label = strings.Join(symbols, ", ")
				}
				run.droppedKeys[key] = true
				run.droppedRegions = append(run.droppedRegions, label)
				// Guarded by `sharded`: an unsharded run's shardStats map starts
				// (and must stay) empty — bugCatchObservations reads its length
				// to decide whether to emit the single-seat row or the per-shard
				// rows, so writing a key here for the bare, unsharded task would
				// silently flip that decision.
				if sharded {
					st := run.shardStats[shardIdx]
					st.parseRetries = run.shardRetries[key]
					st.dropped = true
					run.shardStats[shardIdx] = st
				}
				continue
			}
			if len(parsed) > 0 {
				// "Probed" means this region actually contributed to the exam the
				// dev suite is graded against — a shard that parsed cleanly but
				// produced zero mutants contributed nothing, and must not count as
				// probed just because it wasn't dropped.
				probed++
			}
			for _, m := range parsed {
				if sharded {
					m.ID = fmt.Sprintf("s%d/%s", shardIdx, m.ID)
				}
				mutants = append(mutants, m)
			}
			if sharded {
				st := run.shardStats[shardIdx]
				st.mutants = len(parsed)
				st.parseRetries = run.shardRetries[mgs[i].Key]
				run.shardStats[shardIdx] = st
			}
		}
		regionsTotal = len(mgs)
	}
	run.regionsTotal = regionsTotal
	run.regionsProbed = probed

	// TWO IDENTICAL HUNKS ARE ONE MEASUREMENT, and scoring charges a whole
	// suite run for each. Sharded generation aims several seats at overlapping
	// regions and a model asked for n distinct mutations will sometimes emit
	// the same one twice; the duplicate cannot produce a different verdict
	// (same edit, same source), it only inflates the denominator and the wall
	// clock. Collapsed HERE — where one file's shards are unioned and nowhere
	// else, so nothing can ever dedupe across files — and DISCLOSED below, so
	// the denominator stays reconcilable against the generator's own output.
	//
	// Idempotent per re-entry: this scan re-runs on every tick, and it
	// recomputes `mutants` from the tasks each time rather than accumulating.
	// COUNTED here, COLLAPSED in adequacy.Score. The scorer is where the
	// saving is taken (it grades one of each identical hunk and attributes the
	// answer to both, leaving Killed/Survived/Total and the kill rate exactly
	// as they were); this is only where the fact reaches the verdict.
	if _, dup := adequacy.DedupeMutants(mutants); dup > 0 {
		run.dupMutants = dup
		log.Printf("advpool: run %d: %d duplicate mutant(s) collapsed — identical hunk, one measurement",
			missionID, run.dupMutants)
	}

	if len(mutants) == 0 {
		// Unconditional on len(mutants): a run where every shard parsed
		// cleanly but each produced zero mutants would otherwise sail past
		// this guard (nothing was DROPPED), score against an empty exam, and
		// still claim full coverage. Zero mutants to grade against is fatal
		// regardless of why.
		// Typed so the caller can recognize this as TERMINAL rather than feed
		// it to a transient-retry loop — see ErrNoUsableMutants' doc for the
		// 21-retries-without-re-invoking observation that motivated it.
		return ErrNoUsableMutants{Regions: run.regionsTotal, Dropped: len(run.droppedRegions)}
	}

	// GENERATION ends here, at the one point the seats' results become the
	// exam — not when the tasks were marked done, which is a queue fact, and
	// not at the top of this function, which re-runs on every tick until the
	// dev suite is scored.
	if g := d.endPhase(run, phaseGeneration); g > 0 {
		run.timing.Generation = g
		d.emit(missionID, "phase_generation", run.rs.CodePath, map[string]any{
			"duration_ms": g.Milliseconds(),
		})
	}

	// THE DEV PASS: the mutants against the dev's own suite. On real repos
	// this is most of the audit (35 of 43 minutes on psf/requests'
	// adapters.py), and until this measurement existed the only way to know
	// that was to subtract two log timestamps by hand.
	d.beginPhase(run, phaseDevPass)
	if err := applyDevScore(ctx, run, d.Scorer, mutants, d.MutantSink); err != nil {
		// The phase stays OPEN on a transient scorer error: tickDevAdequacy
		// is re-entered and scores again, and the run really has been in its
		// dev pass the whole time.
		return err
	}
	run.timing.DevPass = d.endPhase(run, phaseDevPass)
	killRate, survivors := run.devKillRate, run.devSurvivors

	// The challenger pass: score the shadow seats' mutants against the SAME dev
	// suite so the comparison measures POTENCY (mutants that survive a good
	// suite), not merely output volume. Results are recorded and never
	// aggregated into the verdict — the exam stays the primary model's.
	//
	// A shadow failure is NEVER fatal: it is measurement, not the gate. Errors
	// are logged and the seat is skipped.
	if strings.TrimSpace(run.rs.ShadowModel) != "" {
		d.runShadowPass(ctx, missionID, run)
	}

	// Emit one telemetry event per shard now that run.shardStats is final
	// (this whole function only reaches here once, guarded by devScored, so
	// this cannot double-emit on a re-entrant tick) — the --record tape, the
	// cockpit, and telemetry all get it from this one write.
	for _, i := range sortedShardIndexes(run.shardStats) {
		st := run.shardStats[i]
		d.emit(missionID, "pool_shard", st.region, map[string]any{
			"shard": i, "region": st.region,
			"region_complexity": st.complexity, "region_lines": st.lines,
			"mutants": st.mutants, "parse_retries": st.parseRetries, "dropped": st.dropped,
		})
	}
	// The challenger's paired telemetry: the SAME pool_shard kind (so the tape
	// and cockpit render it with the existing per-shard handling) marked
	// shadow=true, plus measured — a seat the budget guard skipped, or one
	// whose scoring errored, emits measured=false rather than a silent absence
	// or a fabricated zero. Without this the --record tape carried only the
	// primary's rows and the comparison was invisible in every replay.
	for _, i := range sortedShardIndexes(run.shadowStats) {
		st := run.shadowStats[i]
		d.emit(missionID, "pool_shard", st.region, map[string]any{
			"shard": i, "region": st.region,
			"region_complexity": st.complexity, "region_lines": st.lines,
			"mutants": st.mutants, "parse_retries": st.parseRetries, "dropped": st.dropped,
			"survived": st.survived, "shadow": true, "measured": st.measured,
			"model": run.rs.ShadowModel,
		})
	}
	// Log the headline the moment it's computed — the dev suite's grade — so it
	// is visible even if the downstream test-writer/aggregate steps stall.
	switch {
	case run.baselineFailed:
		// The dev suite did NOT pass on the unmutated code in the jail: nothing
		// was graded, so a "killed N of N" tally would be fabricated from an empty
		// survivor set. Say what actually happened instead.
		log.Printf("advpool: run %d dev-adequacy: COULD NOT GRADE — the dev suite did not pass on the UNMUTATED code in the jail (baseline build/test failed); this is a build/environment failure, not a test-quality verdict", missionID)
	case run.suiteIgnoresFile:
		// The suite passed on source that cannot compile: it provably never
		// compiles or imports the audited file, so no mutant of it could have
		// been graded. A different diagnosis from a failed baseline — the suite
		// is fine, the check command is pointed somewhere else.
		log.Printf("advpool: run %d dev-adequacy: COULD NOT GRADE — the dev suite PASSED on deliberately invalid source, so it never compiles or imports %s; the suite is fine, the check command does not exercise this file", missionID, run.rs.CodePath)
	default:
		// Counts come from the GRADED set. Inferring kills as
		// len(mutants)-len(survivors) treated every compile-gate reject as a
		// kill, which printed "killed 11 of 15" beside a rate of 0% — the exact
		// inflation the gate removes, reappearing in the log line.
		invalidNote := ""
		if run.mutantsInvalid > 0 {
			invalidNote = fmt.Sprintf("; %d mutant(s) failed the compile check and were NOT graded", run.mutantsInvalid)
		}
		// A COUNT cannot be acted on. Sample the compiler's own words so the
		// operator (and, later, a repair round) can see what the generator got
		// wrong, rather than only that it got something wrong.
		for _, id := range sampleInvalidReasons(run, 3) {
			log.Printf("advpool: run %d invalid mutant %s: %s", missionID, id, firstLine(run.invalidReasons[id]))
		}
		log.Printf("advpool: run %d dev-adequacy: the dev's OWN tests scored %.0f%% (killed %d of %d graded mutants, %d survived — bugs the dev's tests miss)%s",
			missionID, killRate*100, run.mutantsTotal-len(survivors), run.mutantsTotal, len(survivors), invalidNote)
	}
	devAdequacyDetail := map[string]any{
		"dev_kill_rate": run.devKillRate, "mutants_total": run.mutantsTotal,
		"survivors": len(run.devSurvivors), "survivor_ids": survivorIDs(run.devSurvivors),
	}
	// dev_pass's phase-boundary duration rides on THIS emit rather than a
	// second one: pool_dev_adequacy already fires at the exact moment the
	// phase closes (run.timing.DevPass was just set, above). Omitted (not
	// zero) when the phase never ran — see Timing's own field doc.
	if run.timing.DevPass > 0 {
		devAdequacyDetail["duration_ms"] = run.timing.DevPass.Milliseconds()
	}
	d.emit(missionID, "pool_dev_adequacy", "", devAdequacyDetail)

	if len(survivors) == 0 {
		// THREE different runs reach here, and only one of them is good news:
		//   1. a perfect dev suite — it killed every mutant;
		//   2. a failed baseline (run.baselineFailed) — nothing was graded;
		//   3. a surviving canary (run.suiteIgnoresFile) — the check command
		//      never compiles or imports the file, so nothing was graded.
		// In all three there are no survivors for the test-writer to expose, so
		// skip it and the pool-adequacy step entirely and go straight to
		// aggregate. Without this the test-writer would be promoted to "write a
		// test targeting the survivors" of which there are none (a degenerate
		// prompt) and the run could NEVER converge — i.e. the pool could grade a
		// bad suite but never certify a perfect one. Cases 2 and 3 do NOT
		// certify on a 100% kill-rate: the could-not-grade flags ride the
		// verdict (see tickAggregate) so the readout says what happened
		// instead of reporting an empty survivor set as a perfect score.
		run.poolScored = true
		run.provenMissed = 0
		run.testWriterMoot = true // it never ran — keep it off the leaderboard
		if tw, terr := d.Q.TaskByID(run.testWriterTaskID); terr == nil && tw != nil && tw.Status == queue.StatusPending {
			cancelled, cerr := d.Q.CancelTask(tw.ID)
			if cerr != nil {
				return fmt.Errorf("advpool: cancel moot test-writer (perfect suite): %w", cerr)
			}
			if !cancelled {
				log.Printf("advpool: run %d: moot test-writer task %d was not pending at cancel time (benign race)", missionID, tw.ID)
			}
		}
		return nil
	}

	// THE AUTHORED PASS begins the moment the dev pass leaves survivors for
	// the writer to attack: its cost is the writer seat's model time, its
	// compile retries AND the scored run of its test, and a phase that
	// started only when the result came back would report just the last of
	// the three. A perfect dev suite returns above and never opens it.
	d.beginPhase(run, phaseAuthored)

	tw, terr := d.Q.TaskByID(run.testWriterTaskID)
	if terr != nil {
		return fmt.Errorf("advpool: load test-writer task: %w", terr)
	}
	if tw == nil || tw.Status != queue.StatusPending {
		// Already promoted/superseded/claimed by something else — nothing to do.
		return nil
	}

	// test-writer's original DependsOn=[DevAdequacyKey] can never be satisfied
	// by a normal worker-completed task: dev-adequacy is driver-internal
	// bookkeeping, never its own claimable task. Break the otherwise-permanent
	// deadlock the same way the brain's re-planning tools do: SupersedeTask
	// with a dep-free replacement now that the survivors are known.
	//
	// NOTE: SupersedeTask auto-uniquifies a replacement that reuses the old
	// key (the old row's key isn't freed until the same transaction that
	// inserts the new row), so the live test-writer task must be tracked by
	// id (run.testWriterTaskID), never re-looked-up by RoleTestWriter's key.
	if run.writerMode == WriterModePerSurvivor {
		// ONE SEAT PER SURVIVOR, all claimable at once. See
		// promotePerSurvivorWriters — and RunSpec.WriterMode for why the
		// batched shape below is still reachable and still the RunSpec
		// default.
		if perr := d.promotePerSurvivorWriters(missionID, run, tw, survivors); perr != nil {
			return perr
		}
	} else {
		newID, serr2 := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
			Key:         RoleTestWriter,
			Role:        RoleTestWriter,
			Title:       tw.Title,
			Instruction: renderTestWriter(run.rs, run.sigs, survivors),
			Model:       tw.Model,
		})
		if serr2 != nil {
			return fmt.Errorf("advpool: promote test-writer with survivors: %w", serr2)
		}
		run.testWriterTaskID = newID
	}

	// The CHALLENGER writer seat, enqueued from the SAME rendered instruction
	// the primary was just superseded with — the same survivors, the same
	// target, under its OWN role key (RoleTestWriterShadow), so
	// tasksByRole(RoleTestWriter) structurally cannot return it.
	//
	// Enqueued HERE, beside the primary, rather than at the point it is read
	// (tickPoolAdequacy): a challenger asked only once the primary has already
	// finished would have to hold the run open to be answered, and shadow work
	// must never delay or gate the primary. Asked in parallel it costs the run
	// nothing.
	//
	// NEVER fatal: the seat is measurement, so an enqueue failure is logged
	// and the challenger is simply skipped.
	if strings.TrimSpace(run.rs.ShadowWriterModel) != "" {
		if run.writerMode == WriterModePerSurvivor {
			// The challenger fans out the SAME way, one seat per survivor,
			// so the head-to-head still compares two writers facing the
			// identical exam under the identical shape. A challenger asked
			// one batched question while the primary answered N per-survivor
			// ones would be confounded by the shape, not by the model.
			d.enqueueShadowWritersPerSurvivor(missionID, run, survivors)
		} else {
			d.enqueueShadowWriter(missionID, run, tw.Title, renderTestWriter(run.rs, run.sigs, survivors))
		}
	}

	if _, err := d.Q.PromoteReady(missionID); err != nil {
		return fmt.Errorf("advpool: promote after test-writer supersede: %w", err)
	}
	return nil
}

// tickPoolAdequacy is step 3: once test-writer is done, validate that its
// test compiles, then score it (via Scorer, brain-side) against the
// survivors the dev's tests missed. ProvenMissed is how many of those
// survivors the pool's test then killed — real, catchable gaps.
func (d *Driver) tickPoolAdequacy(ctx context.Context, missionID int64, run *runState) error {
	// The challenger writer pass, mirroring the challenger GENERATOR pass in
	// tickDevAdequacy: guarded on a trimmed non-empty model, errors logged, the
	// seat skipped. A shadow failure is NEVER fatal — it is measurement, not
	// the gate — so this returns nothing and is run BEFORE the primary's own
	// early return, giving the challenger every tick the primary's pipeline
	// happens to take without ever adding one of its own.
	if strings.TrimSpace(run.rs.ShadowWriterModel) != "" {
		d.runShadowWriterPass(ctx, missionID, run)
	}

	if run.writerMode == WriterModePerSurvivor {
		done, ferr := d.tickWriterFanout(ctx, missionID, run)
		if ferr != nil {
			return ferr
		}
		if !done {
			return nil
		}
		d.finishWriterFanout(run)
		return nil
	}

	tw, err := d.Q.TaskByID(run.testWriterTaskID)
	if err != nil {
		return fmt.Errorf("advpool: load test-writer task: %w", err)
	}
	if tw == nil || tw.Status != queue.StatusDone {
		return nil
	}

	// The worker hands back the model's RAW output (structured fast path); a
	// model commonly wraps a test in ```go fences / prose. Clean it to the bare
	// source before compiling or scoring — symmetric with ParseMutants on the
	// mutant-generator side.
	writerTest := d.Validator.ParseTest(tw.Result)

	if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, writerTest); cerr != nil {
		run.testWriterAttempts++
		if run.testWriterAttempts < MaxTestWriterAttempts {
			// CORRECTIVE retry: re-render the test-writer WITH the compiler error
			// and its own broken test so it fixes the actual problem, instead of
			// the old blind ReopenTask that re-ran the identical prompt and let
			// the model repeat the same mistake until exhaustion. Supersede (the
			// same mechanism the survivor-promote uses) is required to swap the
			// instruction — ReopenTask keeps the old one.
			var newInstr string
			if strings.TrimSpace(writerTest) == "" {
				// The model returned NO usable test (an empty result — seen on
				// hard targets). Feeding an empty "broken test" into the REPAIR
				// prompt ("here is your test: «», fix the compile error") just
				// begets more emptiness and burns the whole retry budget in a
				// degenerate loop, so a later good attempt never gets a slot.
				// Re-issue a FRESH prompt instead — a clean shot, not a repair.
				newInstr = renderTestWriter(run.rs, run.sigs, run.devSurvivors)
			} else {
				var ce *CompileError
				compileMsg := cerr.Error()
				if errors.As(cerr, &ce) && strings.TrimSpace(ce.Output) != "" {
					compileMsg = ce.Output
				}
				newInstr = renderTestWriterWithRepair(run.rs, run.sigs, run.devSurvivors, writerTest, compileMsg)
			}
			newID, serr := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
				Key:         RoleTestWriter,
				Role:        RoleTestWriter,
				Title:       tw.Title,
				Instruction: newInstr,
				Model:       tw.Model,
			})
			if serr != nil {
				return fmt.Errorf("advpool: reissue test-writer with compile feedback: %w", serr)
			}
			run.testWriterTaskID = newID
			if _, perr := d.Q.PromoteReady(missionID); perr != nil {
				return fmt.Errorf("advpool: promote test-writer after compile-feedback reissue: %w", perr)
			}
			return fmt.Errorf("advpool: test-writer result does not compile, reissued for retry (%d/%d): %w",
				run.testWriterAttempts, MaxTestWriterAttempts, cerr)
		}
		// Exhausted: STOP reopening. A hard survivor whose only authored tests
		// never compile must not spin the run to RunDeadline with no verdict —
		// converge now with the real, already-computed dev-adequacy result
		// (kill-rate, survivors, critic findings) rather than throwing it away.
		// cerr rides into the log line itself: the retry path above already
		// wraps it into the reissued instruction (renderTestWriterWithRepair),
		// but the repo path (certify_repo.go's localExecutor) sends progress
		// to io.Discard, so that instruction text never surfaces anywhere a
		// human reads it. Diagnosing the missing-import bug this constant
		// exists to prevent a repeat of took a code trace instead of a log
		// line — this is the fix for that: the LAST compile error is always
		// visible here, on the one path every caller's log actually reaches.
		log.Printf("advpool: %s: test-writer could not produce a compiling test after %d attempts — %d survivor(s) found but not proven-killed; converging without an authored test: %v",
			run.rs.CodePath, MaxTestWriterAttempts, len(run.devSurvivors), cerr)
		run.poolScored = true
		run.provenMissed = 0
		run.testWriterFailed = true
		return nil
	}

	// Capture the compiling killing test for hand-back (read by RunStatus under
	// d.mu, so store it under the same lock).
	d.mu.Lock()
	run.authoredTest = writerTest
	d.mu.Unlock()

	// ScoreReport (via scoreAuthored/AuthoredScorer), never the collapsed
	// Score: a caller MUST be able to tell "the authored test genuinely
	// killed N survivors" from "nothing was actually graded" — Score's bare
	// (killRate, survivors) tuple cannot express CompliantPass/CanaryKilled/
	// Total, so an ungraded run (e.g. the authored test fails on the
	// unmutated compliant code — an ordinary outcome for an LLM-written
	// test, since CompileTest only checks syntax, never "passes") would
	// return an EMPTY survivors slice with no error, and the arithmetic
	// below would silently compute provenMissed = len(devSurvivors) - 0,
	// reporting EVERY survivor as execution-proven from a run in which no
	// mutant ever executed — corral's strongest claim, fabricated.
	rep, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, writerTest, run.devSurvivors, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score pool test: %w", serr)
	}
	// A test that COMPILED and then failed against the unmutated, correct code
	// is repairable, and until now was not repaired. The compile-failure path
	// above has fed the compiler's own error back since CompileError existed,
	// precisely because a bare "does not compile" taught the writer nothing —
	// but a clean-code failure, which is just as diagnosable (the runner says
	// exactly which assertion broke), got no retry at all: the run logged it,
	// marked poolTestUnsound and converged.
	//
	// Measured cost of that, on the first run whose authored test was ever
	// retained (gemini-3.6-flash on pallets/flask, 2026-07-31): the writer
	// produced 13 tests against real internals and TEN PASSED. Three carried
	// wrong API assumptions, and because the compliant check is all-or-nothing
	// per FILE, those three discarded all thirteen — Total=0, nothing scored,
	// the whole run zeroed including ten tests that might have killed
	// survivors. The writer was never told.
	//
	// Deliberately scoped to CompliantPass==false. The other two unsound
	// shapes are NOT the writer's fault and retrying would only burn budget:
	// !CanaryKilled means the project's own command never reads the file (a
	// discovery/config fact), and Total==0 with a passing baseline means there
	// was nothing to score.
	if !rep.CompliantPass {
		// SALVAGE FIRST, before spending another model call. The compliant
		// check is all-or-nothing per FILE, and the measured cost of that is
		// large: on the first authored test corral ever retained
		// (gemini-3.6-flash on pallets/flask, 2026-07-31) 13 tests were
		// written, TEN PASSED, and all 13 were discarded because 3 carried
		// wrong API assumptions.
		//
		// Deselecting the failures and re-scoring with the remainder does not
		// depend on the model being able to repair itself — it is arithmetic
		// over the runner's own output. Accepted ONLY if the remainder
		// actually proves something: a salvage that proves nothing is no
		// better than the retry it would displace, so in that case we fall
		// through and let the writer try again.
		if salvaged, ids, n, ok := d.salvageByDeselect(ctx, run, writerTest, rep, run.devSurvivors); ok {
			run.poolScored = true
			run.writerSalvaged = true
			// GRADED, and therefore measured: the salvaged remainder proved
			// these survivors by execution. It is measured-but-CONFOUNDED (the
			// challenger gets no equivalent rescue), which is a separate
			// question, gated separately by writerSalvaged in
			// recordMutantAttempts — see RULING P11 there.
			run.primaryWriterMeasured = true
			run.provenMissed = salvaged
			run.provenIDs = ids
			log.Printf("advpool: %s: the authored test failed on the unmutated code, but deselecting its %d failing test(s) left a sound remainder that PROVED %d of %d survivor(s)",
				run.rs.CodePath, n, salvaged, len(run.devSurvivors))
			return nil
		}
	}

	if !rep.CompliantPass && run.testWriterAttempts < MaxTestWriterAttempts-1 {
		run.testWriterAttempts++
		failure := compliantFailureOutput(ctx, d.Scorer, run, writerTest)
		newID, serr := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
			Key:         RoleTestWriter,
			Role:        RoleTestWriter,
			Title:       tw.Title,
			Instruction: renderTestWriterRepairing(run.rs, run.sigs, run.devSurvivors, writerTest, "", failure),
			Model:       tw.Model,
		})
		if serr != nil {
			return fmt.Errorf("advpool: reissue test-writer with clean-code failure: %w", serr)
		}
		run.testWriterTaskID = newID
		if _, perr := d.Q.PromoteReady(missionID); perr != nil {
			return fmt.Errorf("advpool: promote test-writer after clean-code-failure reissue: %w", perr)
		}
		log.Printf("advpool: %s: the authored test compiled but FAILED on the unmutated code — reissuing with the failure fed back (%d/%d)",
			run.rs.CodePath, run.testWriterAttempts, MaxTestWriterAttempts)
		return nil
	}

	run.poolScored = true
	if !rep.CompliantPass || !rep.CanaryKilled || rep.Total == 0 {
		// A DIAGNOSIS, not a score: the compiling authored test did not
		// genuinely grade against the survivors. provenMissed must not
		// become len(devSurvivors) (a fabricated maximum, the false-proof
		// inversion of the false-accusation class this codebase already
		// guards against elsewhere) NOR a bare 0 read as "tried and missed"
		// — poolTestUnsound carries the distinction onto the verdict so a
		// caller can print it honestly, the same way testWriterFailed
		// already is.
		log.Printf("advpool: %s: the pool's authored test compiled but did not genuinely grade against the survivors (CompliantPass=%v CanaryKilled=%v Total=%d) — converging with proven_missed=0, not a maximum",
			run.rs.CodePath, rep.CompliantPass, rep.CanaryKilled, rep.Total)
		run.provenMissed = 0
		run.poolTestUnsound = true
		run.authoredTestNotCollected = rep.AuthoredTestUnreached
		return nil
	}
	// PAST the three non-grading diagnoses above: this suite passed on the
	// unmutated code, killed its canary and scored something, so its kill
	// vector is a real observation rather than an absence of one.
	run.primaryWriterMeasured = true
	// THE COUNT IS THE LENGTH OF THE EVIDENCE, never a subtraction. This was
	// `len(devSurvivors) - len(poolSurvivors)`, which credits as proven every
	// survivor the authored pass failed to GRADE — and the authored pass can
	// fail to grade a survivor without any error: its command fails on the
	// compliant code (an authored file pytest never collects exits 5; a
	// `--cov-fail-under` in pytest.ini fails any subset), and the report now
	// files those under Unmeasured rather than Killed. A number derived by
	// subtraction cannot tell "killed" from "not measured"; a number that IS
	// the length of the killed list cannot get that wrong.
	run.provenIDs = provenMutantIDs(rep, run.devSurvivors)
	run.provenMissed = len(run.provenIDs)
	if len(rep.Unmeasured) > 0 {
		// The authored test could not grade some survivors at all — its
		// command fails on the unmutated code. That is a defect in the TEST,
		// and the verdict must say so rather than quietly proving fewer.
		run.poolTestUnsound = true
		for _, id := range rep.Unmeasured {
			log.Printf("advpool: run %d: the authored test's command FAILS ON THE COMPLIANT CODE, so survivor %s could not be graded and is NOT counted as proven: %s",
				missionID, id, rep.UnmeasuredReasons[id])
			break // one reason is the same reason for all of them
		}
	}
	// The two failure paths above each log why they produced nothing. This one
	// — the path that actually grades — logged nothing at all, in either
	// direction. That asymmetry is why a real "tried and missed" (a sound,
	// collected, genuinely-grading authored test that killed NO survivor) was
	// undebuggable after the fact: `certify --repo` has no tape flag, so the
	// run's whole record of the attempt was a single `0 proven missed` in the
	// report. Log the outcome on the success path too, so the interesting case
	// is distinguishable from the boring one without re-running anything.
	if run.provenMissed > 0 {
		log.Printf("advpool: %s: the pool's authored test PROVED %d of %d survivor(s) catchable by execution",
			run.rs.CodePath, run.provenMissed, len(run.devSurvivors))
	} else {
		log.Printf("advpool: %s: the pool's authored test graded soundly (CompliantPass=true CanaryKilled=true Total=%d) but killed NONE of the %d survivor(s) — a real 'tried and missed', not an ungraded run",
			run.rs.CodePath, rep.Total, len(run.devSurvivors))
	}
	return nil
}

// tickAggregate is step 4: once test-critic is done AND pool-adequacy is
// scored, aggregate the Verdict, apply the human gate, sign it (Signer, if
// wired), and — only after that sign succeeds — feed the gate-earned
// leaderboard (Leaderboard, if wired). run.verdict is set (and the run
// considered terminal) only once this whole sequence has succeeded: if
// signing fails, the aggregate is left unset so a later Tick simply
// recomputes and retries — aggregate/sign are both deterministic/idempotent
// over the same scored inputs.
func (d *Driver) tickAggregate(ctx context.Context, missionID int64, run *runState) (*Verdict, error) {
	// A run with NO critic assigned seeds no critic task (see BuildDAG), so the
	// wait below could never be satisfied and the run would spin to its
	// --timeout and bank an unverified verdict — a silent hang, not an error.
	// Skip straight to aggregation with no advisory review, which is the honest
	// representation: no critic ran, so there are no findings, as distinct from
	// a critic that ran and found nothing.
	criticEnabled := strings.TrimSpace(d.Assign[RoleTestCritic]) != ""

	var criticFindings []queue.Finding
	if criticEnabled {
		tc, err := d.taskByKey(missionID, RoleTestCritic)
		if err != nil {
			return nil, err
		}
		if tc == nil || tc.Status != queue.StatusDone {
			return nil, nil
		}

		findings, ferr := d.Q.Findings(missionID, "")
		if ferr != nil {
			return nil, fmt.Errorf("advpool: load findings: %w", ferr)
		}
		criticFindings = filterCriticFindings(findings, tc.ID)
		// The wait is over: everything from the pool score to this moment is
		// what having a critic cost the run. Closed only on the path that
		// actually READ the critic's findings — the early return above
		// leaves the phase open, because the run is still waiting.
		if c := d.endPhase(run, phaseCritic); c > 0 {
			run.timing.Critic = c
			d.emit(missionID, "phase_critic", run.rs.CodePath, map[string]any{
				"duration_ms": c.Milliseconds(),
			})
		}
	}

	// The critic's findings are a second model's UNVERIFIED review — carried on
	// the verdict as advisory (VacuousFindings) but NOT gating the signed record
	// (pass false, not d.blockingFindingOpen(findings)): certification is an
	// execution-proven judgment (kill-rate + proven_missed), never an LLM's
	// opinion, which can hallucinate. blockingFindingOpen remains for a future
	// execution-verified finding path.
	v := aggregate(run.rs, d.Assign, run.devKillRate, run.mutantsTotal, len(run.devSurvivors), run.provenMissed,
		criticFindings, d.Threshold, false, run.testWriterFailed, run.poolTestUnsound)
	v.RegionsTotal = run.regionsTotal
	v.PromptShape = run.promptShape
	v.RegionsProbed = run.regionsProbed
	v.DroppedRegions = run.droppedRegions
	v.DuplicateMutants = run.dupMutants
	// The evidence behind ProvenMissed rides onto the verdict beside the count
	// itself — set here, with the other post-aggregate fields, rather than
	// widening aggregate()'s already-long signature.
	v.ProvenMutantIDs = run.provenIDs
	// MutantsInvalid rides here for the same reason: aggregate() composes the
	// SIGNED verdict and is the second Verdict construction site in this
	// package. Setting it only on the other one (driver.go's Verdict literal)
	// left the count correct in the log and ZERO in the printed, signed
	// verdict — the fourth time on this branch that a value was added in one
	// place and a second construction/conversion site was missed.
	v.MutantsInvalid = run.mutantsInvalid
	v.AuthoredTest = run.authoredTest
	// The mode is stamped from the RUN's resolved value, never from the
	// RunSpec's raw field: an unset spec means batched, and the verdict must
	// say which measurement this is in the same words a reader can look up.
	v.WriterMode = run.writerMode
	v.AuthoredExtra = run.authoredExtra
	v.WriterSeatsUngraded = run.writerSeatsUngraded()
	v.WriterAttempts = writerAttemptSpread(run)
	// Narrows PoolTestUnsound to "your test command never collected the
	// authored test's file" -- set here rather than widening aggregate()'s
	// signature, alongside the other post-aggregate diagnosis fields.
	v.AuthoredTestNotCollected = run.authoredTestNotCollected
	// A baseline that couldn't pass is fail-closed to needs-review by aggregate
	// (devKillRate 0 < threshold); mark it so the readout says "could not grade"
	// instead of reporting the 0 as if the suite were graded and scored zero.
	v.BaselineFailed = run.baselineFailed
	v.BaselineOutput = run.baselineOutput
	// The other could-not-grade case, carried separately so the readout can
	// name the right one: the suite passed but never reads the audited file.
	v.SuiteIgnoresFile = run.suiteIgnoresFile
	// Carried alongside the other could-not-grade / baseline fields, set here
	// rather than widening aggregate()'s signature — see Verdict.BaselineDuration.
	v.BaselineDuration = run.baselineDuration
	// The run's own clock. verdictFromSpec has already put the two phases
	// the caller measured (Selection, Pool) on v.Timing; these are the five
	// this driver measured itself. Total is read last, here, because THIS is
	// where the run ends.
	v.Timing = timingWith(v.Timing, run.timing)
	v.Timing.Total = totalWith(v.Timing, d.now().Sub(run.startedAt))
	v.MutantDurationMedian = run.mutantDurationMedian
	v.MutantDurationMax = run.mutantDurationMax
	// The mutant-level evidence behind DevKillRate/Survivors, carried the same
	// way — see Verdict.DevKilledMutants.
	v.DevKilledMutants = run.devKilled
	v.DevSurvivedMutants = toMutantRefsWith(run.devSurvivors, run.perMutant)
	// Computed HERE, from the refs, rather than in verdictFromSpec: the spec
	// says what the run intended to narrow by, and only the finished refs say
	// what each mutant was really graded with.
	applyPerMutantStats(&v, run.perMutantGraded, v.DevKilledMutants, v.DevSurvivedMutants)

	if d.Signer != nil {
		recordID, head, serr := d.Signer.SignVerdict(ctx, v)
		if serr != nil {
			return nil, fmt.Errorf("advpool: sign verdict: %w", serr)
		}
		v.RecordID = recordID
		v.RecordHead = head
		// Gate-earned fitness (soundness #6): the leaderboard is fed ONLY from a
		// CERTIFIED verdict — a run parked for human review has not earned fitness
		// for anyone yet. A needs-review record is still signed (evidence), but no
		// model gets leaderboard credit until the gate actually certified the run.
		if d.Leaderboard != nil && v.Status == StatusCertified {
			d.feedLeaderboard(v, run.testWriterMoot)
		}
	}

	// BugCatch is fed regardless of Status (certified AND needs-review) — a
	// proven catch or a proven miss is meaningful evidence either way, unlike
	// Leaderboard fitness which is gated on certification. Guarded on a real
	// v.RecordID (nonzero): the BugCatch field doc asserts it is fed AFTER
	// Signer, "once RecordID/RecordHead are set" — a Driver wired with
	// BugCatch but no Signer (or one whose sign attempt failed and returned
	// early above) leaves v.RecordID at its documented zero value, and every
	// row this sink writes would carry that same unlinkable record_id=0. Since
	// Cell.Runs is COUNT(DISTINCT record_id), those rows would all collapse
	// into a single "run", pinning every cell below provisionalBelow forever.
	if d.BugCatch != nil && v.RecordID != 0 {
		d.BugCatch.Record(v.RecordID, v.RecordHead, bugCatchObservations(run, v))
	}

	// Conservative auto-refute/confirm of the test-critic's findings — the full
	// matrix-vs-single-test policy lives on adjudicateCriticFindings' doc. Same
	// RecordID!=0 guard as BugCatch (see its doc comment): a record_id=0 row is
	// unlinkable.
	if d.CriticFindings != nil && v.RecordID != 0 {
		obs := d.adjudicateCriticFindings(ctx, missionID, run, criticFindings, v)
		if len(obs) > 0 {
			d.CriticFindings.Record(v.RecordID, v.RecordHead, obs)
		}
	}

	// The in-memory agreement measurement, carried onto the verdict itself —
	// UNGATED by RecordID/Signer, unlike every sink above: `certify --repo`
	// signs no per-file record and wires no MutantAttempts sink, so this is
	// the only path that measurement reaches that command's verdict at all.
	v.ChallengerAgreement = challengerPair(d, run)

	// Feed both writer seats' per-mutant outcomes, pair-or-nothing, to the
	// DURABLE store (a DIFFERENT sink, gated on RecordID!=0 same as
	// BugCatch/CriticFindings above — see recordMutantAttempts' own doc).
	d.recordMutantAttempts(run, v)

	// Feed the matrix sink with the SAME matrix result tickMatrix already
	// computed — only when the matrix actually ran (run.matrix != nil) and a
	// sink is wired. Same RecordID!=0 guard as CriticFindings/BugCatch above.
	if run.matrix != nil && d.Matrix != nil && v.RecordID != 0 {
		obs := make([]MatrixObservation, len(run.matrix.Rows))
		for i, row := range run.matrix.Rows {
			obs[i] = MatrixObservation{
				TestSelector:    row.Selector,
				TestFile:        row.TestFile,
				Kills:           row.Kills,
				MutantsTotal:    row.MutantsTotal,
				DeleteCandidate: row.DeleteCandidate,
			}
		}
		d.Matrix.Record(v.RecordID, v.RecordHead, obs)
	}

	d.emit(missionID, "pool_verdict", v.Commit, map[string]any{
		"status": v.Status, "dev_kill_rate": v.DevKillRate, "mutants_total": v.MutantsTotal,
		"survivors": v.Survivors, "proven_missed": v.ProvenMissed, "models_by_role": v.ModelsByRole,
		"record_id": v.RecordID, "record_head": v.RecordHead,
	})

	d.mu.Lock()
	run.verdict = &v
	d.mu.Unlock()
	return &v, nil
}

// recordMutantAttempts feeds BOTH writer seats' per-mutant outcomes, or
// neither.
//
// The pair rule is the point: an unpaired vector cannot contribute to a
// within-run correlation, and a sink half-fed with unpairable rows would
// invite pooling correlation across runs — which is confounded, because each
// run has its own mutant set.
// challengerVectors builds the two writer seats' per-mutant kill vectors over
// run.devSurvivors — the set both are asked to kill — or reports ok=false
// when no comparable pair exists. Shared by recordMutantAttempts (the
// durable, per-mutant DB rows) and challengerPair (the in-memory Jaccard/kappa
// carried straight onto the Verdict), so the two never disagree about WHEN a
// comparison is legitimate.
//
// IT ANSWERS "IS A COMPARISON LEGITIMATE AT ALL", NOT "WHICH SURVIVORS ARE
// IN IT". The maps it returns are the two proven-kill SETS; neither is
// zero-filled over devSurvivors, and a caller must not read a miss out of an
// absent key. Under WriterModePerSurvivor each survivor has its own seat on
// each side, so which survivors are comparable is a PER-MUTANT question —
// both consumers answer it with runState.primarySeatMeasured /
// shadowSeatMeasured, and folding in a survivor neither seat attempted is the
// fabrication both of those filters exist to refuse.
//
// UNMEASURED IS NOT ZERO, for the primary and the challenger alike: this
// guard was originally written for the challenger alone, which left two
// reachable paths to a signed verdict with run.provenIDs still nil
// (testWriterFailed and poolTestUnsound) — on either, killedByPrimary would
// be empty and every survivor would read `survived` for a seat that never
// ran. See runState.primaryWriterMeasured / shadowWriterMeasured.
//
// RULING P11 — a SALVAGED primary is not comparable. The two seats run under
// asymmetric leniency: the primary gets salvageByDeselect (a partially-broken
// suite has its failing selectors deselected and is re-scored, and that
// salvaged remainder becomes provenIDs), a clean-code repair round, and 3
// attempts. The challenger gets 2 compile retries and neither rescue. So when
// the primary salvaged, its vector came from a deselected remainder and the
// challenger's did not, and the head-to-head quietly favours the primary —
// the same class of error as scoring the two seats against different mutant
// sets. This pool's standing discipline is to record no comparison rather
// than a confounded one.
//
// P11 IS A PER-SEAT RULING UNDER THE FAN-OUT, and only a file-wide one in
// batched mode. Batched has one primary seat covering every survivor, so a
// salvage there confounds all of them and the whole file is refused. Under
// per-survivor each survivor has its OWN seat and its own salvage, and
// run.writerSalvaged is true as soon as one of them needed the rescue —
// refusing the file on that threw away every clean, symmetric pair on it
// because one seat was partially broken. The salvaged seats' survivors are
// excluded (runState.primarySeatComparable) and the rest are compared.
func challengerVectors(run *runState) (killedByPrimary, killedByShadow map[string]bool, ok bool) {
	if run.rs.ShadowWriterModel == "" || !run.shadowWriterMeasured || !run.primaryWriterMeasured {
		return nil, nil, false
	}
	if run.writerMode != WriterModePerSurvivor && run.writerSalvaged {
		return nil, nil, false
	}
	// `run.provenIDs` is the PRIMARY writer's proven-kill vector (from
	// provenMutantIDs(rep, run.devSurvivors)). Do NOT use run.devKilled: that
	// is the DEV SUITE's vector over every mutant, so pairing it against a
	// writer compares a writer to the developer's own tests, not writer to
	// writer.
	killedByPrimary = make(map[string]bool, len(run.provenIDs))
	for _, id := range run.provenIDs {
		killedByPrimary[id] = true
	}
	killedByShadow = make(map[string]bool, len(run.shadowWriterKilled))
	for _, m := range run.shadowWriterKilled {
		killedByShadow[m.ID] = true
	}
	return killedByPrimary, killedByShadow, true
}

func (d *Driver) recordMutantAttempts(run *runState, v Verdict) {
	// The v.RecordID != 0 guard is the SAME one BugCatch and CriticFindings
	// use, for the same reason (driver.go:1630): a Driver wired without a
	// Signer leaves RecordID at zero, and rows carrying record_id=0 are
	// unlinkable to the audit that produced them.
	if d.MutantAttempts == nil || v.RecordID == 0 {
		return
	}
	killedByPrimary, killedByShadow, ok := challengerVectors(run)
	if !ok {
		return
	}
	outcome := func(killed bool) string {
		if killed {
			return "killed"
		}
		return "survived"
	}
	// PER SEAT, not per file. Under the fan-out one survivor's seat grading
	// sets primaryWriterMeasured for the WHOLE run, and stamping a `survived`
	// row for the seats that never produced a grading test would record a
	// blind spot for a model that was never given a chance to answer — the
	// same fabrication the file-level guard above already refuses, one grain
	// down. In batched mode both helpers report the file-level flag, because
	// there really is one seat covering every survivor.
	attempts := make([]MutantAttempt, 0, 2*len(run.devSurvivors))
	for _, m := range run.devSurvivors {
		if run.primarySeatComparable(m.ID) {
			attempts = append(attempts, MutantAttempt{
				Path: run.rs.CodePath, MutantID: m.ID,
				Model: d.Assign[RoleTestWriter], Role: RoleTestWriter,
				Shadow: false, Outcome: outcome(killedByPrimary[m.ID]),
			})
		}
		if run.shadowSeatMeasured(m.ID) {
			attempts = append(attempts, MutantAttempt{
				Path: run.rs.CodePath, MutantID: m.ID,
				Model: run.rs.ShadowWriterModel, Role: RoleTestWriterShadow,
				Shadow: true, Outcome: outcome(killedByShadow[m.ID]),
			})
		}
	}
	d.MutantAttempts.Record(v.RecordID, v.RecordHead, attempts)
}

// challengerPair computes the primary/challenger agreement DIRECTLY from the
// run's own in-memory kill vectors — no store, no RecordID/Signer required.
// It is the measurement `corral certify --repo` needs for the warehouse
// (internal/scanstore.File.ChallengerJaccard/Kappa/Sufficient): that path
// signs nothing per file and wires no MutantAttemptSink, so a comparison
// gated on d.MutantAttempts or v.RecordID would be structurally inert there,
// exactly the failure mode recordMutantAttempts' own doc warns about
// ("advpool computed the pair, found d.MutantAttempts nil, and threw every
// row away").
//
// nil whenever challengerVectors reports no comparable pair, or Compare
// itself errors (which construction here should make unreachable: both
// vectors are built over the identical run.devSurvivors set).
func challengerPair(d *Driver, run *runState) *modelcorr.Pair {
	killedByPrimary, killedByShadow, ok := challengerVectors(run)
	if !ok {
		return nil
	}
	// PER SEAT, exactly as recordMutantAttempts filters — and for the same
	// reason, one consumer over. challengerVectors gates on the FILE-level
	// measured flags, which under the fan-out are true as soon as ANY seat on
	// each side grades. Folding in a survivor whose primary or challenger
	// seat never produced a grading test reads `false` on both sides by
	// map-zero-value, which Compare counts as bothSurvived: a SHARED BLIND
	// SPOT invented out of a retry budget. SharedSurvivors inflates, and
	// Jaccard is that over the union, so the headline coefficient would rise
	// with the number of seats that never ran — a measurement of corral's own
	// exhaustion, reported as a fact about two models.
	//
	// A survivor enters the comparison only when BOTH sides genuinely
	// attempted it. In batched mode both helpers report the file-level flag,
	// because one seat per writer really did face every survivor, so that
	// path's vectors are unchanged.
	primaryVec := make(map[string]bool, len(run.devSurvivors))
	shadowVec := make(map[string]bool, len(run.devSurvivors))
	for _, m := range run.devSurvivors {
		if !run.primarySeatComparable(m.ID) || !run.shadowSeatMeasured(m.ID) {
			continue
		}
		primaryVec[m.ID] = killedByPrimary[m.ID]
		shadowVec[m.ID] = killedByShadow[m.ID]
	}
	if len(primaryVec) == 0 {
		// The two seats' measured sets do not overlap at all, so there is
		// nothing to compare. Comparing zero mutants would produce a Pair —
		// a number — where the honest answer is that no comparison exists,
		// the same absence a run with no challenger reports.
		return nil
	}
	pair, err := modelcorr.Compare(
		modelcorr.Vector{Model: d.Assign[RoleTestWriter], Killed: primaryVec},
		modelcorr.Vector{Model: run.rs.ShadowWriterModel, Killed: shadowVec},
	)
	if err != nil {
		return nil
	}
	return &pair
}

// adjudicateCriticFindings builds the execution-checked adjudication for each
// of the test-critic's findings on this run. When the tests×mutants matrix
// ran (run.matrix != nil), each whole-test finding's adjudication is driven
// off the matrix's OWN execution-proven per-test row instead of a fresh
// single-test re-score — see matrixAdjudication. When the matrix did NOT run
// (matrix off, no Enumerator wired, or it skipped/failed), this falls back to
// the ORIGINAL single-test path, unchanged: for each critic finding scoped
// whole-test with a runnable single-test selector, re-run the JAIL's Scorer
// with THAT test alone against the run's own mutant set. If it kills at least
// one mutant, execution has proven the "can never fail" claim false —
// AutoAdjudication downgrades it to refuted. Either path: dead-check findings
// and anything the language plugin can't target as a single test are left
// unadjudicated; neither path ever auto-fails the audit — a scoring/jail
// error is logged and simply leaves that one finding unadjudicated.
func (d *Driver) adjudicateCriticFindings(ctx context.Context, missionID int64, run *runState, criticFindings []queue.Finding, v Verdict) []CriticFindingObservation {
	p := langFor(run.rs)
	var obs []CriticFindingObservation
	for _, f := range criticFindings {
		scope := NormalizeScope(f.Scope)
		var adjudication string
		if run.matrix != nil {
			adjudication = AdjUnadjudicated
			if scope == ScopeWholeTest && f.TestSelector != "" {
				if row := matrixRowFor(run.matrix.Rows, f.TestSelector); row != nil {
					adjudication = matrixAdjudication(*row, run.matrix.Catchable)
				}
			}
		} else {
			ran, kills := false, 0
			if scope == ScopeWholeTest && f.TestSelector != "" {
				if cmd, ok := p.SingleTestCmd(f.TestFile, f.TestSelector); ok {
					// Deliberately NOT narrowed to the run's Selection: this
					// call already names ONE test, which is the whole
					// question ("does this single test kill anything?").
					// Unioning it with the selection would answer a
					// different one.
					if kr, survivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, run.mutants, strings.Join(cmd, " ")); serr != nil {
						log.Printf("advpool: run %d: critic auto-refute score failed for %q: %v", missionID, f.TestSelector, serr)
					} else if kr > 0 {
						// kr == 0 covers BOTH the baseline-couldn't-pass case
						// (adequacy.Score returns CompliantPass:false, Total:0,
						// Survived:nil, err:nil — see JailScorer.Score) and the
						// genuine-zero-kills case; either way there is no
						// execution-proven kill, so leave ran=false, kills=0 ->
						// AutoAdjudication yields unadjudicated (inconclusive),
						// never a false "refuted".
						ran, kills = true, len(run.mutants)-len(survivors)
					}
				}
			}
			adjudication = AutoAdjudication(scope, ran, kills)
		}
		obs = append(obs, CriticFindingObservation{
			QueueFindingID: f.ID,
			Model:          v.ModelsByRole[RoleTestCritic],
			TargetTest:     f.Target,
			TestFile:       f.TestFile,
			TestSelector:   f.TestSelector,
			Scope:          scope,
			Evidence:       f.Evidence,
			Severity:       f.Severity,
			Adjudication:   adjudication,
			Source:         "auto",
		})
	}
	return obs
}

// timeoutVerdict builds the signed needs-review verdict for a run that did
// not converge within RunDeadline. It uses whatever partial data was scored
// (dev kill-rate if the dev-adequacy step ran, else zero) and is forced to
// StatusNeedsReview — a timed-out run is NEVER certified, and (mirroring
// tickAggregate's leaderboard gate, which only fires on StatusCertified) it
// earns no leaderboard fitness for any model: a stalled run proved nothing.
func (d *Driver) timeoutVerdict(run *runState) Verdict {
	// Built from verdictFromSpec, exactly like the converged verdict, and
	// then overlaid with whatever the run actually managed to measure. This
	// used to be a bare Verdict literal, so a timed-out run signed with NO
	// TestSelection and no Uncovered at all — the record could not say what
	// measurement its partial numbers even were. It is the second Verdict
	// construction site in this package, and it has now been the place a
	// field was forgotten more than once.
	v := verdictFromSpec(run.rs)
	v.DevKillRate = run.devKillRate
	v.BaselineDuration = run.baselineDuration
	v.MutantsTotal = run.mutantsTotal
	v.MutantsInvalid = run.mutantsInvalid
	v.Survivors = len(run.devSurvivors)
	v.ProvenMissed = run.provenMissed
	// The mutant-level evidence must ride the TIMEOUT verdict too, for the
	// same reason BaselineDuration and the could-not-grade flags do below:
	// a run that scored dev adequacy and only then stalled has real
	// per-mutant data, not zero values nothing ever computed.
	v.DevKilledMutants = run.devKilled
	// Graded, like the killed refs beside them: carrying the grading on one
	// vector and not the other would put two grains of the same evidence in
	// one signed record.
	v.DevSurvivedMutants = toMutantRefsWith(run.devSurvivors, run.perMutant)
	// And the same disclosure the aggregate applies, from the same refs: a
	// run that graded per mutant and only THEN stalled measured what it
	// measured, and the record has to say so.
	applyPerMutantStats(&v, run.perMutantGraded, v.DevKilledMutants, v.DevSurvivedMutants)
	// The could-not-grade flags must ride the TIMEOUT verdict too. A run
	// that scored dev adequacy, could not grade it (failed baseline, or a
	// surviving canary), and only then stalled would otherwise be SIGNED
	// reading "DevKillRate 0, Survivors 0, MutantsTotal N" with no marker —
	// renderAdvVerdict falls straight through to `dev_kill_rate: 0.00`,
	// which is a fabricated measurement. Never fabricate a score.
	v.BaselineFailed = run.baselineFailed
	v.BaselineOutput = run.baselineOutput
	v.SuiteIgnoresFile = run.suiteIgnoresFile
	// TestWriterFailed/PoolTestUnsound must ride the TIMEOUT verdict too:
	// a run that reached tickPoolAdequacy, converged its pool score (or
	// gave up on a non-compiling test) and only THEN stalled (test-critic
	// never finished) would otherwise sign a TimedOut verdict with a
	// ProvenMissed that looks like an ordinary graded value, dropping the
	// caveat that explains it.
	v.TestWriterFailed = run.testWriterFailed
	v.PoolTestUnsound = run.poolTestUnsound
	v.AuthoredTestNotCollected = run.authoredTestNotCollected
	// The writer's own disclosure, for the same reason as the flags above: a
	// run that fanned out, graded some seats and only THEN stalled must sign
	// WHICH measurement its partial numbers are, keep the proofs it earned,
	// and say how many seats never graded. This function's doc records that
	// it has been the place a field was forgotten more than once; these three
	// are pinned by TestTimeoutVerdictCarriesTheWriterDisclosure.
	v.WriterMode = run.writerMode
	v.AuthoredExtra = run.authoredExtra
	v.WriterSeatsUngraded = run.writerSeatsUngraded()
	v.WriterAttempts = writerAttemptSpread(run)
	// Coverage fields (I-5): a run that dispatched N regions and dropped
	// some before hitting RunDeadline must carry that shortfall on the
	// timeout verdict too, or the CLI's RegionsTotal > 0 guard silently
	// suppresses PARTIAL AUDIT for exactly the run most likely to have one
	// (a stall is often the dropped regions' downstream symptom).
	v.RegionsTotal = run.regionsTotal
	v.RegionsProbed = run.regionsProbed
	v.DroppedRegions = run.droppedRegions
	v.DuplicateMutants = run.dupMutants
	v.PromptShape = run.promptShape
	v.ModelsByRole = map[string]string(d.Assign)
	// A stalled run still spent everything it spent, and it is the run an
	// operator most needs the clock for: "which phase was it sitting in when
	// the deadline hit" is answerable only from the phases that DID close,
	// PLUS whatever phase was still open — attributed here with the wall
	// clock it actually spent, not left at the zero-value "—" that means "did
	// not run". A phase in flight when the deadline fires is not the same
	// claim as a phase that never started (see attributeOpenPhases): a
	// per-survivor writer that compiled, failed on the unmutated code, and
	// was mid-reissue when RunDeadline fired genuinely ran for however long
	// it ran, and this is the last chance to say so before its own start
	// time is lost.
	d.attributeOpenPhases(run)
	v.Timing = timingWith(v.Timing, run.timing)
	v.Timing.Total = totalWith(v.Timing, d.now().Sub(run.startedAt))
	v.MutantDurationMedian = run.mutantDurationMedian
	v.MutantDurationMax = run.mutantDurationMax
	v.Status = StatusNeedsReview
	v.TimedOut = true
	v.DevScored = run.devScored
	// The pool half, for the same reason as DevScored above — and the EVIDENCE
	// behind it, not just the count.
	//
	// ProvenMissed has ridden this verdict for a while; ProvenMutantIDs and
	// AuthoredTest did not. That split is incoherent on its face: the record
	// asserted "N survivors are provably catchable" while dropping WHICH ones
	// and the test that proves it, so certify_repo_record's per-survivor
	// Proven flag (derived from ProvenMutantIDs) marked every one of them
	// unproven — a ledger row reading proven_missed=N beside zero proven
	// survivors. One grain of a measurement is not a measurement.
	v.PoolScored = run.poolScored
	v.ProvenMutantIDs = run.provenIDs
	v.AuthoredTest = run.authoredTest
	// The challenger comparison, for the same reason as everything above it:
	// challengerPair is a pure function of run state that already returns nil
	// when the two seats' measured sets do not overlap, so a run that graded
	// both sides and only THEN stalled has a real coefficient — and dropping
	// it here recorded NULL jaccard/kappa that a reader cannot tell apart from
	// "no challenger ran".
	//
	// It was the live instance of the hazard AGENTS.md records: two Verdict
	// construction paths, one field assigned in only one of them.
	// WriterSeatsUngraded three lines up is assigned in both; this was not.
	v.ChallengerAgreement = challengerPair(d, run)
	return v
}

// feedLeaderboard is the gate-earned fitness feed: one (model, role,
// outcome) call per role, derived from the CERTIFIED (Scorer-scored, gated,
// signed) result only — never from a worker's self-report.
func (d *Driver) feedLeaderboard(v Verdict, testWriterMoot bool) {
	outcome := func(ok bool) string {
		if ok {
			return OutcomePass
		}
		return OutcomeFail
	}
	// test-writer: did its authored test kill the survivors it was targeted at?
	// Skipped entirely when it never ran (a perfect dev suite left no survivors
	// to target) — a model must never be recorded as failing a task it didn't
	// attempt, or a strong suite would systematically penalize a good writer.
	if !testWriterMoot {
		d.Leaderboard.Record(v.ModelsByRole[RoleTestWriter], RoleTestWriter, outcome(v.ProvenMissed > 0))
	}
	// mutant-generator: it did its job if it produced usable (compiling) mutants
	// at all — whether those mutants then SURVIVE is the dev suite's business,
	// not the generator's, so a perfect suite killing them is not a generator
	// failure.
	d.Leaderboard.Record(v.ModelsByRole[RoleMutantGenerator], RoleMutantGenerator, outcome(v.MutantsTotal > 0))
	// test-critic: did its findings hold (it actually flagged something)?
	d.Leaderboard.Record(v.ModelsByRole[RoleTestCritic], RoleTestCritic, outcome(len(v.VacuousFindings) > 0))
}

// isOperationalFinding reports whether f is an operational event (e.g. a
// model-unreachable notice filed by a worker), not an audit finding. These are
// visible to operators but never count as a critic's judgment nor block
// certification — an infrastructure hiccup is not a defect in the change.
func isOperationalFinding(f queue.Finding) bool { return f.Type == "ops" }

// filterCriticFindings returns the test-critic task's AUDIT findings
// (excluding operational events), used to populate Verdict.VacuousFindings.
func filterCriticFindings(findings []queue.Finding, criticTaskID int64) []queue.Finding {
	var out []queue.Finding
	for _, f := range findings {
		if f.TaskID == criticTaskID && !isOperationalFinding(f) {
			out = append(out, f)
		}
	}
	return out
}

// blockingFindingOpen mirrors mission.Engine.blockingFindingOpen: any OPEN
// finding at or above BlockSeverity withholds certification. "" disables it.
// Operational findings (model-unreachable, etc.) are excluded — an infra
// hiccup is never certification-blocking.
func (d *Driver) blockingFindingOpen(findings []queue.Finding) bool {
	if d.BlockSeverity == "" {
		return false
	}
	minRank := queue.SeverityRank(d.BlockSeverity)
	for _, f := range findings {
		if isOperationalFinding(f) {
			continue
		}
		if f.Status == queue.FindingOpen && queue.SeverityRank(f.Severity) >= minRank {
			return true
		}
	}
	return false
}

// checkNoProgress is the give-up backstop, mirroring
// mission.Engine.checkNoProgress: while the run's progress fingerprint keeps
// changing, or any task is claimed (a bee is actively holding work — slow is
// not stuck), it is fine. Only when the fingerprint is unchanged AND nothing
// is claimed for NoProgressTicks consecutive ticks does the run fail.
func (d *Driver) checkNoProgress(missionID int64) error {
	if d.NoProgressTicks <= 0 {
		return nil
	}
	fp, claimed, err := d.progressFingerprint(missionID)
	if err != nil {
		return fmt.Errorf("advpool: progress check: %w", err)
	}
	if fp != d.lastFingerprint[missionID] {
		d.lastFingerprint[missionID] = fp
		d.noProgress[missionID] = 0
		return nil
	}
	if claimed > 0 {
		return nil
	}
	d.noProgress[missionID]++
	if d.noProgress[missionID] >= d.NoProgressTicks {
		return fmt.Errorf("advpool: run %d stalled — no forward progress and nothing claimable for %d ticks", missionID, d.NoProgressTicks)
	}
	return nil
}

// progressFingerprint mirrors mission.Engine.progressFingerprint: a string
// that changes whenever the run makes forward progress (a task reaches a
// terminal state or a finding is filed/resolved), plus the claimed count.
func (d *Driver) progressFingerprint(missionID int64) (string, int, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return "", 0, err
	}
	terminal, claimed := 0, 0
	for _, t := range tasks {
		switch t.Status {
		case queue.StatusDone, queue.StatusCancelled, queue.StatusSuperseded:
			terminal++
		case queue.StatusClaimed:
			claimed++
		}
	}
	open, err := d.Q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%d/%d/%d", terminal, len(tasks), len(open)), claimed, nil
}

// taskByKey looks up a mission task by its (still-stable) key. Safe for
// mutant-generator and test-critic, which are only ever Reopened (status
// changes, key never does) — never Superseded (that's test-writer's path;
// see the note in tickDevAdequacy for why that one must be tracked by id).
func (d *Driver) taskByKey(missionID int64, key string) (*queue.Task, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].Key == key {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// shardIndexOfAnyKey parses a shard index out of EITHER a primary
// mutant-generator key or a challenger (shadow) one. tasksByRole is called for
// both roles, and the two key formats differ by prefix — so parsing only the
// primary form dropped every shadow seat into the lexicographic fallback,
// ordering them /0, /1, /10, /2 past ten shards and silently voiding the
// numeric-order guarantee tasksByRole's doc comment spends ten lines making.
func shardIndexOfAnyKey(key string) (int, bool) {
	if i, ok := ShardIndexFromKey(key); ok {
		return i, true
	}
	return ShadowShardIndexFromKey(key)
}

// tasksByRole returns every task for a role, sorted by parsed shard index
// (the bare unsharded key first) rather than by key string — a lexicographic
// sort on Key would order ten-plus shards as /0, /1, /10, /11, /2, ... once
// --max-shards (operator-settable, unbounded) crosses ten. Nothing downstream
// derives shard index from slice position today (ShardIndexFromKey always
// re-parses it from the key), but shard index is itself a recorded metrics
// key, and per-shard metrics are about to fold over exactly this slice — so
// the order must be numeric and deterministic, not an inherited positional
// assumption. Used for the mutant-generator, which fans out into one task per
// symbol shard; taskByKey remains correct for the single-task roles.
func (d *Driver) tasksByRole(missionID int64, role string) ([]queue.Task, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return nil, err
	}
	var out []queue.Task
	for i := range tasks {
		if tasks[i].Role == role {
			out = append(out, tasks[i])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ii, iSharded := shardIndexOfAnyKey(out[i].Key)
		ji, jSharded := shardIndexOfAnyKey(out[j].Key)
		if iSharded != jSharded {
			// Unsharded key sorts first (it stands in for shard 0 in an
			// unsharded run).
			return jSharded
		}
		if iSharded {
			return ii < ji
		}
		// Both unsharded: identical role means identical key, but keep the
		// comparator total.
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// sampleInvalidReasons returns up to n IDs of rejected mutants that carry a
// recorded reason, in Invalid order so the sample is deterministic.
func sampleInvalidReasons(run *runState, n int) []string {
	if len(run.invalidReasons) == 0 {
		return nil
	}
	out := make([]string, 0, n)
	for _, m := range run.mutants {
		if len(out) >= n {
			break
		}
		if _, ok := run.invalidReasons[m.ID]; ok {
			out = append(out, m.ID)
		}
	}
	return out
}

// firstLine trims a compiler's multi-line output to its first meaningful line,
// which is the diagnosis; the rest is usually position noise.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return strings.TrimSpace(s)
}
