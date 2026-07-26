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
)

// runCertifyRepo fans the single-file audit out over a whole repository.
// H1a: goals come from a checked-in JSON file; H1b replaces that with LLM
// derivation behind the same GoalSource interface. No signing here — H1c
// turns the report into a sealed, anchored statement.
func runCertifyRepo(args []string, stdout, stderr io.Writer) int {
	flagArgs, checkArgv := splitCertifyArgs(args)

	fs := flag.NewFlagSet("certify --repo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "path of the repository to audit (required)")
	goalsPath := fs.String("goals", "", "JSON file mapping repo-relative paths to goals (required)")
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
	if *goalsPath == "" {
		fmt.Fprintln(stderr, "corral certify --repo: --goals is required (H1a supplies goals from a file)")
		return 2
	}

	cands, excl, err := reposcan.Enumerate(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: enumerating %s: %v\n", *repoDir, err)
		return 1
	}

	gs, err := reposcan.NewFileGoalSource(*goalsPath)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: reading --goals %s: %v\n", *goalsPath, err)
		return 1
	}

	cfg := reposcan.EmitConfig{
		Owner: *owner, Repo: filepath.Base(*repoDir), Commit: *commit, Root: *repoDir,
		EngineVersion: version, ModelSet: "unset", AuditConfig: "default",
	}
	jobs, goalExcl, err := reposcan.EmitJobs(cfg, cands, gs)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: emitting jobs: %v\n", err)
		return 1
	}
	// The two exclusion sources partition DIFFERENTLY, and conflating them
	// double-counts files:
	//   - Enumerate's exclusions are files that are NOT candidates at all
	//     (no-language / is-test / no-paired-test).
	//   - EmitJobs' ungoaled exclusions are candidates — they are already
	//     inside len(cands).
	// So the file total is candidates + ENUMERATE-only exclusions. Counting
	// len(excl) after the append added every ungoaled path a second time and
	// inflated TotalFiles past the number of files on disk — in a report a
	// later slice signs and anchors to a public transparency log.
	enumExcl := len(excl)
	totalFiles := len(cands) + enumExcl

	// BOTH sources are still REPORTED, or the coverage story is a lie: a reader
	// has to see the ungoaled files too, since they are candidates that were
	// nonetheless not audited.
	excl = append(excl, goalExcl...)

	fmt.Fprintf(stdout, "corral certify --repo %s\n", *repoDir)
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

	// Provider preflight, ONCE, before the fan-out: role models, decorrelation,
	// and the API key are scan-wide facts. Discovering a missing key per-file —
	// after each job has already run its baseline suite in the jail — would burn
	// real minutes to reach an answer the first job could have given instantly.
	if _, err := resolveAuditRoles(localAuditInput{cmdName: "corral certify --repo"}, stderr); err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: %v\n", err)
		if isAuditUsageError(err) {
			return 2
		}
		return 1
	}

	workers := resolveSwarm(*swarmFlag)
	fmt.Fprintf(stdout, "  swarm: %d workers\n", workers)

	// Each job runs the whole tree in a jail and grades it with the project's
	// own test command. Given after `--`; absent, the language plugin's stock
	// recursive command is used — resolved per job, since a repo can mix
	// languages.
	ex := newLocalExecutor(*repoDir, checkArgv, stdout)
	// Cache is nil in H1a: the content-addressed key exists (Task 2) but the
	// persistent store behind it is H1b. A nil Cache means every job is
	// computed fresh — slow, never stale.
	results := reposcan.Scan(context.Background(), jobs, ex, nil, workers)
	rep := reposcan.Aggregate(*owner, cfg.Repo, *commit, totalFiles, len(cands), results, excl)

	printRepoReport(stdout, rep)
	return repoScanExitCode(rep)
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
	for i, e := range excl {
		if i == maxListedExclusions {
			fmt.Fprintf(w, "    ... and %d more excluded file(s)\n", len(excl)-maxListedExclusions)
			break
		}
		fmt.Fprintf(w, "    excluded %s (%s)\n", e.Path, e.Reason)
	}
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

	newBaseline func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error)
	audit       func(context.Context, localAuditInput) (advpool.Verdict, error)
}

func newLocalExecutor(repoDir string, checkArgv []string, progress io.Writer) reposcan.Executor {
	if progress == nil {
		progress = io.Discard
	}
	return localExecutor{
		repoDir:      repoDir,
		checkArgv:    checkArgv,
		baselineRuns: 2,
		// Concurrent jobs write progress; serialize so two files' notices
		// cannot interleave mid-line.
		progress:    &syncWriter{w: progress},
		newBaseline: baselineRunnerFor,
		audit:       auditOneFile,
	}
}

func (l localExecutor) Execute(ctx context.Context, j reposcan.Job) (reposcan.FileResult, error) {
	in := localAuditInput{
		repoDir:  l.repoDir,
		codePath: j.Path,
		testPath: j.TestPath,
		goal:     j.Goal.Text,
		lang:     j.Lang,
		repo:     j.Repo,
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
	res := reposcan.FileResult{Verdict: v, Gradable: !v.BaselineFailed}
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
func (l localExecutor) testCmd(j reposcan.Job) []string {
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
func (l localExecutor) fail(j reposcan.Job, err error) error {
	l.note("%s: could not audit: %v\n", j.Path, err)
	return err
}

func (l localExecutor) note(format string, a ...any) {
	if l.progress == nil {
		return
	}
	fmt.Fprintf(l.progress, "  "+format, a...)
}
