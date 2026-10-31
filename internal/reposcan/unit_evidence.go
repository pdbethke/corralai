// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/tomllite"
)

// Unit evidence is the per-unit twin of Python's one instrumented run
// (CollectSelectionEvidence): a language whose coverage instrument reports
// per FILE, not per test, can still say which test executed which source
// file by running each test UNIT (a test file, a Go package, a Rust target —
// lang.ListUnitsCmd) on its own, under coverage, once. The result builds the
// same EvidenceIndex Python's path builds (IndexFromUnits), so candidacy
// widening reads both the same way.
//
// Running every unit separately, rather than one instrumented run of the
// whole suite, is what makes the evidence attributable: a whole-suite report
// says a file ran, never which test ran it, and the landing hint for an
// evidence-only candidate is exactly "which test". The cost is one run per
// unit, bounded by the caller's own deadline and worker count — this file
// invents neither.

// UnitResult is what one unit's coverage run produced. Err non-empty means
// the unit contributes NO evidence and says why; Counts is then nil. A unit
// that ran cleanly and executed nothing it reports is a success with an
// empty (or all-zero) Counts, not an error.
type UnitResult struct {
	Unit   string // the unit's name as the runner listed it (lang.UnitName)
	Counts map[string]lang.LineCounts
	// Uncounted is every file the unit's reducer measured and could not
	// count, as "<path>: <reason>" (lang.UncountedCoverage). Such a file is
	// absent from Counts, which is the honest count, and the reason is kept
	// here so absence never loses its explanation.
	Uncounted []string
	// Loaded is every file whose module-level code the unit's run executed
	// (lang.LoadedCoverage): it was loaded, whether or not any function of
	// it ran. A file in Counts with nothing executed and in Loaded is
	// import-only, not uncovered (ruling R24). Nil for a reporter that does
	// not report loading.
	Loaded []string
	// Err is the unit's reason as the record keeps it: one fixed phrase per
	// failure class, with an exit code where one was read. It reaches the
	// verdict (TestSelection.UnreportedReason, ExcludedReason) and so the
	// signed, published ledger, so it never holds a runner's error text, an
	// output tail, the operator's argv or a host path.
	Err string
	// TerminalDetail is what the operator needs to act on Err — the runner's error,
	// the failed run's output tail — for the scan's units line on the
	// terminal (printUnitPass) and nothing else. It is never marshalled (the
	// selection cache stores this struct as JSON) and never copied into the
	// test map, so it cannot reach a verdict.
	// Named TerminalDetail, never Detail: a ledger row's Detail is recorded,
	// and a field of that name is one assignment from leaking into it.
	TerminalDetail string `json:"-"`
}

// UnitEvidence is one scan's unit pass. Ran false means there is no unit
// evidence at all, and Note says why (no unit form for the command, no
// readable listing, no coverage instrument, no worker); Units then lists
// whatever was learned before the pass stopped, for disclosure only. Ran
// true means at least one unit was runnable; each unit's own Err says
// whether it contributed.
type UnitEvidence struct {
	Ran   bool
	Units []UnitResult
	// Note is why the pass did not run, as the record keeps it: it becomes
	// every file's TestSelection.Fallback (testmap's someUnitReported, the
	// scan's unitsNotRunReason), which the ledger publishes on every entry,
	// so it is one fixed phrase per reason and never quotes the operator's
	// command, a runner's error or what a run printed.
	Note string
	// TerminalDetail is Note as the operator's terminal says it: the same reason
	// with the command, the error and the output tail that explain it. It
	// is never marshalled and never read into a selection. "" when Note
	// says everything (Said).
	// Named TerminalDetail, never Detail: a ledger row's Detail is recorded,
	// and a field of that name is one assignment from leaking into it.
	TerminalDetail string `json:"-"`
	// Changed is what the pass changed in the operator's command to run
	// each unit (lang.EvidenceNote: vitest's threads pool), "" for nothing.
	// It is set on a pass that ran, every unit green or not, so the scan
	// line can say once that the evidence came from a command the operator
	// did not write. It is not Note, which says why a pass did NOT run.
	Changed string `json:",omitempty"`
}

// Reasons a unit contributes nothing. They are written into UnitResult.Err
// verbatim, and the scan line repeats them. Each is fixed text (an exit code
// aside): what a failure printed goes in UnitResult.TerminalDetail, for the terminal.
const (
	unitNotRunTimeLimit   = "not run (time limit)"
	unitNotRunInterrupted = "not run (interrupted)"
	unitTimedOut          = "timed out"
	unitInterrupted       = "interrupted"
	unitTruncated         = "report truncated"
	unitNotAlone          = "not run: its one-unit form would also run another unit"
	unitNoSuiteExit       = "no suite exit status in the report"
	// unitRunFailed prefixes every reason for a unit that RAN and failed on
	// unmutated code: a red test, a build error, a process that did not exit
	// normally. It is the one reason that proves the unit red in the jail,
	// which is what WithoutRedUnits keys on.
	unitRunFailed = "run failed: "
	// unitGreenAsWritten is a unit red only with the evidence pass's change
	// to its command (vitest's --pool=threads: REA's cliJsonPaths.test.ts
	// calls process.chdir(), which threads forbid), and green when run
	// again exactly as the operator's command runs it (recheckAsWritten).
	// It is not red, so the red-baseline retry never leaves it out; that
	// run took no coverage, so it reports no evidence and runs with every
	// file.
	unitGreenAsWritten = "not measured: red with the evidence pass's change (%s), green as the operator's command runs it, which took no coverage"
	// unitCouldNotRun prefixes a runner's own error: the unit never ran,
	// for a reason outside the tree (a jail that could not start).
	unitCouldNotRun = "could not run: "
	// unitJailCouldNotStart is unitCouldNotRun's one reason: the runner's
	// own error (an OS error naming host paths, a bwrap message) is the
	// unit's TerminalDetail, never its Err.
	unitJailCouldNotStart = unitCouldNotRun + "the jail could not start it"
	// unitNoCommand is a unit the operator's command has no one-unit form
	// for. It once quoted that command, argv and all, into the record.
	unitNoCommand = "not run: the command could not be built"
	// unitCoverageUnavailable prefixes the instrument's own reason, which
	// is the unit's TerminalDetail; unitCoverageUnavailableRecord is the Err.
	unitCoverageUnavailable       = "coverage unavailable: "
	unitCoverageUnavailableRecord = unitCoverageUnavailable + "the coverage instrument reported it unavailable"
	// unitUnparseable is a report the reducer's parser refused; the
	// parser's message is the unit's TerminalDetail.
	unitUnparseable = "unparseable: the coverage report could not be read"
)

