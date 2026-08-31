// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// defaultScanTop bounds a scan by default. Provisional: large enough to be
// useful, small enough to quote a price. Revisit against a real third-party
// repo scan before relying on it.
const defaultScanTop = 25

// There is no default goal-deriver model, for the same reason there are no
// default role models: see the block at the top of certify_local.go. Deriving
// one sentence from a file is not the hard part of this pipeline, but picking
// a vendor on the operator's behalf is not ours to do either. --goals skips
// derivation entirely and needs no model at all.

// runCertifyRepo fans the single-file audit out over a whole repository.
// Goals come from a checked-in JSON file when --goals is given, and are
// otherwise DERIVED per file by a model behind the same GoalSource interface.
// No signing here — H1c turns the report into a sealed, anchored statement.
func runCertifyRepo(args []string, stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify --repo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "path of the repository to audit (required)")
	goalsPath := fs.String("goals", "", "JSON file mapping repo-relative paths to goals (default: derive a goal per file)")
	testsPath := fs.String("tests", "", "JSON file mapping repo-relative SOURCE paths to their test files, consulted before filename convention. Convention cannot pair a project that names tests after behaviour rather than after source files (expressjs/express: lib/response.js is tested by test/res.send.js, res.json.js …), and it can pair the WRONG file (psf/requests pairs adapters.py to an 8-line test_adapters.py while its real coverage is in a 108KB test_requests.py). A mapping to a file that does not exist is refused, never silently fallen back to convention")
	topFlag := fs.Int("top", defaultScanTop, "audit only the N highest-ranked candidates (0 or --all = every candidate). Bounded by default: a whole-repo audit runs a full herd per file, so an unbounded first scan on a large repo costs hours and real money. The DEFAULT bound does not apply with --goals — a hand-written goals map has already chosen the surface — but an explicit --top does")
	allFlag := fs.Bool("all", false, "audit every candidate, ignoring --top")
	deriveModel := fs.String("derive-model", "", "model that derives a goal per file when --goals is not given — REQUIRED unless --goals is supplied; corral has no default models")
	// Per-role models. `certify --local` has had these all along; without them
	// here a repo scan was locked to the Claude defaults with no override.
	writerModelFlag := fs.String("writer-model", "", "model for the test-writer role — REQUIRED, corral has no default models")
	mutantModelFlag := fs.String("mutant-model", "", "model for the mutant-generator role — REQUIRED, corral has no default models")
	criticModelFlag := fs.String("critic-model", "", "model for the test-critic role, which must differ from the writer's; \"off\" disables the critic entirely (it is advisory and never gates the verdict, so a single-vendor run with only one usable model can drop it). No default")
	scopeTestsFlag := fs.Bool("scope-tests", false, "REMOVED — see --whole-suite. Selection by coverage evidence is now the default")
	wholeSuiteFlag := fs.Bool("whole-suite", false, "grade every mutant against the project's WHOLE suite instead of the tests that demonstrably execute each file (the default, from one instrumented run per scan). Costs O(mutants x whole-suite runtime) per file and answers a different question — 'did ANY test catch it' rather than 'do this file's tests test it'. The verdict records which was used")
	shadowModelFlag := fs.String("shadow-model", "", "challenger model that attacks every region a SECOND time. OFF unless named. Recorded for comparison — NEVER gates the verdict")
	owner := fs.String("owner", "local", "owning account for the scan (tenant identifier)")
	commit := fs.String("commit", "", "commit SHA the report is bound to")
	swarmFlag := fs.Int("swarm", 0, "max concurrent audit workers (0 = auto-size to this host's cores); on --substrate workspace it also sizes the private trees that score one file's mutants at once (budget/4, min 1), so --swarm 4 is one tree")
	dryRun := fs.Bool("dry-run", false, "enumerate and emit jobs, then stop — no audits run")
	jsonOut := fs.Bool("json", false, "with --dry-run, emit the repository's audit surface as JSON instead of the human report: per-language counts, every auditable file with its inferred test pairing, and the machine-stable exclusion tally. Needs no key, no jail and no money — it is the free inventory a UI or a tenant's own tooling can consume instead of scraping stdout")
	substrateFlag := fs.String("substrate", substrateJail, "where the audit runs: "+substrateJail+" (bwrap) or "+substrateWorkspace+" (mutate --repo in place; the caller IS the isolation boundary, e.g. an ephemeral CI runner)")
	diffBase := fs.String("diff-base", "", "bound the scan to files changed since this git ref, instead of ranking + --top. In a PR the diff IS the bound: ranking and --top do not apply on this path")
	pushFlag := fs.String("push", "", "append this scan's per-file verdicts to a DuckDB you own — a path, or `md:<db>` for MotherDuck (which reads motherduck_token from the environment; the database is created on first push if it does not already exist — a MotherDuck SHARE is a read target and cannot be pushed to). corral has no hosted tier and keeps nothing: the warehouse is yours, and any DuckDB works, so this is a destination rather than a lock-in. Append-only. Every row carries the ledger's scan id (0 when --record was not given), and — traceable only with --attest — the sha256 of the signed statement it came from, so a row can be checked against something a third party can verify; without --attest, statement_sha256 is honestly empty rather than fabricated; and with --attest, a statement that FAILS to write withholds the push too, since a row that cannot name the statement it came from is not written. It answers what one pull request cannot — a single kill rate is a sample, and the same unchanged diff has scored 0.85 and 0.90; forty of them are a distribution")
	pushSourceFlag := fs.Bool("push-source", false, "with --push, also send the SOURCE BYTES corral holds to your warehouse: the pool's authored test, and the full verdict JSON. Off by default because those bytes are derived from — and quote — your audited code; without this the pushed rows carry numbers, hashes, reasons and model names, and no source leaves the box. Mutant code is NOT carried, by either setting: corral does not keep mutant source at rest, so the corral_mutants.code column exists and is always NULL until something records it. The scan row records which setting was used, so the custody question is answerable from the table rather than from whoever remembers the argv")
	attestFlag := fs.String("attest", "", "write the scan's verdict as an in-toto Statement to this file — the receipt a reviewer can verify without trusting the run that produced it. Consumed by GitHub's attestation API (actions/attest), which signs it keylessly through the workflow's own OIDC identity, so the signature chains to the repository and workflow rather than to a key that lived on an ephemeral runner. Carries every file's kill rate, survivors and proven gaps WITH the honesty flags that say what a zero means, the thresholds it was judged against, and the models in each role")
	transparencyFlag := fs.Bool("transparency", false, "also upload the --attest statement to Sigstore's public Rekor transparency log (requires --attest — there is nothing to log without one). THE ENTRY IS PUBLIC AND PERMANENT: once logged it cannot be removed or edited, by anyone, including you. It carries the same statement --attest writes — the repo URL, the audited commit, per-file paths, kill rates and survivor/proven-gap counts, and the models in each role — and never the audited source itself. Fails OPEN: an unreachable log or a failed upload prints one line and leaves the scan's own verdict and exit code untouched; the local statement and ledger are unaffected either way. Prints the log index and entry UUID on success, and records both in the scan ledger and, with --push, the warehouse")
	maxProvenMissedFlag := fs.String("max-proven-missed", "", "fail the scan (exit 1) if ANY audited file has MORE than this many proven-missed gaps — survivors the pool then killed with a test it WROTE and RAN. Opt-in and unset by default. Prefer this to --min-kill-rate as a merge gate: a kill rate is a proportion of freshly generated mutants and moves between runs on unchanged code, so a threshold set near a healthy value flaps red and gets switched off. A proven-missed gap is a specific demonstrated bug the suite does not catch, established by execution, and 0 means the pool proved nothing — not that it sampled well")
	minKillRateFlag := fs.String("min-kill-rate", "", "fail the scan (exit 1) if ANY audited file's kill rate is below this value (0.0-1.0 inclusive; a minimum, so a file exactly at the threshold passes). Opt-in: unset by default, so exit codes are unchanged unless this is given. Applies PER FILE, not to the aggregate — a well-tested file must not mask a weak one")
	preflightFlag := fs.Bool("preflight", false, "run the project's test suite once with coverage instrumentation and report which source files it never executes. One extra suite run; reports coverage-grade evidence, not proof")
	recordFlag := fs.Bool("record", false, "record every file this scan audited or rejected, and why, into the DuckDB scan ledger (default: off). A BOOL here — unlike `certify --local`'s --record, which takes a tape PATH — see --record-db for where the ledger goes. A recording failure never changes the scan's verdict or exit code")
	mutantsFlag := fs.String("mutants", "", "REPLAY a recorded mutant set (see --record-mutants) instead of generating one: every audited file is graded against exactly the mutants in this file, and not one generator model call is made. Mutants are authored by a model, so an ordinary run re-draws the exam every time and two runs of the same audit are not two samples of one measurement — pin the set and a change to anything ELSE becomes measurable. Every selected file must appear in the set with the SAME bytes it was recorded from; a missing file or a changed one is refused (exit 2) up front, never half-replayed. Reads a corral-mutants-2 document, or an older corral-mutants-1 one, whose whole-file mutants still replay byte-for-byte.")
	recordMutantsFlag := fs.String("record-mutants", "", "write the mutants this scan actually GRADED to this file, as a replayable corral-mutants-2 document — one entry per audited file, each mutant its SEARCH/REPLACE hunk, tied to the sha256 of the source it was derived from. Written even when the scan's gates fail: a red verdict is still a recorded exam. A v2 document re-recorded from a --mutants replay of an older corral-mutants-1 set contains that set's WHOLE-FILE entries, not hunks — the run graded what was recorded, and re-recording it does not manufacture anchors it never had")
	writerModeFlag := fs.String("writer-mode", "", "how the test-writer attacks a file's survivors: `per-survivor` (the default) makes ONE call per survivor — each carrying the file once as a cacheable shared prefix plus that survivor's diff, each repaired on its own budget and each PROVEN ALONE against its own mutant — or `batched`, the original shape: one call carrying every survivor, one repair budget for the file, one proof pass over all of them. Nothing measured changes between them (a survivor is proven iff an authored test kills it alone and passes on the original, either way); what changes is that one unbuildable test no longer spends the whole file's retries and takes every other survivor down with it. The verdict, the report line, the ledger and the attestation all record which mode earned the numbers. Each survivor's proof in per-survivor mode runs its OWN compliant baseline (a compliant pass plus a canary, per seat), so a file with N survivors pays N baselines where batched paid one: on a repo whose suite takes a minute, prefer --writer-mode batched or expect N baselines' worth of wall clock.")
	shadowWriterModelFlag := fs.String("shadow-writer-model", "", "CHALLENGER test-writer: a second writer attacks the SAME survivors as the primary, so the two seats' misses can be compared (Jaccard over survivors, Cohen's kappa). Measurement only — it NEVER gates the verdict. OFF unless named. Recording the per-mutant outcomes additionally needs --mutant-attempts-db")

	var localEndpointFlag stringSlice
	fs.Var(&localEndpointFlag, "local-endpoint", "place a LOCAL seat on a specific ollama daemon, as <role>=<url> (repeatable; e.g. mutant-generator=http://localhost:11436). A daemon is pinned to a GPU by its own environment, so this is how two models occupy two cards at once — corral selects the DAEMON, never the device. Without it every local seat shares OLLAMA_URL, one card and one VRAM budget")

	recordDSNFlag := fs.String("record-db", "", "path to the scan ledger (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb)")
	noGoalCacheFlag := fs.Bool("no-goal-cache", false, "skip the goal cache — every candidate is re-derived even when a PRIOR scan already derived a goal for the exact same bytes, model and prompt revision. Re-buys a model call per file that a content-addressed cache would otherwise have served for free; use this to isolate goal-derivation variance from a comparison, or on a scan whose operator does not want a goal receipt kept in the ledger at all. The cache lives in the same ledger --record-db names, independent of --record itself")
	noSelectionCacheFlag := fs.Bool("no-selection-cache", false, "skip the selection cache — the ONE instrumented coverage run always executes, even when a PRIOR scan already ran the identical instrumented command over a byte-identical tree. Re-buys a full suite run (the single most expensive measurement a scan makes outside model calls) that a content-addressed cache would otherwise have served for free; use this to isolate selection variance from a comparison, or when the operator does not trust the tree to be unchanged. The cache lives in the same ledger --record-db names, and (like the goal cache) is consulted independent of --record itself; only WRITING a fresh hit requires --record, since a scan_id has to exist to write one against")
	timeoutFlag := fs.Duration("timeout", 10*time.Minute, "per-file budget: give up on a single file's run if it makes no progress for this long (not a hard wall-clock cap — a single slow LLM call can overshoot it). Same default and semantics as `certify --local`'s --timeout; raise it for a large file that needs more room to converge")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	// Removed with a POINTER, not deprecated into a warning: --scope-tests
	// picked a file's grading surface by FILENAME convention, which inverted
	// real verdicts. An operator who typed it wanted the cost collapse, and
	// they now get it by default — but from execution evidence, and the
	// replacement flag goes the OTHER way, so silently honouring the old
	// spelling would mean the opposite of what it used to.
	// Validated here, before anything is spent: the mode changes how many
	// model calls a run makes and what its verdict discloses, so a typo must
	// exit 2 rather than quietly take the default and hand back a different
	// measurement than the one that was asked for.
	writerMode, wmErr := advpool.ResolveWriterMode(*writerModeFlag)
	if wmErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", "corral certify --repo", wmErr)
		return 2
	}
	if *scopeTestsFlag {
		fmt.Fprintln(stderr, "corral certify --repo: --scope-tests was removed. Its paired-FILE scoping inverted verdicts (requests/adapters.py 1.00 -> 0.00). Selection is now by coverage evidence and on by default; pass --whole-suite to grade against the whole suite. See docs/design/test-selection.md")
		return 2
	}

	// --transparency logs an attestation; there is none without --attest —
	// caught here, before anything is spent, rather than discovered at the
	// end of a full run when writeAuditStatement never ran.
	if *transparencyFlag && strings.TrimSpace(*attestFlag) == "" {
		fmt.Fprintln(stderr, "corral certify --repo: --transparency logs an attestation; there is none — pass --attest too")
		return 2
	}

	// splitCertifyArgs (shared with `certify --local`) knows nothing about
	// this command's own flag set: it splits purely on the first literal
	// "--", so any of THIS command's flags placed after it silently become
	// arguments to the check command instead of being parsed at all. For
	// most flags that is merely confusing; for --min-kill-rate it is a
	// silent-no-gate: `-- pytest -q --min-kill-rate 0.5` hands
	// "--min-kill-rate" and "0.5" to pytest as plain arguments, the
	// threshold is never applied, and CI goes green on a repo the
	// threshold would have failed — with NO error and NO warning. A
	// warning is not enough here (it scrolls past in CI, and the failure
	// mode is a gate that silently never ran), so this is a hard usage
	// error naming the offending token, checked before anything else that
	// could exit 0 or 1. Names are read from fs itself (fs.VisitAll),
	// never hardcoded, so a flag added later is covered automatically
	// instead of silently reproducing this exact bug class.
	if err := checkArgvNoFlagCollision(fs, checkArgv); err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
		return 2
	}

	// A stray positional argument almost always means a flag's VALUE spilled
	// out of flag parsing and stopped it early — the exact trap this flag
	// set sets for an operator who already knows `certify --local`: THERE,
	// --record takes a STRING (a replayable tape path, `certify --local
	// --record <file>.json`, documented in README.md); HERE it is a BOOL
	// (see --record-db for the ledger path, above). An operator who types
	// what they already know — `--record tape.json --min-kill-rate abc` —
	// gets "tape.json" as an unconsumed positional, flag.Parse stops right
	// there, and everything after it (here, --min-kill-rate) is silently
	// NEVER PARSED: the scan runs with no gate and no ledger override, no
	// error, no warning — the identical silent-no-gate
	// checkArgvNoFlagCollision above exists to close, walking in through a
	// different door. fs.NArg() catches it regardless of which flag or
	// typo produced the stray token, not just this specific one.
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "corral certify --repo: unexpected argument(s) %v — if this looks like a flag's value, note that --record here is a BOOL (see --record-db for the ledger path); flag.Parse stops at the first unrecognized positional and everything after it is silently never parsed\n", fs.Args())
		return 2
	}

	if *repoDir == "" {
		fmt.Fprintln(stderr, "corral certify --repo: --repo is required")
		return 2
	}

	// An unrecognized substrate must be a usage error, not a silent
	// fall-through to the jail default: a run that quietly used the wrong
	// substrate while claiming the other is the exact accountability
	// failure diff scoping and the cache key exist to close.
	if *substrateFlag != substrateJail && *substrateFlag != substrateWorkspace {
		fmt.Fprintf(stderr, "corral certify --repo: --substrate %q is not %s or %s\n", *substrateFlag, substrateJail, substrateWorkspace)
		return 2
	}

	// Validated here, before enumeration even runs — an out-of-range or
	// unparseable --min-kill-rate is a usage error (exit 2), matching
	// --substrate above, not a value that silently limps through to a
	// threshold check that can never be satisfied (or is always satisfied).
	// nil means "unset": the flag is opt-in, and a default threshold here
	// would break every existing caller of this shipped command.
	var minKillRate *float64
	if *minKillRateFlag != "" {
		v, perr := parseMinKillRate(*minKillRateFlag)
		if perr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", perr)
			return 2
		}
		minKillRate = &v
	}

	// Same opt-in shape as --min-kill-rate: nil means unset, and an
	// unparseable value is a usage error rather than a gate that silently
	// never fires.
	var maxProvenMissed *int
	if *maxProvenMissedFlag != "" {
		v, perr := strconv.Atoi(strings.TrimSpace(*maxProvenMissedFlag))
		if perr != nil || v < 0 {
			fmt.Fprintf(stderr, "corral certify --repo: --max-proven-missed must be a non-negative whole number, got %q\n", *maxProvenMissedFlag)
			return 2
		}
		maxProvenMissed = &v
	}

	// Captured here, before enumeration: this is the header row's
	// provenance timestamp for the whole invocation, not just the audit
	// portion of it. Read only if --record is given (see the fail-open call
	// site near the bottom of this function).
	startedAt := time.Now()

	var testMap *reposcan.TestMap
	if *testsPath != "" {
		tm, terr := reposcan.NewFileTestMap(*testsPath)
		if terr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: reading --tests %s: %v\n", *testsPath, terr)
			return 2
		}
		testMap = tm
	}
	cands, excl, err := reposcan.EnumerateWithTests(*repoDir, testMap)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: enumerating %s: %v\n", *repoDir, err)
		return 1
	}

	// --json emits the inventory INSTEAD of the human report, matching the
	// convention `corral scorecard`/`matrix` already set: a consumer must not
	// have to strip prose off the front of a document. Placed BEFORE the first
	// human line so not one byte of prose precedes the JSON; the report code
	// stays a single path writing to a discarded sink, so the two renderings
	// cannot drift apart.
	jsonSink := stdout
	if *jsonOut {
		if !*dryRun {
			// Loud, not silently inert: the audit path produces a verdict, not
			// an inventory, so there is nothing here to serialise. An operator
			// who passed --json and got a normal report would reasonably
			// conclude the flag had worked.
			fmt.Fprintln(stderr, "corral certify --repo: --json requires --dry-run (it serialises the enumeration, not an audit verdict)")
			return 2
		}
		stdout = io.Discard
	}

	fmt.Fprintf(stdout, "corral certify --repo %s\n", *repoDir)

	// Captured BEFORE any candidate-level exclusion is appended below. Only
	// Enumerate's exclusions are non-candidates; every later reason
	// (not-selected, ungoaled, derive-failed, source-too-large) names a file
	// already counted in
	// len(cands), and adding those to the file total would report more files
	// than exist on disk.
	enumExcl := len(excl)

	// effectiveTop is the bound this scan actually APPLIED, for the ledger
	// header (see the --record call site) — deliberately NOT *topFlag. 0 is
	// already --top's own sentinel for "unbounded" ("0 or --all = every
	// candidate", per its help text above), so recording effectiveTop needs
	// no new NULL/pointer plumbing: it just has to be the number that was
	// actually checked, not the number the operator happened to type.
	// Left at its zero value (0, unbounded) on the --diff-base branch below,
	// where --top/--all are never consulted at all — the diff IS the bound
	// there — and set from `limit` in the else branch. Recording *topFlag
	// unconditionally was the bug: with --goals and no explicit --top,
	// `limit` is forced to 0 a few lines down (an unbounded goals-map scan)
	// while *topFlag stays its default 25 — measured on a real scan:
	// "auditing 198 of 198 candidate(s)" recorded alongside `top=25`, a
	// provenance row positively asserting a bound the scan never applied,
	// indistinguishable from a genuine top-25 scan to any later reader.
	var effectiveTop int
	var selected []reposcan.Candidate
	// Changed source files with no pairable test — populated only on the
	// diff-bound path, where a zero-candidate result is otherwise ambiguous.
	var unpairableInDiff []string
	// rankSignal names how candidates were ordered, captured from the same
	// value the human report prints so the JSON inventory can never disagree
	// with it. The diff-bound path never ranks at all, so it stays empty
	// rather than claiming an ordering that was not applied.
	rankSignal := ""
	if *diffBase != "" {
		// In a PR the diff IS the bound: ranking and --top exist to bound what
		// DERIVATION costs over a whole repo, and that question does not apply
		// when the operator has already told us exactly which files changed.
		// A changed file with no paired test is still a candidate the
		// enumerator never produced (Enumerate already excluded it as
		// no-paired-test) — this loop only decides which CANDIDATES are IN
		// bound; anything outside it is accounted, never silently dropped.
		changed, cerr := changedFiles(*repoDir, *diffBase)
		if cerr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", cerr)
			return 1
		}
		changedSet := make(map[string]bool, len(changed))
		for _, p := range changed {
			changedSet[p] = true
		}
		var kept []reposcan.Candidate
		for _, c := range cands {
			// A changed TEST puts its source in scope, not just a changed
			// source. Scoping on c.Path alone made the one change this gate
			// exists to catch invisible to it: a pull request that deletes
			// assertions touches no source file, so the audit reported
			// "NOTHING IN SCOPE" and passed green. Weakening a suite is the
			// pure form of "tests that pass and defend nothing" — the gate
			// cannot be blind to precisely that.
			//
			// The pairing is already resolved (c.TestPath), from the --tests
			// map or from the language plugin's convention, so this costs a
			// map lookup and no new configuration.
			if changedSet[c.Path] || (c.TestPath != "" && changedSet[c.TestPath]) {
				kept = append(kept, c)
				continue
			}
			excl = append(excl, reposcan.Exclusion{Path: c.Path, Reason: reposcan.ReasonNotSelected})
		}
		selected = kept
		// Which CHANGED files corral could not pair with a test. Enumerate
		// already excluded them as no-paired-test, before the diff bound was
		// applied, so they never reach `cands` and a zero-candidate diff cannot
		// otherwise tell "nothing changed that we audit" from "the thing that
		// changed is the thing we cannot read". The merge gate reports those
		// two differently — see printRepoReport.
		for _, e := range excl {
			if e.Reason == reposcan.ReasonNoPairedTest && changedSet[e.Path] {
				unpairableInDiff = append(unpairableInDiff, e.Path)
			}
		}
		sort.Strings(unpairableInDiff)
		fmt.Fprintf(stdout, "  diff against %s: auditing %d of %d candidate(s)\n", *diffBase, len(selected), len(cands))
	} else {
		// Selection precedes derivation, deliberately: bounding afterwards would
		// pay for a goal on every candidate in order to audit 25 of them.
		ranked, rankInfo := reposcan.Rank(*repoDir, cands)

		limit := *topFlag
		if *allFlag {
			limit = 0
		}
		// --top exists to bound what DERIVATION costs. An operator who hand-wrote
		// a goals file has already chosen the surface by hand and paid nothing per
		// file, so the default bound must not apply to it: the bound is taken over
		// ALL candidates, most of which have no hand-written goal, so a default 25
		// would quietly audit a handful of a 40-file goals map. An EXPLICIT --top
		// is still honoured on that path.
		if *goalsPath != "" && !flagWasSet(fs, "top") && !*allFlag {
			limit = 0
		}
		effectiveTop = limit
		var notSelected []reposcan.Exclusion
		selected, notSelected = reposcan.Select(ranked, limit)
		// Appending into excl is safe: notSelected is Select's own freshly
		// allocated slice, and excl is Enumerate's. Nothing is appended to
		// `selected`, which ALIASES ranked's backing array.
		excl = append(excl, notSelected...)

		// The rule is disclosed. A selection nobody can explain is the same
		// problem this project criticises in black-box model routing.
		// Captured, not re-derived: the JSON inventory must name the SAME
		// ordering signal the human report does. A shallow clone silently
		// degrades churn to size alone, so a consumer has to be told which it
		// actually got.
		rankSignal = rankInfo.Signal
		fmt.Fprintf(stdout, "  ranked by %s; auditing %d of %d candidate(s)\n",
			rankInfo.Signal, len(selected), len(cands))
		if rankInfo.Note != "" {
			fmt.Fprintf(stdout, "    %s\n", rankInfo.Note)
		}
	}

	// --mutants: resolved HERE, after selection and before EVERY model-facing
	// step below — the jail preflight, the provider preflight, and goal
	// derivation, which is where the money starts. The point of a replayed set
	// is that it costs no generation, so a refusal that arrives after a scan
	// has already paid for a goal per file would be the one failure mode this
	// flag exists to avoid. Placed before the dry-run return too, so a dry run
	// checks the set as strictly as a real scan does.
	var presetMutants map[string][]adequacy.Mutant
	var mutantsFromSHA string
	if p := strings.TrimSpace(*mutantsFlag); p != "" {
		set, serr := adequacy.ReadMutantSet(p)
		if serr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", serr)
			return 2
		}
		sum, herr := fileSHA256(p)
		if herr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: hashing --mutants %s: %v\n", p, herr)
			return 2
		}
		mutantsFromSHA = sum
		mutRoot, merr := os.OpenRoot(*repoDir)
		if merr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: opening %s: %v\n", *repoDir, merr)
			return 1
		}
		selPaths := make([]string, 0, len(selected))
		for _, c := range selected {
			selPaths = append(selPaths, c.Path)
		}
		sort.Strings(selPaths)
		presetMutants, merr = presetMutantsForSelection(mutRoot, set, selPaths)
		_ = mutRoot.Close()
		if merr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", merr)
			return 2
		}
		fmt.Fprintf(stdout, "  replaying a recorded mutant set for %d file(s) from %s — no mutant-generator model call will be made\n", len(presetMutants), p)
	}

	// --record-mutants: the accumulator every audited file's driver feeds. It
	// is flushed once at the very end of this function, on EVERY exit path
	// past this point (see the deferred write), because a scan whose gate
	// failed is still a scan whose exam is worth keeping — reproducing a red
	// verdict is the first thing anyone does with one.
	// Declared here, not at the Scan call below, so the --record-mutants
	// closure can read the scan's own per-file results: a file served from
	// the verdict cache never runs a dev pass and so never reaches the sink,
	// and the record line has to be able to say so.
	var results []reposcan.FileResult
	var mutantRecorder *mutantSetRecorder
	if p := strings.TrimSpace(*recordMutantsFlag); p != "" {
		mutantRecorder = newMutantSetRecorder()
		defer func() {
			if *dryRun {
				// Inert, and said so rather than left silent (the same rule
				// --record follows just below): a dry run audits nothing, so
				// there are no graded mutants to record, and writing an empty
				// document here would be indistinguishable from a real scan
				// that graded nothing.
				fmt.Fprintln(stderr, "corral certify --repo: --record-mutants ignored — --dry-run grades nothing, so there are no mutants to record")
				return
			}
			n, werr := mutantRecorder.write(p)
			if werr != nil {
				fmt.Fprintf(stderr, "corral certify --repo: --record-mutants NOT written: %v\n", werr)
				return
			}
			audited, cacheHits := 0, 0
			for _, r := range results {
				if !r.Gradable {
					continue
				}
				audited++
				if r.CacheHit {
					cacheHits++
				}
			}
			mutantRecorder.report(stdout, p, n, audited, cacheHits)
		}()
	}

	// EVERY scan-fatal preflight runs BEFORE the first derivation, because
	// derivation is where the money goes: EmitJobs below performs up to --top
	// sequential model calls, and an operator on a host that cannot sandbox
	// used to pay for all of them and then get exit 1 having graded nothing.
	// Both checks are cheap — newLocalExecutor only probes the backend, and its
	// seeds are lazy — and both are scan-wide facts the first job would have
	// known instantly.
	//
	// Skipped on a dry run, which never audits anything: demanding a jail and a
	// provider key to print the jobs a scan WOULD emit would refuse the one
	// invocation that costs nothing.
	var ex *localExecutor
	if !*dryRun {
		// Each job runs the whole tree in a jail and grades it with the
		// project's own test command. Given after `--`; absent, the language
		// plugin's stock recursive command is used — resolved per job, since a
		// repo can mix languages.
		ex = newLocalExecutor(*repoDir, checkArgv, *substrateFlag, *timeoutFlag, stdout)
		ex.wholeSuite = *wholeSuiteFlag
		// The selection cache lives in the same ledger --record-db names,
		// independent of --record for its Get half — see --no-selection-cache's
		// own help text and collectSelection's doc for why writing a hit still
		// needs a scan id that only --record ever assigns.
		if !*noSelectionCacheFlag {
			selCacheDSN := *recordDSNFlag
			if selCacheDSN == "" {
				selCacheDSN = defaultScanDSN()
			}
			ex.selectionCache = newSelectionLedgerCache(selCacheDSN)
		}
		ex.presetMutants = presetMutants
		if mutantRecorder != nil {
			ex.mutantSink = mutantRecorder.sink
		}
		ex.writerMode = writerMode
		ex.models = auditModels{
			writer: *writerModelFlag, mutant: *mutantModelFlag,
			critic: *criticModelFlag, shadow: *shadowModelFlag,
			shadowWriter: *shadowWriterModelFlag,
		}
		// Deferred, not called at the end: a panic mid-scan must still release
		// the staging dirs the shared seeds created. Deferred here so it also
		// covers the early returns below.
		defer ex.Close()
		// Jail preflight: a host that cannot sandbox grades nothing, and saying
		// so now beats reporting every file as ungradable — after paying for a
		// goal for each of them.
		if err := ex.preflight(); err != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
			return 1
		}
		// Provider preflight: role models, decorrelation, and the API key are
		// scan-wide facts too.
		if _, err := resolveAuditRoles(localAuditInput{
			cmdName:           "corral certify --repo",
			writerModel:       *writerModelFlag,
			mutantModel:       *mutantModelFlag,
			criticModel:       *criticModelFlag,
			shadowModel:       *shadowModelFlag,
			shadowWriterModel: *shadowWriterModelFlag,
		}, stderr); err != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
			if isAuditUsageError(err) {
				return 2
			}
			return 1
		}
	}

	// The goal cache lives in the same ledger --record-db names, resolved
	// here so it is available BEFORE derivation — resolveGoalSource wires
	// it into the derived path below, and derivation is where the money
	// goes. Its READ half is unconditional: a fact recorded by an earlier,
	// RECORDED scan is worth reusing whether or not THIS scan also
	// records anything. Its WRITE half is gated on *recordFlag, passed to
	// resolveGoalSource below — see NewCachingGoalSource's doc for why a
	// scan run without --record must not itself grow the default ledger
	// with model-derived text about the operator's source just because it
	// happened to derive a goal.
	var goalCacheDSN string
	var goalStore reposcan.GoalCacheStore
	if !*noGoalCacheFlag {
		goalCacheDSN = *recordDSNFlag
		if goalCacheDSN == "" {
			goalCacheDSN = defaultScanDSN()
		}
		goalStore = newGoalLedgerCache(goalCacheDSN)
	}

	gs, disclosure, code := resolveGoalSource(stderr, *repoDir, *goalsPath, *deriveModel, *dryRun, len(selected), certifyRepoDeriver, goalStore, *noGoalCacheFlag, *recordFlag)
	if code != 0 {
		return code
	}
	// The base announce line prints HERE, unconditionally, BEFORE derivation
	// begins — exactly where it printed before the goal cache existed: an
	// operator watching a cache-wired run must still see "goals derived per
	// file by X@Y…" before the (possibly many) sequential model calls start,
	// not learn only afterward that any derivation happened at all. The
	// caching wrapper's own counts (fresh vs reused) are only known once
	// EmitJobs has asked it about every candidate, so a SECOND line — the
	// one naming those counts — prints after EmitJobs below (see
	// goalCacheDisclosureLine) whenever there is something beyond the base
	// line to say (reused > 0); a cache-wired scan that reused nothing has
	// nothing more to add and stays at one line.
	cachingGS, cacheWired := gs.(*reposcan.CachingGoalSource)
	if disclosure != "" {
		fmt.Fprintln(stdout, disclosure)
	}

	// The resolved role models are part of a verdict's identity: an audit run
	// with a different mutant-generator is a different audit. Until this was
	// wired, EmitConfig hardcoded ModelSet: "unset", so the cache key could
	// not tell two model sets apart and every ledger row recorded "unset" —
	// meaning the ledger could not be used to grade the models it exists to
	// grade.
	// The challenger writer is resolved here like every other seat. It used to
	// be structurally absent from this path — no flag, always empty — which
	// meant the decorrelation measurement could only ever run on ONE FILE at a
	// time via `certify --local`, and a whole-repo scan (the mode with the
	// scale to make a coefficient worth reading) could not produce one at all.
	// resolveRoleModels/modelSetKey take it explicitly so the two paths never
	// disagree about how a seat resolves, and so the model set in the cache key
	// and the ledger names the challenger when one ran.
	rmWriter, rmMutant, rmCritic, rmShadow, rmShadowWriter := resolveRoleModels(localAuditInput{
		writerModel:       *writerModelFlag,
		mutantModel:       *mutantModelFlag,
		criticModel:       *criticModelFlag,
		shadowModel:       *shadowModelFlag,
		shadowWriterModel: *shadowWriterModelFlag,
	})
	modelSet := modelSetKey(rmWriter, rmMutant, rmCritic, rmShadow, rmShadowWriter)

	// Selection evidence: ONE instrumented run of the suite for the whole
	// scan, whose answer every job then asks about its own file. Collected
	// HERE — before auditConfig below — because the grading mode is part of a
	// verdict's identity and the key has to spell which measurement was made.
	//
	// Never fatal, and never on a dry run (ex is nil there: a dry run audits
	// nothing, so it must not run the project's suite). A failure is a Note
	// printed to the operator, and the scan grades whole-suite — a real
	// measurement, just a different question, said out loud.
	if ex != nil && len(selected) > 0 {
		selectionSources := enumeratedSourcePaths(cands, excl[:enumExcl])
		// Announced only when it is about to happen: under --whole-suite
		// collectSelection returns immediately without running anything, and
		// printing "running the suite once with instrumentation…" for a run
		// that instruments nothing is a claim about work never done. A cache
		// HIT is previewed here — cheaply, before the (possibly
		// minutes-long) instrumented run collectSelection would otherwise be
		// about to start — so the announce line can say "reused" instead of
		// "running" for a run that is never going to happen.
		if !*wholeSuiteFlag {
			if reusedFrom, hit := ex.selectionCachePeek(selectionSources); hit {
				fmt.Fprintf(stdout, "  selection: reused — tree unchanged since scan %d\n", reusedFrom)
			} else {
				fmt.Fprintln(stdout, "  selection: running the suite once with per-test coverage instrumentation…")
			}
		}
		// collectSelection times ITSELF, around the instrumented run and
		// nowhere else — see localExecutor.selectionDuration. A clock started
		// here would tick for --whole-suite too, which returns from that call
		// having run nothing at all.
		ex.selection = ex.collectSelection(context.Background(), selectionSources)
		if !ex.selection.Ran {
			fmt.Fprintf(stdout, "  selection: grading by the WHOLE suite — %s\n", ex.selection.Note)
		}
		// SCAN-SCOPED: this instrumented run happens ONCE, for the whole
		// repo, before any per-file driver exists — the driver has no
		// notion of it (see RunSpec.SelectionDuration's doc), so it is
		// recorded here directly rather than through a file's EventSink.
		// Path "" is the scan-scoped marker; omitted (not zero) when the
		// pass never ran (--whole-suite, an unsupported language, no
		// runner) — see scanEventSink.record.
		if ex.selectionDuration > 0 {
			ex.events.forScan("phase_selection", "", map[string]any{
				"duration_ms": ex.selectionDuration.Milliseconds(),
			})
		}
	}
	selectionMethod := ""
	if ex != nil && ex.selection.Ran {
		selectionMethod = "coverage-context"
	}

	// AuditConfig, like ModelSet above, is part of a verdict's identity: it
	// carries the flags that change what a mutant run against a given file
	// MEASURES, not which files get audited. See auditConfigKey for the
	// inclusion/exclusion rationale.
	auditConfig := auditConfigKey(*wholeSuiteFlag, selectionMethod, checkArgv, mutantsFromSHA, writerMode)

	// testSurfacePaths has to STAT the testdata entries it admits, and every
	// read a scan performs is confined to the repository through an *os.Root —
	// a symlink pointing out of the checkout is the exfiltration path that
	// confinement exists to block. Opened here and closed immediately: this is
	// the same root EmitJobs opens for its own digests, held only as long as
	// the surface list takes to build.
	surfaceRoot, rerr := os.OpenRoot(*repoDir)
	if rerr != nil {
		fmt.Fprintf(stderr, "corral certify --repo: opening %s: %v\n", *repoDir, rerr)
		return 1
	}
	surfacePaths := testSurfacePaths(surfaceRoot, cands, excl)
	_ = surfaceRoot.Close()

	// Default --commit from the checkout, the way every other certify mode
	// does. Without this the --record scan ledger stored an EMPTY commit, so a
	// recorded scan named no revision and could not be joined to the code it
	// graded. Resolved once here so both the EmitConfig below and the ledger
	// row see the same value; "" is left as-is, and the downstream refusal in
	// auditSubject still reports it honestly.
	if strings.TrimSpace(*commit) == "" {
		*commit = gitHeadCommit(*repoDir)
	}

	localEndpoints, lerr := parseLocalEndpoints(localEndpointFlag)
	if lerr != nil {
		fmt.Fprintf(stderr, "corral certify --repo: %v\n", lerr)
		return 2
	}
	if ex != nil {
		ex.localEndpoints = localEndpoints
	}

	cfg := reposcan.EmitConfig{
		Owner: *owner, Repo: resolveRepoName(*repoDir, ""), Commit: *commit, Root: *repoDir,
		EngineVersion: reposcan.VerdictGeneration, ModelSet: modelSet, AuditConfig: auditConfig,
		Substrate: *substrateFlag,
		// What the verdicts are GRADED BY decides what TestSurfaceDigest has
		// to cover — one paired test file, or the whole suite. Both are
		// computed from the same facts testCmd itself resolves the command
		// from, so the key can never claim a narrower surface than the one
		// that actually ran.
		FileScopedTests:  gradesFileScoped(checkArgv, selected, cands, excl),
		TestSurfacePaths: surfacePaths,
		// auditConfig above says the SCAN ran selection; it cannot say that
		// THIS file fell back to the whole suite because the evidence never
		// saw it, nor WHICH tests it selected. See fileSelectionKey.
		FileAuditConfig: func(c reposcan.Candidate) string {
			if ex == nil {
				return ""
			}
			return fileSelectionKey(ex.selectionFor(reposcan.Job{Path: c.Path, TestPath: c.TestPath, Lang: c.Lang}))
		},
	}
	jobs, goalExcl, err := reposcan.EmitJobs(cfg, selected, gs)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: emitting jobs: %v\n", err)
		return 1
	}
	// NOW the caching wrapper has asked about every candidate, so its fresh
	// vs reused counts are final. A second line prints only when there is
	// something beyond the base line above to say — goalCacheDisclosureLine
	// returns the base UNCHANGED when reused==0, and printing that again
	// would just repeat the line already on screen.
	if cacheWired {
		fresh, reused := cachingGS.Stats()
		if line := goalCacheDisclosureLine(disclosure, *deriveModel, version, fresh, reused); line != "" && line != disclosure {
			fmt.Fprintln(stdout, line)
		}
		if line := goalReceiptLine(goalCacheDSN, *recordFlag, fresh); line != "" {
			fmt.Fprintln(stdout, line)
		}
	}
	// The exclusion sources partition DIFFERENTLY, and conflating them
	// double-counts files:
	//   - Enumerate's exclusions are files that are NOT candidates at all
	//     (no-language / is-test / no-paired-test).
	//   - not-selected / ungoaled / derive-failed / source-too-large name
	//     CANDIDATES — they are already inside len(cands).
	// So the file total is candidates + ENUMERATE-only exclusions. Counting
	// len(excl) after the appends added every such path a second time and
	// inflated TotalFiles past the number of files on disk — in a report a
	// later slice signs and anchors to a public transparency log.
	totalFiles := len(cands) + enumExcl

	// BOTH sources are still REPORTED, or the coverage story is a lie: a reader
	// has to see the ungoaled files too, since they are candidates that were
	// nonetheless not audited.
	excl = append(excl, goalExcl...)

	// totalFiles is printed rather than left for the reader to add up: the two
	// terms below overlap (ungoaled files are candidates AND excluded), so
	// candidates + excluded is deliberately NOT the file count.
	fmt.Fprintf(stdout, "  %d file(s) walked; %d candidate(s); %d job(s); %d file(s) excluded from the audit\n",
		totalFiles, len(cands), len(jobs), len(excl))
	if n := testMap.Len(); n > 0 {
		// Disclosed, so an operator can see their map was actually read — a
		// mapping silently ignored would be indistinguishable from one that
		// worked.
		fmt.Fprintf(stdout, "  %d source file(s) paired from --tests, ahead of filename convention\n", n)
	}
	printLanguageProfile(stdout, reposcan.BuildLanguageProfile(cands, excl))
	printSearchPairings(stdout, cands)
	printExclusions(stdout, excl)

	// An explicit `-- <cmd>` is applied to EVERY job, so it is only meaningful
	// when every job speaks the same language. Refuse the mixed case rather
	// than grade a mutated .py file with `go test ./...`: that check is green
	// on the baseline AND green on every mutant, which is not an error
	// anywhere in the pipeline — it is a confident 0.00 kill rate landing in
	// the report as a real measurement. Never fabricate a score.
	//
	// The error is CAPTURED rather than returned on the spot, because this
	// gate bounds THE AUDIT and the coverage pre-flight is not the audit: it
	// is one instrumented run of ONE language's suite, over the enumerated
	// source set, that grades nothing and mutates nothing. Returning here
	// made the documented multi-language path unreachable in composition —
	// `certify --repo aisuite --preflight -- pytest -q` (Python +
	// TypeScript) took this exit-2 branch before runPreflight was ever
	// called, so selectPreflightLanguage, added specifically to make that
	// invocation work, could only ever be reached by a scan whose JOBS were
	// single-language. The refusal itself is unchanged in strength: the
	// audit still refuses, with the same message and the same exit 2, on
	// every input it refused before.
	argvErr := checkArgvSpansOneLanguage(checkArgv, jobs)

	// --dry-run returns here, before any suite ever runs — including the
	// pre-flight's own instrumented run. Dry run means no execution, full
	// stop; --preflight opts INTO an extra suite run, it does not opt a dry
	// run out of being dry. The audit's refusal still outranks it, exactly
	// as when this check ran above.
	if *dryRun {
		if argvErr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", argvErr)
			return 2
		}
		// --record is silently INERT on this path, never silently ignored:
		// a dry run computes and prints a complete disposition for every
		// file — precisely what the ledger exists to keep — but it never
		// runs a single job (see the comment above), so there is nothing
		// audited or execution-rejected to record yet; a --record write
		// here would either record nothing real or misrepresent every
		// still-pending job as a decided disposition. Silence was the one
		// option ruled out: an operator who passed --record and sees
		// neither a ledger write nor an explanation would reasonably
		// assume either that the flag just did nothing, or worse, that
		// it worked.
		if *recordFlag {
			fmt.Fprintln(stderr, "corral certify --repo: --record ignored — --dry-run performs no audit, so there is nothing yet to record")
		}
		if *jsonOut {
			inv := buildScanInventoryAt(*repoDir, filepath.Base(*repoDir), totalFiles, rankSignal, cands, len(jobs), excl)
			if err := writeScanInventory(jsonSink, inv); err != nil {
				fmt.Fprintf(stderr, "corral certify --repo: writing JSON inventory: %v\n", err)
				return 1
			}
		}
		return 0
	}

	// Placed here, deliberately: EVERY line --preflight can print lives
	// behind this flag, so a run with it absent is byte-identical to today
	// — no extra progress line, no extra report section, and (see
	// runPreflight) the instrumented suite is never even invoked.
	var preflightResult reposcan.CoverageMap
	var preflightSources []string
	if *preflightFlag {
		// Independent of --top/--diff-base: this answers "what does the
		// suite ever touch in this repo", not "what did THIS scan audit",
		// so it is computed over every enumerated source file, not just the
		// selected/audited subset — see enumeratedSourcePaths.
		preflightSources = enumeratedSourcePaths(cands, excl[:enumExcl])
		fmt.Fprintln(stdout, "  preflight: running the suite once with coverage instrumentation…")
		preflightResult = ex.runPreflight(context.Background(), preflightSources)
	}

	// The audit cannot proceed, but the pre-flight above already answered
	// its own separate question — so its report is printed before the
	// refusal, and the refusal still exits 2. Without --preflight this is
	// byte-for-byte the pre-existing behaviour, just reached a few lines
	// later.
	if argvErr != nil {
		if *preflightFlag {
			printPreflightReport(stdout, preflightResult, preflightSources)
		}
		fmt.Fprintf(stderr, "corral certify --repo: %v\n", argvErr)
		return 2
	}

	// The ledger is opened PER OPERATION, never held across the scan.
	//
	// DuckDB is single-writer per file. This used to open one handle before
	// the scan and hold it until the final write, so for an entire hours-long
	// audit a concurrent `corral scans` against the same (default) DSN failed
	// with "Conflicting lock is held" — the record of the audits already paid
	// for went dark for the duration of the next one. Now the verdict cache
	// opens and closes around each lookup (see ledgerCache.withStore) and the
	// recording sequence below opens and closes around its own writes.
	//
	// The DSN is still RESOLVED here, and probed once by opening and
	// immediately closing it: an unopenable ledger must be reported, and
	// reporting it at the end of a paid scan (where it was always reported)
	// is too late to be useful without also saying so now. --record-db alone
	// still records nothing, silently — that behaviour is gated entirely by
	// *recordFlag, unchanged.
	//
	// An unopenable DSN does not fail the scan: scanStoreErr is carried
	// forward and reported in the --record block at the bottom, in the same
	// place and the same words a write failure has always been reported.
	var ledgerDSN string
	var scanStoreErr error
	if *recordFlag {
		ledgerDSN = *recordDSNFlag
		if ledgerDSN == "" {
			ledgerDSN = defaultScanDSN()
		}
		st, err := scanstore.Open(ledgerDSN)
		if err != nil {
			scanStoreErr = err
			// A DSN that cannot be opened must not be handed to the cache:
			// every lookup would pay an open that cannot succeed.
			ledgerDSN = ""
		} else if cerr := st.Close(); cerr != nil {
			scanStoreErr = cerr
			ledgerDSN = ""
		}
	}

	workers, swarmReadout := resolveScanWorkers(*swarmFlag, *substrateFlag)
	fmt.Fprint(stdout, swarmReadout)
	// Two budgets, on purpose. The jail divides the LLM-worker budget
	// (resolveSwarm, auto-capped so a box does not open 23 model
	// conversations at once); the workspace sizes TREES, which are CPU, not
	// conversations — cores/4 by design, capped by nothing but an explicit
	// --swarm. Feeding it the capped number gave a 24-core box two trees.
	budget := resolveSwarm(*swarmFlag)
	if *substrateFlag == substrateWorkspace {
		budget = treeBudget(*swarmFlag)
	}
	mutantConc := resolveMutantConcurrency(budget, *substrateFlag, workers, len(jobs))
	if mutantConc > 1 {
		// Named for what each concurrent mutant actually gets, because the
		// two substrates buy different things with the same number: a
		// disposable jail apiece, or a private copy of the checkout apiece.
		// The workspace line is an INTENTION, not a result — each file's own
		// probe decides whether its suite survives that many trees, and the
		// per-file `concurrency:` line reports what it decided.
		what := "the jail budget file-parallelism cannot spend"
		if *substrateFlag == substrateWorkspace {
			what = "one private tree each, where the file's own probe allows it"
		}
		fmt.Fprintf(stdout, "  mutant scoring: %d at once per file — %s (scoring runs the suite once per mutant, so this is the dominant cost on any repo with a real suite)\n", mutantConc, what)
	}

	// The workspace substrate pins file-level concurrency to 1 (one checkout,
	// mutated in place), so the operator's budget is unspent at that level.
	// Hand it to each file's OWN audit instead of idling the box: see
	// localExecutor.perFileSwarm for why this is safe only there.
	if *substrateFlag == substrateWorkspace {
		perFile := resolveSwarm(*swarmFlag)
		ex.perFileSwarm = perFile
		if perFile > 1 {
			fmt.Fprintf(stdout, "  per-file: %d worker(s) — files are serialized on this substrate, so each file's own roles run concurrently instead\n", perFile)
		}
	}
	ex.mutantConcurrency = mutantConc

	// ex is non-nil here: it is constructed on every non-dry-run path above,
	// and the dry run returned before this point.
	//
	// The cache is addressed by DSN and opens the ledger only for the instant
	// of a lookup: newLedgerCache("") (no --record, or an unopenable DSN)
	// misses every key, so this needs no extra guard — the cache is simply
	// inactive for the run.
	auditCtx, stopSignals := auditContext(stderr)
	defer stopSignals()

	results = reposcan.Scan(auditCtx, jobs, ex, newLedgerCache(ledgerDSN), workers)
	rep := reposcan.Aggregate(*owner, cfg.Repo, *commit, totalFiles, len(cands), results, excl)

	// The diff selected zero candidates: a docs-only (or no-paired-test-only)
	// PR is the most common change in existence, and it legitimately has
	// nothing to audit. That is a true, honest answer, not a failure — never
	// conflate it with "files were in scope and none could be graded".
	nothingInScope := *diffBase != "" && len(selected) == 0

	// The age of the oldest reused verdict is handed to printRepoReport, not
	// printed here: it belongs beside the "N verdict(s) reused from cache"
	// count, which is emitted MID-report (weakest files and more follow it).
	// Printed from here it landed detached at the very end, so the count and
	// the age — one number that is meaningless without the other — were
	// separated by everything in between. A zero time means nothing was
	// reused.
	oldestReused, _ := oldestReuse(results)
	printRepoReport(stdout, rep, nothingInScope, minKillRate, maxProvenMissed, unpairableInDiff, oldestReused)
	// What the scan consumed from the providers, broken out by role. A
	// whole-repo audit is the mode that actually costs money — it runs a full
	// herd per file — and it reported nothing at all, so "what did that cost
	// me" had no answer from the tool whose central caveat is that audits are
	// expensive. Built from the SAME per-file ModelCalls that feed the
	// ledger and warehouse below — never a second measurement — so this line
	// and scan_model_calls can never disagree.
	if line := costLine(scanModelCallTotals(results)); line != "" {
		fmt.Fprintln(stdout, line)
	}
	// A distinct section, never folded into Excluded/Ungradable/the audited
	// fraction: this is an inventory alongside the audit, not a change to
	// it (see the brief). Printed unconditionally when the flag was given,
	// even when the pre-flight could not run at all.
	if *preflightFlag {
		printPreflightReport(stdout, preflightResult, preflightSources)
	}

	exitCode := repoScanExitCode(rep, nothingInScope, minKillRate, maxProvenMissed)

	// One roster, shared by the push and the statement, so the two can never
	// disagree about which model held which seat.
	models := func() map[string]string {
		m := map[string]string{
			"mutant-generator": strings.TrimSpace(*mutantModelFlag),
			"test-writer":      strings.TrimSpace(*writerModelFlag),
			"test-critic":      strings.TrimSpace(*criticModelFlag),
		}
		for role, v := range m {
			if v == "" || v == "off" {
				delete(m, role)
			}
		}
		return m
	}

	// End-of-scan sequence: ledger record → attestation → push, in that
	// order, because each later step needs something the one before it
	// produces. scanID is the ledger's row id (0 when --record was not
	// given, or when the ledger write failed — 0 is the honest value
	// either way, never a fabricated id). statementSHA256 is the sha256 of
	// the --attest statement writeAuditStatement wrote (empty when --attest
	// was not given, or its write failed). Both are folded into the rows
	// --push writes, which is why push must run last: it is the only step
	// with nothing later depending on it.
	//
	// Every one of the three writes below is FAIL-OPEN, deliberately, and
	// this is the one place in corral where uncertainty must not fail
	// closed: this command's exit code is a CI merge gate. If a ledger
	// write, a statement write, or a push could change it, a full disk or
	// an unreachable warehouse would red-build a pull request over
	// bookkeeping. So each failure is printed loudly on stderr and the
	// verdict and exit code decided above stand unchanged. Do not "fix"
	// this into a failure path — that is precisely the bug this comment
	// exists to head off. Placed after `code` is computed, and calling
	// nothing that can panic into the exit path below.
	//
	// The ledger rows are built ONCE, here, whether or not --record was
	// given: they are also what the attestation hashes and what --push
	// writes. Rebuilding them from the report for the push (which is what
	// this code used to do) is how the two records came to disagree — the
	// report path carried only the files a scan AUDITED, so the warehouse
	// never held a row for the files corral refused.
	// Built from the same per-file ModelCalls the ledger's scan_model_calls
	// rows come from (modelCallRows, below) — never a second measurement, so
	// the scan header's totals and the per-role grain can never disagree.
	modelCallRows := buildScanModelCallRows(results)
	var inTokens, outTokens, modelCallCount int64
	for _, c := range modelCallRows {
		inTokens += c.InputTokens
		outTokens += c.OutputTokens
		modelCallCount += int64(c.Calls)
	}
	host, _ := os.Hostname()
	finishedAt := time.Now()
	scan := scanstore.Scan{
		Owner: *owner, Repo: cfg.Repo, Commit: *commit,
		Substrate: *substrateFlag, EngineVersion: version, ModelSet: cfg.ModelSet,
		Top: effectiveTop, AllCandidates: *allFlag, DiffBase: *diffBase,
		TotalFiles: totalFiles, Candidates: rep.Candidates, Audited: rep.Audited,
		KillRate: killRatePtr(rep.KillRate), CacheHits: rep.CacheHits,
		PreflightRan: preflightResult.Ran, PreflightNote: preflightResult.Note,
		StartedAt: startedAt, FinishedAt: finishedAt,
		// The box and the build, so a wall-clock number in this ledger can be
		// interpreted at all. TreesRequested is the workspace substrate's
		// resolved per-file tree count — an INTENTION; the per-file probe
		// decides what each file got. 0 (and SQL NULL) on the jail, which
		// builds no trees.
		CorralVersion: version, Host: host, Cores: runtime.NumCPU(),
		TreesRequested: func() int {
			if *substrateFlag == substrateWorkspace {
				return mutantConc
			}
			return 0
		}(),
		TotalMillis: finishedAt.Sub(startedAt).Milliseconds(),
		// The instrumented coverage run, at the grain it HAPPENS at: once per
		// scan. Every file's verdict carries the same duration too (so a
		// per-file readout can name each phase of that file's audit), but
		// this is the copy a cost query sums — adding the per-file column
		// would count one run once per file. NULL when no pass ran.
		SelectionMillis: scanSelectionMillis(ex),
		// nil unless collectSelection served a cache HIT this scan — see
		// localExecutor.selectionReusedFrom's own doc.
		SelectionReusedFrom: scanSelectionReusedFrom(ex),
		// What the scan consumed. The run already printed these to stdout and
		// then discarded them, which is how "what did that cost me" had no
		// answer from the tool whose central caveat is that audits are
		// expensive.
		InputTokens: inTokens, OutputTokens: outTokens, ModelCalls: modelCallCount,
		// True only when source bytes ACTUALLY left the box: --push-source
		// with no --push sends nothing, and a ledger row claiming otherwise
		// would answer the custody question wrongly.
		SourcePushed: *pushSourceFlag && strings.TrimSpace(*pushFlag) != "",
	}
	files := buildScanFileRows(results, rep.Excluded, preflightResult, mutantsFromSHA, *repoDir, stderr)
	mutants := buildScanMutantRows(0, results)
	// modelCallRows was built above, before the scan row, so its totals could
	// feed InputTokens/OutputTokens/ModelCalls without a second measurement.
	// The event grain's producer is the scan's own scanEventSink (see
	// localExecutor.events): every job's driver fed it through auditInputFor,
	// and this is the ONE place its accumulated tape is read out, after every
	// file has finished.
	eventRows := scanEventRows(ex)

	var scanID int64
	if *recordFlag {
		if scanStoreErr != nil {
			// The open failure happened earlier (above, before the scan) but
			// is reported HERE — the same place and the same words a write
			// failure has always been reported in, so this is not a new
			// failure surface, just the pre-existing one's error arriving
			// from an earlier point in the run.
			fmt.Fprintf(stderr, "corral certify --repo: scan ledger NOT written: %v\n", scanStoreErr)
		} else {
			// Opened for THIS write and closed immediately: the whole point
			// of not holding the handle across the scan (see the DSN
			// resolution above) is that the ledger is readable by anything
			// else in between.
			st, err := scanstore.Open(ledgerDSN)
			if err != nil {
				fmt.Fprintf(stderr, "corral certify --repo: scan ledger NOT written: %v\n", err)
			} else {
				id, rerr := recordCertifyRepoScan(st, scan, files, mutants, modelCallRows, eventRows, stderr)
				// A fresh MISS's raw evidence is held on the executor (see
				// pendingSelectionPut's own doc): this is the earliest point a
				// scan id exists to Put it against, on the SAME handle just
				// used to record the scan — opening a second one here would
				// pay DuckDB's single-writer lock twice for one write. Best
				// effort, like every write in this fail-open block: a lost
				// selection-cache row costs the NEXT scan an instrumented
				// run, never THIS scan's exit code.
				if rerr == nil && ex != nil && ex.pendingSelectionPut != nil {
					p := ex.pendingSelectionPut
					if perr := st.SelectionCachePut(context.Background(), p.TreeDigest, p.CmdDigest, p.Plugin, p.Substrate, p.Raw, "", id); perr != nil {
						fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but the selection cache was NOT written: %v\n", id, perr)
					}
				}
				if cerr := st.Close(); cerr != nil && rerr == nil {
					fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but closing the ledger failed: %v\n", id, cerr)
				}
				if rerr != nil {
					fmt.Fprintf(stderr, "corral certify --repo: scan ledger NOT written: %v\n", rerr)
				} else {
					scanID = id
				}
			}
		}
	}

	// The bundle: the ledger rows, mapped to the warehouse's shape, ONCE.
	// Built here — after the ledger write, so it carries the scan id, and
	// before the attestation, so the statement can sign its hash — and used
	// by both of the steps that follow. Built even when neither --attest nor
	// --push was given, which costs a few slices and keeps the two paths from
	// diverging.
	//
	// A failure to resolve the repo/commit is NOT fatal here: it only means
	// no bundle can be built, and both consumers already refuse for
	// themselves (writeAuditStatement below, and --push, which never runs
	// with an empty target).
	bundleRepo, bundleCommit, subjErr := auditSubject(*repoDir, rep)
	rosterJSON, _ := json.Marshal(models())
	bundle := buildBundle(scan, scanID, files, mutants, modelCallRows, eventRows,
		auditpush.Link{}, scan.SourcePushed, bundleRepo, bundleCommit, githubRunURL(),
		bundleMeta{
			ModelsByRole: string(rosterJSON),
			MinKillRate:  minKillRate, MaxProvenMissed: maxProvenMissed,
			Passed: exitCode == 0,
		})

	// The audit statement, written after the exit code is known so `passed`
	// records the verdict this run actually returned rather than a guess
	// made before the gates were applied, and after the ledger record above
	// so it can name the scan it belongs to (scanId: 0, honestly, when
	// --record was not given or its write failed).
	var statementSHA256 string
	if strings.TrimSpace(*attestFlag) != "" {
		sha, err := writeAuditStatement(*attestFlag, *repoDir, rep, models(), minKillRate, maxProvenMissed, exitCode == 0, scanID, bundle)
		if err != nil {
			fmt.Fprintf(stderr, "corral certify --repo: writing --attest statement: %v\n", err)
		} else {
			statementSHA256 = sha
			// Close the loop the ordering opens: the statement had to be
			// written after the scan row (it names the scan id), so the row
			// went in with this column empty. Stamp it now, or the local
			// ledger permanently disagrees with the pushed row about whether
			// this scan produced a statement. Fail-open like every other
			// write in this sequence.
			if scanID != 0 && ledgerDSN != "" {
				if uerr := stampStatementSHA256(ledgerDSN, scanID, sha); uerr != nil {
					fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but its statement_sha256 was NOT stamped: %v\n", scanID, uerr)
				}
			}
			bundle.Scan.StatementSHA256 = sha
			fmt.Fprintf(stdout, "  wrote the audit statement to %s — attest it with actions/attest, verify with `gh attestation verify`\n", *attestFlag)

			// --transparency: upload the statement just written to a public
			// Rekor log. Fails OPEN — see uploadToTransparencyLog's doc —
			// so a failure here never changes exitCode, only prints and
			// leaves both receipt columns NULL.
			if *transparencyFlag {
				pubKeyPEM, pkerr := transparencyPublicKeyPEM()
				if pkerr != nil {
					fmt.Fprintf(stderr, "corral certify --repo: --transparency: %v\n", pkerr)
				} else if entry, ok := uploadToTransparencyLog(context.Background(),
					newTransparencyLogger(rekorBaseURL()), *attestFlag, pubKeyPEM, stdout, stderr); ok {
					logIndex := entry.LogIndex
					bundle.Scan.RekorLogIndex = &logIndex
					bundle.Scan.RekorUUID = entry.UUID
					// Same close-the-loop reasoning as statement_sha256
					// above: the receipt only exists after the ledger row
					// does, so it has to be stamped on rather than written
					// at Record time.
					if scanID != 0 && ledgerDSN != "" {
						if uerr := stampRekorReceipt(ledgerDSN, scanID, &logIndex, entry.UUID); uerr != nil {
							fmt.Fprintf(stderr, "corral certify --repo: scan %d recorded, but its rekor receipt was NOT stamped: %v\n", scanID, uerr)
						}
					}
				}
			}
		}
	}

	// Push last: its rows carry scanID and statementSHA256 from the two
	// steps above, so a pushed row and the statement it names can be
	// checked against each other. Link.Require mirrors --attest: when it
	// was given, a row without a statement_sha256 is refused rather than
	// pushed looking traceable when it is not; without --attest there is no
	// statement to point to, and a row's statement_sha256 is honestly
	// empty.
	//
	// A CONSEQUENCE WORTH NAMING, because it is a real operator surprise:
	// with --attest, a FAILED statement write withholds the whole push.
	// statementSHA256 stays empty, Require is still true (it is set from the
	// flag, not from the outcome), and PushBundle then refuses every row.
	// That is the intended direction — an untraceable row is worse than a
	// missing one, and the failure was already printed loudly on stderr —
	// but it means one fail-open write turning bad silently takes the next
	// one with it. Neither changes the exit code; see the block comment
	// above.
	if strings.TrimSpace(*pushFlag) != "" {
		bundle.Link = auditpush.Link{ScanID: scanID, StatementSHA256: statementSHA256, Require: strings.TrimSpace(*attestFlag) != ""}
		switch {
		case subjErr != nil:
			fmt.Fprintf(stderr, "corral certify --repo: pushing to %s: %v\n", *pushFlag, subjErr)
		default:
			c, perr := pushBundle(*pushFlag, bundle)
			switch {
			case perr != nil:
				fmt.Fprintf(stderr, "corral certify --repo: pushing to %s: %v\n", *pushFlag, perr)
			case c.Total() > 0:
				fmt.Fprintf(stdout, "  pushed %d scan, %d file(s), %d mutant(s), %d model-call row(s), %d event(s) to %s\n",
					c.Scans, c.Files, c.Mutants, c.Calls, c.Events, *pushFlag)
			}
		}
	}

	return exitCode
}

