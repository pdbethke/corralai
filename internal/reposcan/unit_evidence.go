// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"fmt"
	"path"
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
	Err    string
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
	Note  string
}

// Reasons a unit contributes nothing. They are written into UnitResult.Err
// verbatim, and the scan line repeats them.
const (
	unitNotRunTimeLimit   = "not run (time limit)"
	unitNotRunInterrupted = "not run (interrupted)"
	unitTimedOut          = "timed out"
	unitInterrupted       = "interrupted"
	unitTruncated         = "report truncated"
	unitNotAlone          = "not run: its one-unit form would also run another unit"
	unitNoSuiteExit       = "no suite exit status in the report"
	// unitCouldNotRun prefixes a runner's own error: the unit never ran,
	// for a reason outside the tree (a jail that could not start).
	unitCouldNotRun = "could not run: "
	// unitCoverageUnavailable prefixes the instrument's own reason.
	unitCoverageUnavailable = "coverage unavailable: "
)

// CollectUnitEvidence lists the units inv collects and runs each one's
// coverage command (CoverageCmd over UnitRunCmd) once, workers at a time.
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
		return UnitEvidence{Note: fmt.Sprintf("%s: `%s` has no one-unit form corral can list and run, so no unit evidence", p.Name(), command)}
	}

	out, exit, stderr, err := runSelectionCmd(ctx, runner, files, listCmd)
	switch {
	case err != nil:
		return UnitEvidence{Note: fmt.Sprintf("%s: listing the test units of `%s` failed: %v", p.Name(), command, err)}
	case exit > 0:
		return UnitEvidence{Note: fmt.Sprintf("%s: listing the test units of `%s` exited %d%s", p.Name(), command, exit, failureTail(stderr, out))}
	case truncated(out):
		return UnitEvidence{Note: fmt.Sprintf("%s: the unit listing of `%s` was truncated, so the units it names cannot be trusted to be all of them", p.Name(), command)}
	}
	units := lang.ParseUnitList(p, inv, out)
	if units == nil {
		return UnitEvidence{Note: fmt.Sprintf("%s: could not read the unit listing of `%s`%s", p.Name(), command, failureTail(stderr, out))}
	}
	if len(units) == 0 {
		return UnitEvidence{Note: fmt.Sprintf("%s: `%s` collects no test units", p.Name(), command)}
	}

	results := make([]UnitResult, len(units))
	cmds := make([][]string, len(units))
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
			results[i].Err = fmt.Sprintf("not run: `%s` has no one-unit command for it", command)
			continue
		}
		cov, ok := reporter.CoverageCmd(unitCmd)
		if !ok {
			results[i].Err = "not run: its one-unit command has no coverage instrumentation"
			continue
		}
		cmds[i] = cov
		runnable = append(runnable, i)
	}
	if len(runnable) == 0 {
		return UnitEvidence{Units: results, Note: fmt.Sprintf("%s: no unit `%s` collects can run alone under coverage, so no unit evidence", p.Name(), command)}
	}

	announce(len(runnable), len(units))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(runnable)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := runUnit(ctx, runner, files, reporter, cmds[i], modulePath)
				r.Unit = results[i].Unit
				results[i] = r
			}
		}()
	}
	for _, i := range runnable {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return UnitEvidence{Ran: true, Units: results}
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

// runUnit runs one unit's coverage command and reads its report. Every way
// the run can fail to measure is an Err, never an empty Counts.
func runUnit(ctx context.Context, runner commandRunner, files map[string]string, reporter lang.CoverageReporter, cmd []string, modulePath string) UnitResult {
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
		return UnitResult{Err: unitCouldNotRun + err.Error()}
	case exit > 0:
		// A unit whose own run failed — a red test, a build error, a
		// toolchain the jail lacks — is not evidence, even when a report
		// still parses: a failing unit can stop before the code it would
		// have executed, so its zeros are not "never executed".
		return UnitResult{Err: fmt.Sprintf("run failed: exited %d%s", exit, failureTail(stderr, lang.BeforeSuiteExit(out)))}
	case detailed && exit < 0:
		return UnitResult{Err: "run failed: the process did not exit normally" + failureTail(stderr, lang.BeforeSuiteExit(out))}
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
		return UnitResult{Err: fmt.Sprintf("run failed: exit %d%s", rc, failureTail(stderr, lang.BeforeSuiteExit(out)))}
	}
	if reason, unavailable := lang.CoverageUnavailable(out); unavailable {
		return UnitResult{Err: unitCoverageUnavailable + reason}
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
			return UnitResult{Err: "unparseable: " + empty.Summary(), Uncounted: lang.UncountedCoverage(out)}
		}
		return UnitResult{Err: fmt.Sprintf("unparseable: %v", err)}
	}
	return UnitResult{Counts: counts, Uncounted: lang.UncountedCoverage(out), Loaded: lang.LoadedCoverage(out)}
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

