// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// defaultScanDSN resolves the DuckDB scan ledger path exactly the way
// localBugCatchDBPath (cmd/corral/certify_local_bugcatch.go) resolves its
// own store: the CORRALAI_SCANS_DB env var, else os.UserHomeDir() falling
// back to os/user.Current(), else ~/.claude/corralai_scans.duckdb. Kept as
// the same resolution order deliberately — an operator who already knows
// that pattern from the bugcatch store should not have to learn a second
// one for this store.
func defaultScanDSN() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_SCANS_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_scans.duckdb")
}

// killRatePtr converts a possibly-NaN kill rate into the *float64 form
// scanstore.Scan.KillRate expects, nil-ing out NaN explicitly at the
// source. reposcan.RepoReport.KillRate is math.NaN() when nothing was
// audited (see internal/reposcan/report.go) — a deliberate choice so a
// stored 0.0 never misrepresents "no measurement was made" as "terrible
// tests". scanstore.sanitizeKillRate already nils a NaN defensively at the
// store boundary too; this call does the same conversion at the source, so
// the intent is visible here rather than relying solely on that backstop.
func killRatePtr(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

// buildScanFileRows turns one repo report into the rows scanstore.Record
// wants — one per file the scan judged, audited or rejected. It reads from
// TWO sources, deliberately, not one:
//
//   - results (reposcan.Scan's own return value, still in scope at the
//     call site) — one FileResult per JOB actually run. A gradable result
//     becomes an "audited" row (KillRate + Survivors, evidence "proven").
//     An UNgradable result is still a row: prep-failed, baseline-failed,
//     flaky-baseline, suite-ignores-file, executor-error and cancelled are
//     the scan's MOST EXPENSIVE rejections — corral selected most of these
//     files, emitted jobs for them, prepped a jail, and ran the check
//     command at least once — and before this fix they were tallied only
//     in rep.Ungradable (a map[reason]int with no per-file paths) and had
//     NO row here at all: a file the scan visibly worked on was invisible
//     to "why did file X get skipped on scan N", the exact question this
//     store exists to answer (see internal/scanstore/store.go's package
//     doc). Evidence for these rows is NOT a blanket "proven" — see
//     ungradableEvidence below, which is where "proven" earns its keep as
//     the label this table's defensibility rests on.
//   - rep.Excluded — every file that never became a job at all: the
//     enumerate-level reasons (no-language, is-test, no-paired-test,
//     ambiguous-test, not-regular-file) and the candidate-level ones added
//     before job emission (ungoaled, source-too-large, derive-failed,
//     not-selected). These are decided by filename pairing or bookkeeping,
//     never by running anything, so evidence is "paired" for the ones a
//     pairing was actually ATTEMPTED for and "" for the ones rejected before
//     TestPaths was ever called (see exclusionEvidence, which used to stamp
//     "paired" on all of them indiscriminately) — unless
//     the coverage pre-flight (a SEPARATE, independent inventory over the
//     same enumerated source set — see runPreflight) also measured this
//     path, in which case evidence is promoted to "coverage": a row that
//     carries a preflight_state IS carrying coverage-derived evidence, and
//     Task 1's own TestRecordRoundTripsEveryDisposition models exactly
//     that shape (reason "not-selected", preflight_state "not-executed",
//     evidence "coverage") — leaving it "paired" here would silently
//     contradict that fixture and make `WHERE evidence='coverage'` return
//     nothing for a row that plainly has coverage evidence attached.
//
// A path cannot legitimately appear in both sources (results only holds
// paths that became jobs; rep.Excluded only holds paths that never did),
// but a `seen` set skips a second row for any path already written rather
// than assume that invariant holds forever. The results half of that guard
// is dedupeResultsByPath, SHARED with buildScanMutantRows: a duplicate path
// that produced one scan_files row and two full sets of scan_mutants rows
// would silently double-count in exactly the grain that exists for grading
// models.
//
// preflightState overlays preflight_state onto every row, but ONLY when
// the coverage pre-flight actually ran — see preflightState's own doc
// comment for why a path absent from the map must stay empty rather than
// being recorded as "not-executed".
// mutantsFrom is the sha256 of the recorded mutant set this scan REPLAYED
// (`--mutants`), or "" when it generated its own. It rides only on AUDITED
// rows: a rejected or excluded file sat no exam at all, and stamping a set
// identifier on it would claim it was graded against one.
// ParentSHA256 — the validity key a later reader checks against its own
// checkout to tell a live verdict from a stale one — has ONE source per
// disposition, and which source is not a detail:
//
//   - An AUDITED file takes the hash its own MUTANTS carry
//     (advpool.MutantRef.ParentSHA256). The generator hashed the exact bytes
//     it mutated, before anything overlaid the file, so that hash IS what
//     was graded. All of a file's mutants must agree; if they do not, there
//     is no single answer to "what was audited" and the row records NOTHING
//     (with a line on stderr) rather than picking one and making a stale
//     verdict look live.
//   - A file that was never graded — rejected, or excluded before it ever
//     became a job — has no mutants to take a hash from, so and only so is
//     repoDir/path read here. "Never audited" is a state the seal reader has
//     to be able to report, and it needs a hash to report it against.
//
// Re-reading the checkout for an AUDITED file would be a different source,
// not a second one: on the workspace substrate the audit writes each mutant
// into the file in place and restores it afterwards, so a read at record
// time is not guaranteed to be the bytes the generator hashed — and a
// validity key that can disagree with the mutants it was derived from is
// worse than none. (An earlier version of this comment claimed
// `certify --repo` "never writes into --repo". It does.)
//
// A file that cannot be read gets "" rather than a fabricated hash.
func buildScanFileRows(results []reposcan.FileResult, excluded []reposcan.Exclusion, preflight reposcan.CoverageMap, mutantsFrom string, repoDir string, stderr io.Writer) []scanstore.File {
	results = dedupeResultsByPath(results)
	rows := make([]scanstore.File, 0, len(results)+len(excluded))
	seen := make(map[string]bool, len(results)+len(excluded))

	for _, r := range results {
		path := r.Job.Path
		seen[path] = true

		if r.Gradable {
			kr := r.Verdict.DevKillRate
			// CacheKey and VerdictJSON are written on THIS branch only — the
			// load-bearing invariant is ledgerCache.Get's own: it hands back
			// Gradable:true unconditionally on a hit, justified by "only
			// gradable results are ever recorded with a key" (verdict_cache.go).
			// A rejected/excluded row (below, and in the second loop) must
			// carry an empty CacheKey and VerdictJSON, or a LATER scan's cache
			// lookup would resurrect a non-verdict as a graded one.
			verdictJSON, merr := marshalVerdict(r.Verdict)
			if merr != nil {
				// A marshalling failure must NOT fail the scan: the audit
				// already ran and the verdict already stands on every other
				// column of this row. The only cost is that this row is not
				// cacheable — a future re-audit of this exact content pays
				// for it again — which is far cheaper than losing the whole
				// run's recording over a serialization bug.
				// Routed to the caller's own stderr, not the global logger:
				// this command threads a stderr writer everywhere else,
				// and a caller capturing stderr must actually SEE a
				// fail-open disclosure.
				fmt.Fprintf(stderr, "corral certify --repo: %s: verdict not cached (marshal failed): %v\n", path, merr)
				verdictJSON = ""
			}
			rows = append(rows, scanstore.File{
				Path: path, Lang: r.Job.Lang, Disposition: "audited",
				KillRate: &kr, Survivors: r.Verdict.Survivors, Gradable: true,
				Evidence: "proven", PreflightState: preflightState(preflight, path),
				// TimedOut rides straight through from the verdict: a claim
				// carries how it was earned, and a query over this ledger
				// must be able to tell a banked, unconverged score apart
				// from a clean audited row.
				TimedOut: r.Verdict.TimedOut,
				// TestWriterFailed rides through the same way: without this
				// a row with survivors > 0 and a clean-looking kill rate
				// would read as "no real gaps" when it actually means "gaps
				// found, no killing test could be authored to prove them."
				TestWriterFailed: r.Verdict.TestWriterFailed,
				// ProvenMissed rides through too — corral's strongest claim
				// (a survivor its own authored test killed BY EXECUTION),
				// now stored per-row so a later query can filter on
				// `proven_missed > 0` instead of losing the distinction the
				// moment this row is written.
				ProvenMissed: r.Verdict.ProvenMissed,
				// PoolTestUnsound rides through the same way, DISTINCT from
				// TestWriterFailed: a compiling test WAS produced here, its
				// own scoring report just never genuinely graded — without
				// this a row with survivors > 0 and this diagnosis would be
				// indistinguishable from an ordinary "tried and missed" 0.
				PoolTestUnsound: r.Verdict.PoolTestUnsound,
				// The EVIDENCE behind ProvenMissed, not just its magnitude.
				// Every field above records a VERDICT about the attempt; these
				// two record the attempt ITSELF. Without them a row reading
				// `proven_missed = 0` is terminal — you can see that the pool
				// tried and caught nothing, but never what it tried, and on the
				// repo path there is no tape to fall back on, so the only way
				// to ask again is to pay for another audit. That is exactly the
				// wall a real pallets/flask "tried and missed" hit on
				// 2026-07-31.
				ProvenMutantIDs: strings.Join(r.Verdict.ProvenMutantIDs, ","),
				AuthoredTest:    r.Verdict.AuthoredTest,
				// ModelsByRole is canonicalized the same way the scan-wide
				// model_set is (reposcan.CanonicalKV), so a per-file role
				// assignment is byte-comparable with it rather than a second,
				// possibly-drifting serialization.
				ModelsByRole:             reposcan.CanonicalKV(r.Verdict.ModelsByRole),
				MutantsTotal:             r.Verdict.MutantsTotal,
				RegionsTotal:             r.Verdict.RegionsTotal,
				RegionsProbed:            r.Verdict.RegionsProbed,
				DroppedRegions:           strings.Join(r.Verdict.DroppedRegions, ","),
				VacuousFindings:          len(r.Verdict.VacuousFindings),
				Status:                   r.Verdict.Status,
				AuthoredTestNotCollected: r.Verdict.AuthoredTestNotCollected,
				BaselineFailed:           r.Verdict.BaselineFailed,
				// SuiteBaselineMillis is the cost-model input: how long the
				// dev suite's own compliant run took, in milliseconds — see
				// scanstore.File.SuiteBaselineMillis. Like every timing
				// column, it says what THIS scan spent: a reused verdict
				// carries the ORIGINAL run's baseline in its JSON, and
				// recording that here would be another scan's measurement
				// under this scan's id — 0 stores as NULL downstream.
				SuiteBaselineMillis: baselineMillisUnlessReused(r),
				// CacheHit rides through from reposcan.FileResult — this row's
				// verdict was served from a prior scan's cache_key match, not
				// earned by this scan. ReusedFromScanID stays nil here: the
				// cache doesn't hand back the source scan id yet (that's a
				// later task's job), and a nil is the honest value in the
				// meantime — "reused, source scan not recorded" — not a
				// fabricated lineage.
				// WHICH MEASUREMENT this row's kill rate is. Without these the
				// ledger stores a number and loses the question it answers:
				// 0.65 against the 14 tests that execute the file and 0.65
				// against all 1431 are different claims, and no other column
				// can tell them apart afterwards.
				TestSelection:     r.Verdict.TestSelection.Method,
				SelectedTests:     r.Verdict.TestSelection.Selected,
				SuiteTests:        r.Verdict.TestSelection.Of,
				SelectionFallback: r.Verdict.TestSelection.Fallback,
				// Uncovered also makes the store write kill_rate NULL (see
				// scanstore.fileKillRate): no test executes this file, so
				// there is no measurement to record.
				Uncovered: r.Verdict.Uncovered,
				// WHICH exam this kill rate answers, when it was a recorded
				// one. See scanstore.File.MutantsFrom.
				MutantsFrom: mutantsFrom,
				// How many private trees scored this file at once, or why
				// it only got one — the same fact the screen and the
				// attestation say. See scanstore.File.Trees.
				Trees:           r.Verdict.Concurrency.Trees,
				ConcurrencyNote: r.Verdict.Concurrency.Note,
				// And which dep dirs those trees SHARED — comma-joined,
				// NULL when none. See scanstore.File.SharedDirs.
				SharedDirs: strings.Join(r.Verdict.Concurrency.Shared, ","),
				CacheHit:   r.CacheHit,
				// VerdictJSON is the single serialization every future Get
				// has to parse (marshalVerdict, verdict_cache.go) — "" above
				// on a marshal failure, never a partial or hand-rolled blob.
				VerdictJSON: verdictJSON,
				// CacheKey is reposcan's content address for this exact
				// verdict; a future scan's ledgerCache.Get matches on this
				// column. r.Job.CacheKey is populated for every job
				// (fresh-computed or itself a cache hit — Scan sets
				// hit.Job = j on a hit, so CacheKey rides through reuse too).
				CacheKey: r.Job.CacheKey,
				// ComputedAt is when this verdict was actually EARNED — the
				// original audit's timestamp, not this scan's. On a cache
				// hit it rides through from ledgerCache.Get unchanged, which
				// is what lets oldestReuse (verdict_cache.go) report how old
				// a reused verdict really is instead of when it was reused.
				ComputedAt: r.ComputedAt,
				// The validity key: the bytes the generator mutated, taken
				// from this file's own mutants. See the doc above.
				ParentSHA256: auditedParentSHA256(path, r.Verdict, stderr),
				// The denominators the rate is over, split. MutantsGraded is
				// the verdict's own "actually graded" count (compile-gate
				// rejects excluded) and MutantsInvalid is what the gate threw
				// out — a rate over 8 of 12 mutants and one over 8 of 8 are
				// different claims. MutantsTimedOut has no source in the
				// verdict yet and stays nil — SQL NULL — rather than
				// claiming, on every row, that nothing timed out.
				MutantsGraded:  r.Verdict.MutantsTotal,
				MutantsInvalid: r.Verdict.MutantsInvalid,
				// At which GRAIN the rate was measured, carried in the ledger
				// so the warehouse row can be built from the ledger alone.
				PerMutant:            r.Verdict.TestSelection.PerMutant,
				TestsPerMutantMin:    spreadMin(r.Verdict.TestSelection.TestsPerMutant),
				TestsPerMutantMedian: spreadMedian(r.Verdict.TestSelection.TestsPerMutant),
				TestsPerMutantMax:    spreadMax(r.Verdict.TestSelection.TestsPerMutant),
				// WHERE THE MINUTES WENT, one nullable column per phase.
				// millisOrNil, not a bare Milliseconds(): a phase that did
				// not run must read back as unknown, and a stored 0 would be
				// averaged into the cost model as a phase that is free.
				//
				// ALL NULL ON A CACHE HIT (unmeasuredOnReuse). A reused
				// verdict's Timing round-trips through verdict_json and comes
				// back fully populated with the minutes the run that EARNED
				// it spent; storing them again on this scan's row would tell
				// a cost query that a cache hit costs as much as the audit it
				// replaced. The row still says the verdict was reused
				// (CacheHit, ReusedFromScanID), and the scan it was reused
				// FROM still holds the real clock.
				SelectionMillis:    unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.Selection)),
				GenerationMillis:   unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.Generation)),
				PoolMillis:         unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.Pool)),
				DevPassMillis:      unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.DevPass)),
				AuthoredPassMillis: unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.AuthoredPass)),
				CriticMillis:       unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.Critic)),
				TotalMillis:        unmeasuredOnReuse(r, millisOrNil(r.Verdict.Timing.Total)),
				// And the shape of the dev pass at the file grain: one slow
				// mutant or forty ordinary ones.
				MutantMillisMedian: unmeasuredOnReuse(r, millisOrNil(r.Verdict.MutantDurationMedian)),
				MutantMillisMax:    unmeasuredOnReuse(r, millisOrNil(r.Verdict.MutantDurationMax)),
				// The primary/challenger agreement — NULL (all three
				// pointers nil) unless a comparable pair was actually
				// computed. See advpool.Verdict.ChallengerAgreement's doc
				// for the full gating.
				ChallengerJaccard:    challengerJaccard(r.Verdict.ChallengerAgreement),
				ChallengerKappa:      challengerKappa(r.Verdict.ChallengerAgreement),
				ChallengerSufficient: challengerSufficient(r.Verdict.ChallengerAgreement),
				// How many goals reposcan's DERIVER produced for this file —
				// 0 (not NULL: the column is a plain int, see
				// scanstore.File.GoalsDerived's doc) unless this file's goal
				// actually came from derivingGoalSource.
				GoalsDerived: goalsDerivedFor(r.Job.Goal),
			})
			continue
		}

		// Mirrors Aggregate's own fallback (internal/reposcan/report.go) so
		// this row's reason always matches what rep.Ungradable[reason]
		// counted for the same file — an empty Reason must not become an
		// empty (and therefore useless) reason column.
		reason := r.Reason
		if reason == "" {
			reason = reposcan.ReasonExecutorError
		}
		rows = append(rows, scanstore.File{
			Path: path, Lang: r.Job.Lang, Disposition: "rejected", Reason: reason,
			Gradable: false, Evidence: ungradableEvidence(reason), PreflightState: preflightState(preflight, path),
			Detail: r.Detail,
			// Ungradable: no verdict, so no mutants, so the hash is a read
			// of the checkout — the same rule the excluded rows below follow.
			ParentSHA256: auditedFileSHA256(repoDir, path),
			// A job WAS emitted for this file (it has a Goal — EmitJobs never
			// emits one without), so the same GoalsDerived question has a real
			// answer here too, even though the file never got a verdict.
			GoalsDerived: goalsDerivedFor(r.Job.Goal),
		})
	}

	for _, e := range excluded {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true

		pfState := preflightState(preflight, e.Path)
		rows = append(rows, scanstore.File{
			Path: e.Path, Lang: detectLang(e.Path), Disposition: "rejected", Reason: e.Reason,
			Gradable: false, Evidence: exclusionEvidence(e.Reason, pfState), PreflightState: pfState,
			// Never graded, so there are no mutants to take a hash from:
			// this is the one disposition whose hash is a read of the
			// checkout. See buildScanFileRows' doc.
			ParentSHA256: auditedFileSHA256(repoDir, e.Path),
		})
	}
	return rows
}