// fileSelectionKey is the per-file half of the audit config: WHICH question
// this one file's kill rate answers. The scan-level auditConfigKey can only
// say that selection ran.
//
// Two components, and the second is the one a review caught missing:
//
//   - file-selection=<mode> — coverage-context, uncovered, or whole-suite.
//     Never the Fallback TEXT, which can carry an error string (a path, a
//     pid) and would make the key unstable across identical runs.
//   - selected-tests=<digest> — the ids themselves, for a non-empty
//     selection. The selection is derived from COVERAGE EVIDENCE, so a
//     change anywhere in the repo's source can route a test through this
//     file or away from it while the file, the test surface and the argv are
//     all byte-identical. The test-surface digest cannot catch that: it
//     moves when a test FILE changes, and nothing about a test file changed.
//     Without this the cache serves a verdict measured by a set of tests
//     that no longer grades the file.
//
// The ids are sorted (Select already sorts them; sorted again here so the key
// cannot depend on that) and joined on \x00 — a byte no node id can contain —
// then folded to a sha256, for the same two reasons auditConfigKey folds the
// argv: the list is long, and it routinely contains `=` and `,`, which are
// CanonicalKV's own delimiters.
func fileSelectionKey(sel lang.Selection) string {
	mode := "file-selection=whole-suite"
	switch {
	case sel.Method == "":
	case len(sel.Tests) == 0:
		mode = "file-selection=uncovered"
	default:
		mode = "file-selection=" + sel.Method
	}
	if len(sel.Tests) == 0 {
		return mode
	}
	ids := append([]string{}, sel.Tests...)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return mode + ",selected-tests=" + hex.EncodeToString(sum[:])
}

