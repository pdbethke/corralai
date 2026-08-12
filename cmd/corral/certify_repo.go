// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// defaultScanTop bounds a scan by default. Provisional: large enough to be
// useful, small enough to quote a price. Revisit against a real third-party
// repo scan before relying on it.
const defaultScanTop = 25

// defaultDeriveModel is the goal-deriver's model. It is intentionally the same
// tier as the mutant generator: deriving one sentence from a file is not the
// hard part of this pipeline.
const defaultDeriveModel = defaultLocalMutantModel

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
	deriveModel := fs.String("derive-model", defaultDeriveModel, "model that derives a goal per file when --goals is not given")
	// Per-role models. `certify --local` has had these all along; without them
	// here a repo scan was locked to the Claude defaults with no override.
	writerModelFlag := fs.String("writer-model", "", "model for the test-writer role (default "+defaultLocalWriterModel+")")
	mutantModelFlag := fs.String("mutant-model", "", "model for the mutant-generator role (default "+defaultLocalMutantModel+")")
	criticModelFlag := fs.String("critic-model", "", "model for the test-critic role, which must differ from the writer's; \"off\" disables the critic entirely (it is advisory and never gates the verdict, so a single-vendor run with only one usable model can drop it) (default "+defaultLocalCriticModel+")")
	scopeTestsFlag := fs.Bool("scope-tests", false, "grade each file against its OWN paired test file instead of the project's whole suite. MUCH faster — scoring runs the suite once per mutant, so this collapses an O(mutants x suite runtime) cost — but it CHANGES THE MEASUREMENT: a mutant that some unrelated test happened to catch now reads as a survivor, so the reported gap count can go UP. Ignored when an explicit -- <cmd> is given, and for languages with no verified per-file invocation")
	shadowModelFlag := fs.String("shadow-model", "", "challenger model that attacks every region a SECOND time (default "+defaultLocalShadowModel+"; \"off\" disables). Recorded for comparison — NEVER gates the verdict. The default is a Claude model, so a non-Anthropic scan must set or disable it")
	owner := fs.String("owner", "local", "owning account for the scan (tenant identifier)")
	commit := fs.String("commit", "", "commit SHA the report is bound to")
	swarmFlag := fs.Int("swarm", 0, "max concurrent audit workers (0 = auto-size to this host's cores)")
	dryRun := fs.Bool("dry-run", false, "enumerate and emit jobs, then stop — no audits run")
	jsonOut := fs.Bool("json", false, "with --dry-run, emit the repository's audit surface as JSON instead of the human report: per-language counts, every auditable file with its inferred test pairing, and the machine-stable exclusion tally. Needs no key, no jail and no money — it is the free inventory a UI or a tenant's own tooling can consume instead of scraping stdout")
	substrateFlag := fs.String("substrate", substrateJail, "where the audit runs: "+substrateJail+" (bwrap) or "+substrateWorkspace+" (mutate --repo in place; the caller IS the isolation boundary, e.g. an ephemeral CI runner)")
	diffBase := fs.String("diff-base", "", "bound the scan to files changed since this git ref, instead of ranking + --top. In a PR the diff IS the bound: ranking and --top do not apply on this path")
	minKillRateFlag := fs.String("min-kill-rate", "", "fail the scan (exit 1) if ANY audited file's kill rate is below this value (0.0-1.0 inclusive; a minimum, so a file exactly at the threshold passes). Opt-in: unset by default, so exit codes are unchanged unless this is given. Applies PER FILE, not to the aggregate — a well-tested file must not mask a weak one")
	preflightFlag := fs.Bool("preflight", false, "run the project's test suite once with coverage instrumentation and report which source files it never executes. One extra suite run; reports coverage-grade evidence, not proof")
	recordFlag := fs.Bool("record", false, "record every file this scan audited or rejected, and why, into the DuckDB scan ledger (default: off). A BOOL here — unlike `certify --local`'s --record, which takes a tape PATH — see --record-db for where the ledger goes. A recording failure never changes the scan's verdict or exit code")
	recordDSNFlag := fs.String("record-db", "", "path to the scan ledger (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb)")
	timeoutFlag := fs.Duration("timeout", 10*time.Minute, "per-file budget: give up on a single file's run if it makes no progress for this long (not a hard wall-clock cap — a single slow LLM call can overshoot it). Same default and semantics as `certify --local`'s --timeout; raise it for a large file that needs more room to converge")
	if err := fs.Parse(flagArgs); err != nil {
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
			if changedSet[c.Path] {
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
		ex.scopeTests = *scopeTestsFlag
		ex.models = auditModels{
			writer: *writerModelFlag, mutant: *mutantModelFlag,
			critic: *criticModelFlag, shadow: *shadowModelFlag,
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
			cmdName:     "corral certify --repo",
			writerModel: *writerModelFlag,
			mutantModel: *mutantModelFlag,
			criticModel: *criticModelFlag,
			shadowModel: *shadowModelFlag,
		}, stderr); err != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
			if isAuditUsageError(err) {
				return 2
			}
			return 1
		}
	}

	gs, disclosure, code := resolveGoalSource(stderr, *repoDir, *goalsPath, *deriveModel, *dryRun, len(selected), newLLMDeriver)
	if code != 0 {
		return code
	}
	// Printed on EVERY path that has something to disclose. A machine-invented
	// goal has no goal-critic — a goal cannot be executed, so a second model
	// grading the first would be opinion on opinion — which means DISCLOSURE is
	// the accountability mechanism: the reader is told what question was asked
	// and by whom, and execution answers it afterwards through mutant yield.
	if disclosure != "" {
		fmt.Fprintln(stdout, disclosure)
	}

	// The resolved role models are part of a verdict's identity: an audit run
	// with a different mutant-generator is a different audit. Until this was
	// wired, EmitConfig hardcoded ModelSet: "unset", so the cache key could
	// not tell two model sets apart and every ledger row recorded "unset" —
	// meaning the ledger could not be used to grade the models it exists to
	// grade.
	rmWriter, rmMutant, rmCritic, rmShadow := resolveRoleModels(localAuditInput{
		writerModel: *writerModelFlag,
		mutantModel: *mutantModelFlag,
		criticModel: *criticModelFlag,
		shadowModel: *shadowModelFlag,
	})
	modelSet := modelSetKey(rmWriter, rmMutant, rmCritic, rmShadow)

	// AuditConfig, like ModelSet above, is part of a verdict's identity: it
	// carries the flags that change what a mutant run against a given file
	// MEASURES, not which files get audited. See auditConfigKey for the
	// inclusion/exclusion rationale.
	auditConfig := auditConfigKey(*scopeTestsFlag, checkArgv)

	cfg := reposcan.EmitConfig{
		Owner: *owner, Repo: filepath.Base(*repoDir), Commit: *commit, Root: *repoDir,
		EngineVersion: reposcan.VerdictGeneration, ModelSet: modelSet, AuditConfig: auditConfig,
		Substrate: *substrateFlag,
		// What the verdicts are GRADED BY decides what TestSurfaceDigest has
		// to cover — one paired test file, or the whole suite. Both are
		// computed from the same facts testCmd itself resolves the command
		// from, so the key can never claim a narrower surface than the one
		// that actually ran.
		FileScopedTests:  gradesFileScoped(checkArgv, *scopeTestsFlag, selected),
		TestSurfacePaths: testSurfacePaths(cands, excl),
	}
	jobs, goalExcl, err := reposcan.EmitJobs(cfg, selected, gs)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: emitting jobs: %v\n", err)
		return 1
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

	// Opened here, BEFORE the scan, so ONE handle serves two purposes: it is
	// the verdict cache's read path during reposcan.Scan below (see
	// newLedgerCache), and it is the ledger's write path in the --record
	// block at the end of this function. Opening it only at the end, as this
	// used to, would mean two separate handles on the same DuckDB file within
	// one process — the cache would have nothing to read from during the
	// scan it exists to speed up.
	//
	// This does NOT change --record's own meaning or when recording happens:
	// the store is opened only when *recordFlag is set, exactly as it was
	// opened only inside the (now-shorter) --record block before. --record-db
	// alone still records nothing, silently — that behaviour is entirely
	// gated by *recordFlag below, unchanged.
	//
	// One consequence is correct but surprising, so it is stated here rather
	// than discovered: the handle is now held for the WHOLE scan, not just the
	// instant of the final write. DuckDB is single-process-exclusive on a
	// file, so during a long `--record` audit a concurrent `corral scans`
	// against the same (default) DSN cannot open the ledger at all — see
	// openScanStore, which already says so in its error. Nothing is lost; the
	// reader simply has to wait, or be pointed at a copy.
	//
	// An unopenable DSN does not fail the scan: scanStoreErr is carried
	// forward and reported in the --record block at the bottom, in the same
	// place and the same words a write failure has always been reported —
	// this is not a new failure path, just the existing one's error surfacing
	// earlier in the run than the write itself does.
	var scanStore *scanstore.Store
	var scanStoreErr error
	if *recordFlag {
		dsn := *recordDSNFlag
		if dsn == "" {
			dsn = defaultScanDSN()
		}
		st, err := scanstore.Open(dsn)
		if err != nil {
			scanStoreErr = err
		} else {
			scanStore = st
			// Deferred here, not in the --record block: this function has
			// several early-return paths above --record's own site (the
			// argvErr refusal just above, for one), and every one of them
			// must still release the DuckDB handle.
			defer func() { _ = scanStore.Close() }()
		}
	}

	workers, swarmReadout := resolveScanWorkers(*swarmFlag, *substrateFlag)
	fmt.Fprint(stdout, swarmReadout)
	mutantConc := resolveMutantConcurrency(resolveSwarm(*swarmFlag), *substrateFlag, workers, len(jobs))
	if mutantConc > 1 {
		fmt.Fprintf(stdout, "  mutant scoring: %d at once per file — the jail budget file-parallelism cannot spend (scoring runs the suite once per mutant, so this is the dominant cost on any repo with a real suite)\n", mutantConc)
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
	// scanStore was opened above, BEFORE this call, specifically so it could
	// serve as the cache's read path here: newLedgerCache(nil) (no --record,
	// or an unopenable DSN) already misses every key, so this needs no extra
	// nil-guard — the cache is simply inactive for the run.
	results := reposcan.Scan(context.Background(), jobs, ex, newLedgerCache(scanStore), workers)
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
	printRepoReport(stdout, rep, nothingInScope, minKillRate, unpairableInDiff, oldestReused)
	// A distinct section, never folded into Excluded/Ungradable/the audited
	// fraction: this is an inventory alongside the audit, not a change to
	// it (see the brief). Printed unconditionally when the flag was given,
	// even when the pre-flight could not run at all.
	if *preflightFlag {
		printPreflightReport(stdout, preflightResult, preflightSources)
	}

	exitCode := repoScanExitCode(rep, nothingInScope, minKillRate)

	// FAIL-OPEN, deliberately, and this is the one place in corral where
	// uncertainty must not fail closed: this command's exit code is a CI
	// merge gate. If a ledger write could change it, a full disk or a busy
	// DuckDB file would red-build a pull request over bookkeeping. So a
	// recording failure is printed loudly on stderr and the verdict and
	// exit code decided above stand unchanged. Do not "fix" this into a
	// failure path — that is precisely the bug this comment exists to head
	// off. Placed after `code` is computed, and calling nothing that can
	// panic into the exit path below.
	if *recordFlag {
		if scanStoreErr != nil {
			// The open failure happened earlier (above, before the scan) but
			// is reported HERE — the same place and the same words a write
			// failure has always been reported in, so this is not a new
			// failure surface, just the pre-existing one's error arriving
			// from an earlier point in the run.
			fmt.Fprintf(stderr, "corral certify --repo: scan ledger NOT written: %v\n", scanStoreErr)
		} else {
			scan := scanstore.Scan{
				Owner: *owner, Repo: cfg.Repo, Commit: *commit,
				Substrate: *substrateFlag, EngineVersion: version, ModelSet: cfg.ModelSet,
				Top: effectiveTop, AllCandidates: *allFlag, DiffBase: *diffBase,
				TotalFiles: totalFiles, Candidates: rep.Candidates, Audited: rep.Audited,
				KillRate: killRatePtr(rep.KillRate), CacheHits: rep.CacheHits,
				PreflightRan: preflightResult.Ran, PreflightNote: preflightResult.Note,
				StartedAt: startedAt, FinishedAt: time.Now(),
			}
			files := buildScanFileRows(results, rep.Excluded, preflightResult, stderr)
			if err := recordCertifyRepoScan(scanStore, scan, files, results, stderr); err != nil {
				fmt.Fprintf(stderr, "corral certify --repo: scan ledger NOT written: %v\n", err)
			}
		}
	}

	return exitCode
}