// ungradableEvidence decides the evidence label for a job that ran (or
// tried to) and came back ungradable — see buildScanFileRows. "proven" is
// the single label this whole table's defensibility rests on: a later
// grading query treats it as "corral actually executed something against
// this file", and that claim must be TRUE, not merely "corral meant to".
//
// Two of the six ungradable reasons never reach that bar:
//   - reposcan.ReasonCancelled is written by reposcan.Scan itself, BEFORE
//     ex.Execute is ever called (see internal/reposcan/scan.go) — the
//     check command never ran, full stop. This function's own earlier
//     comment used to concede this and stamp "proven" anyway; that was
//     the overclaim, not a defensible choice.
//   - reposcan.ReasonPrepFailed is returned at the very top of
//     localExecutor.Execute (cmd/corral/certify_repo.go), when the
//     language-wide jail seed fails to build — BEFORE l.newBaseline is
//     ever reached, so the check command never ran for this file either.
//
// Both get "" (no evidence claim), the same value an excluded file that
// was never even a candidate gets — deliberately not a fifth enum value:
// "no evidence" and "" already mean the same thing everywhere else in
// this table (see exclusionEvidence), and reusing it means one fewer
// distinct string a query has to know about, not one more.
//
// The other four reasons (baseline-failed, flaky-baseline,
// suite-ignores-file, executor-error) DO clear the bar: baseline-failed
// and flaky-baseline are decided FROM the check command's own baseline
// runs (it ran — possibly more than once — and either failed
// consistently or disagreed with itself); suite-ignores-file is decided
// from a completed audit run (the canary survived); executor-error covers
// every other failure inside Execute, all of which are reached only AFTER
// at least one real baseline run has already happened (CheckBaselineStable
// erroring mid-run, or the audit step itself failing once baseline passed
// — see Execute's own ordering). All four keep "proven".
func ungradableEvidence(reason string) string {
	switch reason {
	case reposcan.ReasonCancelled, reposcan.ReasonPrepFailed:
		return ""
	default:
		return "proven"
	}
}