// auditConfigKey is the SCAN-WIDE half of KeyInputs.AuditConfig: the settings
// that can change a given FILE's measured verdict.
//
// "Canonical" here means CanonicalKV's sorted name=value rendering of THIS
// map, and no more than that. EmitJobs appends the per-file component
// (fileSelectionKey) after this function's output, with a comma, so the full
// AuditConfig string a job carries is scan-wide-sorted-then-per-file — a
// deterministic order, not a globally sorted one. That is fine, and it is
// written down because reading the result as one sorted list is the way a
// future change starts inserting into the middle of it.
//
// --top, --all and --diff-base are deliberately NOT here. They select which
// files get audited; they do not change what a mutant run against an audited
// file measures. Including them would stop a diff-scoped PR run from ever
// reusing a nightly full-repo verdict for the same unchanged file — which is
// most of the value the cache exists to deliver.
//
// --min-kill-rate is NOT here either, for a different reason: it decides this
// process's EXIT CODE (see repoScanExitCode) and cannot change a measurement.
// Keying it meant the CI merge-gate invocation — the one that always passes a
// threshold — could never reuse a nightly verdict.
//
// The operator's `-- <cmd>` IS here, and it is the most load-bearing entry:
// that argv is what testCmd hands to the baseline AND to every mutant, so it
// is the grading surface itself. Without it, `-- pytest -q tests/unit` and a
// later `-- pytest -q` key identically for unchanged source, and the
// whole-suite run signs a kill rate measured against a subset it never ran.
//
// It is folded to a sha256 rather than written verbatim, for two reasons: a
// real check command is long, and it routinely contains `=` and `,` — the
// delimiters CanonicalKV itself uses, so operator text could otherwise forge
// or corrupt neighbouring components. The words are joined on \x00 (a byte no
// argv word can contain) so no re-splitting of the same characters can
// collide. An EMPTY argv serializes as absent, never as the digest of an empty
// string: the plugin-default path must key exactly as it always has.
//
// The GRADING MODE is here for the same reason: a kill rate earned against
// the tests that demonstrably execute a file and one earned against the whole
// suite are answers to different questions, and a verdict may only be reused
// for the question it was measured under. Both halves are keyed — whole-suite
// as an explicit component, and selection by its METHOD (the evidence kind,
// e.g. coverage-context), so a scan whose instrumented run failed and fell
// back to the whole suite cannot silently reuse a selected verdict.
//
// WHICH tests were selected is keyed too, but PER FILE and not here — see
// fileSelectionKey. This comment used to claim the test-surface digest
// covered them; it does not. That digest moves when a test FILE changes,
// and the selection is derived from coverage evidence, which an ordinary
// non-test source change elsewhere in the repo can move on its own.
//
// Bias when adding to this list: include. Over-inclusion causes a needless
// miss, which costs money. Under-inclusion serves a stale verdict, which
// signs an unmeasured claim.
func auditConfigKey(wholeSuite bool, method string, checkArgv []string, mutantsFrom, writerMode string) string {
	m := map[string]string{}
	// The WRITER MODE, for the same reason the grading mode is here: the two
	// shapes are different exams. Per-survivor proves each survivor ALONE
	// against its own mutant, on its own repair budget, behind its own
	// compliant baseline; batched proves all of them together on one budget
	// and one pass. Whichever earned the numbers is disclosed on the report
	// line, recorded in the ledger and SIGNED into the attestation — so a key
	// blind to it would serve a per-survivor verdict to a batched run and
	// then sign `writerMode: per-survivor` for a run that never executed it.
	//
	// Keyed by its resolved spelling, never by a bool: an EMPTY mode is a
	// caller that named none (the brain, a test) and must key exactly as it
	// always has, which is also why this is the one component that can be
	// absent while a mode is nonetheless in force downstream.
	if writerMode != "" {
		m["writer-mode"] = writerMode
	}
	// A REPLAYED run sat a different exam from a generated one, so its
	// verdict is not interchangeable with a cached generated verdict for the
	// same content — and a generated verdict is not interchangeable with a
	// replay of some OTHER set either. Without this the cache would serve one
	// exam's kill rate under another exam's name, silently: exactly the
	// blindness the hardcoded ModelSet:"unset" key had, in the one dimension
	// --mutants exists to control.
	if mutantsFrom != "" {
		m["mutants-from"] = mutantsFrom
	}
	if wholeSuite {
		m["whole-suite"] = "true"
	} else if method != "" {
		m["test-selection"] = method
	}
	if len(checkArgv) > 0 {
		sum := sha256.Sum256([]byte(strings.Join(checkArgv, "\x00")))
		m["check-argv"] = hex.EncodeToString(sum[:])
	}
	return reposcan.CanonicalKV(m)
}

// testSurfacePaths is every file that can change what this repository's whole
// recursive suite MEASURES — the grading surface of a scan with no
// --scope-tests and no explicit `-- <cmd>`.
//
// The rule is DIRECTORY-based, not filename-based. A file counts if it lives
// in a directory that holds at least one recognized test file, where the
// recognized tests are:
//
//   - every CANDIDATE's paired test, including candidates --top or --diff-base
//     left unselected. The suite does not stop running a test because this
//     scan chose not to audit its source file.
//   - every file Enumerate rejected as `is-test`.
//
// Filename markers alone are not enough, and that gap was live: `conftest.py`
// — the most common Python shared-fixture file there is — matches none of the
// `_test.` / `test_` / `_spec.` / `.test.` / `.spec.` / `spec_` markers, so
// Enumerate files it as `no-paired-test` and it reached no key at all. Same
// for tests/helpers.py, tests/fixtures.py, jest.setup.js and golden fixture
// data. Weaken a fixture in any of them and every file's key used to stay put:
// HIT, and the ledger repeats a kill rate for a suite that genuinely got worse.
//
// The widening is deliberately confined to the SURFACE. isTestFile still
// decides which files are audit CANDIDATES and is untouched — widening that
// would change what gets audited, a far larger blast radius than the cache.
//
// Over-inclusion is expected and fine: a README beside the tests now
// invalidates. Over-invalidation costs a miss, which costs money.
// Under-invalidation signs a claim about content that was never measured.
//
// Two exclusion reasons are still held out, because including them would break
// the scan rather than widen it: `not-a-regular-file` (a symlink cannot be
// read through the *os.Root and a FIFO would block forever) and `skipped-dir`
// (build output the walk deliberately did not look at).
//
// ...with ONE carve-out in the skipped-dir holdout: `testdata`. It is in
// skipDirs, so a Go golden fixture comes back as a skipped-dir exclusion, and
// internal/foo/testdata/ holds no recognized test file either, so the directory
// rule never reaches it. Weakening a golden is the commonest way a Go suite
// changes what it measures — and Go is this repository's own language, so the
// gap applied to corral auditing itself. Anything under a path segment named
// exactly `testdata` is admitted.
//
// The carve-out is narrowed twice, both load-bearing:
//   - It admits only entries CONFIRMED to be regular files, stat'ed through
//     the scan's own *os.Root. skippedDirFiles emits a DIRECTORY path for an
//     unreadable subtree, and DigestTestSurface hashes through DigestFile,
//     which hard-errors on any non-regular entry — scan-fatal. So admitting a
//     directory would abort scans rather than widen keys.
//   - The stat goes through the SAME root the scan uses for its confined
//     reads, never bare os.Stat: a symlink out of the checkout is exactly the
//     exfiltration path that confinement exists to block.
//
// Residual: a testdata entry that is not a regular file is left OUT rather
// than erroring. It cannot be a golden, and a scan-fatal error here would be
// a worse failure than a slightly narrower surface — but it does mean such an
// entry reaches no key.
//
// WHAT THIS STILL DOES NOT COVER — stated plainly, because a maintainer reads
// this to decide whether to trust the key:
//   - a fixture file in a directory that holds no recognized test. A repo-root
//     conftest.py whose tests all live under tests/ configures every one of
//     them and reaches no key at all.
//   - a non-regular entry under testdata (see above).
//   - THE FILE-SCOPED PATH IGNORES THIS LIST ENTIRELY. EmitJobs only digests
//     the surface when FileScopedTests is false; when it is true the digest is
//     the one paired test file. So `-- pytest tests/test_a.py` really does
//     load tests/conftest.py, and weakening that fixture leaves the key
//     unmoved: a HIT on a kill rate the changed suite would no longer
//     produce. That is an OPEN WRONG-HIT PATH, not an intended narrowing.
//
// No new walk: both lists are already in hand at the call site; root is only
// stat'ed for the testdata carve-out.
func testSurfacePaths(root *os.Root, cands []reposcan.Candidate, excl []reposcan.Exclusion) []string {
	dirOf := func(p string) string { return path.Dir(path.Clean(filepath.ToSlash(p))) }

	// Pass 1: which directories hold a recognized test file.
	testDirs := map[string]bool{}
	for _, c := range cands {
		if c.TestPath != "" {
			testDirs[dirOf(c.TestPath)] = true
		}
	}
	for _, e := range excl {
		if e.Reason == reposcan.ReasonIsTest {
			testDirs[dirOf(e.Path)] = true
		}
	}

	// Pass 2: the recognized tests themselves, plus everything living beside
	// one. DigestTestSurface cleans, de-duplicates and sorts, so repeats and
	// order here are harmless.
	paths := make([]string, 0, len(cands)+len(excl))
	for _, c := range cands {
		if c.TestPath != "" {
			paths = append(paths, c.TestPath)
		}
		if testDirs[dirOf(c.Path)] {
			paths = append(paths, c.Path)
		}
	}
	for _, e := range excl {
		switch e.Reason {
		case reposcan.ReasonIsTest:
			paths = append(paths, e.Path)
		case reposcan.ReasonSkippedDir:
			if underTestdata(e.Path) && isRegularInRoot(root, e.Path) {
				paths = append(paths, e.Path)
			}
		case reposcan.ReasonNotRegularFile:
			// Never digested — see the comment above.
		default:
			if testDirs[dirOf(e.Path)] {
				paths = append(paths, e.Path)
			}
		}
	}
	return paths
}

// normalizeTestToken reduces an argv token or a candidate's TestPath to the
// repo-relative file path it names, so the two can be compared. A pytest node
// id (`tests/test_a.py::test_x`) names the same FILE as `tests/test_a.py`, and
// the file is what the digest covers.
func normalizeTestToken(tok string) string {
	t := filepath.ToSlash(tok)
	if i := strings.Index(t, "::"); i >= 0 {
		t = t[:i]
	}
	return path.Clean(t)
}

// knownTestPaths is every path this scan's enumeration already recognizes as a
// test FILE: each candidate's paired test (selected or not — the suite does
// not stop running a test because this scan chose not to audit its source) and
// every file Enumerate rejected as `is-test`.
//
// It exists for gradesFileScoped's exclusivity check, and it is deliberately
// the same data testSurfacePaths draws on, from the same in-hand lists: no new
// filesystem walk, and no second notion of "what is a test" to drift from the
// first.
func knownTestPaths(cands []reposcan.Candidate, excl []reposcan.Exclusion) map[string]bool {
	known := make(map[string]bool, len(cands)+len(excl))
	for _, c := range cands {
		if c.TestPath != "" {
			known[normalizeTestToken(c.TestPath)] = true
		}
	}
	for _, e := range excl {
		if e.Reason == reposcan.ReasonIsTest {
			known[normalizeTestToken(e.Path)] = true
		}
	}
	return known
}