// IndexFromUnits builds the EvidenceIndex a unit pass supports. ok is false
// when the pass never ran or no unit succeeded: there is nothing to widen
// candidacy with.
//
// A source file's covering count is the number of successful units that
// executed at least one of its lines AND can land a test for it:
// unitTestFile(unit, source) names the test file an authored test for that
// source would go in when it is graded by that unit, and "" when no such
// file exists (a Go test outside the source's package cannot reach its
// unexported code; a Rust integration test of one crate does not grade
// another). Such a unit is left out of the file's count altogether, so it
// can never promote the file. mostCovering is the landing file of the units
// with the most executed lines of the file, summed per landing file, with
// moreSpecificTestPath breaking ties as ParseEvidenceIndex does.
//
// Two shapes are left OUT of the index — unmeasured, never a measured
// zero — because a zero would be a claim the evidence does not support:
//   - a file some unit executed but no unit can land a test for. It was
//     executed; reading it as "no test covers this" would be false.
//   - a file no landable unit executed, when the pass was partial (a unit
//     failed, timed out or never ran). The unit that did not finish may be
//     the one that executes it, and a failed unit is never "covered nothing".
func IndexFromUnits(ev UnitEvidence, unitTestFile func(unit, source string) string) (EvidenceIndex, bool) {
	if !ev.Ran {
		return EvidenceIndex{}, false
	}
	type tally struct {
		executed, hasStatements, loaded bool
		units                           int
		perFile                         map[string]int
	}
	tallies := map[string]*tally{}
	succeeded, partial := 0, false
	for _, u := range ev.Units {
		if u.Err != "" {
			partial = true
			continue
		}
		succeeded++
		loaded := make(map[string]bool, len(u.Loaded))
		for _, f := range u.Loaded {
			loaded[f] = true
		}
		for src, lc := range u.Counts {
			t := tallies[src]
			if t == nil {
				t = &tally{perFile: map[string]int{}}
				tallies[src] = t
			}
			t.hasStatements = t.hasStatements || lc.Executable > 0
			t.loaded = t.loaded || loaded[src]
			if lc.Executed == 0 {
				continue
			}
			t.executed = true
			landing := unitTestFile(u.Unit, src)
			if landing == "" {
				continue
			}
			t.units++
			t.perFile[landing] += lc.Executed
		}
	}
	if succeeded == 0 {
		return EvidenceIndex{}, false
	}

	files := make(map[string]evidenceFileEntry, len(tallies))
	for src, t := range tallies {
		if t.units == 0 && (t.executed || partial) {
			continue
		}
		covering := make(map[string]bool, len(t.perFile))
		best, bestLines := "", -1
		for testFile, lines := range t.perFile {
			covering[testFile] = true
			if lines > bestLines || (lines == bestLines && moreSpecificTestPath(testFile, best)) {
				best, bestLines = testFile, lines
			}
		}
		files[src] = evidenceFileEntry{
			coveringTests: t.units,
			coveringUnits: t.units,
			mostCovering:  best,
			coveringFiles: covering,
			// A unit that loaded the file ran its module-level code: the
			// unit-pass counterpart of a per-test index's static coverage,
			// which is what makes an unexecuted file import-only.
			hasStatic:     t.loaded,
			hasStatements: t.hasStatements,
		}
	}
	return EvidenceIndex{files: files, unitKind: true}, true
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

// goImportPathOf is the import path of the package in dir: the nearest
// go.mod at or above it names the module, and dir's place below that go.mod
// the rest. ok is false when no go.mod above dir declares a module.
func goImportPathOf(files map[string]string, dir string) (string, bool) {
	for d := dir; ; d = path.Dir(d) {
		if text, ok := files[under(d, "go.mod")]; ok {
			mod, err := lang.GoModuleDirective(text)
			if err != nil {
				return "", false
			}
			if d == dir {
				return mod, true
			}
			return mod + "/" + strings.TrimPrefix(dir, under(d, "")), true
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}

func rustUnitLanding(files map[string]string) func(unit, source string) string {
	var once sync.Once
	pkgDirs := map[string]string{} // package name -> its directory; "" when two share a name
	return func(unit, source string) string {
		once.Do(func() {
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
		})
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