// exclusionEvidence decides the evidence label for a file that was
// excluded WITHOUT ever running (see buildScanFileRows). A non-empty
// preflightState means the coverage pre-flight's own instrumented run
// measured this exact path, which outranks a bare filename-pairing guess —
// and applies even to a file pairing never looked at, because the
// instrumented run really did measure it.
//
// Absent that, the label turns on ONE question: was a filename pairing
// actually attempted for this file? This function used to answer "yes"
// unconditionally, stamping "paired" on every exclusion — including a
// .editorconfig rejected as no-language, which no plugin ever claimed and
// TestPaths was never called for. That is a false evidence claim, in the one
// column whose whole purpose is to keep proof and guesswork apart, and it was
// written into every scan this ledger has ever recorded. It was invisible
// until `corral scans` could SELECT it — a write-only store hides its own
// data-quality bugs.
//
// It also contradicted the contract stated immediately above in
// ungradableEvidence's own doc: that "" is "the same value an excluded file
// that was never even a candidate gets — (see exclusionEvidence)". It never
// was.
//
// The split follows internal/reposcan/candidate.go's own ordering, not
// taste. Pairing was NOT attempted for:
//   - no-language (:215) — no plugin matched, so TestPaths is never reached
//   - is-test (:222) — the file IS a test, excluded before pairing
//   - not-a-regular-file, skipped-dir, gitignored — walk-time, earlier
//     still, before language detection even runs
//
// Pairing WAS attempted, and its result is precisely what decided the row, for
// no-paired-test (:241, the search came up empty), ambiguous-test (the search
// collided), and not-selected (it WAS a candidate, so it has a pair — only the
// --top bound excluded it).
//
// An UNKNOWN reason falls through to "" rather than "paired": a future
// exclusion reason added elsewhere must not silently inherit an evidence claim
// nobody decided it had earned.
func exclusionEvidence(reason, preflightState string) string {
	if preflightState != "" {
		return "coverage"
	}
	switch reason {
	case reposcan.ReasonNoPairedTest, reposcan.ReasonAmbiguousTest, reposcan.ReasonNotSelected:
		return "paired"
	default:
		return ""
	}
}