// underTestdata reports whether rel has a path segment named exactly
// `testdata` — the Go convention for fixture data, and a directory the walk
// skips. Segment equality, not a substring: `internal/testdata_helpers/x.go`
// is ordinary source, not fixture data.
func underTestdata(rel string) bool {
	for _, seg := range strings.Split(path.Clean(filepath.ToSlash(rel)), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// isRegularInRoot reports whether rel is a regular file, stat'ed through the
// scan's own confined root. Lstat, not Stat: a symlink must answer "no" on its
// own terms rather than on its target's, since the target is not this
// repository's content. Any error is a "no" — the caller's contract is to
// leave the entry out of the surface rather than fail the scan.
func isRegularInRoot(root *os.Root, rel string) bool {
	if root == nil {
		return false
	}
	fi, err := root.Lstat(path.Clean(filepath.ToSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}

// gradesFileScoped answers the ONE question KeyInputs.TestSurfaceDigest turns
// on: will every job in this scan really be graded against its own paired test
// file alone? It deliberately mirrors localExecutor.testCmd's own resolution
// order rather than reading the flags naively, because the flags are not the
// answer:
//
//   - An explicit `-- <cmd>` outranks --scope-tests entirely (testCmd returns
//     it verbatim), so the only file-scoped case there is a command that names
//     a test file — `pytest -q tests/test_a.py`, or a pytest node id with a
//     `::selector` on it. A command naming a DIRECTORY (`pytest -q tests/unit`)
//     is a subset of the suite, not one file, so it stays whole-suite: the argv
//     is in the cache key too (auditConfigKey), so a different subset keys
//     differently anyway, and the whole-suite digest still covers every file
//     inside that directory.
//     ONE named test is not enough: testCmd hands the SAME argv to every job,
//     so `-- pytest tests/test_a.py` with a.py and b.py both selected grades
//     BOTH files with tests/test_a.py. Keying that as file-scoped would give
//     b.py the digest of tests/test_b.py — a file that never runs — while the
//     file that really grades it appears in no key at all (auditConfigKey
//     digests the argv TEXT, which does not move when the named test's
//     CONTENTS change). So every selected candidate's own test must be named;
//     anything less keys as whole-suite. That over-invalidates a mixed argv,
//     which costs a miss — money — where the other direction signs a kill rate
//     for a surface that was never measured.
//     Coverage alone is still not enough, and the converse hole was live:
//     `-- pytest tests/test_a.py tests/test_b.py` with --top 1 (or a
//     --diff-base) selecting only a.py names every SELECTED candidate's test,
//     yet tests/test_b.py runs in that same command and its assertions kill
//     a.py's mutants. Keyed file-scoped, a.py's key is digest(tests/test_a.py)
//     alone; weaken tests/test_b.py and the key does not move — a HIT on a
//     kill rate the weakened suite would no longer produce. So the argv must
//     ALSO name no test file OUTSIDE the selected set. The known-test set is
//     the enumeration's own (candidates' TestPaths plus the `is-test`
//     exclusions, the same facts testSurfacePaths draws on) — no new walk.
//     A token disqualifies the scan two ways: when it IS a known test path
//     outside the selected set, and when it is a DIRECTORY PREFIX of one.
//     The prefix half is not belt-and-braces — it was its own live hole:
//     `-- pytest tests/test_a.py tests/unit` covers the one selected
//     candidate's test, and `tests/unit` is not itself a known test FILE, so
//     an exact-match-only check let it through — while tests/unit/test_b.py
//     ran in that same command and killed a.py's mutants. The argv TEXT is
//     keyed, but a named directory's CONTENTS are not, so a test inside it
//     can grade the run without ever moving the key. Both halves compare
//     through normalizeTestToken, so a trailing slash (`tests/unit/`) names
//     the same directory, and the repo root ("." or "./") gets an empty
//     prefix so it matches every path — its tok+"/" form is "./", which
//     prefixes no repo-relative path, so the general rule alone would ignore
//     a token that names the entire suite. A directory holding only SELECTED
//     tests is still allowed: it names nothing the key does not already
//     cover.
//     Everything else — a flag like -q or -k, a flag value like "not slow",
//     a directory with no known test under it — is IGNORED. Note which
//     direction "ignored" points: an ignored token does not force whole-suite,
//     so it LEAVES the scan file-scoped. That is the unsafe direction, and it
//     is why the two residuals below are stated rather than filed away:
//     everything the matching fails to recognize silently becomes a
//     file-scoped key.
//     RESIDUAL: exclusivity can only recognize tests the ENUMERATION knows
//     about. An argv naming a test file Enumerate never saw — one under a
//     skipped directory, or an unpaired file matching no test-filename
//     marker — does not disqualify, so that file grades the run and reaches
//     no key. Deliberately the same recognition set testSurfacePaths uses; a
//     second, wider notion of "what is a test" would be free to drift from
//     the first.
//     RESIDUAL: exact and prefix matching are both TEXTUAL and
//     repo-relative. An argv naming tests by an ABSOLUTE path
//     (`/home/me/repo/tests/unit`) or through a SYMLINKED directory
//     normalizes to a string the enumeration's repo-relative paths do not
//     match, so it does not disqualify — and by the paragraph above, that
//     leaves the scan file-scoped while those tests grade it. Closing this
//     means resolving argv tokens against the repo root (and through
//     symlinks) rather than comparing text, which is a larger change than
//     this check.
//   - With no explicit argv the answer is always false, even though
//     coverage-guided selection does narrow each file's command: the
//     selection is derived from a whole-suite instrumented run, so every test
//     file in the tree can change what it picks. Keying the whole-suite
//     digest over-invalidates the narrowly-graded files, which only costs
//     money — the other direction would key a run as if one file were the
//     surface while a change anywhere else silently moved the selection.
func gradesFileScoped(checkArgv []string, selected, cands []reposcan.Candidate, excl []reposcan.Exclusion) bool {
	if len(selected) == 0 {
		return false
	}
	if len(checkArgv) > 0 {
		named := make(map[string]bool, len(checkArgv))
		for _, tok := range checkArgv {
			named[normalizeTestToken(tok)] = true
		}
		// The tests this scan is allowed to be scoped to.
		selectedTests := make(map[string]bool, len(selected))
		for _, c := range selected {
			if c.TestPath == "" {
				return false
			}
			selectedTests[normalizeTestToken(c.TestPath)] = true
		}
		// Coverage: every selected candidate's own test must be named.
		for t := range selectedTests {
			if !named[t] {
				return false
			}
		}
		// Exclusivity: and NO test file outside that set may be named —
		// directly, or by naming a directory that CONTAINS one.
		known := knownTestPaths(cands, excl)
		for tok := range named {
			if known[tok] && !selectedTests[tok] {
				return false
			}
			// The repo root is spelled "." (and "./", which normalizes to the
			// same thing), and its tok+"/" form is "./" — which prefixes NO
			// repo-relative path. Left to the general rule the root token
			// would be silently ignored while naming every test in the tree,
			// so it gets an empty prefix, which every path matches.
			prefix := tok + "/"
			if tok == "." {
				prefix = ""
			}
			for kt := range known {
				if selectedTests[kt] {
					continue
				}
				if strings.HasPrefix(kt, prefix) {
					return false
				}
			}
		}
		return true
	}
	// No explicit `-- <cmd>`: the scan runs the language plugin's own whole
	// recursive suite, and coverage-guided selection narrows it PER FILE from
	// evidence produced by that same whole suite. The grading surface is
	// therefore a SUBSET of the whole suite chosen by evidence that itself
	// depends on every test file in the tree — so the key keeps the
	// whole-suite digest. A superset is always a safe invalidation: it can
	// only cost a needless re-audit, never serve a verdict measured against a
	// surface that has since changed.
	return false
}

// parseMinKillRate parses and range-validates the --min-kill-rate flag value.
// Split out so the validation is testable directly, without a repo, a jail,
// or an API key — mirroring how the substrate check next to its call site is
// a plain string comparison. Valid range is 0.0-1.0 inclusive (the flag is a
// minimum, so both ends are legal thresholds).
func parseMinKillRate(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("--min-kill-rate %q is not a number: %w", s, err)
	}
	// Stated positively (the value must lie IN [0,1]) rather than as the
	// negation ("< 0 || > 1"): ParseFloat accepts "NaN"/"nan" cleanly
	// (err == nil), and every comparison against NaN is false — so the
	// negated form lets NaN silently pass both bounds. !(v >= 0 && v <= 1)
	// rejects NaN by construction, the same way checking argv[0] directly
	// beat enumerating bad input shapes elsewhere in this action's history.
	if !(v >= 0 && v <= 1) {
		return 0, fmt.Errorf("--min-kill-rate %q is out of range: must be between 0.0 and 1.0 inclusive", s)
	}
	return v, nil
}

// changedFiles lists paths, relative to root, that differ from baseRef. In a
// PR the diff is the natural bound on an audit: ~84 suite runs per file is a
// day's work across a repo and a normal CI job across a three-file change.
//
// Two things a plain `git diff <baseRef>` gets wrong for this caller:
//
//   - It is a THREE-DOT range (baseRef...HEAD), not two-dot. Two-dot compares
//     trees directly, so once baseRef has advanced past the branch point,
//     files changed only ON baseRef are reported as changed here too and get
//     audited — over-scoping, which is expensive (~84 suite runs per file),
//     not merely untidy. Three-dot compares against the merge base, which is
//     what "what this PR changed" means, and is the cheaper direction.
//   - `git diff --name-only` emits paths relative to the REPOSITORY root
//     regardless of cwd, while reposcan.Enumerate (and every candidate this
//     list is intersected against) produces paths relative to root. When root
//     is a subdirectory of the repo (a package inside a monorepo), the two
//     frames never intersect without --relative, which both restricts the
//     diff to root and reports paths relative to it — matching Enumerate's
//     frame exactly.
func changedFiles(root, baseRef string) ([]string, error) {
	// baseRef is not passed behind a `--` separator below, so a value
	// starting with `-` is a legal-looking git OPTION, not a ref —
	// `git check-ref-format 'refs/heads/-evil'` exits 0, so a branch
	// actually named that way is a ref an attacker fully controls (e.g. a
	// pull_request_target workflow passing `diff-base:
	// ${{ github.head_ref }}`, the PR author's own branch name). Something
	// like `--output=<path>` would make git WRITE to that path on the
	// runner instead of comparing anything. Refuse it outright rather than
	// ever handing it to git as a bare argument.
	if strings.HasPrefix(baseRef, "-") {
		return nil, fmt.Errorf("corral certify --repo: --diff-base %q looks like a git option, not a ref (it starts with '-'); refusing to pass it to git diff", baseRef)
	}
	rangeArg := baseRef + "...HEAD"
	// #nosec G204 -- fixed binary; baseRef is validated above to not start
	// with '-', so rangeArg cannot be mistaken for an option by git diff.
	cmd := exec.CommandContext(context.Background(), "git", "diff", "--name-only", "--relative", rangeArg)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// cmd.Output() alone discards git's own explanation, surfacing only
		// "exit status 128" — exec.ExitError.Stderr is already populated with
		// the actual reason (e.g. an unresolvable ref) and must not be thrown
		// away.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
				return nil, fmt.Errorf("corral certify --repo: git diff against %s: %w: %s", rangeArg, err, detail)
			}
		}
		return nil, fmt.Errorf("corral certify --repo: git diff against %s: %w", rangeArg, err)
	}
	var changed []string
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// deriverFactory builds a Deriver for a model. Injected so the goal-source
// wiring can be tested without a provider credential — and, more importantly,
// without any possibility of a real model call from a unit test.
type deriverFactory func(model string) (reposcan.Deriver, error)

// certifyRepoDeriver is the factory runCertifyRepo actually uses. A package
// var rather than a direct call to newLLMDeriver so a test can prove a
// refusal happens BEFORE derivation — "no model was called" is not something
// stdout can be asked about, and the only honest way to assert it is to make
// the construction observable.
var certifyRepoDeriver deriverFactory = newLLMDeriver

// goalCacheDisclosureLine formats the derived-goals disclosure once a scan
// knows how many goals it actually paid for versus served from the goal
// cache. base is resolveGoalSource's own disclosure line for the derived
// path — printed UNCHANGED whenever nothing was reused (reused == 0), so a
// scan that never hit the cache says exactly what corral has always said.
// Only once there is something to disclose beyond "a model derived these"
// does the line change shape, to name both numbers — and even then it must
// carry the SAME accountability clause base does ("no goal-critic; each
// goal is judged after the fact by mutant yield"), not drop it: a reused
// goal is still an unaudited machine claim, exactly as much as a freshly
// derived one, and a reader who only ever sees a scan with reused > 0 must
// not be told any less about how these goals are judged than a reader of a
// scan with reused == 0 is.
func goalCacheDisclosureLine(base, model, engineVersion string, fresh, reused int) string {
	if reused == 0 {
		return base
	}
	return fmt.Sprintf("  goals: %d derived by %s@%s, %d reused (identical source) — no goal-critic; each goal is judged after the fact by mutant yield", fresh, model, engineVersion, reused)
}

// goalReceiptLine is the receipt disclosed once per scan that actually PUT
// something into the goal cache. fresh > 0 with the cache wired means at
// least one derivation reached the store's Put half THIS scan (see
// CachingGoalSource.GoalFor); recordEnabled mirrors CachingGoalSource's own
// writable gate exactly (both are *recordFlag), so this line and an actual
// write always agree — a scan without --record derives (fresh can be > 0)
// but writes nothing, and this returns "" for it too. Split out of
// runCertifyRepo so the printing DECISION is testable without a ledger, a
// jail or a scan.
func goalReceiptLine(dsn string, recordEnabled bool, fresh int) string {
	if fresh <= 0 || !recordEnabled {
		return ""
	}
	return fmt.Sprintf("  goal receipts kept in %s (--no-goal-cache to skip)", dsn)
}

// resolveGoalSource picks where goals come from and returns the ONE line that
// discloses it. Split out of runCertifyRepo so both the choice and its
// disclosure are testable: on the derived path there is no goal-critic, so this
// line is the entire accountability mechanism for a machine-invented goal.
//
// An empty disclosure means there is nothing to disclose — a hand-written
// --goals map is the operator's own claim, and a scan that selected nothing
// will never ask for a goal at all.
//
// store, when non-nil (and noGoalCache false), wraps the derived path in a
// reposcan.CachingGoalSource: a goal derived from identical bytes, by the
// same model under the same prompt revision, is a fact reused rather than
// re-purchased. Never wired on the --goals path (that branch returns
// before store is ever consulted — see internal/reposcan/goal_cache_test.go's
// TestPinnedGoalsBypass doc) or when nothing was selected (no goal will ever
// be asked for).
//
// recordEnabled is *recordFlag, threaded through to the CachingGoalSource's
// own writable gate: Get always runs regardless of it (a fact an earlier,
// RECORDED scan wrote is still worth reading), but a scan run WITHOUT
// --record must not itself write model-derived text about the operator's
// source into the default ledger just because it happened to derive a
// goal — the same read-always/write-under---record rule the selection
// cache already follows (see NewCachingGoalSource's doc).
//
// Returns the process exit code to use on failure; 0 means the source is good.
func resolveGoalSource(stderr io.Writer, repoDir, goalsPath, deriveModel string, dryRun bool, nSelected int, newDeriver deriverFactory, store reposcan.GoalCacheStore, noGoalCache, recordEnabled bool) (reposcan.GoalSource, string, int) {
	// --goals takes precedence when given, so hand-written goals keep working
	// and that path needs no provider credential at all.
	if goalsPath != "" {
		gs, err := reposcan.NewFileGoalSource(goalsPath)
		if err != nil {
			fmt.Fprintf(stderr, "corral certify --repo: reading --goals %s: %v\n", goalsPath, err)
			return nil, "", 1
		}
		return gs, "", 0
	}
	if dryRun {
		// A dry run stops before any audit, so deriving a goal for each
		// selected file would spend real money to produce nothing. Report the
		// jobs the scan WOULD emit, with a goal plainly marked as not derived
		// — reporting them as `ungoaled` instead would be a claim about the
		// files, for a question that was never asked.
		return notDerivedGoals{}, "  dry run: goals were NOT derived (no model calls); jobs below are what the scan would emit", 0
	}
	if nSelected > 0 {
		// No default deriver model: --goals or --derive-model, pick one. This
		// refuses BEFORE the credential check below so the message names the
		// actual problem (no model chosen) rather than the symptom a missing
		// key would produce.
		if strings.TrimSpace(deriveModel) == "" {
			fmt.Fprintf(stderr, "corral certify --repo: no goal source. corral has no default models, so deriving a goal per file needs --derive-model <model>; or supply --goals <file> to skip derivation entirely (that path calls no model and needs no key)\n")
			return nil, "", 2
		}
		// Constructed only when there is something to derive FOR. It fails
		// closed on a missing credential, which is the right answer for a real
		// scan — but demanding a provider key to report "0 candidates" would
		// refuse a scan that was never going to call a model.
		d, derr := newDeriver(deriveModel)
		if derr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", derr)
			return nil, "", 2
		}
		var gs reposcan.GoalSource = reposcan.NewDerivingGoalSource(repoDir, d, deriveModel, version, 3)
		disclosure := fmt.Sprintf("  goals derived per file by %s@%s — no goal-critic; each goal is judged after the fact by mutant yield", deriveModel, version)
		// A goal derived from identical bytes, by the same model under the
		// same prompt revision, is a fact — reused with a receipt, not
		// re-derived. Not wired at all under --no-goal-cache or when the
		// caller has no store to give (an unopenable --record-db, or a scan
		// with the goal cache turned off some other way).
		if store != nil && !noGoalCache {
			gs = reposcan.NewCachingGoalSource(repoDir, gs, store, deriveModel, GoalPromptRev, recordEnabled)
		}
		return gs, disclosure, 0
	}
	// Nothing was selected, so no goal will ever be asked for. A real source
	// rather than a nil interface: EmitJobs returns early on an empty candidate
	// list today, and a nil GoalSource would be a trap the day that changes.
	return noGoals{}, "", 0
}

// checkArgvNoFlagCollision reports whether checkArgv (the tokens after
// "--", destined for the check command untouched) contains anything that
// looks like one of fs's OWN flags — spelled with a leading "-" or "--",
// optionally carrying "=value". splitCertifyArgs (shared with `certify
// --local`) splits on the first literal "--" with zero knowledge of which
// flags belong to this command, so a flag placed after "--" by mistake is
// silently swallowed into the check command's own argv instead of being
// parsed — see the call site for why that is dangerous specifically for
// --min-kill-rate (a silently-skipped merge gate).
//
// Flag names come from fs.VisitAll — every flag ever registered on fs,
// set or not — rather than a hardcoded list: a hardcoded list drifts the
// first time a new flag is added to runCertifyRepo, silently stops
// covering it, and reproduces exactly the bug class this check exists to
// close.
//
// A bare token with no leading dash (the overwhelming majority of real
// check-command arguments — "false", "-q"'s VALUE, a test path, "0.5")
// never matches: TrimLeft strips leading dashes, and a token that had
// none to begin with is left unchanged by it, which the equality check
// below catches and skips. A single-dash short flag some OTHER tool
// happens to define (pytest's "-q", "-x") only collides if its single
// letter happens to equal one of THIS command's flag names outright,
// which none of them do today (all are multi-letter) — so a real,
// unrelated check command is never falsely rejected.
func checkArgvNoFlagCollision(fs *flag.FlagSet, checkArgv []string) error {
	names := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { names[f.Name] = true })

	for _, tok := range checkArgv {
		trimmed := strings.TrimLeft(tok, "-")
		if trimmed == tok {
			continue // no leading dash at all: not flag-shaped
		}
		if trimmed == "" {
			continue // bare "--" or "-": not a named flag either
		}
		name := trimmed
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if names[name] {
			return fmt.Errorf(
				"the check command (after --) contains %q, which is one of this command's OWN flags — flags must be given BEFORE --, never after (splitCertifyArgs has no way to tell a real command-line argument from a misplaced flag). Move %q before -- ; if the check command genuinely needs a colliding token, wrap it in a script and pass the script as the check command instead",
				tok, tok)
		}
	}
	return nil
}

// flagWasSet reports whether the operator passed a flag explicitly, as opposed
// to inheriting its default. flag.FlagSet has no accessor for this; Visit
// walks only the flags actually set.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// noGoals supplies no goal for anything. Used only when the scan selected
// zero candidates, so it can never silently ungoal a file an operator meant
// to audit.
type noGoals struct{}

func (noGoals) GoalFor(reposcan.Candidate) (reposcan.Goal, bool, error) {
	return reposcan.Goal{}, false, nil
}

// notDerivedGoals stands in for the deriver on a --dry-run with no --goals: it
// lets the scan show which jobs it would emit without making a model call. The
// goal text says so in plain words, and its provenance is not a model name, so
// a placeholder can never be mistaken for a derived goal if one of these jobs
// is ever printed or serialised. A dry run stops before Scan, so it is never
// audited against.
type notDerivedGoals struct{}

func (notDerivedGoals) GoalFor(reposcan.Candidate) (reposcan.Goal, bool, error) {
	return reposcan.Goal{
		Text:       "(not derived — dry run)",
		Provenance: "not-derived:dry-run",
	}, true, nil
}

// checkArgvSpansOneLanguage fails closed when the operator gave an explicit
// test command and the job set is multi-language. One command cannot grade two
// languages: the wrong-language jobs would run a check that never observes the
// mutation, so every mutant "survives" and a 0.00 kill rate is reported as if
// it had been measured. Refusing is the honest answer — the operator can
// re-run per language, or drop `--` and let each job use its plugin's stock
// command.
func checkArgvSpansOneLanguage(checkArgv []string, jobs []reposcan.Job) error {
	if len(checkArgv) == 0 || len(jobs) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		seen[j.Lang] = true
	}
	if len(seen) < 2 {
		return nil
	}
	langs := make([]string, 0, len(seen))
	for l := range seen {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return fmt.Errorf(
		"an explicit test command after `--` grades every file, but this scan spans %d languages (%s).\n"+
			"  One command cannot grade them all: the wrong-language files would report a 0.00 kill rate that was never measured.\n"+
			"  Either drop `--` (each file is then graded with its own language's stock test command), or scan one language at a time",
		len(langs), strings.Join(langs, ", "))
}

// preflightMaxOutput caps the coverage pre-flight's own instrumented suite
// run at 8 MiB of combined stdout+stderr — overriding sandbox.Run's own
// 16 KiB default (see adequacy.WithMaxOutput) via a jail/enumerator built
// specifically for this one call, never for the scan's ordinary mutant runs
// — and, on the workspace substrate, via adequacy.WithWorkspaceMaxOutput,
// which did not exist until the review round below and left that substrate
// completely unbounded until then (see the F1 note there).
//
// A real `coverage json` report was measured at 467 KB against a real
// project (pallets/flask): at the 16 KiB default every real run truncates
// before ParseCoverage ever sees valid JSON, so the pre-flight could not
// succeed on any non-toy Python repository. 8 MiB is comfortably generous
// headroom above that.
//
// For Go this cap is sized against the REDUCED profile
// (goCoverageReduceScript in internal/lang/go.go), not the raw one
// -coverpkg=./... produces. The raw profile is ~quadratic in package count
// (measured up to 253 MB on grpc-go — see go.go's CoverageCmd doc comment)
// and 8 MiB would not have been "generous headroom" for it at all; it would
// have made the pre-flight fail closed (truncated, Ran=false) on any Go
// repo above roughly a few dozen packages, including corral's own tree.
// The reduction collapses that to one line per file BEFORE this cap ever
// sees it — corral's own 53 MB raw profile reduces to a few KB — so 8 MiB
// stays generous for Go too, for the profile this cap actually measures.
const preflightMaxOutput = 8 << 20

// preflightTimeout bounds the ONE instrumented suite run --preflight
// performs. Coverage instrumentation adds real overhead over a plain test
// run, so this is deliberately looser than the jail's own 60s
// zero-value default (see sandbox.Run) that the scan's ordinary per-mutant
// runs fall back to today.
const preflightTimeout = 5 * time.Minute

// selectionMaxOutput and selectionTimeout bound the ONE instrumented run
// selection evidence comes from — separately from the coverage pre-flight's
// bounds, which were sized for a payload Go reduces to a few KB before the
// cap ever sees it. A truncated evidence document is unparseable evidence,
// and the whole scan then grades whole-suite — disclosed, but the exact
// silent-degradation shape this feature exists to avoid.
//
// The Python reducer runs inside the instrumented shell and emits, per
// file, the node ids that executed it plus each test's line ranges and the
// import-time ranges (corral-selection-2), measured 2026-08-30: flask
// 1,331,508 bytes, requests 1,053,331 bytes (the unreduced `coverage json
// --show-contexts` for the same flask run was 411 MB — see
// docs/design/test-selection.md). 64 MiB is ~50× that; 15 min is ~11×
// requests' 79 s suite. Generous on purpose: over-sizing
// costs bounded memory on one run, under-sizing costs a scan that silently
// grades a different question.
const selectionMaxOutput = 64 << 20

// selectionTimeout: see selectionMaxOutput.
const selectionTimeout = 15 * time.Minute

// coverageRunner is the minimal seam runPreflight needs to hand to
// reposcan.Preflight — satisfied structurally by both *adequacy.WorkspaceRunner
// and adequacy.Enumerator (bwrapJail), matching reposcan's own unexported
// commandRunner exactly so either concrete substrate runner can be passed
// straight through without reposcan and cmd/corral needing to share a type.
type coverageRunner interface {
	Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error)
}

// selectPreflightLanguage picks which language runPreflight instruments
// when a scan's candidates span more than one — called only in that case;
// the single-language case never reaches this function.
//
// With no explicit `-- <cmd>` there is no principled way to pick a stock
// TestCmd() across multiple languages, so this always declines (langName
// == "").
//
// With an explicit checkArgv, the operator's own command IS the
// disambiguator, PROVIDED exactly one candidate language's
// lang.CoverageReporter accepts it: try every candidate language's
// CoverageCmd(checkArgv) (skipping any that doesn't implement
// CoverageReporter at all, e.g. Ruby/JS/TS today — see internal/lang) and
// count how many say yes. Exactly one match is unambiguous — this is what
// makes andrewyng/aisuite (python + typescript candidates, `-- pytest -q`)
// resolvable: typescript has no CoverageReporter, so python is the only
// candidate at all, not merely the most likely one. Zero or more than one
// match keeps the original blanket refusal: Go's CoverageCmd accepts ANY
// non-empty argv by design (see go.go — it wraps whatever it's given
// without inspecting shape), so a genuinely mixed python+go scan given `--
// pytest -q` still declines rather than guess which language the operator
// meant, exactly as before this function existed.
func selectPreflightLanguage(langs map[string]bool, checkArgv []string) (langName string, note string) {
	names := make([]string, 0, len(langs))
	for n := range langs {
		names = append(names, n)
	}
	sort.Strings(names)

	if len(checkArgv) > 0 {
		var matches []string
		for _, n := range names {
			plug, ok := lang.ByName(n)
			if !ok {
				continue
			}
			reporter, ok := plug.(lang.CoverageReporter)
			if !ok {
				continue // no CoverageReporter for this language at all — never a match
			}
			if _, ok := reporter.CoverageCmd(checkArgv); ok {
				matches = append(matches, n)
			}
		}
		if len(matches) == 1 {
			return matches[0], ""
		}
		if len(matches) > 1 {
			return "", fmt.Sprintf(
				"preflight: scan spans %d languages (%s) and the `--` test command matches more than one of them's coverage instrumentation (%s) — ambiguous, skipped",
				len(names), strings.Join(names, ", "), strings.Join(matches, ", "))
		}
		// len(matches) == 0: none of the candidate languages' coverage
		// instrumentation recognized the operator's own command either —
		// falls through to the same refusal the no-checkArgv case returns.
	}
	return "", fmt.Sprintf(
		"preflight: scan spans %d languages (%s) — one instrumented suite run cannot cover more than one, skipped",
		len(names), strings.Join(names, ", "))
}

// preflightLanguages derives the language set the pre-flight may instrument
// from the ENUMERATED SOURCE SET — every language-detected non-test file
// reposcan.Enumerate walked — not from the candidate set.
//
// That distinction is the whole feature. Candidates are what corral's
// test-PAIRING heuristic matched, and the pre-flight's stated justification
// (README, docs/corral/github-action.md) is that pairing finds nothing in
// repos that don't name tests after source files. Deriving the language set
// from candidates made the pre-flight decline with "no candidates to
// instrument" on exactly those repos — it inherited the limitation it was
// built to route around. Measured before this fix: python-jsonschema/
// jsonschema (0 candidates, 31 Python files excluded no-paired-test),
// tox-dev/filelock (0/35), pallets/itsdangerous (0/10), pallets/markupsafe
// (0/7) — four repos where the feature could not run at all.
//
// enumeratedSourcePaths is the same slice printPreflightReport buckets
// against, so the languages instrumented and the files reported on are
// derived from one set, not two that can drift apart.
func preflightLanguages(sources []string) map[string]bool {
	langs := map[string]bool{}
	for _, p := range sources {
		if plug, ok := lang.Detect(p); ok {
			langs[plug.Name()] = true
		}
	}
	return langs
}