// CollectUnitEvidence lists the units inv collects and runs each one's
// coverage command (CoverageCmd over EvidenceUnitCmd over UnitRunCmd) once,
// workers at a time.
//
// It never stretches its budget: ctx is the caller's deadline for the whole
// pass, and a unit not yet started when it passes is recorded unitNotRunTimeLimit
// while every unit that finished keeps its evidence — a partial pass is
// still evidence about the units that ran, and says which did not. workers
// is the run's own parallelism setting, passed through; a value below one is
// refused rather than replaced with a number nobody chose.
//
// modulePath is passed to CoverageCounts exactly as Preflight passes it: the
// Go module path for Go, the repository root for the filesystem-path
// reporters.
//
// announce is called once, after the listing and before the first unit
// runs, with how many units will run and how many the listing named, so the
// caller can tell the operator what a pass that may last its whole deadline
// is about to do. It is not called when no unit runs. It is required: the
// pass has no output of its own to fall back on.
func CollectUnitEvidence(ctx context.Context, runner commandRunner, files map[string]string, p lang.Plugin, inv lang.Invocation, modulePath string, workers int, announce func(runnable, listed int)) UnitEvidence {
	command := strings.Join(inv.Argv, " ")
	if workers < 1 {
		return UnitEvidence{Note: fmt.Sprintf("%s: unit evidence needs at least one worker, and was given %d workers", p.Name(), workers)}
	}
	reporter, ok := p.(lang.CoverageReporter)
	if !ok {
		return UnitEvidence{Note: fmt.Sprintf("%s: no coverage instrumentation for this language, so no unit evidence", p.Name())}
	}
	listCmd, ok := lang.ListUnitsCmd(p, inv)
	if !ok {
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit pass could not list units: its command has no one-unit form corral can list and run", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: `%s` has no one-unit form corral can list and run, so no unit evidence", p.Name(), command)}
	}

	out, exit, stderr, err := runSelectionCmd(ctx, runner, files, listCmd)
	switch {
	case err != nil:
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit pass could not list units: the jail could not start the listing", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: listing the test units of `%s` failed: %v", p.Name(), command, err)}
	case exit > 0:
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit pass could not list units: the listing exited %d", p.Name(), exit),
			TerminalDetail: fmt.Sprintf("%s: listing the test units of `%s` exited %d%s", p.Name(), command, exit, failureTail(stderr, out))}
	case truncated(out):
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit pass could not list units: the listing was truncated, so the units it names cannot be trusted to be all of them", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: the unit listing of `%s` was truncated, so the units it names cannot be trusted to be all of them", p.Name(), command)}
	}
	units := lang.ParseUnitList(p, inv, out)
	if units == nil {
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit pass could not list units: the listing could not be read", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: could not read the unit listing of `%s`%s", p.Name(), command, failureTail(stderr, out))}
	}
	if len(units) == 0 {
		return UnitEvidence{Note: fmt.Sprintf("%s: the test command collects no test units", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: `%s` collects no test units", p.Name(), command)}
	}

	results := make([]UnitResult, len(units))
	cmds := make([][]string, len(units))
	// asWritten is each unit's one-unit command as the operator's command
	// runs it, for recheckAsWritten.
	asWritten := make([][]string, len(units))
	evidenceCmds := make([][]string, len(units))
	var runnable []int
	for i, u := range units {
		name := lang.UnitName(u)
		results[i].Unit = name
		if u != name {
			results[i].Err = unitNotAlone
			continue
		}
		unitCmd, ok := lang.UnitRunCmd(p, inv, u)
		if !ok {
			results[i].Err = unitNoCommand
			results[i].TerminalDetail = fmt.Sprintf("`%s` has no one-unit command for it", command)
			continue
		}
		// The evidence pass's own flags (vitest's threads pool) go on the
		// unit command before coverage wraps it; grading never gets them.
		asWritten[i] = unitCmd
		evidenceCmds[i] = lang.EvidenceUnitCmd(p, inv, unitCmd)
		cov, ok := reporter.CoverageCmd(evidenceCmds[i])
		if !ok {
			results[i].Err = "not run: its one-unit command has no coverage instrumentation"
			continue
		}
		cmds[i] = cov
		runnable = append(runnable, i)
	}
	if len(runnable) == 0 {
		return UnitEvidence{Units: results, Note: fmt.Sprintf("%s: no unit the test command collects can run alone under coverage, so no unit evidence", p.Name()),
			TerminalDetail: fmt.Sprintf("%s: no unit `%s` collects can run alone under coverage, so no unit evidence", p.Name(), command)}
	}

	noCoverage := lang.UnitNoCoverageReason(p, inv)
	note := lang.EvidenceNote(p, inv)
	// said is every sentence the pass adds about how it ran its units,
	// beyond what it changed in the command (note); Changed is note
	// followed by them, built once at the end, so a later change to note
	// (a shared cache's options) can never drop or repeat a sentence.
	var said []string

	// Every unit runs over the same tree, so a runner that can seed it
	// (seedingRunner) writes it once, here, and each unit runs on a
	// copy-on-write overlay of that seed instead of a fresh copy of its own.
	// The seed also shares the runner's tool cache (lang.EvidenceCache),
	// when its row has one and the seed can overlay it.
	unitRunner := runner
	var seeded sandbox.SeededRunner
	cache := lang.EvidenceCacheFor(p, inv)
	cacheTarget := ""
	if sr, ok := runner.(seedingRunner); ok {
		// A cache whose tool would not take its options (an installed
		// version that predates them) is not asked for: the seed shares
		// none, and the pass says why below.
		asked, unsupported := askedCache(cache, inv.Argv, treeFileReader(runner, files))
		s, err := sr.Seed(files, asked)
		switch {
		case err == nil:
			seeded, unitRunner = s, s
			switch {
			case unsupported != "":
				said = append(said, unitsUnsharedCache(cache, unsupported))
			case cache != nil:
				shared := s.Cache()
				cacheTarget = shared.Target
				if cacheTarget == "" {
					said = append(said, unitsUnsharedCache(cache, shared.Unshared))
				}
			}
		case errors.Is(err, sandbox.ErrNoOverlay):
			said = append(said, unitsFreshCopy(err))
			if cache != nil {
				said = append(said, unitsUnsharedCache(cache, overlayRefuser(err)+" cannot overlay"))
			}
		default:
			for _, i := range runnable {
				results[i].Err = unitJailCouldNotStart
				results[i].TerminalDetail = err.Error()
			}
			return UnitEvidence{Units: results, Note: fmt.Sprintf("%s: the unit pass did not run: the jail could not start it", p.Name()),
				TerminalDetail: fmt.Sprintf("%s: the unit pass could not seed the tree its units run over: %v", p.Name(), err)}
		}
	}

	if cacheTarget != "" && cache.Args != nil {
		// The cache's own words go on each unit command after the
		// evidence flags, before coverage wraps it, and only when the
		// cache is shared: a unit told to use a cache that is not there
		// would not be the run the operator's command describes. They
		// are words the operator did not write, so the note names them
		// — on the scan line and in a red unit's reason — as the
		// evidence flags are named.
		args := cache.Args(cacheTarget)
		note = unitsCacheArgs(note, cache, args)
		for _, i := range runnable {
			cov, ok := reporter.CoverageCmd(append(slices.Clone(evidenceCmds[i]), args...))
			if !ok {
				results[i].Err = "not run: its one-unit command has no coverage instrumentation with the shared cache's arguments"
				cmds[i] = nil
				continue
			}
			cmds[i] = cov
		}
		runnable = slices.DeleteFunc(runnable, func(i int) bool { return cmds[i] == nil })
	}

	announce(len(runnable), len(units))
	frozen := runnable
	if cacheTarget != "" && len(runnable) > 0 {
		// The first runnable unit warms the shared cache: run once, alone,
		// before any worker starts, with the cache writable. Its build
		// flags are every later unit's, so what it caches is what they
		// build. Its result is that unit's evidence; it is not run again.
		// Every later unit sees the cache frozen.
		first := runnable[0]
		r := runUnit(ctx, warmingRunner{seeded}, files, reporter, cmds[first], modulePath, noCoverage, note)
		r = recheckAsWritten(ctx, unitRunner, files, r, asWritten[first], note)
		r.Unit = results[first].Unit
		results[first] = r
		frozen = runnable[1:]
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(frozen)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := runUnit(ctx, unitRunner, files, reporter, cmds[i], modulePath, noCoverage, note)
				r = recheckAsWritten(ctx, unitRunner, files, r, asWritten[i], note)
				r.Unit = results[i].Unit
				results[i] = r
			}
		}()
	}
	for _, i := range frozen {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if seeded != nil {
		// The evidence is whole either way; a seed left behind is the
		// host's to clean up, and the operator is told where it is.
		if err := seeded.Close(); err != nil {
			said = append(said, fmt.Sprintf("the pass's seed could not be removed: %v", err))
		}
	}
	return UnitEvidence{Ran: true, Units: results, Changed: unitsChanged(note, said)}
}