// detectLang resolves a language name for a file the scan never ran (so
// there is no Job.Lang to read) using the same lang.Detect the enumerator
// itself uses. Empty when no plugin recognizes the path (e.g. README.md) —
// never guessed.
func detectLang(path string) string {
	if plug, ok := lang.Detect(path); ok {
		return plug.Name()
	}
	return ""
}

// preflightState answers, for one path, what the coverage pre-flight
// learned about it — "executed", "not-executed", or "" (empty). Empty
// covers TWO cases that must not be conflated with a claim: the pre-flight
// never ran at all (preflight.Ran is false — --preflight was not given, or
// it declined/failed), and the pre-flight DID run but this path is absent
// from its Executed map, meaning its own instrumentation never measured
// this path at all (e.g. outside coverage.py's own source scope, or a
// different language than the one instrumented). Recording either case as
// a state would store a claim corral did not make.
func preflightState(cm reposcan.CoverageMap, path string) string {
	if !cm.Ran {
		return ""
	}
	executed, measured := cm.Executed[path]
	if !measured {
		return ""
	}
	if executed {
		return "executed"
	}
	return "not-executed"
}

// dedupeResultsByPath keeps the FIRST result for each source path, and is
// the one place that invariant is enforced for BOTH row builders.
//
// reposcan.Scan produces one result per job and jobs are one per candidate
// path, so a duplicate should not arise today. Neither builder assumes that
// holds forever: buildScanFileRows would write one row for a repeated path
// while buildScanMutantRows wrote two complete sets of mutant rows, and the
// per-mutant grain is precisely the grain a "which generator produces mutants
// a suite misses" leaderboard is computed over — a silent double-count there
// grades the models wrong.
func dedupeResultsByPath(results []reposcan.FileResult) []reposcan.FileResult {
	seen := make(map[string]bool, len(results))
	out := make([]reposcan.FileResult, 0, len(results))
	for _, r := range results {
		if seen[r.Job.Path] {
			continue
		}
		seen[r.Job.Path] = true
		out = append(out, r)
	}
	return out
}