// scanRunner builds the substrate runner ONE whole-suite instrumented run
// uses (the coverage pre-flight, and selection evidence), with the files
// it needs seeded. Shared so the two runs cannot drift in how they reach
// the suite.
// maxOutput and timeout are the CALLER's own bounds: the coverage pre-flight
// and the selection evidence run read payloads that differ by more than an
// order of magnitude (see selectionMaxOutput), and a single shared pair would
// have to be the looser of the two for both — spending the pre-flight's
// memory ceiling on a payload it never produces.
func (l *localExecutor) scanRunner(langName string, plug lang.Plugin, maxOutput int, timeout time.Duration) (runner coverageRunner, files map[string]string, err error) {
	if l.substrate == substrateWorkspace {
		// The real checkout IS the workspace, and the command runs with
		// cwd == l.repoDir == reposcan.Enumerate's own root — coverage.py
		// already reports paths relative to that (see python.go's
		// ParseCoverage doc comment), so there is nothing to relativize.
		//
		// WithWorkspaceMaxOutput mirrors the jail branch's WithMaxOutput
		// below — a WorkspaceRunner built SPECIFICALLY for this one call,
		// never reused for RunTest's ordinary per-mutant runs (which stay
		// on this substrate's original unbounded bytes.Buffer). Without
		// this, the workspace substrate had no cap at all on the
		// instrumented profile it reads back — measured against grpc-go: a
		// 253 MB profile read entirely into memory (827 MB peak RSS).
		// WithPerRunEnv: this is a single one-off instrumented run, not a
		// baseline/mutant loop, but it is still the SAME real checkout the
		// workspace substrate's ordinary scoring runs mutate — a Python
		// repo whose developer left a __pycache__ behind (or a run
		// squeezed into the same wall-clock second as an earlier
		// preflight call on a re-run) is exposed to the identical stale-
		// bytecode read, just against coverage output instead of a
		// kill/survive verdict. See lang.Plugin.WorkspaceRunEnv's doc
		// comment.
		runner = adequacy.NewWorkspaceRunner(l.repoDir, timeout,
			adequacy.WithWorkspaceMaxOutput(maxOutput),
			adequacy.WithPerRunEnv(plug.WorkspaceRunEnv))
		files = map[string]string{}
	} else {
		if l.jailErr != nil {
			return nil, nil, l.jailErr
		}
		if l.seeds == nil {
			return nil, nil, errors.New("no repo seed available")
		}
		seed, serr := l.seeds.get(langName)
		if serr != nil {
			return nil, nil, fmt.Errorf("jail preparation failed for %s: %w", langName, serr)
		}
		// A jail/enumerator built SPECIFICALLY for this one call — never
		// reused for the scan's ordinary per-mutant runs, which must keep
		// sandbox.Run's stock 16 KiB default (see preflightMaxOutput).
		runner = adequacy.NewEnumerator(l.iso, timeout,
			adequacy.WithReadOnlyBinds(seed.binds), adequacy.WithMaxOutput(maxOutput))
		files = seed.files
		// Same reasoning as the workspace branch: cmd.Dir is the jail's own
		// ephemeral workspace root, which IS the seeded repo root, so
		// coverage.py's paths are already relative to it.
	}
	return runner, files, nil
}

// runPreflight runs the coverage pre-flight ONCE for the whole scan (never
// once per audited file) over every ENUMERATED SOURCE FILE in the repo,
// independent of --top/--diff-base AND of whether test-pairing made any of
// them a candidate: it answers "what does the suite ever touch in this
// repo", not "what did this particular scan choose to audit".
//
// One instrumented command can only speak one language. When every
// candidate resolved to the same language plugin, that is the obvious
// choice. When candidates span more than one language, this DECLINES BY
// DEFAULT (a repo with no candidates also declines) — but not
// unconditionally: see selectPreflightLanguage, which resolves the
// multi-language case when the operator's own `-- <cmd>` unambiguously
// names one of them (andrewyng/aisuite — python + typescript candidates —
// is the repo this distinction exists for: `-- pytest -q` is not
// ambiguous just because the repo also has files in a language nothing
// here can instrument). A file belonging to a language this run never
// instruments is not silently accused either way: it is simply ABSENT
// from the resulting CoverageMap.Executed (never measured), which
// splitPreflightFindings reports as a count, never a name — see Part 1's
// tri-state contract on lang.CoverageReporter.ParseCoverage.
func (l *localExecutor) runPreflight(ctx context.Context, sources []string) reposcan.CoverageMap {
	langs := preflightLanguages(sources)
	if len(langs) == 0 {
		return reposcan.CoverageMap{Note: "preflight: no source files to instrument"}
	}

	var langName string
	if len(langs) == 1 {
		for n := range langs {
			langName = n
		}
	} else {
		var note string
		langName, note = selectPreflightLanguage(langs, l.checkArgv)
		if langName == "" {
			return reposcan.CoverageMap{Note: note}
		}
	}

	plug, ok := lang.ByName(langName)
	if !ok {
		return reposcan.CoverageMap{Note: fmt.Sprintf("preflight: unknown language %q", langName)}
	}

	testCmd := l.checkArgv
	if len(testCmd) == 0 {
		testCmd = plug.TestCmd()
	}

	runner, files, rerr := l.scanRunner(langName, plug, preflightMaxOutput, preflightTimeout)
	if rerr != nil {
		return reposcan.CoverageMap{Note: fmt.Sprintf("preflight: %v", rerr)}
	}

	var repoRoot string
	if langName == "go" {
		// Go's coverage profile paths are import paths, not filesystem
		// paths, on EITHER substrate — always need the module prefix to
		// strip it back to a repo-relative path (see go.go's ParseCoverage).
		//
		// Fail-closed on a go.mod this cannot parse: without the prefix
		// nothing aligns and the report would be a confident
		// `0 executed, 0 findings, N not measured` — the silent empty result
		// (see goModulePath).
		mp, merr := goModulePath(l.repoDir)
		if merr != nil {
			return reposcan.CoverageMap{Note: fmt.Sprintf("preflight: cannot determine the Go module path, so no coverage path could be resolved: %v", merr)}
		}
		repoRoot = mp
	}

	return reposcan.Preflight(ctx, runner, files, plug, testCmd, repoRoot)
}

// pendingSelectionCachePut is a MISS's raw evidence, held on the executor
// between collectSelection (which has the bytes but no scan id yet) and the
// recording sequence (which has the id but not the bytes) — see
// localExecutor.pendingSelectionPut's own doc for why the Put cannot happen
// at collection time.
type pendingSelectionCachePut struct {
	TreeDigest, CmdDigest, Plugin, Substrate string
	Raw                                      []byte
}

// resolveSelectionPlugin is the language/plugin/testCmd resolution shared by
// collectSelection and selectionCachePeek: both need to know WHICH
// instrumented command a scan would run before either running it or asking
// the cache about it, and repeating this logic in two places (rather than
// factoring it once) is how they would drift about which plugin a mixed
// repo resolves to. note is non-empty only when plug is nil — the reason no
// plugin could be resolved, in the exact wording collectSelection has always
// returned as a SelectionEvidence.Note.
func (l *localExecutor) resolveSelectionPlugin(sources []string) (plug lang.Plugin, langName string, testCmd []string, note string) {
	langs := preflightLanguages(sources)
	if len(langs) == 0 {
		return nil, "", nil, "no source files"
	}
	if len(langs) == 1 {
		for n := range langs {
			langName = n
		}
	} else {
		if langName, note = selectPreflightLanguage(langs, l.checkArgv); langName == "" {
			return nil, "", nil, note
		}
	}
	p, ok := lang.ByName(langName)
	if !ok {
		return nil, "", nil, fmt.Sprintf("unknown language %q", langName)
	}
	tc := l.checkArgv
	if len(tc) == 0 {
		tc = p.TestCmd()
	}
	return p, langName, tc, ""
}

// selectionCacheKey computes the (tree_digest, cmd_digest) pair collectSelection
// and selectionCachePeek both key the selection cache on for plug/testCmd,
// through the SAME instrumentation Instrument would apply before the suite
// actually ran — the instrumentation flags are part of what produced the
// evidence, not the operator's bare testCmd (see reposcan.TreeDigest's own
// doc on the universe half of this key). The substrate the scan is running
// on (l.substrate) is the CALLER'S half of the key — see
// localExecutor.selectionCache's own doc — not computed here, because it is
// already a field, not something this method has to derive.
//
// ok is false whenever no key can be computed at all: plug has no
// TestSelector, Instrument refuses testCmd, or TreeDigest could not name a
// tree (outside a git work tree, or a git failure) — every one of those
// means "this scan cannot be cache-keyed", never a value worth caching
// against.
func (l *localExecutor) selectionCacheKey(plug lang.Plugin, testCmd []string) (treeDigest, cmdDigest string, ok bool) {
	sel, selOK := plug.(lang.TestSelector)
	if !selOK {
		return "", "", false
	}
	cmd, cmdOK := sel.Instrument(testCmd)
	if !cmdOK {
		return "", "", false
	}
	td, ok := l.treeDigestOnce()
	if !ok {
		return "", "", false
	}
	return td, selectionCmdDigest(cmd), true
}

// treeDigestOnce is reposcan.TreeDigest(l.repoDir), memoized on the
// executor: selectionCachePeek and collectSelection both need it for the
// SAME scan over the SAME checkout (a git ls-files plus a hash per file,
// cheap but not free on a large repo), and the tree cannot change under a
// scan that only ever reads it — a scan does not mutate the checkout it is
// auditing. Computed once, cached for every later call this scan makes.
// ok=false (an unrepresentable tree — outside a git work tree, or a git
// failure) is cached too: a repeat call is not going to grow a git work
// tree that was not there a moment ago.
func (l *localExecutor) treeDigestOnce() (digest string, ok bool) {
	if l.treeDigestComputed {
		return l.treeDigest, l.treeDigest != ""
	}
	td, err := reposcan.TreeDigest(l.repoDir)
	l.treeDigestComputed = true
	if err != nil {
		l.treeDigest = ""
		return "", false
	}
	l.treeDigest = td
	return td, td != ""
}

// selectionCachePeek previews whether collectSelection is about to serve a
// cache HIT, without consuming anything — it exists only so the caller can
// choose the right announce line BEFORE the (possibly minutes-long)
// instrumented run: "selection: reused …" instead of "running the suite
// once…" for a hit neither one of which has happened yet at print time.
// collectSelection performs the identical lookup moments later; both calls
// are cheap (a git ls-files plus one ledger read), and duplicating them is
// far simpler than threading state between two calls that would otherwise
// have to agree by construction.
func (l *localExecutor) selectionCachePeek(sources []string) (scanID int64, ok bool) {
	if l.wholeSuite || l.selectionCache == nil {
		return 0, false
	}
	plug, _, testCmd, _ := l.resolveSelectionPlugin(sources)
	if plug == nil {
		return 0, false
	}
	treeDigest, cmdDigest, keyOK := l.selectionCacheKey(plug, testCmd)
	if !keyOK {
		return 0, false
	}
	_, id, hit, err := l.selectionCache.SelectionCacheGet(context.Background(), treeDigest, cmdDigest, plug.Name(), l.substrate)
	if err != nil || !hit {
		return 0, false
	}
	return id, true
}

// collectSelection runs the selector's instrumented command once for the
// scan's language, unless --whole-suite OR a prior scan already ran the
// IDENTICAL instrumented command over a byte-identical tree (see
// selectionCacheKey) — in which case that scan's raw evidence is reused
// verbatim and l.selectionReusedFrom names which scan. Any failure is a
// Note, never fatal: the scan still has a real measurement to make.
func (l *localExecutor) collectSelection(ctx context.Context, sources []string) reposcan.SelectionEvidence {
	if l.wholeSuite {
		return reposcan.SelectionEvidence{Note: "--whole-suite"}
	}
	plug, langName, testCmd, note := l.resolveSelectionPlugin(sources)
	if plug == nil {
		return reposcan.SelectionEvidence{Note: note}
	}

	var treeDigest, cmdDigest string
	var keyOK bool
	if l.selectionCache != nil {
		treeDigest, cmdDigest, keyOK = l.selectionCacheKey(plug, testCmd)
		if keyOK {
			if raw, scanID, hit, err := l.selectionCache.SelectionCacheGet(ctx, treeDigest, cmdDigest, langName, l.substrate); err == nil && hit {
				id := scanID
				l.selectionReusedFrom = &id
				return reposcan.SelectionEvidence{Raw: raw, Ran: true}
			}
		}
	}

	runner, files, err := l.scanRunner(langName, plug, selectionMaxOutput, selectionTimeout)
	if err != nil {
		return reposcan.SelectionEvidence{Note: fmt.Sprintf("selection: %v", err)}
	}
	// THE INSTRUMENTED RUN, and the only statement in this function that runs
	// the project's suite. The clock is here, and only here: every return
	// above is a decision, not a run, and must record no time.
	start := time.Now()
	ev := collectSelectionEvidence(ctx, runner, files, plug, testCmd)
	// Recorded even when the run produced no usable evidence: the suite still
	// executed, and the minutes it burned are part of what this scan cost.
	l.selectionDuration = time.Since(start)
	// Held for the recording sequence to Put once this scan has a ledger id
	// (see pendingSelectionPut's own doc) — only for a genuine run that
	// produced usable evidence, keyed under a cache this scan could resolve.
	if ev.Ran && l.selectionCache != nil && keyOK {
		l.pendingSelectionPut = &pendingSelectionCachePut{TreeDigest: treeDigest, CmdDigest: cmdDigest, Plugin: langName, Substrate: l.substrate, Raw: ev.Raw}
	}
	return ev
}

// collectSelectionEvidence is reposcan.CollectSelectionEvidence behind a
// package var, the same seam newWorkspacePool/probeWorkspacePool use: it is
// the one call in collectSelection that runs the project's whole suite, and a
// test of what is TIMED must be able to stand in for it without one.
var collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string) reposcan.SelectionEvidence {
	return reposcan.CollectSelectionEvidence(ctx, runner, files, p, testCmd)
}

// selectionCmdDigest is the sha256 of the EXACT instrumented command argv —
// the plugin's Instrument output, not the operator's bare testCmd, because
// the instrumentation flags (a coverage-context plugin, a per-test report
// format) are part of what produced the evidence: two different flag sets
// against the same tree can measure two different things. Length-prefixed
// per argument, the same discipline reposcan.KeyInputs.CacheKey follows, so
// no two different argv slices can fold to the same digest by concatenation.
func selectionCmdDigest(cmd []string) string {
	h := sha256.New()
	for _, a := range cmd {
		fmt.Fprintf(h, "%d:%s|", len(a), a)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// goModulePath reads the `module` directive out of repoDir's go.mod, for
// stripping the import-path prefix goPlugin.ParseCoverage needs.
//
// It returns an ERROR rather than "" when it cannot find one, and that is
// the point of the signature. Failing to parse the module line is not a
// cosmetic miss: without the prefix, no profile path aligns with any
// repo-relative source path, so every Go file falls out of
// CoverageMap.Executed and the report reads `0 executed, 0 findings, N not
// measured` — confident, empty, and indistinguishable from a genuinely
// uninstrumented repo. A silent empty result is the one outcome this
// feature's whole tri-state contract exists to prevent, so the caller
// reports the failure instead (see runPreflight).
//
// Both legal spellings the earlier prefix-cut mis-parsed are handled: a
// quoted path (`module "example.com/x"`, and its backquoted variant) and a
// trailing `//` comment (`module example.com/x // v2`), plus tab separation.
func goModulePath(repoDir string) (string, error) {
	p := filepath.Join(repoDir, "go.mod")
	f, err := os.Open(p) // #nosec G304,G703 -- repoDir is the operator's own --repo
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		rest, ok := strings.CutPrefix(line, "module")
		if !ok {
			continue
		}
		// "module" must be its own token: `moduleX ...` is not the
		// directive, while `module\tpath` is.
		if rest != "" && !strings.ContainsAny(rest[:1], " \t") {
			continue
		}
		// A `//` comment may trail the directive; everything after it is
		// commentary, not part of the path.
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = rest[:i]
		}
		rest = strings.TrimSpace(rest)
		// go.mod tokens may be quoted (") or raw-quoted (`).
		if len(rest) >= 2 {
			if (rest[0] == '"' && rest[len(rest)-1] == '"') || (rest[0] == '`' && rest[len(rest)-1] == '`') {
				rest = rest[1 : len(rest)-1]
			}
		}
		if rest == "" {
			return "", fmt.Errorf("%s has a module directive with no path", p)
		}
		return rest, nil
	}
	if serr := scanner.Err(); serr != nil {
		return "", fmt.Errorf("reading %s: %w", p, serr)
	}
	return "", fmt.Errorf("%s has no module directive", p)
}

// enumeratedSourcePaths reconstructs "every file reposcan.Enumerate treated
// as source" — language-detected, not a test file — regardless of whether it
// ended up a candidate. cands alone is a narrower set (Enumerate additionally
// demoted an ambiguous pairing, or found no paired test, into an exclusion),
// so this adds back the two Enumerate-level reasons that still describe a
// language-detected, non-test file: ReasonNoPairedTest and
// ReasonAmbiguousTest. enumOnlyExcl MUST be Enumerate's own exclusions only
// (excl[:enumExcl] in runCertifyRepo) — later-appended reasons
// (not-selected/ungoaled/derive-failed/source-too-large) describe candidates
// already counted via cands, and re-detecting their language here would be
// redundant, not wrong, but the caller passes the narrower slice anyway to
// keep the two exclusion universes (Enumerate-level vs candidate-level) from
// being conflated the same way the totalFiles accounting above avoids it.
func enumeratedSourcePaths(cands []reposcan.Candidate, enumOnlyExcl []reposcan.Exclusion) []string {
	seen := make(map[string]bool, len(cands)+len(enumOnlyExcl))
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range cands {
		add(c.Path)
	}
	for _, e := range enumOnlyExcl {
		if e.Reason != reposcan.ReasonNoPairedTest && e.Reason != reposcan.ReasonAmbiguousTest {
			continue
		}
		if _, ok := lang.Detect(e.Path); ok {
			add(e.Path)
		}
	}
	return out
}

// preflightFindings splits the enumerated source set into the three buckets
// --preflight reports, using CoverageMap's tri-state Executed map: present
// true is bucket 1, present false is bucket 2 (the actual finding — a file
// the run measured and never touched), and absent is bucket 3, reported only
// as a COUNT, never by name — naming an unmeasured file would be an
// accusation about a file the instrumented run never even looked at (e.g.
// coverage.py's own [tool.coverage.run] source=[...] scoping).
type preflightFindings struct {
	executed    int
	unexercised []string // measured, never executed — the real finding, sorted
	notMeasured int
}

func splitPreflightFindings(sourceFiles []string, cm reposcan.CoverageMap) preflightFindings {
	var f preflightFindings
	for _, p := range sourceFiles {
		v, measured := cm.Executed[p]
		switch {
		case !measured:
			f.notMeasured++
		case v:
			f.executed++
		default:
			f.unexercised = append(f.unexercised, p)
		}
	}
	sort.Strings(f.unexercised)
	return f
}

// printPreflightReport prints the --preflight section: a separate inventory
// alongside the audit report, never folded into Excluded/Ungradable/the
// audited fraction (see the brief). When the pre-flight could not run
// (Ran == false), only Note is printed and NO file list follows — there is
// nothing to report a finding about.
func printPreflightReport(w io.Writer, cm reposcan.CoverageMap, sourceFiles []string) {
	fmt.Fprintln(w, "\nCoverage pre-flight (coverage-derived evidence from one instrumented suite run, not proof):")
	if !cm.Ran {
		fmt.Fprintf(w, "  could not run: %s\n", cm.Note)
		return
	}
	f := splitPreflightFindings(sourceFiles, cm)
	fmt.Fprintf(w, "  %d file(s) executed at least once\n", f.executed)
	if f.notMeasured > 0 {
		fmt.Fprintf(w, "  %d file(s) not measured by this run (never observed by the instrumentation — includes files outside its scope and files with nothing to measure — not a finding)\n", f.notMeasured)
	}
	if len(f.unexercised) == 0 {
		fmt.Fprintln(w, "  0 file(s) measured and never executed by the suite")
		return
	}
	fmt.Fprintf(w, "  %d file(s) measured and NEVER executed by the suite:\n", len(f.unexercised))
	for _, p := range f.unexercised {
		fmt.Fprintf(w, "    %s\n", p)
	}
}

// repoScanExitCode is the scan's automated signal. A scan that measured
// NOTHING is not a passing scan: exiting 0 would read as green in CI for a
// repo where every single file failed to grade — the exact false-green the
// COULD-NOT-GRADE line prevents for a human reader, left unfixed for the
// automated one. Split out as a function so both branches are testable
// without a jail and an API key.
//
// nothingInScope distinguishes the two ways a scan can audit zero files, a
// distinction that matters once --diff-base exists: the most common PR in
// existence (docs-only, or touching only files with no paired test)
// legitimately has nothing in scope, and that is a true, honest answer — not
// a failure to report. Zero GRADABLE out of a NON-empty scope is still a
// real failure: files were in scope and none could be graded. Callers pass
// true only on the diff path, and only when the diff selected zero
// candidates; the whole-repo (non-diff) path always passes false, so its
// exit codes are unchanged.
//
// minKillRate is nil unless the operator opted in with --min-kill-rate: a
// default threshold here would break every existing caller of this shipped
// command. When set, it is checked PER FILE against r.Weakest (which holds
// every audited file, not a truncated worst-N list — see report.go's
// Aggregate) rather than against the aggregate r.KillRate: an aggregate lets
// a well-tested file mask an untested one, which is precisely the
// substitution this product exists to refuse.
//
// Ordering is deliberate and load-bearing: nothingInScope and Audited == 0
// are decided FIRST, unconditionally. r.KillRate (and every per-file rate
// backing it) is only meaningful once at least one file was actually
// audited; RepoReport.KillRate is NaN when Audited == 0 and every comparison
// against NaN is false, so a threshold check reached in that state would
// silently never fire — checking Audited == 0 first, and returning early,
// is what keeps that failure from being maskable by (or masking) a breach.
func repoScanExitCode(r reposcan.RepoReport, nothingInScope bool, minKillRate *float64, maxProvenMissed *int) int {
	if nothingInScope {
		return 0
	}
	if r.Audited == 0 {
		return 1
	}
	// A scan whose only graded files never actually finished the pool —
	// every one hit its wall-clock deadline before the test-writer/critic
	// ran (advpool.Verdict.TimedOut) — must not read as a passing gate.
	// The dev-adequacy MEASUREMENT is real and stays in the report
	// (Audited > 0, a real KillRate), but corral's own adversarial
	// verification never ran to completion for ANY file this scan touched,
	// so there is nothing here for a merge gate to certify. Exiting 0 here
	// would be the silent-no-gate class this scan already closes three
	// other ways, arriving by a fourth route: a measurement banked, but
	// never gated, reading as "pass".
	if r.Audited > 0 && r.TimedOut == r.Audited {
		return 1
	}
	if minKillRate != nil {
		for _, f := range r.Weakest {
			// An uncovered file fails the gate BEFORE the rate is consulted.
			// Its rate is withheld (nothing executes the file, so nothing
			// graded it), and a withheld number must never satisfy a
			// threshold: a file no test touches is the worst case the gate
			// exists to catch, not a pass on an unmeasured 0.
			if f.Uncovered {
				return 1
			}
			if f.KillRate < *minKillRate {
				return 1
			}
		}
	}
	if maxProvenMissed != nil {
		for _, f := range r.Weakest {
			if f.ProvenMissed > *maxProvenMissed {
				return 1
			}
			// A zero here is only trustworthy when the pool actually got to
			// try. With survivors present and the writer having failed — or
			// having produced a test that never genuinely graded — ProvenMissed
			// reads 0 because nothing proved anything, not because the suite is
			// clean. Failing closed is the only honest reading: a gate that
			// passes on an unmeasured question is the failure this tool exists
			// to find in other people's pipelines.
			if f.Survivors > 0 && (f.TestWriterFailed || f.PoolTestUnsound) {
				return 1
			}
		}
	}
	return 0
}

// maxListedExclusions caps the per-file exclusion list. A real repo excludes
// hundreds of files (every test file, every .md), and a wall of them buries
// the report — but accounting must not be lost to tidiness, so the tally by
// reason above it is COMPLETE and the cap announces exactly how many lines it
// withheld. Nothing is silently dropped; the counts still add up.
const maxListedExclusions = 20

// printSearchPairings discloses every candidate whose test pairing came from
// the recursive fallback (reposcan.Candidate.ViaSearch) rather than from the
// language plugin's own naming convention — a test that EXISTS but that no
// TestPaths candidate predicted. Silent otherwise: the vast majority of a
// scan's candidates pair by convention, and printing a line per file there
// would just repeat what the "%d candidate(s)" count already says.
func printSearchPairings(w io.Writer, cands []reposcan.Candidate) {
	var viaSearch []reposcan.Candidate
	for _, c := range cands {
		if c.ViaSearch {
			viaSearch = append(viaSearch, c)
		}
	}
	for i, c := range viaSearch {
		if i == maxListedExclusions {
			fmt.Fprintf(w, "    ... and %d more paired by search\n", len(viaSearch)-maxListedExclusions)
			break
		}
		fmt.Fprintf(w, "    %s paired by search: %s\n", c.Path, c.TestPath)
	}
}

func printExclusions(w io.Writer, excl []reposcan.Exclusion) {
	if len(excl) == 0 {
		return
	}
	byReason := map[string]int{}
	for _, e := range excl {
		byReason[e.Reason]++
	}
	reasons := make([]string, 0, len(byReason))
	for r := range byReason {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Fprintf(w, "    %d %s\n", byReason[r], r)
	}
	for i, e := range orderExclusionsForListing(excl) {
		if i == maxListedExclusions {
			fmt.Fprintf(w, "    ... and %d more excluded file(s)\n", len(excl)-maxListedExclusions)
			break
		}
		fmt.Fprintf(w, "    excluded %s (%s)\n", e.Path, e.Reason)
	}
}

// candidateLevelReasons name exclusions that describe a CANDIDATE — a file the
// scan judged auditable and then did not audit. Enumerate's reasons
// (no-language / is-test / no-paired-test) describe files that were never
// candidates at all.
var candidateLevelReasons = map[string]bool{
	reposcan.ReasonNotSelected:    true,
	reposcan.ReasonUngoaled:       true,
	reposcan.ReasonDeriveFailed:   true,
	reposcan.ReasonSourceTooLarge: true,
}

// orderExclusionsForListing puts candidate-level exclusions ahead of
// enumerate-level ones, preserving order within each group.
//
// This is presentation only — the tally by reason above the listing is complete
// either way. It matters because the cap is 20 lines and enumerate's exclusions
// come first by construction: a dogfood run of this repo spent all 20 lines on
// `no-language` noise and named not one of the 189 files that fell outside the
// bound. For a BOUNDED scan, which candidates were left out is the interesting
// question; that every .md file has no language is not.
func orderExclusionsForListing(excl []reposcan.Exclusion) []reposcan.Exclusion {
	out := make([]reposcan.Exclusion, 0, len(excl))
	for _, e := range excl {
		if candidateLevelReasons[e.Reason] {
			out = append(out, e)
		}
	}
	for _, e := range excl {
		if !candidateLevelReasons[e.Reason] {
			out = append(out, e)
		}
	}
	return out
}