// seedingRunner is a commandRunner that can write the tree once for many
// runs (adequacy's bwrapJail.Seed): the SeededRunner it returns runs each
// command on a copy-on-write overlay of that one seed, for the very files
// map it was given and no other, until Close. Seed returns
// sandbox.ErrNoOverlay when the backend cannot overlay, and the pass then
// runs each unit on a fresh copy, as every run did before, and says so
// (unitsFreshCopy). cache, when non-nil, is the tool cache the seed is to
// share among the runs (SeededRunner.Cache says whether it does).
type seedingRunner interface {
	Seed(files map[string]string, cache *lang.EvidenceCache) (sandbox.SeededRunner, error)
}

// warmingRunner runs a seed's commands through its Warm: the one run that
// fills the shared cache, read through runUnit like every other unit's.
type warmingRunner struct{ s sandbox.SeededRunner }

func (w warmingRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	res, err := w.s.Warm(ctx, files, cmd)
	return res.Output, err
}

func (w warmingRunner) EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	return w.s.Warm(ctx, files, cmd)
}

// askedCache is the cache the pass asks its seed to share, and why it asks
// for none when the cache's tool would not take its options (an installed
// version that predates them, or one that cannot be read): cache itself
// when it has no Unsupported or Unsupported gives no reason, else nil and
// that reason. read is the tree as the jail will see it (treeFileReader).
func askedCache(cache *lang.EvidenceCache, argv []string, read func(rel string) ([]byte, error)) (*lang.EvidenceCache, string) {
	if cache == nil || cache.Unsupported == nil {
		return cache, ""
	}
	if why := cache.Unsupported(argv, read); why != "" {
		return nil, why
	}
	return cache, ""
}