// buildScanMutantRows turns the scan's gradable results into scan_mutants
// rows — one per mutant the dev suite's own tests killed OR left surviving,
// keyed to scanID (the id Record just handed back). Ungradable results are
// skipped: nothing was ever scored, so they have no mutant fates to record.
//
// Duplicate source paths are filtered by dedupeResultsByPath, the SAME guard
// buildScanFileRows applies — see there for what a divergence between the two
// would silently do to a per-mutant leaderboard.
//
// A survivor's Proven flag is looked up from r.Verdict.ProvenMutantIDs — the
// same ids that back Verdict.ProvenMissed — never derived by any other
// means, so a scan_mutants row and the verdict it came from can never
// disagree on which survivors were proven.
func buildScanMutantRows(scanID int64, results []reposcan.FileResult) []scanstore.Mutant {
	var rows []scanstore.Mutant
	for _, r := range dedupeResultsByPath(results) {
		if !r.Gradable {
			continue
		}
		proven := make(map[string]bool, len(r.Verdict.ProvenMutantIDs))
		for _, id := range r.Verdict.ProvenMutantIDs {
			proven[id] = true
		}
		// TestsRun/Rule ride at the grain the grading happened: when the run
		// graded each mutant with the tests that reach its own lines, the
		// file's kill rate averages over mutants that faced DIFFERENT test
		// sets, and only these two columns can say which set each one faced.
		// Both are zero on a run graded by one shared command per file.
		for _, m := range r.Verdict.DevKilledMutants {
			rows = append(rows, scanstore.Mutant{
				ScanID: scanID, Path: r.Job.Path, MutantID: m.ID,
				Outcome: "killed", ParentSHA256: m.ParentSHA256,
				TestsRun: m.TestsRun, SelectionRule: m.Rule,
				// How long THIS mutant's own grading run took. NULL, never
				// 0, on a run that did not time its mutants — the whole
				// point of the column is to let a query name the mutants
				// that ate the dev pass, and a zero would name all of them.
				DurationMillis: millisOrNil(m.Duration),
				// WHICH TEST CAUGHT IT — on the killed rows only. A survivor
				// row has no killer by construction, so the column is not
				// even written there: an empty string beside a survivor
				// would read as "we looked and could not tell" instead of
				// "nothing caught it". Empty here too whenever the runner's
				// output did not say, and stored as NULL rather than "".
				KilledBy: m.KilledBy,
			})
		}
		for _, m := range r.Verdict.DevSurvivedMutants {
			rows = append(rows, scanstore.Mutant{
				ScanID: scanID, Path: r.Job.Path, MutantID: m.ID,
				Outcome: "survived", ParentSHA256: m.ParentSHA256,
				Proven: proven[m.ID],
				// A survivor the AUTHORED test killed is, by construction, one
				// the dev suite did not: it is in DevSurvivedMutants. So for
				// this grain proven and proven-by-authored-alone coincide, and
				// the column is written from the same lookup rather than from a
				// second derivation that could disagree with it. It is a
				// separate column because the mutant table admits killed rows
				// too, where the two are NOT the same claim.
				ProvenByAuthoredAlone: proven[m.ID],
				TestsRun:              m.TestsRun, SelectionRule: m.Rule,
				DurationMillis: millisOrNil(m.Duration),
			})
		}
	}
	return rows
}

