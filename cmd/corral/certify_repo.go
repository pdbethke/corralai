// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
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
	topFlag := fs.Int("top", defaultScanTop, "audit only the N highest-ranked candidates (0 or --all = every candidate). Bounded by default: a whole-repo audit runs a full herd per file, so an unbounded first scan on a large repo costs hours and real money. The DEFAULT bound does not apply with --goals — a hand-written goals map has already chosen the surface — but an explicit --top does")
	allFlag := fs.Bool("all", false, "audit every candidate, ignoring --top")
	deriveModel := fs.String("derive-model", defaultDeriveModel, "model that derives a goal per file when --goals is not given")
	owner := fs.String("owner", "local", "owning account for the scan (tenant identifier)")
	commit := fs.String("commit", "", "commit SHA the report is bound to")
	swarmFlag := fs.Int("swarm", 0, "max concurrent audit workers (0 = auto-size to this host's cores)")
	dryRun := fs.Bool("dry-run", false, "enumerate and emit jobs, then stop — no audits run")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if *repoDir == "" {
		fmt.Fprintln(stderr, "corral certify --repo: --repo is required")
		return 2
	}

	cands, excl, err := reposcan.Enumerate(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: enumerating %s: %v\n", *repoDir, err)
		return 1
	}

	fmt.Fprintf(stdout, "corral certify --repo %s\n", *repoDir)

	// Selection precedes derivation, deliberately: bounding afterwards would
	// pay for a goal on every candidate in order to audit 25 of them.
	ranked, rankInfo := reposcan.Rank(*repoDir, cands)
	// Captured BEFORE any candidate-level exclusion is appended below. Only
	// Enumerate's exclusions are non-candidates; every later reason
	// (not-selected, ungoaled, derive-failed, source-too-large) names a file
	// already counted in
	// len(cands), and adding those to the file total would report more files
	// than exist on disk.
	enumExcl := len(excl)

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
	selected, notSelected := reposcan.Select(ranked, limit)
	// Appending into excl is safe: notSelected is Select's own freshly
	// allocated slice, and excl is Enumerate's. Nothing is appended to
	// `selected`, which ALIASES ranked's backing array.
	excl = append(excl, notSelected...)

	// The rule is disclosed. A selection nobody can explain is the same
	// problem this project criticises in black-box model routing.
	fmt.Fprintf(stdout, "  ranked by %s; auditing %d of %d candidate(s)\n",
		rankInfo.Signal, len(selected), len(cands))
	if rankInfo.Note != "" {
		fmt.Fprintf(stdout, "    %s\n", rankInfo.Note)
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
		ex = newLocalExecutor(*repoDir, checkArgv, stdout)
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
		if _, err := resolveAuditRoles(localAuditInput{cmdName: "corral certify --repo"}, stderr); err != nil {
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

	cfg := reposcan.EmitConfig{
		Owner: *owner, Repo: filepath.Base(*repoDir), Commit: *commit, Root: *repoDir,
		EngineVersion: version, ModelSet: "unset", AuditConfig: "default",
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
	printExclusions(stdout, excl)

	// An explicit `-- <cmd>` is applied to EVERY job, so it is only meaningful
	// when every job speaks the same language. Refuse the mixed case rather
	// than grade a mutated .py file with `go test ./...`: that check is green
	// on the baseline AND green on every mutant, which is not an error
	// anywhere in the pipeline — it is a confident 0.00 kill rate landing in
	// the report as a real measurement. Never fabricate a score.
	if err := checkArgvSpansOneLanguage(checkArgv, jobs); err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
		return 2
	}

	if *dryRun {
		return 0
	}

	workers := resolveSwarm(*swarmFlag)
	fmt.Fprintf(stdout, "  swarm: %d workers\n", workers)

	// ex is non-nil here: it is constructed on every non-dry-run path above,
	// and the dry run returned before this point.
	//
	// Cache is nil in H1a: the content-addressed key exists (Task 2) but the
	// persistent store behind it is H1b. A nil Cache means every job is
	// computed fresh — slow, never stale.
	results := reposcan.Scan(context.Background(), jobs, ex, nil, workers)
	rep := reposcan.Aggregate(*owner, cfg.Repo, *commit, totalFiles, len(cands), results, excl)

	printRepoReport(stdout, rep)
	return repoScanExitCode(rep)
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

// repoScanExitCode is the scan's automated signal. A scan that measured
// NOTHING is not a passing scan: exiting 0 would read as green in CI for a
// repo where every single file failed to grade — the exact false-green the
// COULD-NOT-GRADE line prevents for a human reader, left unfixed for the
// automated one. Split out as a function so both branches are testable
// without a jail and an API key.
func repoScanExitCode(r reposcan.RepoReport) int {
	if r.Audited == 0 {
		return 1
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

func printRepoReport(w io.Writer, r reposcan.RepoReport) {
	commit := r.Commit
	if strings.TrimSpace(commit) == "" {
		// Never print a bare dangling "@ " — say plainly that the report is
		// not bound to a commit, because that is what it means for anyone
		// trying to reproduce it.
		commit = "(no commit given)"
	}
	fmt.Fprintf(w, "\nRepo adequacy — %s/%s @ %s\n", r.Owner, r.Repo, commit)
	if r.Audited == 0 {
		fmt.Fprintln(w, "  COULD-NOT-GRADE: nothing was audited; no score is reported.")
	} else {
		fmt.Fprintf(w, "  kill rate %.2f over %d audited file(s) (%.0f%% of %d candidates)\n",
			r.KillRate, r.Audited, 100*r.AuditedFraction(), r.Candidates)
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
	}
	if r.CacheHits > 0 {
		fmt.Fprintf(w, "  %d verdict(s) reused from cache\n", r.CacheHits)
	}
	if len(r.Weakest) > 0 {
		fmt.Fprintln(w, "  weakest files:")
		for i, f := range r.Weakest {
			if i == 10 {
				fmt.Fprintf(w, "    ... and %d more\n", len(r.Weakest)-10)
				break
			}
			fmt.Fprintf(w, "    %.2f  %s (%d survivor(s))\n", f.KillRate, f.Path, f.Survivors)
		}
	}
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
type localExecutor struct {
	repoDir      string
	checkArgv    []string
	baselineRuns int // how many times to run the unmutated suite; 2 is the floor
	progress     io.Writer

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

	newBaseline func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error)
	audit       func(context.Context, localAuditInput) (advpool.Verdict, error)
}

func newLocalExecutor(repoDir string, checkArgv []string, progress io.Writer) *localExecutor {
	if progress == nil {
		progress = io.Discard
	}
	l := &localExecutor{
		repoDir:      repoDir,
		checkArgv:    checkArgv,
		baselineRuns: 2,
		// Concurrent jobs write progress; serialize so two files' notices
		// cannot interleave mid-line.
		progress:    &syncWriter{w: progress},
		newBaseline: baselineRunnerFor,
		audit:       auditOneFile,
	}
	// Resolve the sandbox ONCE for the whole scan: the backend name is an input
	// to the seed (it decides which dep dirs can be bind-mounted rather than
	// copied), and it is a scan-wide constant — resolving it per file would
	// re-run the backend probe for every job to reach the same answer. The scan
	// exposes no --jail flag, so the auto backend is resolved (same rules as
	// prepareAuditJail's empty in.jail), minus the `--jail container` advice on
	// failure — a flag this command does not offer.
	iso, err := resolveScanJail()
	backendName := ""
	if err != nil {
		l.jailErr = err
	} else {
		l.iso = iso
		backendName = iso.Name()
	}
	// The scan exposes no --bind-dir/--no-bind-deps: nil/false are the
	// documented defaults (auto-detected dep dirs are bound, nothing extra).
	l.seeds = newSeedCache(func(langName string) (repoSeed, error) {
		if l.jailErr != nil {
			return repoSeed{cleanup: func() {}}, l.jailErr
		}
		return buildRepoSeed("corral certify --repo", repoDir, langName, backendName, nil, false, l.progress)
	})
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

func (l *localExecutor) Execute(ctx context.Context, j reposcan.Job) (reposcan.FileResult, error) {
	in := localAuditInput{
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
		// One worker per file: the scan's budget is spent on file-level
		// fan-out, so a nested per-file swarm would multiply it by the worker
		// count and melt the box the --swarm bound exists to protect.
		swarm: 1,
		// H1a produces a REPORT, not a sealed statement: no ledger, no
		// signing key, no scorecard feed (N concurrent audits must not
		// contend on one single-process DuckDB file). Signing is H1c.
		stdout: io.Discard,
		stderr: io.Discard,
	}

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
	l.note("%s: kill rate %.2f (%d survivor(s))\n", j.Path, v.DevKillRate, v.Survivors)
	return res, nil
}

// testCmd resolves the project's own test command for one job: the operator's
// `-- <cmd>` if given, else the job language's stock recursive command.
// Repo-aware jail wiring REQUIRES a command (there is no synthetic scaffold to
// fall back on), and a repo can mix languages, so it is resolved per job. An
// unknown language yields nil and the audit fails closed downstream rather
// than grading with someone else's command.
func (l *localExecutor) testCmd(j reposcan.Job) []string {
	if len(l.checkArgv) > 0 {
		return l.checkArgv
	}
	if p, ok := lang.ByName(j.Lang); ok {
		return p.TestCmd()
	}
	return nil
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