// unitsChanged is UnitEvidence.Changed: what the pass changed in the
// command (note, "" for nothing), then every other sentence it said about
// how it ran its units, in order.
func unitsChanged(note string, said []string) string {
	changed := note
	for _, s := range said {
		changed = joinChanged(changed, s)
	}
	return changed
}

// treeFileReader reads a file of the tree a unit runs over, as the jail
// will see it: through the runner when it can (TreeReader: the seed's own
// files, then the dependency directories it mounts into the jail, where
// node_modules is on a real run), else from the seed's files alone. Both
// name a file by lang.TreeRel's one rule. A file outside the seed, on a
// runner that cannot read further, is fs.ErrNotExist.
func treeFileReader(runner commandRunner, files map[string]string) func(rel string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if tr, ok := runner.(TreeReader); ok {
			return tr.ReadTreeFile(files, rel)
		}
		clean, ok := lang.TreeRel(rel)
		if !ok {
			return nil, fmt.Errorf("%q is not a path inside the tree", rel)
		}
		if s, ok := files[clean]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
}

// TreeReader is a commandRunner that reads one file of a run's tree as the
// jail sees it (adequacy's bwrapJail.ReadTreeFile): from files, else from
// the dependency directory mounted at that path.
type TreeReader interface {
	ReadTreeFile(files map[string]string, rel string) ([]byte, error)
}

// unitsCacheArgs is note (lang.EvidenceNote, "" for no change) extended
// with the options a shared cache adds to every unit command.
func unitsCacheArgs(note string, cache *lang.EvidenceCache, args []string) string {
	what := strings.Join(args, " ") + " (a shared " + cacheName(cache) + ", filled by the first unit and frozen for every later one)"
	if note == "" {
		return "the evidence pass runs each unit with " + what
	}
	return note + ", and with " + what
}

// cacheName is what the pass calls a cache: its Name, else its Env, else
// "tool cache".
func cacheName(cache *lang.EvidenceCache) string {
	switch {
	case cache.Name != "":
		return cache.Name
	case cache.Env != "":
		return cache.Env
	}
	return "tool cache"
}

// unitsUnsharedCache is said, once per pass, of a pass whose seed could not
// share the runner's tool cache (why, from SeededRunner.Cache): each unit
// started from an empty one, as every jailed run did before the cache was
// shared. It costs time, never evidence.
func unitsUnsharedCache(cache *lang.EvidenceCache, why string) string {
	name := cacheName(cache)
	if why == "" {
		why = "the seed gave no reason"
	}
	return "each unit started from an empty " + name + " (no shared cache: " + why + ")"
}

// unitsFreshCopy is said, once per pass, of a pass whose runner could not
// seed its units' tree (err, an ErrNoOverlay): each unit wrote a full copy
// of the tree, which costs the tree's size in tmpfs per worker (at 12 or
// more workers that exhausted REA's tmpfs, and every unit failed ENOSPC).
// It names the backend that refused when the error does.
func unitsFreshCopy(err error) string {
	return "units ran on a fresh copy each (" + overlayRefuser(err) + " cannot overlay)"
}

// overlayRefuser names the backend an ErrNoOverlay came from, for the
// pass's sentences about it.
func overlayRefuser(err error) string {
	who := "this backend"
	var no *sandbox.NoOverlayError
	if errors.As(err, &no) {
		who = "the " + no.Backend + " backend"
		if no.Backend == "bwrap" {
			who = "this bwrap"
		}
	}
	return who
}

// joinChanged appends one sentence to UnitEvidence.Changed.
func joinChanged(changed, more string) string {
	if changed == "" {
		return more
	}
	return changed + "; " + more
}

// Said is why the pass did not run as the operator's terminal says it:
// TerminalDetail when there is one, else Note. Never read it into a selection.
func (ev UnitEvidence) Said() string {
	if ev.TerminalDetail != "" {
		return ev.TerminalDetail
	}
	return ev.Note
}

// Settled reports whether this pass may be kept for a later scan of the
// same tree (the selection cache): it ran, and EVERY unit succeeded. Any
// unit failure disqualifies it, because none can be told apart from the
// environment's: a deadline, an interrupt or a runner that could not start,
// but equally a coverage instrument the jail lacks, or a unit red because a
// toolchain is missing. The cache is keyed on the tree, so a cached failure
// would be served again after the operator fixed the environment (ruling
// R15). An unsettled pass is still evidence for the scan that made it; it
// is only never reused.
func (ev UnitEvidence) Settled() bool {
	if !ev.Ran {
		return false
	}
	for _, u := range ev.Units {
		if u.Err != "" {
			return false
		}
	}
	return true
}

// GreenAsWritten is how many units were red only with the evidence pass's
// change to their command and green as the operator's command runs them
// (unitGreenAsWritten).
func (ev UnitEvidence) GreenAsWritten() int {
	prefix, _, _ := strings.Cut(unitGreenAsWritten, "(")
	n := 0
	for _, u := range ev.Units {
		if strings.HasPrefix(u.Err, prefix) {
			n++
		}
	}
	return n
}