// buildScanModelCallRows maps every file's Verdict.ModelCalls into the
// ledger's per-(file, role) money grain. Unlike buildScanMutantRows, this is
// NOT restricted to Gradable results: a file whose baseline failed, or whose
// test-writer exhausted its retries, can still have dispatched a
// mutant-generator or test-writer seat before the run gave up on it, and that
// spend is real whether or not the file ended up graded.
//
// A CACHE HIT IS EXCLUDED, and that is the one exclusion here. A reused
// verdict's ModelCalls round-trip through verdict_json and ledgerCache.Get
// restores them verbatim, so the slice is fully populated with the tokens the
// run that EARNED the verdict already recorded under its own scan id. Writing
// them again would bill the same calls twice, and a warehouse summing
// scan_model_calls across scans would read a repo audited nightly from cache
// as costing full price every night.
func buildScanModelCallRows(results []reposcan.FileResult) []scanstore.ModelCall {
	var rows []scanstore.ModelCall
	for _, r := range dedupeResultsByPath(results) {
		if r.CacheHit {
			continue
		}
		for _, c := range r.Verdict.ModelCalls {
			rows = append(rows, scanstore.ModelCall{
				Path: r.Job.Path, Role: c.Role, Model: c.Model,
				Calls: c.Calls, Retries: c.Retries,
				InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
				WallMillis: c.Wall.Milliseconds(),
			})
		}
	}
	return rows
}