// auditConfigKey is the canonical KeyInputs.AuditConfig: the settings that can
// change a given FILE's measured verdict.
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
// Bias when adding to this list: include. Over-inclusion causes a needless
// miss, which costs money. Under-inclusion serves a stale verdict, which
// signs an unmeasured claim.
func auditConfigKey(scopeTests bool, checkArgv []string) string {
	m := map[string]string{}
	if scopeTests {
		m["scope-tests"] = "true"
	}
	if len(checkArgv) > 0 {
		sum := sha256.Sum256([]byte(strings.Join(checkArgv, "\x00")))
		m["check-argv"] = hex.EncodeToString(sum[:])
	}
	return reposcan.CanonicalKV(m)
}

// testSurfacePaths is every test file this repository's whole recursive suite
// would run — the grading surface of a scan with no --scope-tests and no
// explicit `-- <cmd>`. Two sources, because neither alone is the suite:
//
//   - every CANDIDATE's paired test, including candidates --top or --diff-base
//     left unselected. The suite does not stop running a test because this
//     scan chose not to audit its source file.
//   - every file Enumerate rejected as `is-test`. A shared helper, a
//     conftest.py or a fixture module is nobody's paired test, so it appears
//     in no Candidate.TestPath — and weakening one is exactly the change that
//     silently made a suite worse while every file's key stayed put.
//
// No new walk: both lists are already in hand at the call site.
func testSurfacePaths(cands []reposcan.Candidate, excl []reposcan.Exclusion) []string {
	paths := make([]string, 0, len(cands))
	for _, c := range cands {
		if c.TestPath != "" {
			paths = append(paths, c.TestPath)
		}
	}
	for _, e := range excl {
		if e.Reason == reposcan.ReasonIsTest {
			paths = append(paths, e.Path)
		}
	}
	return paths
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
//   - --scope-tests only takes effect for a language with a VERIFIED per-file
//     invocation and a paired test to scope to; otherwise testCmd silently
//     falls back to the full suite. If even one selected file would fall back,
//     this returns false for the whole scan. That over-invalidates the genuinely
//     scoped files, which only costs money — the other direction would key a
//     whole-suite grading run as if one file were the surface.
func gradesFileScoped(checkArgv []string, scopeTests bool, selected []reposcan.Candidate) bool {
	if len(selected) == 0 {
		return false
	}
	if len(checkArgv) > 0 {
		named := make(map[string]bool, len(checkArgv))
		for _, tok := range checkArgv {
			t := filepath.ToSlash(tok)
			// A pytest node id (`tests/test_a.py::test_x`) names the same file.
			if i := strings.Index(t, "::"); i >= 0 {
				t = t[:i]
			}
			named[path.Clean(t)] = true
		}
		for _, c := range selected {
			if c.TestPath == "" {
				return false
			}
			if !named[path.Clean(filepath.ToSlash(c.TestPath))] {
				return false
			}
		}
		return true
	}
	if !scopeTests {
		return false
	}
	for _, c := range selected {
		p, ok := lang.ByName(c.Lang)
		if !ok {
			return false
		}
		fs, canScope := p.(lang.FileScopedTester)
		if !canScope {
			return false
		}
		if _, scoped := fs.FileScopedTestCmd(c.TestPath); !scoped {
			return false
		}
	}
	return true
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

// resolveGoalSource picks where goals come from and returns the ONE line that
// discloses it. Split out of runCertifyRepo so both the choice and its
// disclosure are testable: on the derived path there is no goal-critic, so this
// line is the entire accountability mechanism for a machine-invented goal.
//
// An empty disclosure means there is nothing to disclose — a hand-written
// --goals map is the operator's own claim, and a scan that selected nothing
// will never ask for a goal at all.
//
// Returns the process exit code to use on failure; 0 means the source is good.
func resolveGoalSource(stderr io.Writer, repoDir, goalsPath, deriveModel string, dryRun bool, nSelected int, newDeriver deriverFactory) (reposcan.GoalSource, string, int) {
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
		// Constructed only when there is something to derive FOR. It fails
		// closed on a missing credential, which is the right answer for a real
		// scan — but demanding a provider key to report "0 candidates" would
		// refuse a scan that was never going to call a model.
		d, derr := newDeriver(deriveModel)
		if derr != nil {
			fmt.Fprintf(stderr, "corral certify --repo: %v\n", derr)
			return nil, "", 2
		}
		return reposcan.NewDerivingGoalSource(repoDir, d, deriveModel, version, 3),
			fmt.Sprintf("  goals derived per file by %s@%s — no goal-critic; each goal is judged after the fact by mutant yield", deriveModel, version),
			0
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

	var runner coverageRunner
	var files map[string]string
	var repoRoot string

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
		runner = adequacy.NewWorkspaceRunner(l.repoDir, preflightTimeout,
			adequacy.WithWorkspaceMaxOutput(preflightMaxOutput),
			adequacy.WithPerRunEnv(plug.WorkspaceRunEnv))
		files = map[string]string{}
	} else {
		if l.jailErr != nil {
			return reposcan.CoverageMap{Note: fmt.Sprintf("preflight: %v", l.jailErr)}
		}
		if l.seeds == nil {
			return reposcan.CoverageMap{Note: "preflight: no repo seed available"}
		}
		seed, serr := l.seeds.get(langName)
		if serr != nil {
			return reposcan.CoverageMap{Note: fmt.Sprintf("preflight: jail preparation failed for %s: %v", langName, serr)}
		}
		// A jail/enumerator built SPECIFICALLY for this one call — never
		// reused for the scan's ordinary per-mutant runs, which must keep
		// sandbox.Run's stock 16 KiB default (see preflightMaxOutput).
		runner = adequacy.NewEnumerator(l.iso, preflightTimeout,
			adequacy.WithReadOnlyBinds(seed.binds), adequacy.WithMaxOutput(preflightMaxOutput))
		files = seed.files
		// Same reasoning as the workspace branch: cmd.Dir is the jail's own
		// ephemeral workspace root, which IS the seeded repo root, so
		// coverage.py's paths are already relative to it.
	}
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
func repoScanExitCode(r reposcan.RepoReport, nothingInScope bool, minKillRate *float64) int {
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
			if f.KillRate < *minKillRate {
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

// printWeakFile prints one "weakest files" line, including the marker and
// the disambiguating proven-missed count — factored out so the truncation
// fallback (F4, below) renders a byte-identical line for a file that falls
// outside the top 10.
func printWeakFile(w io.Writer, f reposcan.WeakFile) {
	marker := ""
	switch {
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
	fmt.Fprintf(w, "    %.2f  %s %s%s\n", f.KillRate, f.Path, detail, marker)

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
		for _, line := range strings.Split(strings.TrimRight(f.AuthoredTest, "\n"), "\n") {
			fmt.Fprintf(w, "        %s\n", line)
		}
	}
}

// unpairableInDiff, when non-empty, names source files the diff CHANGED that
// corral could not pair with a test. It exists to keep the merge gate honest in
// the one case where a green result is actively misleading: a zero-candidate
// diff has two causes, and they are not the same answer.
func printRepoReport(w io.Writer, r reposcan.RepoReport, nothingInScope bool, minKillRate *float64, unpairableInDiff []string, oldestReused time.Time) {
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
	// Sorted, like printExclusions: map iteration order is random, and a
	// report a later slice signs and anchors has to be byte-reproducible.
	ungradableReasons := make([]string, 0, len(r.Ungradable))
	for reason := range r.Ungradable {
		ungradableReasons = append(ungradableReasons, reason)
	}
	sort.Strings(ungradableReasons)
	for _, reason := range ungradableReasons {
		fmt.Fprintf(w, "  ungradable: %d (%s)\n", r.Ungradable[reason], reason)
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
		var breaches []reposcan.WeakFile
		for _, f := range r.Weakest {
			if f.KillRate < *minKillRate {
				breaches = append(breaches, f)
			}
		}
		if len(breaches) > 0 {
			fmt.Fprintf(w, "  KILL-RATE BREACH: %d file(s) below --min-kill-rate %.2f:\n", len(breaches), *minKillRate)
			for _, f := range breaches {
				fmt.Fprintf(w, "    %.2f  %s (%.2f below threshold)\n", f.KillRate, f.Path, *minKillRate-f.KillRate)
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
// asked for. Every job on that substrate mutates the SAME checkout in place
// (adequacy.NewWorkspaceRunner over --repo), and the apply/restore ledger is
// per-runner: it assumes exclusivity. Run two jobs at once and job B's suite
// executes while job A has a mutant — or adequacy.CanaryCode, which does not
// compile — written into A's file. B's surviving mutants are then recorded as
// KILLED (an inflated kill rate on a record this product signs) and B's
// baseline can fail into a spurious baseline-failed/flaky-baseline. Neither
// is detectable after the fact.
//
// The fix is serialization, not a per-job tree copy: copying the tree per job
// is the memory ceiling this substrate exists to escape. That cost is real, so
// the readout STATES the clamp rather than silently differing from --swarm.
func resolveScanWorkers(swarmFlag int, substrate string) (int, string) {
	if substrate == substrateWorkspace {
		return 1, fmt.Sprintf("  swarm: 1 worker — --substrate %s mutates one checkout in place, so jobs run one at a time\n", substrateWorkspace)
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
// The workspace substrate is pinned to 1 and cannot spend the budget on this
// axis at all: adequacy.WorkspaceRunner mutates ONE checkout in place with NO
// mutex, so two concurrent applyFiles interleave and one job's suite runs
// against another's mutant — recording SURVIVORS AS KILLED and signing an
// inflated kill rate that is undetectable after the fact. Unlike the file axis
// (where serialization is a throughput choice) this one is a correctness
// boundary. Fails closed: any degenerate budget/worker count yields 1, never
// unbounded.
func resolveMutantConcurrency(budget int, substrate string, workers, jobs int) int {
	if substrate == substrateWorkspace {
		return 1
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
type auditModels struct{ writer, mutant, critic, shadow string }

type localExecutor struct {
	// models are the per-role overrides threaded into every job's
	// localAuditInput (see auditInputFor).
	models auditModels

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

	// scopeTests runs each file's mutants against its OWN paired test file
	// instead of the project's whole suite. Off by default because it changes
	// the MEASUREMENT, not just the cost — see lang.FileScopedTester.
	scopeTests bool

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
	return localAuditInput{
		repoDir:  l.repoDir,
		codePath: j.Path,
		testPath: j.TestPath,
		goal:     j.Goal.Text,
		lang:     j.Lang,
		repo:     j.Repo,
		iso:      l.iso,
		// "local" rather than "" when the operator gave no --commit: an empty
		// commit makes auditOneFile fall back to `git rev-parse HEAD` — which
		// reads the CWD's repo, not the SCANNED one, and would stamp every
		// verdict with an unrelated sha.
		commit:    orDefault(j.Commit, "local"),
		checkArgv: l.testCmd(j),
		substrate: l.substrate,
		timeout:   l.timeout,
		// See localExecutor.perFileSwarm: 1 everywhere the scan really does
		// fan out over files, and the otherwise-unspent budget on the
		// workspace substrate, which serializes them.
		swarm: l.effectivePerFileSwarm(),
		// The other half of the same budget: files in parallel x mutants in
		// parallel. Reaches only the bwrap-jail scorer, never the workspace
		// runner — resolveMutantConcurrency pins workspace at 1.
		mutantConcurrency: l.mutantConcurrency,
		// H1a produces a REPORT, not a sealed statement: no ledger, no
		// signing key, no scorecard feed (N concurrent audits must not
		// contend on one single-process DuckDB file). Signing is H1c.
		stdout: io.Discard,
		stderr: io.Discard,

		// Per-role models, empty unless the operator passed the flags — the
		// zero value keeps auditRoles' own Claude defaults, so an invocation
		// that names none behaves exactly as before.
		writerModel: l.models.writer,
		mutantModel: l.models.mutant,
		criticModel: l.models.critic,
		shadowModel: l.models.shadow,
	}
}

func (l *localExecutor) Execute(ctx context.Context, j reposcan.Job) (reposcan.FileResult, error) {
	in := l.auditInputFor(j)

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
	if v.Survivors > 0 && !v.TestWriterFailed && !v.PoolTestUnsound {
		l.note("%s: kill rate %.2f (%d survivor(s), %d proven missed)\n", j.Path, v.DevKillRate, v.Survivors, v.ProvenMissed)
	} else {
		l.note("%s: kill rate %.2f (%d survivor(s))\n", j.Path, v.DevKillRate, v.Survivors)
	}
	return res, nil
}

// testCmd resolves the project's own test command for one job: the operator's
// `-- <cmd>` if given, else the job language's stock recursive command.
// Repo-aware jail wiring REQUIRES a command (there is no synthetic scaffold to
// fall back on), and a repo can mix languages, so it is resolved per job. An
// unknown language yields nil and the audit fails closed downstream rather
// than grading with someone else's command.
// testCmd resolves the command a job is graded with. This ONE function's result
// becomes both the baseline command and the scoring command, which is why
// --scope-tests is applied here and nowhere else: a scoped scoring run graded
// against an unscoped baseline would be comparing different things and would
// silently corrupt every kill rate.
func (l *localExecutor) testCmd(j reposcan.Job) []string {
	// An operator who named a command has already chosen the surface; scoping
	// must never rewrite it out from under them.
	if len(l.checkArgv) > 0 {
		return l.checkArgv
	}
	p, ok := lang.ByName(j.Lang)
	if !ok {
		return nil
	}
	if l.scopeTests {
		// Only for a language whose per-file invocation is verified, and only
		// with a paired test to scope TO. Both fall back to the full suite
		// rather than improvising something that would quietly run everything
		// (or nothing) while the caller believed it was scoped.
		if fs, canScope := p.(lang.FileScopedTester); canScope {
			if cmd, scoped := fs.FileScopedTestCmd(j.TestPath); scoped {
				return cmd
			}
		}
	}
	return p.TestCmd()
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