// CutShort is how many units the pass's own deadline cut: never started
// before it passed (unitNotRunTimeLimit) or stopped by it mid-run
// (unitTimedOut). It is the one number an operator can act on by giving the
// pass more time, so the caller names its bound beside it; every other reason
// a unit contributes nothing is the tree's or the environment's, and more
// time would not change it.
func (ev UnitEvidence) CutShort() int {
	n := 0
	for _, u := range ev.Units {
		if u.Err == unitNotRunTimeLimit || u.Err == unitTimedOut {
			n++
		}
	}
	return n
}

// runUnit runs one unit's coverage command and reads its report. Every way
// the run can fail to measure is an Err, never an empty Counts. noCoverage
// is the runner's known cause for a report that names no project file at
// all (lang.UnitNoCoverageReason), "" for none. note is what the evidence
// pass changed in the operator's command (lang.EvidenceNote), "" for
// nothing: a red unit's TerminalDetail names it, because the operator's own run of
// the same unit may be green without it.
func runUnit(ctx context.Context, runner commandRunner, files map[string]string, reporter lang.CoverageReporter, cmd []string, modulePath, noCoverage, note string) UnitResult {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return UnitResult{Err: unitNotRunTimeLimit}
		}
		return UnitResult{Err: unitNotRunInterrupted}
	}
	_, detailed := runner.(detailedRunner)
	out, exit, stderr, err := runSelectionCmd(ctx, runner, files, cmd)
	switch {
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return UnitResult{Err: unitTimedOut}
	case err != nil && ctx.Err() != nil:
		return UnitResult{Err: unitInterrupted}
	case err != nil:
		return UnitResult{Err: unitJailCouldNotStart, TerminalDetail: err.Error()}
	case exit > 0:
		// A unit whose own run failed — a red test, a build error, a
		// toolchain the jail lacks — is not evidence, even when a report
		// still parses: a failing unit can stop before the code it would
		// have executed, so its zeros are not "never executed".
		return UnitResult{Err: fmt.Sprintf(unitRunFailed+"exited %d", exit), TerminalDetail: runDetail(note, failureTail(stderr, lang.BeforeSuiteExit(out)))}
	case detailed && exit < 0:
		return UnitResult{Err: unitRunFailed + "the process did not exit normally", TerminalDetail: runDetail(note, failureTail(stderr, lang.BeforeSuiteExit(out)))}
	case truncated(out):
		// Checked before parsing, as Preflight does: a report cut on a
		// clean boundary parses as a partial map that looks complete.
		return UnitResult{Err: unitTruncated}
	}
	// The suite's own exit status, from the trailer every CoverageCmd
	// prints (lang.SuiteExit). The command's exit status above is not
	// enough: a coverage wrapper reduces the report whatever the suite
	// exited with, and ends with the REDUCER's status, so a red unit's run
	// can exit 0. Only a green unit's coverage is evidence, and a report
	// that does not say how its suite exited is treated as red.
	//
	// A red suite is reported as red even when a later step also failed.
	// Otherwise an instrument's own "unavailable" notice (lang.
	// CoverageUnavailable) is read BEFORE the missing-trailer case: Rust's
	// llvm-tools check runs before the suite, so its notice comes with no
	// trailer, and "no suite exit status" would hide the one thing the
	// operator has to install.
	rc, ok := lang.SuiteExit(out)
	if ok && rc != 0 {
		// What the suite printed before its trailer says why it failed
		// (stderr when the runner kept it apart); the report after it
		// does not.
		return UnitResult{Err: fmt.Sprintf(unitRunFailed+"exit %d", rc), TerminalDetail: runDetail(note, failureTail(stderr, lang.BeforeSuiteExit(out)))}
	}
	if reason, unavailable := lang.CoverageUnavailable(out); unavailable {
		// The instrument's notice is tool text: the operator's, in TerminalDetail.
		return UnitResult{Err: unitCoverageUnavailableRecord, TerminalDetail: reason}
	}
	if !ok {
		return UnitResult{Err: unitNoSuiteExit}
	}
	counts, err := reporter.CoverageCounts(out, modulePath)
	if err != nil {
		// A report that counted nothing because every file it measured was
		// uncountable gives their number in the reason and the list in
		// Uncounted, where the scan's JSON carries it: in every unit's
		// reason it made REA's scan output 1.4 MB (ruling R22).
		var empty *lang.NoFileEntriesError
		if errors.As(err, &empty) {
			if len(empty.Disclosed) == 0 && noCoverage != "" {
				return UnitResult{Err: noCoverage}
			}
			return UnitResult{Err: "unparseable: " + empty.Summary(), Uncounted: lang.UncountedCoverage(out)}
		}
		// The parser's message can quote the report: the operator's, in
		// TerminalDetail; the record keeps the class.
		return UnitResult{Err: unitUnparseable, TerminalDetail: err.Error()}
	}
	return UnitResult{Counts: counts, Uncounted: lang.UncountedCoverage(out), Loaded: lang.LoadedCoverage(out)}
}

// recheckAsWritten runs a unit that the evidence pass found red once more,
// exactly as the operator's command runs it, when the pass had changed its
// command (note, lang.EvidenceNote): red only with that change, it is not a
// red unit, and is recorded unitGreenAsWritten. Red both ways, a run that
// errs, or a pass that changed nothing keeps r as it is. The rerun takes no
// coverage: the change exists because the operator's own form can lose it
// (vitest's forks pool ends workers before they write any), so coverage from
// it could not be shown complete, and a unit with incomplete edges would
// under-select.
func recheckAsWritten(ctx context.Context, runner commandRunner, files map[string]string, r UnitResult, cmd []string, note string) UnitResult {
	if note == "" || cmd == nil || !strings.HasPrefix(r.Err, unitRunFailed) {
		return r
	}
	_, exit, _, err := runSelectionCmd(ctx, runner, files, cmd)
	if err != nil || exit != 0 {
		return r
	}
	return UnitResult{Err: fmt.Sprintf(unitGreenAsWritten, note)}
}