// nonLineSpanRules are the rules under which a mutant did NOT get a narrowed
// command, in the order the breakdown prints them. Listed explicitly rather
// than derived by ranging the map: the order must be stable across runs, and
// "lines" — the case where the narrowing worked — is deliberately absent,
// because a breakdown that printed it would bury the qualifier in the number
// it qualifies.
var nonLineSpanRules = []string{lang.SpanRuleStatic, lang.SpanRuleUnreached, lang.SpanRuleFile}

// selectionRuleBreakdown renders the mutants that were NOT narrowed by their
// own lines: "; 4 static, 1 unreached, 2 file", or "" when every mutant was.
// A "coverage-lines" run whose mutants mostly ran the file's whole selection
// narrowed almost nothing, and the spread alone cannot say that — 3 to 41
// tests per mutant reads the same whether the 41s were measured or were the
// file's selection standing in for evidence nobody had.
func selectionRuleBreakdown(rules map[string]int) string {
	var parts []string
	for _, r := range nonLineSpanRules {
		if n := rules[r]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, r))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, ", ")
}

// printWeakFile prints one "weakest files" line, including the marker and
// the disambiguating proven-missed count — factored out so the truncation
// fallback (F4, below) renders a byte-identical line for a file that falls
// outside the top 10.
func printWeakFile(w io.Writer, f reposcan.WeakFile) {
	marker := ""
	switch {
	case f.Uncovered:
		// FIRST, and it withholds the rate below: the selection evidence
		// found NO test executing this file, so its kill rate measures
		// nothing. Printing "0.00" here would read as "your tests caught
		// nothing" — an accusation about a measurement that was never made,
		// when the real finding is that the file is untested outright.
		marker = "  [UNCOVERED — no test executes this file]"
	case f.TimedOut:
		marker = "  [TIMED OUT — pool did not converge]"
	case f.TestWriterFailed:
		marker = "  [WRITER FAILED — survivor(s) not proven-killed]"
	case f.PoolTestUnsound:
		marker = "  [TEST UNSOUND — authored test did not genuinely grade]"
	}
	// The explicit "N proven missed" count is printed ONLY when it is
	// trustworthy AND needed to disambiguate: TimedOut, TestWriterFailed and
	// PoolTestUnsound already carry their own marker explaining why
	// ProvenMissed reads 0 (it is not meaningful on any of those three), so
	// repeating "0 proven missed" next to a marker would be redundant at
	// best. A file with Survivors == 0 has nothing to prove — the bare
	// survivor count already says so. The remaining case — Survivors > 0, no
	// marker — is exactly the ambiguous one this whole field exists to
	// resolve: the writer ran, authored a compiling test that genuinely
	// graded, and either proved a real gap (ProvenMissed > 0, corral's
	// strongest claim) or proved nothing (0, a real "tried and missed"
	// result) — either way it must be printed, never left as a bare survivor
	// count that could be misread as silence on the question.
	detail := fmt.Sprintf("(%d survivor(s))", f.Survivors)
	if f.Survivors > 0 && !f.TestWriterFailed && !f.TimedOut && !f.PoolTestUnsound {
		detail = fmt.Sprintf("(%d survivor(s), %d proven missed)", f.Survivors, f.ProvenMissed)
	}
	rate := fmt.Sprintf("%.2f", f.KillRate)
	if f.Uncovered {
		rate = "withheld"
	}
	fmt.Fprintf(w, "    %s  %s %s%s", rate, f.Path, detail, marker)
	// Which measurement this line's number IS. Printed on EVERY line that
	// has one to name — selected, uncovered, or whole-suite-with-a-reason —
	// not only the interesting ones: a report where the selected files say so
	// and the others say nothing leaves the reader to infer the mode from an
	// absence, which is exactly how two different questions get read as one
	// number. A row carrying neither a method nor a fallback (a pre-selection
	// scan's own record) still prints nothing, because it genuinely does not
	// know.
	switch {
	case f.SelectionMethod != "" && !f.Uncovered && f.PerMutant && f.MeasuredSpread():
		// Per-mutant grading makes SelectedTests the file's UNION — the
		// tests SOME mutant faced — and no mutant's own denominator. The
		// spread is the honest half: "234 of 620" alone invites the reader
		// to take 234 for the number every mutant survived, when the true
		// figure may be 3. The method is the verdict's own, never stamped
		// here, so the label cannot outlive the measurement it names.
		fmt.Fprintf(w, "   graded by %d of %d tests — %d to %d per mutant, median %d (%s%s)",
			f.SelectedTests, f.SuiteTests,
			f.TestsPerMutant.Min, f.TestsPerMutant.Max, f.TestsPerMutant.Median,
			f.SelectionMethod, selectionRuleBreakdown(f.Rules))
	case f.SelectionMethod != "" && !f.Uncovered && f.PerMutant:
		// Per-mutant, but no mutant was graded — every one was rejected by
		// the compile gate, which leaves the spread at {0,0,0}. "0 to 0 per
		// mutant, median 0" would report a range as measured when nothing
		// was measured at all, so the line says which measurement it is and
		// then says it found nothing to measure.
		fmt.Fprintf(w, "   graded by %d of %d tests (%s; no mutant graded)", f.SelectedTests, f.SuiteTests, f.SelectionMethod)
	case f.SelectionMethod != "" && !f.Uncovered:
		fmt.Fprintf(w, "   graded by %d of %d tests (%s)", f.SelectedTests, f.SuiteTests, f.SelectionMethod)
	case f.SelectionMethod != "" && f.Uncovered:
		// The uncovered line used to fall through to nothing — the ONE line
		// where the mode is most load-bearing was the one line that said
		// which measurement it was by saying nothing at all. "None execute
		// it" IS the selection's answer, not the absence of one.
		fmt.Fprintf(w, "   graded by the tests for this file — none execute it (%s)", f.SelectionMethod)
	case f.SelectionFallback != "":
		fmt.Fprintf(w, "   graded by the whole suite (%s)", f.SelectionFallback)
	}
	if f.ProvenByAuthoredAlone && f.ProvenMissed > 0 {
		// The proven count's meaning changed with the authored pass: only
		// the authored test ran against each survivor, so a proof is the
		// authored test's own — never a dev test that happened to flake.
		fmt.Fprint(w, " — proven by the authored test alone")
	}
	// What a mutant-generator shard actually SAW: "chunk" when every shard
	// showed only its own symbols, "file" when even one fell back (including
	// an unsharded run, which always shows the whole file). Silent — never a
	// fabricated "file" — for a run that predates this disclosure.
	if f.PromptShape != "" {
		fmt.Fprintf(w, " — prompts: %s", f.PromptShape)
	}
	fmt.Fprintln(w)
	// How many private trees scored this file at once, or why it only got
	// one — the same wording noteConcurrency printed live during the run,
	// through the one shared helper, so the screen and the record agree.
	// Silent when nothing was recorded (Trees < 1): the jail substrate builds
	// no trees, and a verdict served from a pre-concurrency cache row carries
	// none — matching noteConcurrency, which already stays quiet there.
	if f.Trees >= 1 {
		fmt.Fprintf(w, "   concurrency: %s\n", concurrencyDisclosure(f.Trees, f.ConcurrencyNote, f.SharedDirs))
	}
	// WHICH SHAPE the writer attacked in, and what it cost in calls. The two
	// modes prove the same thing — a survivor is proven iff an authored test
	// kills it alone and passes on the original — but they attempt it
	// differently and cost differently, so a proven count read without the
	// mode is two incomparable measurements wearing one number. Silent when
	// nothing recorded a mode (a run that named none, or a verdict from
	// before the mode existed): a reader is told nothing rather than told the
	// wrong one.
	//
	// A CACHE HIT prints the mode and NOTHING ELSE, for the same reason the
	// timing line above is suppressed entirely: the call count round-trips
	// through verdict_json and comes back fully populated with the EARNING
	// run's calls, which this scan did not make. The mode is a property of
	// the verdict and stays true however it was served; the count is a cost
	// this run did not pay.
	calls, ungraded := f.WriterCalls, f.WriterSeatsUngraded
	if f.CacheHit {
		calls, ungraded = 0, 0
	}
	if line := writerModeDisclosure(f.WriterMode, calls, ungraded); line != "" {
		fmt.Fprintf(w, "   %s\n", line)
	}
	// WHERE THE MINUTES WENT. Printed through the same helper `corral scans
	// show --timing` uses, so the line an operator reads now and the line
	// they read back out of the ledger are the same sentence.
	//
	// Silent when the run measured no phase at all — a verdict served from a
	// cache row written before any of this existed. Seven em dashes would be
	// noise, and worse, would look like a measurement of nothing.
	//
	// And silent for a REUSED verdict, whatever it carries. A cached verdict's
	// Timing round-trips through verdict_json intact, so the line would render
	// a full, plausible clock for a file this run spent no time on at all —
	// the reader has no way to tell it apart from minutes actually spent, and
	// the "N verdict(s) reused from cache" disclosure above is the honest
	// statement about this file's cost.
	if f.Timing.Measured() && !f.CacheHit {
		fmt.Fprintln(w, timingLine(f.Timing, f.MutantsGraded,
			time.Duration(f.MutantMillisMedian)*time.Millisecond,
			time.Duration(f.MutantMillisMax)*time.Millisecond))
	}

	// The artifact that makes "N proven, catchable gap(s)" actionable. --repo
	// is the mode the GitHub Action runs, and it reported the COUNT while
	// dropping the test that produced it — so a developer was told a gap is
	// provable and handed nothing to act on, for a test that had already
	// compiled and executed. Printed here so it reaches stdout and therefore
	// the job summary, which the Action copies verbatim.
	//
	// Only when the run actually proved something: on TimedOut /
	// TestWriterFailed / PoolTestUnsound, ProvenMissed is not meaningful and
	// the markers above already say why.
	if f.ProvenMissed > 0 && strings.TrimSpace(f.AuthoredTest) != "" {
		fmt.Fprintf(w, "      the pool wrote this test and RAN it to prove the gap — add it to your suite:\n")
		printAuthoredSource(w, f.AuthoredTest)
	}
	// The parts that would not merge into the file above. Printed in FULL,
	// each under its own header: every one is a test corral wrote, compiled
	// and ran to kill the survivor it names, and ProvenMissed counts it — so
	// printing only the merged file would report N provable gaps and hand the
	// developer fewer than N tests. On a language whose parts routinely will
	// not merge that is not an edge case, it is the whole output.
	//
	// Not gated on ProvenMissed > 0 the way the merged file is: a part only
	// ever exists BECAUSE it proved its survivor, so an extra with a zero
	// count would be a bug worth seeing rather than noise worth hiding.
	for _, p := range f.AuthoredExtra {
		fmt.Fprintf(w, "      proven test for %s (separate file — it cannot be merged with the others):\n", p.MutantID)
		if r := strings.TrimSpace(p.Reason); r != "" {
			fmt.Fprintf(w, "        why: %s\n", r)
		}
		printAuthoredSource(w, p.Source)
	}
}

// printAuthoredSource writes one authored test's source, indented, so several
// of them in a row read as separate files rather than one run-on block.
func printAuthoredSource(w io.Writer, src string) {
	for _, line := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		fmt.Fprintf(w, "        %s\n", line)
	}
}

// unpairableInDiff, when non-empty, names source files the diff CHANGED that
// corral could not pair with a test. It exists to keep the merge gate honest in
// the one case where a green result is actively misleading: a zero-candidate
// diff has two causes, and they are not the same answer.
// reasonGloss explains, in one clause, what an ungradable disposition MEANS —
// and specifically whether it is corral failing, the caller's invocation, or a
// file with nothing to audit.
//
// Every reason printed as a bare code read as a failure. On spf13/afero that
// made FOUR correct calls look like four crashes: two files live in separate Go
// modules the test command cannot reach, and two are pure interface
// declarations with no behavior a mutant could violate. An audit whose report
// understates its own accuracy is telling less than the truth, in the direction
// that happens to look worse — which is still not the full story.
//
// Unknown reasons get no gloss rather than a guessed one.
func reasonGloss(reason string) string {
	switch reason {
	case reposcan.ReasonTestCmdCannotCollect:
		return " — your test command would not run the test corral writes, so no gap could be proven; NOT a corral failure, and nothing was spent"
	case reposcan.ReasonUngoaled:
		return " — no testable property: the file is purely declarative (interfaces, constants, generated code), so there is nothing a mutant could violate"
	case reposcan.ReasonBaselineFailed:
		return " — the project's own suite did not pass on the UNMUTATED code in this environment; a build/environment problem, not a test-quality verdict"
	case reposcan.ReasonFlakyBaseline:
		return " — the unmutated suite gave different answers on repeated runs, so no mutant result from it could be trusted"
	case reposcan.ReasonExecutorError:
		return " — the audit itself failed to run; see the detail below"
	case reposcan.ReasonSuiteIgnoresFile:
		return " — the suite never exercises this file, so a mutant in it cannot be caught by construction"
	case reposcan.ReasonCancelled:
		return " — the run was interrupted before this file was graded"
	case reposcan.ReasonPrepFailed:
		return " — the workspace could not be prepared for this file"
	case reposcan.ReasonMappedTestMissing:
		return " — the --tests map names a test file that does not exist"
	}
	return ""
}

func printRepoReport(w io.Writer, r reposcan.RepoReport, nothingInScope bool, minKillRate *float64, maxProvenMissed *int, unpairableInDiff []string, oldestReused time.Time) {
	commit := r.Commit
	if strings.TrimSpace(commit) == "" {
		// Never print a bare dangling "@ " — say plainly that the report is
		// not bound to a commit, because that is what it means for anyone
		// trying to reproduce it.
		commit = "(no commit given)"
	}
	fmt.Fprintf(w, "\nRepo adequacy — %s/%s @ %s\n", r.Owner, r.Repo, commit)
	switch {
	case nothingInScope && len(unpairableInDiff) > 0:
		// The dangerous half of "zero candidates". These files DID change and
		// corral could not pair them with tests, so no audit ran — reporting
		// "no audit was needed" here would be a fail-open: the gate goes green
		// on exactly the change it was installed to inspect. Filename pairing
		// routinely finds nothing on JS/TS layouts, so this is not a corner
		// case, and the reader is told the way out rather than left guessing.
		fmt.Fprintf(w, "  NOT AUDITED: the diff changed %d source file(s) corral could not pair with a test, so nothing was graded. This is a pairing limitation, NOT a clean bill of health:\n", len(unpairableInDiff))
		for _, p := range unpairableInDiff {
			fmt.Fprintf(w, "    %s\n", p)
		}
		fmt.Fprintln(w, "    Supply a source→test map with --tests (see the docs), or audit these files directly with `corral certify --local`.")
	case nothingInScope:
		// "Nothing in scope" and "nothing could be graded" must not print the
		// same line: one is the honest, expected outcome of a docs-only PR;
		// the other is a real failure to report.
		fmt.Fprintln(w, "  NOTHING IN SCOPE: the diff touched no candidate; no audit was needed.")
	case r.Audited == 0:
		fmt.Fprintln(w, "  COULD-NOT-GRADE: nothing was audited; no score is reported.")
	case r.GradedFiles == 0:
		// Audited, but nothing graded: every audited file is UNCOVERED. There
		// is no mean to print (KillRate is NaN by construction) and a 0.00
		// here would be the withheld number arriving as a repo-wide verdict.
		fmt.Fprintf(w, "  NO GRADED FILE: all %d audited file(s) are UNCOVERED — no test executes them, so no kill rate was measured\n", r.Audited)
	case r.UncoveredFiles > 0:
		// The denominator is stated whenever it differs from Audited: the
		// mean is over the files that were actually graded, and a reader
		// dividing by "audited" would get a different number than the one
		// printed.
		fmt.Fprintf(w, "  kill rate %.2f over %d graded file(s) — %d audited, %d UNCOVERED and excluded from the mean (%.0f%% of %d candidates audited)\n",
			r.KillRate, r.GradedFiles, r.Audited, r.UncoveredFiles, 100*r.AuditedFraction(), r.Candidates)
	default:
		fmt.Fprintf(w, "  kill rate %.2f over %d audited file(s) (%.0f%% of %d candidates)\n",
			r.KillRate, r.Audited, 100*r.AuditedFraction(), r.Candidates)
	}
	// A claim carries how it was earned: some of the "audited" files above
	// were scored by a run that hit its wall-clock deadline before the pool
	// converged (advpool.Verdict.TimedOut, banked by driveLocalRun's
	// bankableTimeoutVerdict rather than discarded). The number is real —
	// see Verdict.DevScored, which gates whether such a file is Gradable at
	// all — but it must not read as an ordinary clean audit alongside it.
	if r.TimedOut > 0 {
		fmt.Fprintf(w, "  %d of the audited file(s) scored under an UNCONVERGED run — timed out before the pool finished (marked [TIMED OUT] below)\n", r.TimedOut)
		// When EVERY audited file timed out, corral's own adversarial
		// verification never ran to completion for anything this scan
		// touched — see repoScanExitCode, which fails the scan for exactly
		// this reason (a merge gate must not go green on "we measured the
		// dev suite but never actually gated it").
		if r.Audited > 0 && r.TimedOut == r.Audited && !nothingInScope {
			fmt.Fprintln(w, "  DID NOT FINISH: every audited file timed out before the pool converged — this scan did not actually gate anything")
		}
	}
	// Same honesty rule as TimedOut, for a different failure mode: these
	// files DID converge, but the pool found survivor(s) it could not author
	// a compiling test to prove — proven_missed reads 0 for them not because
	// the suite is clean, but because no killing test was ever authored.
	// Printed unconditionally alongside TimedOut (not folded into it, and not
	// mutually exclusive with it): a file can hit either independently.
	if r.TestWriterFailed > 0 {
		fmt.Fprintf(w, "  %d of the audited file(s) had survivor(s) the pool could not author a compiling test to kill — proven_missed reads 0 for them, NOT a clean suite (marked [WRITER FAILED] below)\n", r.TestWriterFailed)
	}
	// PoolTestUnsound is a DIFFERENT diagnosis from TestWriterFailed: a
	// compiling test WAS produced, but its own scoring report never
	// genuinely graded (it failed on the unmutated compliant code, or the
	// canary was never killed, or nothing was scored). Same honesty rule,
	// printed alongside TestWriterFailed/TimedOut, never folded into either.
	if r.PoolTestUnsound > 0 {
		fmt.Fprintf(w, "  %d of the audited file(s) had a compiling authored test that never genuinely graded (failed on clean code, or never reads the file) — proven_missed reads 0 for them, NOT a clean suite (marked [TEST UNSOUND] below)\n", r.PoolTestUnsound)
	}
	// ProvenMissed is corral's strongest claim — a survivor its authored
	// test then killed BY EXECUTION, a specific demonstrated bug the dev
	// suite misses — and it must read as that, not slide past as a bare
	// number. r.ProvenMissed==0 is itself ambiguous across the whole repo
	// (see WeakFile.ProvenMissed's doc for the four causes, TimedOut
	// included), so this line's wording resolves it inline rather than
	// printing a number a reader has to interpret unaided.
	if r.Audited > 0 {
		if r.ProvenMissed > 0 {
			fmt.Fprintf(w, "  %d proven, catchable gap(s): the pool authored a test that killed a survivor by EXECUTION — see weakest files below\n", r.ProvenMissed)
		} else {
			fmt.Fprintln(w, "  0 proven gaps: no authored test killed a survivor in this run — see the per-file marker below for why (no survivors / writer failed / test unsound / timed out / tried and missed)")
		}
	}
	// WHICH MEASUREMENT the numbers above are. Selection and whole-suite
	// answer different questions, and a scan can mix them file by file (the
	// evidence covers most files and misses one), so the split is stated
	// rather than left to the per-file lines to imply. Uncovered files are a
	// SUBSET of the selected ones — the evidence ran and found nothing that
	// executes them — so they are named inside that clause, not as a third
	// bucket that would not add up.
	if r.Audited > 0 {
		fmt.Fprintf(w, "  test selection: %d file(s) graded by the tests that execute them (%d of those UNCOVERED — no test executes them at all), %d by the whole suite\n",
			r.SelectedFiles, r.UncoveredFiles, r.WholeSuiteFiles)
	}
	// Sorted, like printExclusions: map iteration order is random, and a
	// report a later slice signs and anchors has to be byte-reproducible.
	ungradableReasons := make([]string, 0, len(r.Ungradable))
	for reason := range r.Ungradable {
		ungradableReasons = append(ungradableReasons, reason)
	}
	sort.Strings(ungradableReasons)
	for _, reason := range ungradableReasons {
		fmt.Fprintf(w, "  ungradable: %d (%s)%s\n", r.Ungradable[reason], reason, reasonGloss(reason))
		// Detail is the operator's actual diagnosis (e.g. WHY the toolchain
		// check failed) — the count alone answered "how many" but not "why",
		// which used to mean a code trace instead of reading the report.
		for _, detail := range r.UngradableDetails[reason] {
			fmt.Fprintf(w, "    e.g. %s\n", detail)
		}
	}
	if r.CacheHits > 0 {
		fmt.Fprintf(w, "  %d verdict(s) reused from cache\n", r.CacheHits)
		// A hit COUNT alone is not disclosure — see oldestReuse's own doc
		// comment. It goes here, in the same breath as the count, so the
		// reader learns how old the oldest contributing verdict is before
		// reading a single number the reuse helped produce.
		//
		// Rounded to the MINUTE, not the hour: an hour-rounded duration
		// prints "(0s ago)" for anything under thirty minutes, so the
		// commonest case of all — a CI re-run minutes after the first —
		// read as though there were no age to disclose at all.
		if !oldestReused.IsZero() {
			fmt.Fprintf(w, "  reused verdicts: oldest earned %s (%s ago)\n",
				oldestReused.UTC().Format(time.RFC3339), time.Since(oldestReused).Round(time.Minute))
		}
	}
	if len(r.Weakest) > 0 {
		fmt.Fprintln(w, "  weakest files:")
		printed := make(map[string]bool, len(r.Weakest))
		for i, f := range r.Weakest {
			if i == 10 {
				fmt.Fprintf(w, "    ... and %d more\n", len(r.Weakest)-10)
				break
			}
			printWeakFile(w, f)
			printed[f.Path] = true
		}
		// F4: the repo-level line above promises "see weakest files below"
		// for every proven, catchable gap — a promise the truncation at 10
		// entries (weakest-first, by kill rate) can silently break, because a
		// proven gap can sit on a file with a HIGH kill rate that never makes
		// the cut. Any such file is listed explicitly here instead of being
		// hidden behind "... and N more".
		for _, f := range r.Weakest {
			if f.ProvenMissed > 0 && !printed[f.Path] {
				fmt.Fprint(w, "    (not in the top 10 weakest, but has a proven gap) ")
				printWeakFile(w, f)
			}
		}
	}
	// A distinct line from COULD-NOT-GRADE: that line means nothing was
	// measured at all; this one means files WERE measured and at least one
	// scored below the operator's own floor — an operator reading the report
	// must be able to tell which file to go write tests for. Guarded the same
	// way repoScanExitCode is (nothingInScope / Audited == 0 decide first) so
	// the two never disagree about what happened: r.Weakest is empty in both
	// of those states anyway, but the guard keeps the intent explicit.
	if minKillRate != nil && !nothingInScope && r.Audited > 0 {
		var breaches, uncovered []reposcan.WeakFile
		for _, f := range r.Weakest {
			switch {
			case f.Uncovered:
				uncovered = append(uncovered, f)
			case f.KillRate < *minKillRate:
				breaches = append(breaches, f)
			}
		}
		if len(breaches) > 0 {
			fmt.Fprintf(w, "  KILL-RATE BREACH: %d file(s) below --min-kill-rate %.2f:\n", len(breaches), *minKillRate)
			for _, f := range breaches {
				fmt.Fprintf(w, "    %.2f  %s (%.2f below threshold)\n", f.KillRate, f.Path, *minKillRate-f.KillRate)
			}
		}
		// Reported separately, and with no number: these files have no rate
		// to be "below" the threshold — nothing executes them. They fail the
		// gate (see repoScanExitCode) and an operator whose build just went
		// red must be able to see WHY without hunting for a 0.00 that is not
		// printed anywhere.
		if len(uncovered) > 0 {
			fmt.Fprintf(w, "  UNCOVERED: %d file(s) no test executes at all:\n", len(uncovered))
			for _, f := range uncovered {
				fmt.Fprintf(w, "    %s: UNCOVERED — no test executes it (fails --min-kill-rate)\n", f.Path)
			}
		}
	}
	// The proven-missed gate, reported the same way and for the same reason:
	// an operator whose build just went red must be able to see WHICH file and
	// WHY without re-reading the whole report.
	if maxProvenMissed != nil && !nothingInScope && r.Audited > 0 {
		var breaches, unmeasured []reposcan.WeakFile
		for _, f := range r.Weakest {
			switch {
			case f.ProvenMissed > *maxProvenMissed:
				breaches = append(breaches, f)
			case f.Survivors > 0 && (f.TestWriterFailed || f.PoolTestUnsound):
				unmeasured = append(unmeasured, f)
			}
		}
		if len(breaches) > 0 {
			fmt.Fprintf(w, "  PROVEN-GAP BREACH: %d file(s) above --max-proven-missed %d:\n", len(breaches), *maxProvenMissed)
			for _, f := range breaches {
				fmt.Fprintf(w, "    %d proven gap(s)  %s — each one a bug the pool DEMONSTRATED your tests miss, by writing a test and running it\n", f.ProvenMissed, f.Path)
			}
		}
		if len(unmeasured) > 0 {
			// Failing closed, and saying so. A 0 here is not a clean bill of
			// health: the pool had survivors to prove and could not author a
			// test that graded, so nothing was established either way.
			fmt.Fprintf(w, "  PROVEN-GAP UNMEASURED: %d file(s) left survivors the pool could not test:\n", len(unmeasured))
			for _, f := range unmeasured {
				why := "the authored test never graded"
				if f.TestWriterFailed {
					why = "no compiling test could be authored"
				}
				fmt.Fprintf(w, "    %s — %d survivor(s), %s, so 'proven_missed: 0' means nothing was proven, NOT that the suite is clean\n", f.Path, f.Survivors, why)
			}
		}
	}
}