// scanModelCallTotals sums buildScanModelCallRows' per-(file, role) grain
// into ONE []advpool.ModelCall per role for the WHOLE scan, in roster order —
// the shape costLine takes, for the end-of-scan stdout line. A role's model
// is assumed constant across the scan (the roster is resolved once, before
// the fan-out); the first non-empty Model seen for a role is kept.
//
// Excludes a cache hit for the same reason buildScanModelCallRows does — see
// its doc. The scan header's token/call totals and the end-of-scan `cost:`
// line are both built from here, so a scan that reused every verdict prints
// no cost line at all, which is the truth: it bought nothing.
func scanModelCallTotals(results []reposcan.FileResult) []advpool.ModelCall {
	totals := make(map[string]*advpool.ModelCall, len(rosterRoleOrder))
	for _, r := range dedupeResultsByPath(results) {
		if r.CacheHit {
			continue
		}
		for _, c := range r.Verdict.ModelCalls {
			t, ok := totals[c.Role]
			if !ok {
				t = &advpool.ModelCall{Role: c.Role, Model: c.Model}
				totals[c.Role] = t
			}
			t.Calls += c.Calls
			// Retries is nullable: sum only what was actually measured, and
			// leave the total nil (not 0) when nothing contributing to it
			// ever measured a retry — the same NULL-not-zero rule the
			// column follows end to end. Today this branch never runs
			// (nothing produces a non-nil Retries yet), but the summation
			// must not silently coerce a future measured value to 0 via a
			// nil pointer arithmetic panic or a wrong default.
			if c.Retries != nil {
				if t.Retries == nil {
					zero := 0
					t.Retries = &zero
				}
				*t.Retries += *c.Retries
			}
			t.InputTokens += c.InputTokens
			t.OutputTokens += c.OutputTokens
			t.Wall += c.Wall
		}
	}
	out := make([]advpool.ModelCall, 0, len(totals))
	for _, role := range rosterRoleOrder {
		if t, ok := totals[role]; ok {
			out = append(out, *t)
		}
	}
	return out
}

// recordCertifyRepoScan writes scan and files to st in one transaction.
// st is opened, and closed, by the caller AROUND THIS CALL — not held across
// the scan. DuckDB is single-writer per file, and a handle held for the whole
// run locked the operator's ledger out of `corral scans` for the duration of
// every audit (see runCertifyRepo's DSN resolution, and ledgerCache, which
// opens per lookup for the same reason). Every error case (a failed write) is
// returned unchanged to the caller, which is responsible for the fail-open
// handling — this function does not print anything itself, so it stays
// testable as a pure function of its inputs.
//
// scan_mutants is written separately, AFTER scan+files have already
// committed and the scan id is known. A RecordMutants failure is logged and
// swallowed here, not returned: the verdict is already computed and
// recorded in scan_files above, so losing mutant detail costs analysis, not
// correctness, and must not turn into "scan ledger NOT written" for a scan
// that, in every way that matters, was.
// It returns the ledger's scan id so the caller can thread it into the
// audit statement and warehouse push that follow — the link this function's
// own package comment set out to make traceable.
func recordCertifyRepoScan(st *scanstore.Store, scan scanstore.Scan, files []scanstore.File, mutants []scanstore.Mutant, calls []scanstore.ModelCall, events []scanstore.Event, stderr io.Writer) (int64, error) {
	id, err := st.Record(context.Background(), scan, files)
	if err != nil {
		return 0, err
	}
	// The mutant, model-call and event rows are built by the CALLER and
	// stamped with the id here. They are the same slices the warehouse bundle
	// is built from, which is the point: rebuilding them from the report a
	// second time is how the ledger and the warehouse start disagreeing about
	// what the scan found.
	stampScanID(id, mutants, calls, events)
	if err := st.RecordMutants(context.Background(), mutants); err != nil {
		// The caller's stderr, not the global logger, for the same reason as
		// in buildScanFileRows: fail-open still has to be LOUD where the
		// operator is actually looking.
		fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but scan_mutants was NOT written: %v\n", id, err)
	}
	if err := st.RecordModelCalls(context.Background(), calls); err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but scan_model_calls was NOT written: %v\n", id, err)
	}
	if err := st.RecordEvents(context.Background(), events); err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but scan_events was NOT written: %v\n", id, err)
	}
	return id, nil
}

// stampScanID writes the ledger's assigned id onto rows the caller built
// before that id existed. One place, so a grain cannot be forgotten: a row
// with scan_id 0 is a row that joins to nothing.
func stampScanID(id int64, mutants []scanstore.Mutant, calls []scanstore.ModelCall, events []scanstore.Event) {
	for i := range mutants {
		mutants[i].ScanID = id
	}
	for i := range calls {
		calls[i].ScanID = id
	}
	for i := range events {
		events[i].ScanID = id
	}
}

// auditedParentSHA256 is the hash a graded file's own mutants carry. Every
// mutant of one file must report the same parent: they were all derived from
// one set of bytes, and a disagreement means either two different sources
// reached the generator or something rewrote the file mid-audit. Either way
// there is no single answer to "which bytes does this verdict describe", and
// the honest record is none — announced, not swallowed, because a validity
// key going silently missing is how a stale verdict later reads as live.
func auditedParentSHA256(path string, v advpool.Verdict, stderr io.Writer) string {
	sha := ""
	for _, group := range [][]advpool.MutantRef{v.DevKilledMutants, v.DevSurvivedMutants} {
		for _, m := range group {
			if m.ParentSHA256 == "" {
				continue
			}
			if sha == "" {
				sha = m.ParentSHA256
				continue
			}
			if m.ParentSHA256 != sha {
				fmt.Fprintf(stderr, "corral certify --repo: %s: mutants disagree about the parent they came from (%s vs %s) — recording no parent_sha256 for this file\n",
					path, sha, m.ParentSHA256)
				return ""
			}
		}
	}
	return sha
}