// runDetail is a failed unit run's TerminalDetail: what the evidence pass changed in
// its command (note), then the end of what it printed (a failureTail), for
// the terminal only.
func runDetail(note, tail string) string {
	tail = strings.TrimPrefix(tail, ": ")
	switch {
	case note == "":
		return tail
	case tail == "":
		return note
	}
	return note + ": " + tail
}

// truncated reports whether out ends with the runner's truncation marker.
// A suffix, not a substring, for the reason Preflight gives: the marker is
// appended once, at the very end, and a suite may print its text.
func truncated(out string) bool {
	return strings.HasSuffix(strings.TrimRight(out, "\n"), sandbox.TruncationMarker)
}

// failureTail is the end of what a failed command printed, for its note:
// stderr when the runner kept it apart, else the combined output.
func failureTail(stderr, out string) string {
	tail := selectionNoteTail(stderr)
	if tail == "" {
		tail = selectionNoteTail(out)
	}
	if tail == "" {
		return ""
	}
	return ": " + tail
}

// IndexFromUnits builds the EvidenceIndex a unit pass supports, as a view of
// the pass's test map (MapFromUnits): the map holds what each unit measured
// and which units have no evidence, this holds what candidacy makes of it. ok is false
// when the pass never ran or no unit succeeded: there is nothing to widen
// candidacy with.
//
// A source file's covering count is the number of successful units that
// executed at least one of its lines AND can land a test for it:
// uf.Landing(unit, source) names the test file an authored test for that
// source would go in when it is graded by that unit, and "" when no such
// file exists (a Go test outside the source's package cannot reach its
// unexported code; a Rust integration test of one crate does not grade
// another). Such a unit is left out of the file's count altogether, so it
// can never promote the file. mostCovering is the landing file of the units
// with the most executed lines of the file, summed per landing file, with
// moreSpecificTestPath breaking ties as ParseEvidenceIndex does.
//
// A file's covering test FILES (EvidenceIndex.CoveredByAny, the --diff-base
// question "does this changed test defend this source?") are every test file
// each of those units compiled, uf.TestFiles(unit), together with its
// landing. For a file unit that is the unit itself; a Go package unit
// compiles every _test.go of its package, internal and external, so a change
// to any of them changes what ran the source. Recording only the landing
// left an evidence-only file out of a PR's scope when the PR changed one of
// the package's other test files (final review I2).
//
// Two shapes are left OUT of the index — unmeasured, never a measured
// zero — because a zero would be a claim the evidence does not support:
//   - a file some unit executed but no unit can land a test for. It was
//     executed; reading it as "no test covers this" would be false.
//   - a file no landable unit executed, when the pass was partial (a unit
//     failed, timed out or never ran). The unit that did not finish may be
//     the one that executes it, and a failed unit is never "covered nothing".
func IndexFromUnits(ev UnitEvidence, uf UnitFiles) (EvidenceIndex, bool) {
	if !ev.Ran {
		return EvidenceIndex{}, false
	}
	m := unitMap(ev, uf)
	type tally struct {
		executed bool
		units    int
		perFile  map[string]int
		compiled map[string]bool
	}
	tallies := map[string]*tally{}
	for src := range m.Sources {
		tallies[src] = &tally{perFile: map[string]int{}, compiled: map[string]bool{}}
	}
	partial := len(m.Unreported()) > 0
	if len(m.Unreported()) == len(m.Tests) {
		return EvidenceIndex{}, false
	}
	for _, e := range m.Edges {
		if e.Executed == 0 {
			continue
		}
		t := tallies[e.Source]
		t.executed = true
		landing := uf.Landing(e.Test, e.Source)
		if landing == "" {
			continue
		}
		t.units++
		t.perFile[landing] += e.Executed
		t.compiled[landing] = true
		for _, tf := range m.Tests[e.Test].Files {
			t.compiled[tf] = true
		}
	}

	files := make(map[string]evidenceFileEntry, len(tallies))
	for src, t := range tallies {
		if t.units == 0 && (t.executed || partial) {
			continue
		}
		best, bestLines := "", -1
		for testFile, lines := range t.perFile {
			if lines > bestLines || (lines == bestLines && moreSpecificTestPath(testFile, best)) {
				best, bestLines = testFile, lines
			}
		}
		s := m.Sources[src]
		files[src] = evidenceFileEntry{
			coveringTests: t.units,
			coveringUnits: t.units,
			mostCovering:  best,
			coveringFiles: t.compiled,
			// A unit that loaded the file ran its module-level code: the
			// unit-pass counterpart of a per-test index's static coverage,
			// which is what makes an unexecuted file import-only.
			hasStatic:     s.HasStatic,
			hasStatements: s.HasStatements,
		}
	}
	return EvidenceIndex{files: files, unitKind: true}, true
}

// UnitFiles is what IndexFromUnits needs to know about the repository's
// units: Landing, the test file an authored test for a source lands in when
// a unit grades it (UnitTestFile), and TestFiles, every test file a unit
// compiles (UnitTestFiles). Both are required; UnitFilesFor builds them.
type UnitFiles struct {
	Landing   func(unit, source string) string
	TestFiles func(unit string) []string
}