// resolveScanWorkers sizes the scan's concurrent worker pool AND renders the
// readout line for it, together, so the number printed is always the number
// used.
//
// The jail substrate keeps resolveSwarm's behaviour and readout exactly: each
// job builds its own disposable jail workspace, so jobs are independent and
// concurrency is free correctness-wise.
//
// The workspace substrate is clamped to ONE worker, whatever the operator
// asked for — but it is no longer clamped to one RUN. Files are serialized
// because a tree is a copy of the whole checkout and N files' pools at once
// is N times that disk; WITHIN a file, adequacy.WorkspacePool gives each
// concurrent mutant its own private tree, so the suite really does run many
// at a time (see resolveMutantConcurrency, and the per-file `concurrency:`
// line the executor prints once the pool's probe has answered).
//
// The readout must not claim more than that. It used to say the substrate
// "mutates one checkout in place, so jobs run one at a time", which was true
// of the whole audit and is now true only of the file axis; an operator who
// reads it as "corral cannot use my box" is being told something false about
// the thing this design changed.
func resolveScanWorkers(swarmFlag int, substrate string) (int, string) {
	if substrate == substrateWorkspace {
		return 1, fmt.Sprintf("  swarm: 1 worker — --substrate %s audits one file at a time (mutants within a file run concurrently, in private trees)\n", substrateWorkspace)
	}
	n := resolveSwarm(swarmFlag)
	return n, fmt.Sprintf("  swarm: %d workers\n", n)
}

// resolveMutantConcurrency divides ONE bounded budget of concurrent jails
// between the scan's two independent parallel axes: files scored at once
// (resolveScanWorkers) and mutants scored at once WITHIN a file. They multiply,
// so they cannot each take the budget — a 16-worker scan whose every file also
// scored 16 mutants at once would open 256 jails and thrash the box. The
// invariant is `workers × result <= budget`, and it is pinned by a test that
// sweeps the whole space rather than a few chosen points.
//
// Why this is worth doing at all: scoring runs the target's whole suite once
// per mutant, so an audit costs O(mutants × suite runtime) — 1.46s/suite on
// pallets/flask but 77s on psf/requests, where the suite is ~96% of a file's
// cost. That loop is embarrassingly parallel and was simply never distributed.
//
// The division targets the case that actually matters commercially. A
// diff-scoped PR audits ONE changed file, so file-parallelism can spend NONE of
// the budget: N-1 workers idle while ~42 mutants score strictly one at a time.
// Giving the leftover to the mutant loop is what makes that shape fast. When
// files already saturate the budget the result is 1 and nothing changes, which
// is why this is safe to turn on by default.
//
// The workspace substrate divides the budget differently, and used to be
// pinned to 1. The pin was a correctness boundary, not a throughput choice:
// adequacy.WorkspaceRunner mutates ONE checkout in place with NO mutex, so two
// concurrent applyFiles interleave and one job's suite runs against another's
// mutant — recording SURVIVORS AS KILLED and signing an inflated kill rate
// that is undetectable after the fact. adequacy.WorkspacePool removes the
// shared tree (one private copy per worker) and with it the reason for the
// pin, so the budget can finally reach the axis that dominates the cost of a
// real audit: scoring runs the target's whole suite once per mutant.
//
// The share is a QUARTER of the budget, not all of it, and it does not divide
// by the file workers because resolveScanWorkers already holds the workspace
// substrate at one file at a time. A quarter because a tree is not a jail: it
// is a copy of the checkout on disk running a REAL suite that wants CPU,
// memory and I/O of its own (the spec's cores/4 default, --swarm overriding
// the cores half).
//
// It is NOT capped by jobs, and that asymmetry with the jail branch is
// deliberate. jobs is the FILE count, and the number this branch sizes is
// trees-per-file — the two are unrelated the moment files stop running
// concurrently. Capping by it would have pinned a diff-scoped one-file audit
// to a single tree forever, which is the exact shape (one changed file, ~40
// mutants, 23 of 24 cores idle) this whole design exists for. There is no
// mutant count to cap by either: mutants do not exist until the generator has
// run, long after this decision. An unused tree costs one copy of the
// checkout and nothing else — adequacy.Score never runs more mutants at once
// than it has mutants.
//
// Whether the trees are actually USED is not decided here: the pool's probe
// runs the unmutated baseline in all N at once and downgrades to 1, disclosed,
// if the suite is not concurrency-safe. This function sizes the ambition; the
// probe is what keeps it honest.
//
// Fails closed on both branches: any degenerate budget/worker/job count yields
// 1, never unbounded.
// treeBudget is the budget the workspace substrate divides into private
// trees: the operator's --swarm when given, else every core on the host. It
// deliberately bypasses resolveSwarm's localSwarmAutoCap — that cap bounds
// concurrent MODEL calls, and a tree is a test process, not a model call.
func treeBudget(swarmFlag int) int {
	if swarmFlag > 0 {
		return swarmFlag
	}
	return runtime.NumCPU()
}

func resolveMutantConcurrency(budget int, substrate string, workers, jobs int) int {
	if substrate == substrateWorkspace {
		if budget < 1 {
			return 1
		}
		n := budget / 4
		if n < 1 {
			return 1
		}
		return n
	}
	if budget < 1 || workers < 1 || jobs < 1 {
		return 1
	}
	// Divide by the workers that will ACTUALLY run, not the configured pool.
	// resolveScanWorkers sizes the pool from the host's cores with no knowledge
	// of how many files the scan selected, so a 1-file scan on an 8-core box
	// reports 7 workers and 6 of them never claim anything. Dividing by 7 there
	// yields 1 and hands the mutant loop nothing — silently disabling this
	// feature in exactly the diff-scoped case it exists for. Caught by measuring
	// on the box, not by the unit tests, which had been fed the honest-but-wrong
	// configured count.
	active := workers
	if jobs < active {
		active = jobs
	}
	n := budget / active
	if n < 1 {
		return 1
	}
	return n
}

// localExecutor runs one scan job through the SAME in-process adversarial
// pool `corral certify --local` drives (auditOneFile), in repo-aware mode:
// the whole tree is seeded into the jail and the audited file is mutated in
// place, so a real multi-file package resolves.
//
// newBaseline and audit are the two seams; both default to the real
// implementations and exist so the adapter's honesty wiring can be exercised
// without a jail (a jail needs bwrap/container privileges no unit test can
// assume).
// auditModels carries the operator's per-role model overrides for a repo
// scan. `certify --local` has exposed these since it shipped; `certify --repo`
// exposed only --derive-model, pinning every other role to the hardcoded
// Claude defaults with no override and no env escape hatch — so corral's
// flagship whole-repo command could ONLY ever run on Anthropic. That is a
// limitation on its own (and it undercuts corral's own cross-vendor
// decorrelation argument), and it became a hard blocker the day the Anthropic
// account hit its usage limit mid-scan with a Google key already in the
// credstore.
//
// Every field is empty unless the operator passed the flag; empty means
// "apply auditRoles' own default", so a scan that names none is byte-identical
// to before.
type auditModels struct{ writer, mutant, critic, shadow, shadowWriter string }

type localExecutor struct {
	// models are the per-role overrides threaded into every job's
	// localAuditInput (see auditInputFor).
	models auditModels

	// localEndpoints places local seats on specific ollama daemons for every
	// file this scan audits — one daemon per GPU. See parseLocalEndpoints.
	localEndpoints map[string]string

	// perFileSwarm is how many workers ONE file's own audit may use. It is >1
	// only on the workspace substrate, where resolveScanWorkers has already
	// forced file-level concurrency to 1 (files share a single checkout), so
	// the operator's --swarm budget would otherwise go entirely unspent — the
	// box idling while a file's ~8 mutant-generator shards, its test-writer
	// and its critic run strictly one after another.
	//
	// On a JAIL substrate this stays 1: files there really do run
	// concurrently, so a nested per-file swarm would multiply the budget by
	// the worker count.
	//
	// Safe only because in-process workers are LLM-ONLY — agentworker.RunRole
	// is a single model.Chat call and the worker path never references
	// sandbox, adequacy or the workspace. That matters: WorkspaceRunner has no
	// mutex, and concurrent applyFiles against the shared checkout would
	// corrupt the tree. Anything that gives workers workspace access must
	// revisit this.
	perFileSwarm int
	// mutantConcurrency is how many mutants ONE file scores at once — the
	// leftover of the jail budget that file-parallelism cannot spend. Always 1
	// on the workspace substrate; see resolveMutantConcurrency.
	mutantConcurrency int

	// presetMutants is the `--mutants` replay set, keyed by repo-relative
	// path: a file present here is graded against exactly these mutants and
	// seeds no generator. Every SELECTED file is present or the scan already
	// refused (see presetMutantsForSelection), so a nil lookup here means an
	// ordinary generated run, never a silent half-replay.
	presetMutants map[string][]adequacy.Mutant

	// mutantSink is `--record-mutants`: fed the mutants each file's dev pass
	// actually graded. nil records nothing.
	mutantSink func(codePath string, ms []adequacy.Mutant)

	// events is the scan's own tape: every driver beat, across every audited
	// file, landing as a scanstore.Event row (see scanEventSink's doc). One
	// sink per scan, shared by every file job — newLocalExecutor always
	// constructs one, so it is never nil for a real scan; a bare
	// localExecutor{} built directly by a seam-level test leaves it nil,
	// which forFile/forScan both treat as "record nothing".
	events *scanEventSink

	// selection is what ONE instrumented run of this repo's suite learned
	// about which tests execute which files, collected once per scan (see
	// collectSelection) and asked per job (see selectionFor). Its zero value
	// means no evidence — every file grades whole-suite, disclosed.
	selection reposcan.SelectionEvidence
	// selectionDuration is how long the scan's ONE instrumented coverage run
	// took, and ZERO when there was no such run. It is measured inside
	// collectSelection, around the run itself — never around the CALL, which
	// returns immediately under --whole-suite, for an unsupported language,
	// or when the runner could not be built. A clock around the call recorded
	// microseconds for those, which the ledger stored as 1ms and the report
	// printed as `selection 0s`: a scan claiming it ran a selection pass, for
	// free, when it ran none.
	//
	// The driver cannot measure this itself — it happens once, for the whole
	// repo, before any file's run exists — so every file's RunSpec is handed
	// the answer (advpool.RunSpec.SelectionDuration).
	//
	// It is a PER-SCAN cost carried on every file's verdict, not a per-file
	// measurement: summing it across files would invent time nobody spent.
	selectionDuration time.Duration

	// selectionCache is the (tree_digest, cmd_digest, plugin)-keyed store
	// collectSelection consults before running the instrumented suite, and
	// records a fresh run's evidence into once the scan has a ledger id
	// (see recordCertifyRepoScan's caller in certify_repo.go). nil under
	// --no-selection-cache or a --dry-run (which never builds a
	// localExecutor at all) — every lookup and every hold-for-Put below is
	// gated on it being non-nil.
	selectionCache selectionCacheStore
	// treeDigest/treeDigestComputed memoize treeDigestOnce: reposcan.TreeDigest
	// runs at most once per scan regardless of how many callers ask for it
	// this scan (see treeDigestOnce's own doc). "" with treeDigestComputed
	// true means the tree could not be named (outside a git work tree, or a
	// git failure) — a real, cached answer, not "not yet asked".
	treeDigest         string
	treeDigestComputed bool
	// selectionReusedFrom is set the moment collectSelection serves a cache
	// HIT: the id of the scan whose evidence this scan is reusing. nil on
	// every scan that ran its own instrumented pass or ran none at all —
	// see scanstore.Scan.SelectionReusedFrom's doc for why this is the only
	// signal that tells "reused" apart from "never ran".
	selectionReusedFrom *int64
	// pendingSelectionPut holds a freshly-collected MISS's raw evidence and
	// the key it was collected under, for the recording sequence to Put once
	// this scan's own ledger id exists — collectSelection runs long before
	// Record does (see collectSelection's doc), so there is no scan id to
	// write the row against at collection time. nil whenever nothing was
	// collected fresh (a cache hit, no cache wired, or the pass did not run
	// at all).
	pendingSelectionPut *pendingSelectionCachePut

	// writerMode is the scan-wide --writer-mode, carried onto every job's
	// localAuditInput. Resolved and validated once at the flag boundary.
	writerMode string

	// wholeSuite is the operator's --whole-suite opt-out: grade every mutant
	// against the project's whole suite instead of the tests that
	// demonstrably execute the file. It changes the MEASUREMENT, not just the
	// cost, which is why the verdict records which one was made.
	wholeSuite bool

	repoDir      string
	checkArgv    []string
	baselineRuns int // how many times to run the unmutated suite; 2 is the floor
	progress     io.Writer

	// timeout is the per-file budget threaded into every job's
	// localAuditInput.timeout (see Execute) — the `--timeout` passthrough:
	// before it existed, a repo scan's per-file audit was silently pinned to
	// auditOneFile's own 10-minute fallback (in.timeout <= 0), with no way
	// for the operator to give a large file more room. Zero means "use that
	// same fallback", so a caller that never sets it (every existing
	// newLocalExecutor call site before this field existed, and the
	// seam-level unit tests that construct a bare localExecutor) keeps
	// today's behaviour exactly.
	timeout time.Duration

	// seeds memoizes the repo seed per language for this scan. Without it,
	// prep runs twice per audited file (once for the baseline runner, once for
	// the audit): on a 189-file Go repo that is 378 tree copies and 378 `go mod
	// vendor` runs, up to NumCPU concurrently, which exhausts TMPDIR rather
	// than merely running slowly. A nil cache means "prepare per file", which
	// is what the unit tests that construct a bare localExecutor rely on.
	seeds *seedCache

	// iso is the scan's sandbox, resolved ONCE at construction (see below) and
	// handed to every job via localAuditInput.iso — never re-resolved per
	// file. Nil when resolution failed (jailErr is set instead).
	iso sandbox.Isolator

	// jailErr is the sandbox-resolution failure, if any, captured once at
	// construction. It is scan-fatal (no jail = nothing can be graded on this
	// host), surfaced by preflight before the fan-out — never swallowed.
	jailErr error

	// substrate selects where every job in this scan runs — "" or
	// substrateJail (the bwrap jail, today's behavior) or substrateWorkspace
	// (mutate repoDir in place, the caller IS the isolation boundary). A
	// newLocalExecutor constructor parameter, not set after construction: it
	// must be known BEFORE the sandbox-resolution preflight below decides
	// whether a jail is even needed, so a bwrap-less CI runner is never
	// refused for a jail --substrate workspace was never going to use. Every
	// existing caller of newLocalExecutor that doesn't care passes "" and
	// keeps the zero-value (jail-equivalent) default.
	substrate string

	newBaseline func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error)
	audit       func(context.Context, localAuditInput) (advpool.Verdict, error)
}

// effectivePerFileSwarm is perFileSwarm clamped to the only substrate where a
// per-file swarm is safe, and defaulted to today's single worker. A zero or
// negative budget must never become unbounded concurrency.
func (l *localExecutor) effectivePerFileSwarm() int {
	if l.substrate != substrateWorkspace || l.perFileSwarm < 1 {
		return 1
	}
	return l.perFileSwarm
}

func newLocalExecutor(repoDir string, checkArgv []string, substrate string, timeout time.Duration, progress io.Writer) *localExecutor {
	if progress == nil {
		progress = io.Discard
	}
	l := &localExecutor{
		repoDir:      repoDir,
		checkArgv:    checkArgv,
		baselineRuns: 2,
		substrate:    substrate,
		timeout:      timeout,
		// Concurrent jobs write progress; serialize so two files' notices
		// cannot interleave mid-line.
		progress:    &syncWriter{w: progress},
		newBaseline: baselineRunnerFor,
		audit:       auditOneFile,
		events:      newScanEventSink(nil),
	}
	// The substrate must be known BEFORE this preflight runs: the workspace
	// substrate needs no sandbox by construction (buildJailWiring's workspace
	// branch never builds a seed, resolves an isolator, or binds a mount), so
	// resolving one here would refuse a bwrap-less CI runner for a jail the
	// scan was never going to use. The jail substrate (including the "" zero
	// value — today's shipped default) keeps the exact preflight behaviour
	// below: a bwrap-less host still fails, with the same message.
	backendName := ""
	if substrate != substrateWorkspace {
		// Resolve the sandbox ONCE for the whole scan: the backend name is an
		// input to the seed (it decides which dep dirs can be bind-mounted
		// rather than copied), and it is a scan-wide constant — resolving it
		// per file would re-run the backend probe for every job to reach the
		// same answer. The scan exposes no --jail flag, so the auto backend is
		// resolved (same rules as prepareAuditJail's empty in.jail), minus the
		// `--jail container` advice on failure — a flag this command does not
		// offer.
		iso, err := resolveScanJail()
		if err != nil {
			l.jailErr = err
		} else {
			l.iso = iso
			backendName = iso.Name()
		}
	}
	// The seed is jail preparation — a tree copy, `go mod vendor`, a full
	// tree walk into memory — and buildJailWiring's workspace branch never
	// reads any of it (it builds its own empty overlay map and mutates
	// repoDir directly). Wiring it anyway would not just waste the work: a
	// failed `go mod vendor` (no network, a private proxy, a small TMPDIR —
	// exactly the conditions an ephemeral CI runner can hit) caches an error
	// that turns EVERY job ungradable (see Execute's `l.seeds != nil`
	// guard below), which is a false COULD-NOT-GRADE red build from a step
	// this substrate exists to skip. Same shape as the sandbox guard above:
	// left nil, so Execute's existing nil-cache branch (already exercised by
	// the seam-level unit tests) applies.
	//
	// The scan exposes no --bind-dir/--no-bind-deps: nil/false are the
	// documented defaults (auto-detected dep dirs are bound, nothing extra).
	if substrate != substrateWorkspace {
		l.seeds = newSeedCache(func(langName string) (repoSeed, error) {
			if l.jailErr != nil {
				return repoSeed{cleanup: func() {}}, l.jailErr
			}
			return buildRepoSeed("corral certify --repo", repoDir, langName, backendName, nil, false, l.progress)
		})
	}
	return l
}

// preflight reports a scan-fatal condition discovered at construction — today,
// a sandbox that cannot isolate on this host. Checked ONCE before the fan-out,
// like the provider-key preflight: discovering it per file would report every
// file as ungradable for a reason the first job could have stated instantly.
func (l *localExecutor) preflight() error { return l.jailErr }

// Close releases every staging dir this scan's seeds created. Idempotent.

func (l *localExecutor) Close() {
	if l.seeds != nil {
		l.seeds.close()
	}
}

// auditInputFor builds the per-file audit input for one job. Extracted from
// Execute so the scan's role-model plumbing is assertable without standing up
// a jail, a baseline runner and a whole audit: a "supported" model flag that
// parses and is then dropped on the floor is exactly the silently-discarded-
// input shape this codebase keeps producing.
func (l *localExecutor) auditInputFor(j reposcan.Job) localAuditInput {
	// Resolved ONCE per job and carried on the input, so the narrowed command
	// the executor's own baseline runs and the Selection the pool applies per
	// pass (advpool.DevCommand, JailScorer.authoredCmd) can never be derived
	// twice and disagree.
	sel := l.selectionFor(j)
	return localAuditInput{
		localEndpoints: l.localEndpoints,
		repoDir:        l.repoDir,
		codePath:       j.Path,
		testPath:       j.TestPath,
		goal:           j.Goal.Text,
		lang:           j.Lang,
		repo:           j.Repo,
		iso:            l.iso,
		// "local" rather than "" when the operator gave no --commit: an empty
		// commit makes auditOneFile fall back to `git rev-parse HEAD` — which
		// reads the CWD's repo, not the SCANNED one, and would stamp every
		// verdict with an unrelated sha.
		commit:    orDefault(j.Commit, "local"),
		checkArgv: l.testCmd(j, sel),
		// The BASE command, before narrowing: RunSpec.TestCmd must stay the
		// whole-suite command because the Selection is applied downstream,
		// per pass (advpool.DevCommand for the dev/shadow passes, the
		// scorer's authoredCmd for the authored pass). Handing it the
		// already-narrowed command would narrow twice — and would drop the
		// pool's own authored test, which no evidence run ever saw.
		baseArgv:  l.baseCmd(j),
		selection: sel,
		// The scan's one instrumented run, carried onto every file's spec —
		// see localExecutor.selectionDuration.
		selectionDuration: l.selectionDuration,
		substrate:         l.substrate,
		timeout:           l.timeout,
		// See localExecutor.perFileSwarm: 1 everywhere the scan really does
		// fan out over files, and the otherwise-unspent budget on the
		// workspace substrate, which serializes them.
		swarm: l.effectivePerFileSwarm(),
		// The other half of the same budget: files in parallel x mutants in
		// parallel. On the jail substrate that many disposable jails; on the
		// workspace substrate that many PRIVATE TREES in the pool (see
		// resolveMutantConcurrency, which no longer pins the workspace to 1
		// because adequacy.WorkspacePool removed the shared checkout that
		// made the pin necessary).
		mutantConcurrency: l.mutantConcurrency,
		// Where the pool's concurrency probe writes its answer for this file.
		// A pointer because the input travels BY VALUE from here to
		// buildJailWiring; allocated for every job so the executor can print
		// the disclosure below and the verdict can carry it, and left at its
		// zero value (Trees 0) by the jail substrate, which builds no trees
		// and has nothing to disclose.
		concurrency: new(adequacy.Disclosure),
		// And the box the job's ONE pool of private trees lives in. Created
		// here, on the workspace substrate only, because THIS is the scope
		// that owns it: the baseline-stability check and the audit are two
		// consumers of one pool, and Execute closes it when both are done.
		// nil on the jail substrate, which has no trees to share.
		pool: l.workspacePoolBox(),
		// H1a produces a REPORT, not a sealed statement: no ledger, no
		// signing key, no scorecard feed (N concurrent audits must not
		// contend on one single-process DuckDB file). Signing is H1c.
		stdout: io.Discard,
		stderr: io.Discard,

		// Per-role models, empty unless the operator passed the flags — the
		// zero value keeps auditRoles' own Claude defaults, so an invocation
		// that names none behaves exactly as before.
		writerModel:       l.models.writer,
		mutantModel:       l.models.mutant,
		criticModel:       l.models.critic,
		shadowModel:       l.models.shadow,
		shadowWriterModel: l.models.shadowWriter,
		writerMode:        l.writerMode,

		// A nil entry is an ordinary generated run — see localExecutor.presetMutants.
		presetMutants: l.presetMutants[j.Path],
		mutantSink:    l.mutantSink,
		// This file's own adapter onto the scan's shared tape — see
		// scanEventSink.forFile's doc for why every file needs its own
		// (the driver's Emit carries no path).
		eventSink: l.events.forFile(j.Path),
	}
}

// scanSelectionMillis is the scan header's selection_ms: how long the ONE
// instrumented coverage run took, or nil when there was no such run —
// --whole-suite, an unsupported language, a --dry-run that built no executor
// at all. nil is SQL NULL; a 0 would claim the pass ran and cost nothing.
func scanSelectionMillis(ex *localExecutor) *int64 {
	if ex == nil {
		return nil
	}
	return millisOrNil(ex.selectionDuration)
}

// scanSelectionReusedFrom is the scan header's selection_reused_from: the id
// of the prior scan whose coverage evidence THIS scan reused, or nil when
// this scan ran its own pass (or ran none at all) — see
// localExecutor.selectionReusedFrom's own doc for why this is the only
// column that tells "reused" apart from "never ran".
func scanSelectionReusedFrom(ex *localExecutor) *int64 {
	if ex == nil {
		return nil
	}
	return ex.selectionReusedFrom
}

// scanEventRows drains the scan's accumulated tape, or nil when ex is nil —
// a --dry-run, which audits nothing and builds no executor (and so no sink)
// at all. The honest empty tape, never a fabricated one.
func scanEventRows(ex *localExecutor) []scanstore.Event {
	if ex == nil {
		return nil
	}
	return ex.events.drain()
}

// concurrencyDisclosure renders the human-readable half of "how many trees
// scored this file, or why one" — the ONE place that wording is spelled out,
// shared by the live progress line (noteConcurrency, below), the per-file
// report line (printWeakFile) and, through them, the ledger and the
// attestation. An operator who sees "concurrency: 1 (…)" on screen and a
// different phrase in the record would have no way to tell whether they are
// the same fact; a second copy of this string is exactly how that drifts.
func concurrencyDisclosure(trees int, note string, shared []string) string {
	if trees < 1 {
		// Not a measurement of one tree — no measurement at all. Only the
		// ledger reader ever renders this: the live line and the report line
		// print nothing rather than a row of prose. See advpool.Concurrency.
		return "not recorded"
	}
	head := "1"
	var parts []string
	if trees > 1 {
		head = fmt.Sprintf("%d trees", trees)
		parts = append(parts, fmt.Sprintf("baseline passed under %d", trees))
	}
	if note != "" {
		parts = append(parts, note)
	}
	// The dep dirs the trees SHARED, named on the same line as the count
	// they qualify: "6 trees" is an isolation claim, and these directories
	// are the exact places it does not hold.
	if len(shared) > 0 {
		parts = append(parts, "shared: "+strings.Join(shared, ", "))
	}
	if len(parts) == 0 {
		return head
	}
	return head + " (" + strings.Join(parts, "; ") + ")"
}

// noteConcurrency prints one file's concurrency disclosure: how many private
// trees its pool got, or that it got one and WHY.
//
// Nothing is printed for a disclosure that was never written (Trees 0): the
// jail substrate has no trees to disclose, and inventing a line for it would
// claim a measurement that never happened. Trees == 1 with no note is the
// ordinary "the budget only bought one tree" case — the substrate's own
// default, and not news either.
func (l *localExecutor) noteConcurrency(path string, d *adequacy.Disclosure) {
	if d == nil || d.Trees < 1 {
		return
	}
	if d.Trees == 1 && d.Note == "" {
		return
	}
	l.note("%s: concurrency: %s\n", path, concurrencyDisclosure(d.Trees, d.Note, d.Shared))
}

// workspacePoolBox returns the box this job's private-tree pool will live in,
// or nil on a substrate that builds no trees. One box per JOB: two files must
// never share trees (the second file's mutants would be written into a tree
// the first file's suite is reading), and the same file's baseline check and
// audit must never build two (a second copy of the checkout and a second
// probe, for an answer the first one already has).
func (l *localExecutor) workspacePoolBox() *workspacePool {
	if l.substrate != substrateWorkspace {
		return nil
	}
	return &workspacePool{}
}