// auditedFileSHA256 hashes the file at repoDir/path — the audited bytes —
// through the same fileSHA256 the recorded-mutant-set path uses, so a hash in
// this ledger and one in a recorded set are the same function of the same
// bytes. "" when the file cannot be read: a missing hash is honest, an
// invented one is a validity claim that would tell a later reader a stale
// verdict is live.
func auditedFileSHA256(repoDir, path string) string {
	sum, err := fileSHA256(filepath.Join(repoDir, path))
	if err != nil {
		return ""
	}
	return sum
}

// spreadMin/Median/Max lift advpool's per-mutant spread into the three
// nullable ledger columns. A nil spread means no mutant was graded per
// mutant, and all three stay nil — never {0,0,0}, which would read as "every
// mutant ran no tests".
// Each returns a COPY. Handing back &s.Min would leave the recorded row
// pointing into the verdict it was derived from, so a later mutation of the
// verdict would silently rewrite a number already treated as recorded — and
// a record that can change after the fact is not a record.
func spreadMin(s *advpool.TestsPerMutantSpread) *int {
	if s == nil {
		return nil
	}
	v := s.Min
	return &v
}

func spreadMedian(s *advpool.TestsPerMutantSpread) *int {
	if s == nil {
		return nil
	}
	v := s.Median
	return &v
}

func spreadMax(s *advpool.TestsPerMutantSpread) *int {
	if s == nil {
		return nil
	}
	v := s.Max
	return &v
}

// challengerJaccard/Kappa/Sufficient lift a *modelcorr.Pair onto the
// ledger's three nullable columns. All three stay nil on a nil pair — "the
// challenger did not run" — never a fabricated 0.0/false, which would read
// as "the challenger ran and disagreed completely".
//
// Kappa additionally stays nil when the pair itself says it is undefined
// (p_e == 1, both seats degenerate over the same outcome — see
// modelcorr.Pair.KappaDefined's doc): a caller storing Kappa unconditionally
// would write a fabricated 0 for exactly the case modelcorr invented the
// flag to keep distinct from a real zero.
//
// And JACCARD stays nil unless the pair says Sufficient. modelcorr.Compare
// ZEROES Jaccard when the survivor union is below MinSurvivorUnion, and
// Pair.Sufficient's own doc is explicit that callers MUST check it first.
// Storing that zero would file "the union was too small for the coefficient
// to mean anything" as "the two writers missed nothing in common" — the
// strongest possible claim in the opposite direction, in a column a
// cross-repo query averages. Sufficient itself is still recorded, so "we
// compared, and the sample was too small" stays legible.
func challengerJaccard(p *modelcorr.Pair) *float64 {
	if p == nil || !p.Sufficient {
		return nil
	}
	v := p.Jaccard
	return &v
}

func challengerKappa(p *modelcorr.Pair) *float64 {
	if p == nil || !p.KappaDefined {
		return nil
	}
	v := p.Kappa
	return &v
}

func challengerSufficient(p *modelcorr.Pair) *bool {
	if p == nil {
		return nil
	}
	v := p.Sufficient
	return &v
}

// goalsDerivedFor answers scanstore.File.GoalsDerived for one file: 1 when
// this file's goal actually came from reposcan's DERIVER
// (reposcan.GoalWasDerived), 0 otherwise — a hand-written --goals entry, or
// no goal at all. 0 is the field's own documented default (see its doc in
// internal/scanstore/store.go): a plain int, not a pointer, so there is no
// NULL to preserve here the way there is for the Challenger columns above.
func goalsDerivedFor(g reposcan.Goal) int {
	if reposcan.GoalWasDerived(g) {
		return 1
	}
	return 0
}

// unmeasuredOnReuse returns ms for a file this scan actually audited, and nil
// — SQL NULL — for one whose verdict was REUSED from the ledger cache.
//
// It exists because a cached verdict is indistinguishable from a fresh one by
// its contents: Timing and the mutant-duration summaries ride through
// verdict_json and ledgerCache.Get restores them exactly. So the only thing
// that can tell a reader "this scan did not spend these minutes" is the
// FileResult's own CacheHit flag, and every timing column on the row goes
// through here rather than each one remembering the rule.
//
// NULL is the same value a phase that never ran gets, and that is correct at
// this grain: the question the column answers is "how long did THIS scan
// spend on this file", and the answer for a reused verdict is "nothing worth
// a clock". The run that earned the verdict still holds the real numbers on
// its own row, reachable through reused_from_scan_id.
// baselineMillisUnlessReused is unmeasuredOnReuse for the one timing column
// that predates the timing work and is a plain int64 (0 = NULL downstream).
func baselineMillisUnlessReused(r reposcan.FileResult) int64 {
	if r.CacheHit {
		return 0
	}
	return r.Verdict.BaselineDuration.Milliseconds()
}

func unmeasuredOnReuse(r reposcan.FileResult, ms *int64) *int64 {
	if r.CacheHit {
		return nil
	}
	return ms
}