// UnitFilesFor is p's UnitFiles over the repository's files.
func UnitFilesFor(p lang.Plugin, files map[string]string) UnitFiles {
	return UnitFiles{Landing: UnitTestFile(p, files), TestFiles: UnitTestFiles(p, files)}
}

// UnitTestFiles answers which of the repository's test files a unit
// compiles and runs, for --diff-base's question of which changed test files
// a unit's evidence speaks for. It names files the repository has, never a
// guessed path, and nothing for a unit it cannot place.
//
//   - JavaScript, TypeScript, Ruby, PHP: a unit is a test file; it is the
//     one file.
//   - Go: a unit is a package import path; `go test` compiles every
//     `_test.go` in that package's directory, internal (`package p`) and
//     external (`package p_test`) alike.
//   - Rust: `<pkg>/test:<name>` (nextest `<pkg>::<name>`) is the target's
//     tests/<name>.rs or tests/<name>/main.rs. A lib or bin unit's tests are
//     in-file modules of its sources, which Landing already names; Rust
//     evidence promotes nothing yet (ruling R23).
//
// Every other language names nothing.
func UnitTestFiles(p lang.Plugin, files map[string]string) func(unit string) []string {
	switch p.Name() {
	case "javascript", "typescript", "ruby", "php":
		return func(unit string) []string {
			if _, ok := files[unit]; ok {
				return []string{unit}
			}
			return nil
		}
	case "go":
		return goUnitTestFiles(files)
	case "rust":
		return rustUnitTestFiles(files)
	}
	return func(string) []string { return nil }
}

func goUnitTestFiles(files map[string]string) func(unit string) []string {
	var once sync.Once
	byPkg := map[string][]string{} // import path -> its directory's _test.go files, sorted
	return func(unit string) []string {
		once.Do(func() {
			byDir := map[string][]string{}
			for f := range files {
				if strings.HasSuffix(f, "_test.go") {
					byDir[path.Dir(f)] = append(byDir[path.Dir(f)], f)
				}
			}
			for dir, fs := range byDir {
				pkg, ok := goImportPathOf(files, dir)
				if !ok {
					continue
				}
				sort.Strings(fs)
				byPkg[pkg] = fs
			}
		})
		return byPkg[unit]
	}
}

func rustUnitTestFiles(files map[string]string) func(unit string) []string {
	var once sync.Once
	pkgDirs := map[string]string{}
	return func(unit string) []string {
		once.Do(func() { pkgDirs = rustPackageDirs(files) })
		pkg, kind, name, ok := rustUnitParts(unit)
		if !ok || kind != "test" {
			return nil
		}
		dir := pkgDirs[pkg]
		if dir == "" {
			return nil
		}
		for _, f := range []string{under(dir, "tests/"+name+".rs"), under(dir, "tests/"+name+"/main.rs")} {
			if _, ok := files[f]; ok {
				return []string{f}
			}
		}
		return nil
	}
}

// UnitTestFile is the landing resolver IndexFromUnits takes, for p's units
// over the repository's files (repo-relative path to text, as the scan's
// seed holds them). It answers which test file a test authored for source
// would go in when unit grades it, or "" when unit cannot grade a test for
// source at all. The text is read only for the manifests that say what a
// unit name means on disk (go.mod, Cargo.toml); every other file is checked
// for existence only. A manifest missing from files resolves nothing: the
// answer is "", never a guessed path.
//
//   - JavaScript, TypeScript, Ruby, PHP: a unit is a test file, and is its
//     own landing when the repository has it.
//   - Go: a unit is a package import path. It lands only on the source's OWN
//     package, in the source's directory (the nearest go.mod names the
//     module), and only on an INTERNAL test file — one whose package clause
//     is the source's: `<source base>_test.go` when that is one, else the
//     directory's lexically first internal `_test.go`, else nothing. The
//     package clauses are read from files' text.
//   - Rust: `<pkg>/test:<name>` (nextest `<pkg>::<name>`) lands on the
//     package's tests/<name>.rs or tests/<name>/main.rs; `<pkg>/lib` (nextest
//     `<pkg>`) on the source itself — an in-file #[cfg(test)] module — when
//     it is a library file of that package; `<pkg>/bin:<name>` (nextest
//     `<pkg>::bin/<name>`) on the source itself when it is that binary's
//     root. Every Rust unit requires the source's own crate (the nearest
//     Cargo.toml with a [package] name) to be the unit's package, since a
//     grading run is scoped to the source's crate.
//
// Every other language (Python grades through its own selector) lands
// nothing.
func UnitTestFile(p lang.Plugin, files map[string]string) func(unit, source string) string {
	switch p.Name() {
	case "javascript", "typescript", "ruby", "php":
		return func(unit, _ string) string {
			if _, ok := files[unit]; ok {
				return unit
			}
			return ""
		}
	case "go":
		return goUnitLanding(files)
	case "rust":
		return rustUnitLanding(files)
	}
	return func(string, string) string { return "" }
}

// under joins a repo-relative directory and a relative path, "." being the
// repository root.
func under(dir, rel string) string {
	if dir == "." || dir == "" {
		return rel
	}
	return dir + "/" + rel
}