func (l *localExecutor) Execute(ctx context.Context, j reposcan.Job) (reposcan.FileResult, error) {
	in := l.auditInputFor(j)
	// The job owns its private trees for as long as the job lasts — through
	// the baseline-stability check AND the audit, which is the whole point of
	// hanging them here rather than on the wiring's own cleanup (that one is
	// deliberately released between the two). Deferred immediately so every
	// early return below still deletes the copies. A no-op when there are
	// none, which is every jail-substrate job.
	defer in.pool.close()

	// The scan-wide, per-language seed: the tree copy, the vendoring and the
	// tree walk depend only on the repo + language, so they are done ONCE and
	// shared (read-only) by every job. A nil cache means each job prepares its
	// own, which is what the seam-level unit tests exercise.
	if l.seeds != nil {
		seed, serr := l.seeds.get(j.Lang)
		if serr != nil {
			// Prep failed for this language: every job of it is ungradable, and
			// the cached error means the work is attempted once rather than
			// once per file. Never a fabricated 0.0 — ungradable WITH a reason.
			l.note("%s: jail preparation failed for %s — not graded: %v\n", j.Path, j.Lang, serr)
			return reposcan.FileResult{Gradable: false, Reason: reposcan.ReasonPrepFailed}, nil
		}
		in.seed = &seed
	}

	// Honesty invariant 2: a flapping suite makes a mutant look killed or
	// survived at random, so any kill rate derived from it is a coin flip.
	// Checked BEFORE the audit runs — an unstable baseline is not a score to
	// be discarded afterwards, it is a measurement never taken.
	runner, cleanup, err := l.newBaseline(ctx, in)
	if err != nil {
		return reposcan.FileResult{}, l.fail(j, err)
	}
	// The concurrency probe has run by now (building the baseline runner is
	// what builds — and probes — this file's pool), so its answer is available
	// and is said out loud, per file. The DOWNGRADE is the interesting event:
	// an operator watching a workspace audit crawl has to be able to see that
	// their suite failed under N trees, and why, rather than inferring it from
	// the wall clock. Silent when nothing was measured (the jail substrate,
	// which builds no trees).
	l.noteConcurrency(j.Path, in.concurrency)
	// Deferred, not called inline: a panic in CheckBaselineStable or in the
	// jail beneath it would otherwise leak the vendor staging dir. Released
	// explicitly before l.audit too, since the audit builds its own jail and
	// this one is dead the moment the baseline question is answered.
	released := false
	release := func() {
		if !released {
			released = true
			cleanup()
		}
	}
	defer release()

	// recordingBaseline remembers the outcome CheckBaselineStable observed, so
	// the consistent PASS/FAIL value is available at zero extra cost — no
	// additional suite run just to learn which way a stable baseline went.
	rec := &recordingBaseline{inner: runner}
	stable, err := reposcan.CheckBaselineStable(rec, l.baselineRuns)
	if err != nil {
		return reposcan.FileResult{}, l.fail(j, err)
	}
	if !stable {
		l.note("%s: unstable baseline — not graded\n", j.Path)
		return reposcan.FileResult{Gradable: false, Reason: reposcan.ReasonFlakyBaseline}, nil
	}
	// Stable means every run AGREED — including agreeing to fail. A suite that
	// is consistently red is a legitimate ungradable (that is the existing
	// BaselineFailed case), and auditing it anyway would spend mutant
	// generation, critic calls and a third full suite run to produce a verdict
	// we already know must be discarded. On a repo with a broken suite that is
	// the entire LLM bill, for every file, to reach an answer the first two
	// runs already gave. The BaselineFailed handling below stays as the
	// backstop for anything this misses.
	if !rec.last {
		// Print the runner's OWN output, not just the fact of failure. Twice in
		// one day a paid audit dead-ended here with nothing to go on: a Python
		// venv the offline jail could not see, and a Go repo never diagnosed at
		// all. The refusal to grade is correct; the silence about WHY was not.
		if br, ok := runner.(interface{ BaselineOutput() string }); ok {
			if out := strings.TrimSpace(br.BaselineOutput()); out != "" {
				l.note("%s: baseline does not pass unmutated — not graded. The suite said:\n%s\n", j.Path, indentLines(out, "    "))
				if hint := moduleImportHint(out); hint != "" {
					l.note("%s: %s\n", j.Path, hint)
				}
				return reposcan.FileResult{Gradable: false, Reason: reposcan.ReasonBaselineFailed}, nil
			}
		}
		l.note("%s: baseline does not pass unmutated — not graded\n", j.Path)
		return reposcan.FileResult{Gradable: false, Reason: reposcan.ReasonBaselineFailed}, nil
	}
	release()

	v, err := l.audit(ctx, in)
	if err != nil {
		return reposcan.FileResult{}, l.fail(j, err)
	}
	// Never fabricate a score: a verdict whose baseline could not pass is
	// ungradable WITH ITS REASON, never a 0.0 kill rate that would read as
	// "terrible tests". (Scan re-asserts this; setting it here means the
	// adapter is honest on its own, for any caller.)
	res := reposcan.FileResult{Verdict: v, Gradable: !v.BaselineFailed && !v.SuiteIgnoresFile}
	// Checked BEFORE BaselineFailed so the more specific diagnosis wins: a
	// suite that passes on invalid source is not broken, it is pointed at
	// something other than this file, and an operator told "baseline failed"
	// would go debug the wrong thing.
	if v.SuiteIgnoresFile {
		res.Reason = reposcan.ReasonSuiteIgnoresFile
		l.note("%s: the check command never compiles or imports this file — not graded\n", j.Path)
		return res, nil
	}
	if v.BaselineFailed {
		res.Reason = reposcan.ReasonBaselineFailed
		l.note("%s: baseline failed — not graded\n", j.Path)
		return res, nil
	}
	// ProvenMissed rides along on the same line as DevKillRate/Survivors
	// only when it is trustworthy AND has something to say: on a
	// TestWriterFailed run no compiling test was ever authored, and on a
	// PoolTestUnsound run one was authored but never genuinely graded — a "0
	// proven missed" in either case would misread as a clean result instead
	// of an unproven one (their own notes, printed elsewhere, already cover
	// it); with 0 survivors there is nothing to prove. The remaining case —
	// survivors found, a test was authored and genuinely graded — is exactly
	// where ProvenMissed answers the question a bare kill rate leaves open:
	// did the pool's own test demonstrate a real, catchable bug (corral's
	// strongest claim), or did it try and prove nothing.
	switch {
	case v.Uncovered:
		// The live note is a reader too, and the first one an operator sees.
		// No test executes this file, so its rate measures nothing — printing
		// it here would put the withheld number on screen minutes before the
		// report refuses to.
		l.note("%s: UNCOVERED — no test executes it (rate withheld)\n", j.Path)
	case v.Survivors > 0 && !v.TestWriterFailed && !v.PoolTestUnsound:
		l.note("%s: kill rate %.2f (%d survivor(s), %d proven missed)\n", j.Path, v.DevKillRate, v.Survivors, v.ProvenMissed)
	default:
		l.note("%s: kill rate %.2f (%d survivor(s))\n", j.Path, v.DevKillRate, v.Survivors)
	}
	return res, nil
}

// selectionFor answers ONE job from the scan's single instrumented run: which
// tests demonstrably execute this file, and the narrowed command that runs
// just those. Every non-answer is a Selection with an empty Method and a
// non-empty Fallback saying why — --whole-suite, an unknown language, no
// evidence, or a selector that refused this file — because a whole-suite
// grade under selection is a DIFFERENT measurement and the record has to say
// which one it is.
//
// The base handed to the selector is the operator's `-- <cmd>` when they gave
// one, else the plugin's stock recursive command: the operator's markers and
// flags are honoured, and the selection only ever narrows what they chose.
func (l *localExecutor) selectionFor(j reposcan.Job) lang.Selection {
	if l.wholeSuite {
		return lang.Selection{Fallback: "--whole-suite"}
	}
	p, ok := lang.ByName(j.Lang)
	if !ok {
		return lang.Selection{Fallback: "unknown language " + j.Lang}
	}
	base := l.checkArgv
	if len(base) == 0 {
		base = p.TestCmd()
	}
	return l.selection.For(p, l.repoDir, j.Path, j.TestPath, base)
}

// baseCmd is the job's UNNARROWED command: the operator's `-- <cmd>` if they
// gave one, else the job language's stock recursive command. It is what the
// selector narrowed FROM, and what advpool.DevCommand / the scorer's
// authoredCmd narrow again per pass — see localAuditInput.baseArgv.
func (l *localExecutor) baseCmd(j reposcan.Job) []string {
	if len(l.checkArgv) > 0 {
		return l.checkArgv
	}
	if p, ok := lang.ByName(j.Lang); ok {
		return p.TestCmd()
	}
	return nil
}

// testCmd resolves the command a job is graded with. This ONE function's
// result becomes both the baseline command and the executor's own scoring
// command, which is why the selection is applied here and nowhere else on
// this path: a narrowed scoring run graded against an unnarrowed baseline
// would be comparing different things and would silently corrupt every kill
// rate.
//
// It is advpool.DevCommandArgv — the SAME function the dev pass itself uses —
// rather than a second local rendering of the same rule. The two had already
// drifted on the UNCOVERED case: the dev pass ran the paired test file alone
// (which is what "no test executes this file" MEASURES), while this resolved
// the operator's whole command, so the baseline and the scoring run were not
// the same exam. See TestExecutorBaselineMatchesTheDevPassCommand.
//
// With no selection it falls back exactly as before: the operator's `-- <cmd>`
// if given, else the job language's stock recursive command (baseCmd). Repo-
// aware jail wiring REQUIRES a command (there is no synthetic scaffold to fall
// back on), and a repo can mix languages, so it is resolved per job. An
// unknown language yields nil and the audit fails closed downstream rather
// than grading with someone else's command.
func (l *localExecutor) testCmd(j reposcan.Job, sel lang.Selection) []string {
	return advpool.DevCommandArgv(sel, j.Lang, l.baseCmd(j), j.TestPath)
}

// printLanguageProfile renders the per-language inventory the enumeration
// already knows but used to discard. Everything here is FREE — no model call,
// no jail, no key — which is what makes it the right first thing to show
// somebody pointing corral at a repository for the first time.
//
// "no paired test" is deliberately given equal billing to "auditable": it needs
// no LLM and is often the most actionable line in the report, and it is the
// number that explains a low candidate count instead of leaving it mysterious.
func printLanguageProfile(w io.Writer, profile []reposcan.LanguageStat) {
	if len(profile) == 0 {
		return
	}
	fmt.Fprintln(w, "  languages detected:")
	for _, s := range profile {
		fmt.Fprintf(w, "    %-12s %3d source file(s): %d auditable, %d with no paired test",
			s.Lang, s.Auditable+s.NoPairedTest+s.Ambiguous, s.Auditable, s.NoPairedTest)
		if s.Ambiguous > 0 {
			// Called out separately because this is where corral KNOWS it is
			// uncertain — the right place to ask a human rather than guess.
			fmt.Fprintf(w, ", %d ambiguous", s.Ambiguous)
		}
		if s.TestFiles > 0 {
			fmt.Fprintf(w, " (+%d test file(s))", s.TestFiles)
		}
		fmt.Fprintln(w)
	}
}

// recordingBaseline wraps a BaselineRunner and remembers the last outcome it
// observed. CheckBaselineStable answers "did the runs agree?" but not "agree on
// WHAT"; when it reports stable, every run returned the same value, so `last`
// IS that value — recoverable without paying for another suite run.
type recordingBaseline struct {
	inner reposcan.BaselineRunner
	last  bool
}

func (r *recordingBaseline) RunBaseline() (bool, error) {
	v, err := r.inner.RunBaseline()
	if err != nil {
		return false, err
	}
	r.last = v
	return v, nil
}

// fail reports WHY a job could not be audited and returns the error
// unchanged. Scan collapses every executor error into the single reason
// "executor-error", which is honest about the count but tells an operator
// nothing about the cause — a scan where every file failed for one fixable
// reason (no provider key, no jail on this host) must say so, not just report
// a wall of ungradables.
func (l *localExecutor) fail(j reposcan.Job, err error) error {
	l.note("%s: could not audit: %v\n", j.Path, err)
	return err
}

func (l *localExecutor) note(format string, a ...any) {
	if l.progress == nil {
		return
	}
	fmt.Fprintf(l.progress, "  "+format, a...)
}

// indentLines prefixes every line of s with pad — used to set a subprocess's
// own output apart from corral's report lines, so a multi-line traceback reads
// as quoted evidence rather than as more report.
func indentLines(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// moduleImportHint inspects a failing baseline's own output and, when it
// shows a Python import failing during COLLECTION (ModuleNotFoundError or
// ImportError — pytest's own wording for "never even started running the
// suite"), returns one extra line naming the most common cause: PEP-735
// dependency groups aren't installed by a plain `pip install`, so a project
// that moved its test deps into [dependency-groups] looks, from the outside,
// exactly like a broken baseline. Empty when the output doesn't show that
// shape — this is a hint for one specific failure, not a catch-all.
func moduleImportHint(suiteOutput string) string {
	if !strings.Contains(suiteOutput, "ModuleNotFoundError") && !strings.Contains(suiteOutput, "ImportError") {
		return ""
	}
	return "note: your test dependencies may be missing — PEP-735 [dependency-groups] are not pip extras; try `pip install --group <name>` or install them explicitly"
}

// signableKillRate is the rate a statement or a warehouse row may carry for
// one file: nil for an UNCOVERED file, whose rate the report withholds and
// the ledger stores NULL because no test executes the file and nothing graded
// it. Shared by the attestation and the push so the two can never disagree
// about which numbers are real — a withheld number that leaks into either one
// comes back as fact, and one of them is signed.
func signableKillRate(f reposcan.WeakFile) *float64 {
	if f.Uncovered {
		return nil
	}
	kr := f.KillRate
	return &kr
}

// signableSpread and pushableSpread carry a file's per-mutant spread across
// the package boundary — into the signed statement and into the warehouse
// row — and carry NOTHING when none was measured. Two functions rather than
// one because certify and auditpush each own their copy of the type (see
// their own docs: certify cannot import advpool, and auditpush imports no
// engine package at all); the rule they share is the single nil-check in
// MeasuredSpread, so neither of them re-derives "was this measured?".
func signableSpread(f reposcan.WeakFile) *certify.TestsPerMutantSpread {
	if !f.MeasuredSpread() {
		return nil
	}
	s := f.TestsPerMutant
	return &certify.TestsPerMutantSpread{Min: s.Min, Median: s.Median, Max: s.Max}
}

func pushableSpread(f reposcan.WeakFile) *auditpush.TestsPerMutantSpread {
	if !f.MeasuredSpread() {
		return nil
	}
	s := f.TestsPerMutant
	return &auditpush.TestsPerMutantSpread{Min: s.Min, Median: s.Median, Max: s.Max}
}

// writeAuditStatement renders the scan into certify's in-toto audit statement
// and writes it to path, returning the sha256 (hex) of the bytes it wrote —
// the hash a pushed row carries so it can be traced back to this statement.
//
// scanID is the local scan ledger's row id (0 when --record was not given —
// the honest value, not a sentinel: this statement then signs scanId: 0
// rather than fabricate a ledger row that does not exist).
//
// Every file the report scored is carried, not only the weakest: a statement
// that listed only the failures would be a highlight reel, and the claim a
// reviewer is being asked to accept is about the whole audited surface.
func writeAuditStatement(path, repoDir string, r reposcan.RepoReport, models map[string]string, minKillRate *float64, maxProvenMissed *int, passed bool, scanID int64, bundle auditpush.Bundle) (string, error) {
	files := make([]certify.AuditedFile, 0, len(r.Weakest))
	for _, f := range r.Weakest {
		files = append(files, certify.AuditedFile{
			Path:             f.Path,
			KillRate:         signableKillRate(f),
			Survivors:        f.Survivors,
			ProvenMissed:     f.ProvenMissed,
			TimedOut:         f.TimedOut,
			TestWriterFailed: f.TestWriterFailed,
			PoolTestUnsound:  f.PoolTestUnsound,
			// The statement is the one artifact a third party verifies, so it
			// must say which measurement it is signing — and must not sign a
			// rate for a file nothing executes.
			TestSelection:         f.SelectionMethod,
			ProvenByAuthoredAlone: f.ProvenByAuthoredAlone,
			// And which shape earned that proven count. A verifier comparing
			// two signed statements has to be able to see that one file's
			// writer got a call per survivor and the other's got one call
			// for all of them.
			WriterMode:        f.WriterMode,
			SelectedTests:     f.SelectedTests,
			SuiteTests:        f.SuiteTests,
			SelectionFallback: f.SelectionFallback,
			Uncovered:         f.Uncovered,
			// And at which grain: a signed rate averaged over mutants that
			// each faced a different test set needs the spread to be read
			// as the measurement it is.
			PerMutant:      f.PerMutant,
			TestsPerMutant: signableSpread(f),
			// How many private trees scored this file at once, or why it
			// only got one — the same fact the screen and the ledger say —
			// plus the dep dirs those trees shared, which is where the
			// isolation the count implies does not hold.
			Trees:           f.Trees,
			ConcurrencyNote: f.ConcurrencyNote,
			SharedDirs:      f.SharedDirs,
			// What a mutant-generator shard actually saw — see
			// certify.AuditedFile.PromptShape's doc.
			PromptShape: f.PromptShape,
			// Whether this file's goal was served from the goal cache — see
			// certify.AuditedFile.GoalReused's doc.
			GoalReused: f.GoalReused,
		})
	}
	// Same resolution the warehouse push uses: a statement whose subject names
	// no revision is worse than none — it looks authoritative and binds to
	// nothing — and two copies of this logic would drift.
	repo, commit, err := auditSubject(repoDir, r)
	if err != nil {
		return "", fmt.Errorf("refusing to write an audit statement: %w", err)
	}

	// The WHOLE bundle this scan would push (or did, if --push also ran) —
	// all five grains, not just the file rows. Hashing only the file rows
	// would sign a fraction of what the push writes, and the mutant grain is
	// exactly where the evidence behind proven_missed lives.
	//
	// Every StatementSHA256 is blanked first: the statement's own hash cannot
	// depend on itself. Hashed and signed here so a verifier holding the
	// statement and the pushed rows can check either one against the other,
	// in either order, rather than trusting only the row's one-way pointer
	// back.
	rowsSHA, err := warehouseRowsSHA256(bundle)
	if err != nil {
		return "", err
	}

	stmt := certify.BuildAuditAttestation(certify.AuditStatement{
		Repo:                repo,
		Commit:              commit,
		Files:               files,
		Audited:             r.Audited,
		Candidates:          r.Candidates,
		ModelsByRole:        models,
		MinKillRate:         minKillRate,
		MaxProvenMissed:     maxProvenMissed,
		Passed:              passed,
		ScanID:              scanID,
		WarehouseRowsSHA256: rowsSHA,
	})
	b, err := json.MarshalIndent(stmt, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// warehouseRowsSHA256 is the hex sha256 of the bundle's canonical JSON, with
// BlankUnpushedSource applied and every StatementSHA256 blanked — the value
// the signed statement carries as warehouseRowsSha256.
//
// Blanking the statement hash is not cosmetic: the pushed rows carry the hash
// of the statement that names them, so hashing them WITH that field would
// make the statement's hash depend on itself.
//
// WHAT THIS HASH IS, EXACTLY: a SELF-CONSISTENCY CHECK over the canonical
// JSON of the bundle this process built and pushed. It binds the statement to
// the rows the run produced, so the two cannot be swapped or edited
// independently, and a holder of BOTH artifacts can check them against each
// other in either order.
//
// WHAT IT IS NOT: a hash a third party can reproduce from the warehouse
// alone. The writer's SQL conversions are lossy by design, and rebuilding the
// bundle from a SELECT would not give these bytes back:
//
//   - kill_rate is written NULL for an uncovered file (insertFileRow), while
//     the bundle row carries the *float64 the run held. NULL back out is not
//     the value that went in.
//   - tests_per_mutant_min/median/max are dropped entirely unless PerMutant
//     is set, so a whole-suite row's spread — present in the bundle, absent
//     from the table — cannot be recovered.
//
// (The source columns are NOT in this category: BlankUnpushedSource runs
// here and in PushBundle, from the same function, so what is hashed is what
// the warehouse receives.)
//
// So the honest claim is "the statement and the pushed rows came from one
// run and neither has been altered since", not "anyone can recompute this
// from your warehouse". Reproducing it needs the bundle, which means the
// pushing side. Do not upgrade the claim in a doc or a README without first
// making the two conversions above lossless.
func warehouseRowsSHA256(b auditpush.Bundle) (string, error) {
	// The hash must cover what the WAREHOUSE receives, not what this process
	// happens to hold. Without --push-source the writer stores SQL NULL for
	// the source columns, so hashing them here would sign a number no
	// verifier could ever reproduce from the rows they can actually see — a
	// cross-check that never checks out is worse than none.
	//
	// THE SAME FUNCTION THE WRITER CALLS (auditpush.PushBundle runs it too).
	// This rule used to be spelled out twice, and two copies of a custody set
	// is one copy that gets a field added to it and one that does not.
	auditpush.BlankUnpushedSource(&b)
	b.Scan.StatementSHA256 = ""
	b.Files = append([]auditpush.Row(nil), b.Files...)
	for i := range b.Files {
		b.Files[i].StatementSHA256 = ""
	}
	b.Mutants = append([]auditpush.MutantRow(nil), b.Mutants...)
	for i := range b.Mutants {
		b.Mutants[i].StatementSHA256 = ""
	}
	b.Calls = append([]auditpush.ModelCallRow(nil), b.Calls...)
	for i := range b.Calls {
		b.Calls[i].StatementSHA256 = ""
	}
	b.Events = append([]auditpush.EventRow(nil), b.Events...)
	for i := range b.Events {
		b.Events[i].StatementSHA256 = ""
	}
	// Link is deliberately NOT hashed: it holds the statement hash itself.
	b.Link = auditpush.Link{}
	js, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(js)
	return hex.EncodeToString(sum[:]), nil
}

// stampStatementSHA256 opens the ledger, stamps one scan's statement hash,
// and closes it again — one more operation-scoped handle, for the same
// reason as every other one in this function: DuckDB is single-writer per
// file, and a reader must never have to wait out an audit to see the audits
// that came before it.
func stampStatementSHA256(dsn string, scanID int64, sha string) error {
	st, err := scanstore.Open(dsn)
	if err != nil {
		return err
	}
	if uerr := st.SetStatementSHA256(context.Background(), scanID, sha); uerr != nil {
		_ = st.Close()
		return uerr
	}
	return st.Close()
}

// stampRekorReceipt mirrors stampStatementSHA256: open the ledger, stamp one
// scan's --transparency receipt, close it again. logIndex is passed through
// as the pointer it already is — nil never reaches here (the caller only
// calls this after a successful upload), but the store's own SetRekorReceipt
// keeps the nil-means-not-uploaded contract regardless of the caller.
func stampRekorReceipt(dsn string, scanID int64, logIndex *int64, uuid string) error {
	st, err := scanstore.Open(dsn)
	if err != nil {
		return err
	}
	if uerr := st.SetRekorReceipt(context.Background(), scanID, logIndex, uuid); uerr != nil {
		_ = st.Close()
		return uerr
	}
	return st.Close()
}

// pushBundle appends one recorded scan, at all five grains, to a warehouse
// the operator owns. It replaced pushAuditRows, which built its rows from
// the REPORT — and so pushed only the files a scan audited, leaving the
// files corral refused invisible to the one question the warehouse exists to
// answer.
//
// A thin wrapper on purpose: the mapping lives in certify_repo_bundle.go and
// there is only one of it.
func pushBundle(target string, b auditpush.Bundle) (auditpush.Counts, error) {
	return auditpush.PushBundle(target, b)
}

// gitHeadCommit resolves repoDir's HEAD, or "" when repoDir is not a checkout
// (or git is unavailable). Callers turn "" into an explicit refusal rather than
// recording a row that names no revision.
func gitHeadCommit(repoDir string) string {
	// #nosec G204 -- fixed argv; repoDir is the operator's own --repo path, never remote input
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// auditSubject resolves the repo and commit a statement or a warehouse row is
// bound to, refusing rather than emitting one that names no revision.
func auditSubject(repoDir string, r reposcan.RepoReport) (repo, commit string, err error) {
	commit = strings.TrimSpace(r.Commit)
	if commit == "" {
		commit = gitHeadCommit(repoDir)
	}
	if commit == "" {
		return "", "", fmt.Errorf("no commit: a verdict that names no revision cannot be verified or joined to anything. Pass --commit <sha>, or run inside a git checkout")
	}
	return resolveRepoName(repoDir, r.Repo), commit, nil
}

// resolveRepoName names the repository a row is filed under: an explicit name
// wins, then origin's URL, then the checkout directory's own name.
//
// A checkout with no remote still has to name itself: repo is the KEY
// dimension in a warehouse that spans projects, and a row filed under "."
// cannot be told apart from every other project audited from a directory. The
// absolute path's last element is a poor name but a distinguishing one, which
// is the whole requirement here.
func resolveRepoName(repoDir, given string) string {
	repo := strings.TrimSpace(given)
	if repo != "" && repo != "." {
		return repo
	}
	// #nosec G204 -- fixed argv; repoDir is the operator's own --repo path, never remote input
	if out, gerr := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").Output(); gerr == nil {
		repo = strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	}
	if repo == "" || repo == "." {
		if abs, aerr := filepath.Abs(repoDir); aerr == nil {
			repo = filepath.Base(abs)
		}
	}
	return repo
}

// writerModeDisclosure renders the per-file writer line: which shape the
// writer seat attacked in, and how many calls that took.
//
// Shared by the live report and `corral scans show` for the same reason
// timingLine and concurrencyDisclosure are: the sentence an operator reads
// during the run and the one they read back out of the ledger must be the
// same sentence, or the two will drift and a reader will have to work out
// whether they mean the same thing.
//
// Returns "" when the mode was NOT RECORDED — a run whose caller named no
// mode, or a verdict earned before the mode existed. Silence is the honest
// rendering: neither spelling is true of such a row, and printing one would
// be an invented fact about how a measurement was made.
func writerModeDisclosure(mode string, calls, seatsUngraded int) string {
	if strings.TrimSpace(mode) == "" {
		return ""
	}
	// The call count is a MEASUREMENT (the seat's own ledger row), so 0 means
	// "no cost row for this seat", not "the writer made no calls" — a cached
	// verdict and a pre-cost-column row both look like that. The mode is
	// still worth saying on its own.
	if calls <= 0 {
		return "writer: " + mode
	}
	unit := "calls"
	if calls == 1 {
		unit = "call"
	}
	// Partial failure, said out loud. Neither WRITER FAILED nor TEST UNSOUND
	// fires when SOME seats graded, so without this a file where three of
	// twenty-four survivors were never actually attempted reads exactly like
	// one where all twenty-four were.
	if seatsUngraded > 0 {
		return fmt.Sprintf("writer: %s (%d %s, %d seats ungraded — those survivors were never attempted, so the proven count is over the rest)",
			mode, calls, unit, seatsUngraded)
	}
	return fmt.Sprintf("writer: %s (%d %s)", mode, calls, unit)
}