func goUnitLanding(files map[string]string) func(unit, source string) string {
	var once sync.Once
	testFiles := map[string][]string{} // directory -> its _test.go files, sorted
	return func(unit, source string) string {
		once.Do(func() {
			for f := range files {
				if strings.HasSuffix(f, "_test.go") {
					testFiles[path.Dir(f)] = append(testFiles[path.Dir(f)], f)
				}
			}
			for _, fs := range testFiles {
				sort.Strings(fs)
			}
		})
		dir := path.Dir(source)
		pkg, ok := goImportPathOf(files, dir)
		if !ok || unit != pkg {
			return ""
		}
		// Only an INTERNAL test file can land: one in the source's own
		// package clause. An external test file (`package <name>_test`)
		// is compiled as a separate package and cannot reach unexported
		// identifiers, which is the very reason a unit outside the package
		// does not land (R4). A source whose package clause cannot be read
		// lands nothing.
		clause := goPackageClause(files[source])
		if clause == "" {
			return ""
		}
		internal := func(f string) bool {
			text, ok := files[f]
			return ok && goPackageClause(text) == clause
		}
		own := under(dir, strings.TrimSuffix(path.Base(source), ".go")+"_test.go")
		if internal(own) {
			return own
		}
		for _, f := range testFiles[dir] {
			if internal(f) {
				return f
			}
		}
		return ""
	}
}

// goPackageClause is the package name a Go file's text declares, or ""
// when no package clause can be read. Comments before it — line comments,
// build constraints, block comments — are passed over.
func goPackageClause(text string) string {
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for line != "" {
			if inBlock {
				end := strings.Index(line, "*/")
				if end < 0 {
					line = ""
					break
				}
				inBlock = false
				line = strings.TrimSpace(line[end+2:])
				continue
			}
			if strings.HasPrefix(line, "//") {
				line = ""
				break
			}
			if strings.HasPrefix(line, "/*") {
				inBlock = true
				line = line[2:]
				continue
			}
			rest, ok := strings.CutPrefix(line, "package")
			if !ok || rest == "" || !strings.ContainsAny(rest[:1], " \t") {
				return ""
			}
			name := strings.Fields(rest)
			if len(name) == 0 {
				return ""
			}
			return strings.TrimSuffix(name[0], ";")
		}
	}
	return ""
}

// goImportPathOf is lang.GoImportPathOf over the scan's files.
func goImportPathOf(files map[string]string, dir string) (string, bool) {
	return lang.GoImportPathOf(func(name string) (string, bool) {
		text, ok := files[name]
		return text, ok
	}, dir)
}

func rustUnitLanding(files map[string]string) func(unit, source string) string {
	var once sync.Once
	pkgDirs := map[string]string{}
	return func(unit, source string) string {
		once.Do(func() { pkgDirs = rustPackageDirs(files) })
		pkg, kind, name, ok := rustUnitParts(unit)
		if !ok {
			return ""
		}
		dir := pkgDirs[pkg]
		if dir == "" || rustOwningPackage(files, source) != pkg {
			return ""
		}
		switch kind {
		case "test":
			for _, f := range []string{under(dir, "tests/"+name+".rs"), under(dir, "tests/"+name+"/main.rs")} {
				if _, ok := files[f]; ok {
					return f
				}
			}
		case "lib":
			binRoot := source == under(dir, "src/main.rs") || strings.HasPrefix(source, under(dir, "src/bin/"))
			if strings.HasPrefix(source, under(dir, "src/")) && !binRoot {
				return source
			}
		case "bin":
			roots := []string{under(dir, "src/bin/"+name+".rs"), under(dir, "src/bin/"+name+"/main.rs")}
			if name == pkg {
				roots = append(roots, under(dir, "src/main.rs"))
			}
			for _, r := range roots {
				if source == r {
					return source
				}
			}
		}
		return ""
	}
}

// rustPackageDirs maps each Cargo package name to its directory, "" when
// two manifests share a name (neither can be told apart from the other).
func rustPackageDirs(files map[string]string) map[string]string {
	pkgDirs := map[string]string{}
	for f, text := range files {
		if path.Base(f) != "Cargo.toml" {
			continue
		}
		name, ok := tomllite.StringValue(text, "package", "name")
		if !ok {
			continue
		}
		if _, seen := pkgDirs[name]; seen {
			pkgDirs[name] = ""
			continue
		}
		pkgDirs[name] = path.Dir(f)
	}
	return pkgDirs
}

// rustUnitParts reads a Rust unit name in either runner's spelling: cargo
// test's `<pkg>/lib`, `<pkg>/test:<name>`, `<pkg>/bin:<name>`, or a nextest
// binary id, `<pkg>`, `<pkg>::<name>`, `<pkg>::bin/<name>`. ok is false for
// any other shape, including nextest's example and bench binaries.
func rustUnitParts(unit string) (pkg, kind, name string, ok bool) {
	if p, rest, found := strings.Cut(unit, "::"); found {
		if k, n, sub := strings.Cut(rest, "/"); sub {
			return p, k, n, k == "bin" && n != ""
		}
		return p, "test", rest, rest != ""
	}
	p, target, found := strings.Cut(unit, "/")
	if !found {
		return unit, "lib", "", unit != ""
	}
	if target == "lib" {
		return p, "lib", "", p != ""
	}
	k, n, found := strings.Cut(target, ":")
	if !found || (k != "test" && k != "bin") || n == "" {
		return "", "", "", false
	}
	return p, k, n, p != ""
}

// rustOwningPackage is the package that owns source: the nearest Cargo.toml
// above it with a [package] name (a virtual manifest is passed over), as
// lang's ScopeCmd resolves it on disk; "" when none.
func rustOwningPackage(files map[string]string, source string) string {
	for d := path.Dir(source); ; d = path.Dir(d) {
		if text, ok := files[under(d, "Cargo.toml")]; ok {
			if name, ok := tomllite.StringValue(text, "package", "name"); ok {
				return name
			}
		}
		if d == "." || d == "/" {
			return ""
		}
	}
}
