// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/testmap"
)

const testsUsage = `corral tests — the repository's test map: which test units execute which source files.

  corral tests map --repo-dir <dir> [--jail bwrap|container] [--workers N] [--out <file>] -- <test cmd>
                                  build the map through the evidence path certify --repo grades by
                                  (the same selection cache included), call no model, and print how
                                  many sources and units it holds, the sources no unit executes and
                                  each unit that contributed no evidence, with why. --out writes
                                  the map as JSON
`

// runTests dispatches `corral tests <verb>`. Only `map` exists; a name that is
// not a verb is refused by name rather than falling through to anything else.
func runTests(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, testsUsage)
		return 0
	}
	if args[0] == "map" {
		return runTestsMap(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "corral tests: unknown verb %q\n%s", args[0], testsUsage)
	return 2
}

// runTestsMap builds the map with the executor certify --repo builds, calling
// the same collectSelection / collectUnits — never a second evidence
// collector, so the map this prints is the map a scan would grade by. It
// calls no model: nothing past evidence collection exists on this path.
func runTestsMap(args []string, stdout, stderr io.Writer) int {
	const name = "corral tests map"
	flagArgs, checkArgv := splitCertifyArgs(args)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: corral tests map --repo-dir <dir> [--jail bwrap|container] [--workers N] [--out <file>] -- <test cmd>\n\n")
		fs.PrintDefaults()
	}
	repoDir := fs.String("repo-dir", "", "path of the repository to map (required)")
	outPath := fs.String("out", "", "write the test map to this file as JSON that corral's own map reader reads back")
	substrateFlag := fs.String("substrate", substrateJail, "where the evidence runs: "+substrateJail+" (bwrap) or "+substrateWorkspace+" (the checkout itself; the caller IS the isolation boundary, e.g. an ephemeral CI runner), as certify --repo")
	jailFlag := fs.String("jail", "", "sandbox backend for --substrate jail: bwrap (Linux), container or sandbox-exec (macOS) (default: auto-detect for this OS)")
	workers := fs.Int("workers", 0, "test units run alone under coverage at once. REQUIRED when the language's evidence is a unit pass on --substrate jail (every language but Python); corral names no count. --substrate workspace runs one at a time, as certify --repo does, and takes no count")
	runnerFlag := fs.String("runner", "", runnerHelp)
	cacheDSNFlag := fs.String("cache-db", "", "path to corral's local CACHE of instrumented test selections (default: $CORRALAI_CACHE_DB, else $CACHE/corralai_cache.duckdb, see corral state paths); a byte-identical tree reuses the evidence certify --repo collected")
	noCacheFlag := fs.Bool("no-selection-cache", false, "skip the selection cache: always run the evidence pass, and keep nothing")
	if err := fs.Parse(flagArgs); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument(s) %v — the test command goes after `--`\n", name, fs.Args())
		return 2
	}
	if *repoDir == "" {
		fmt.Fprintf(stderr, "%s: --repo-dir is required\n", name)
		return 2
	}
	if *substrateFlag != substrateJail && *substrateFlag != substrateWorkspace {
		fmt.Fprintf(stderr, "%s: --substrate %q is not %s or %s\n", name, *substrateFlag, substrateJail, substrateWorkspace)
		return 2
	}
	switch *jailFlag {
	case "", "auto", "bwrap", "container", "sandbox-exec":
	default:
		fmt.Fprintf(stderr, "%s: --jail %q is not bwrap, container or sandbox-exec\n", name, *jailFlag)
		return 2
	}
	if *jailFlag != "" && *substrateFlag == substrateWorkspace {
		fmt.Fprintf(stderr, "%s: --jail %s has no effect with --substrate %s, which runs no sandbox; drop one of them\n", name, *jailFlag, substrateWorkspace)
		return 2
	}
	if *runnerFlag != "" {
		if err := lang.CheckRunnerID(*runnerFlag); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 2
		}
	}
	if *workers < 0 {
		fmt.Fprintf(stderr, "%s: --workers must be a positive whole number, got %d\n", name, *workers)
		return 2
	}

	cands, excl, err := reposcan.EnumerateWithTests(*repoDir, nil)
	if err != nil {
		fmt.Fprintf(stderr, "%s: enumerating %s: %v\n", name, *repoDir, err)
		return 1
	}
	sources := enumeratedSourcePaths(cands, excl)

	ex := newLocalExecutorWithJail(*repoDir, checkArgv, *substrateFlag, *jailFlag, defaultRunTimeout, stdout)
	defer ex.Close()
	ex.runner = *runnerFlag
	if !*noCacheFlag {
		dsn, cerr := cacheDSNOr(*cacheDSNFlag)
		if cerr != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, cerr)
			return 2
		}
		ex.selectionCache = newSelectionLedgerCache(dsn)
	}
	plug, langName, _, note := ex.resolveSelectionPlugin(sources)
	if plug == nil {
		fmt.Fprintf(stderr, "%s: no test map can be built: %s\n", name, note)
		return 1
	}
	unitPass := runsUnitPass(plug)
	if unitPass {
		switch {
		case *substrateFlag == substrateWorkspace && *workers > 1:
			fmt.Fprintf(stderr, "%s: --workers %d has no effect with --substrate %s, which runs one unit at a time; drop it\n", name, *workers, substrateWorkspace)
			return 2
		case *substrateFlag == substrateJail && *workers < 1:
			fmt.Fprintf(stderr, "%s: --workers is required: %s's evidence runs each test unit alone under coverage, and corral names no worker count — pass --workers N\n", name, langName)
			return 2
		}
		ex.unitWorkers, _ = resolveScanWorkers(*workers, *substrateFlag)
	}
	if err := ex.preflight(); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}

	ctx, stop := interruptContext(stderr, "\ncorral: interrupted — stopping the evidence pass; no map is written. Press Ctrl-C again to abandon it at once")
	defer stop()
	var m testmap.Map
	if unitPass {
		ex.unitInventory = checkoutInventory(cands, excl)
		ex.units = ex.collectUnits(ctx, sources)
		if !ex.units.Ran {
			fmt.Fprintf(stderr, "%s: the unit pass did not run, so there is no map: %s\n", name, ex.unitsNotRunReason(langName))
			return 1
		}
		m = ex.unitMap
	} else {
		ex.selection = ex.collectSelection(ctx, sources)
		if !ex.selection.Ran {
			fmt.Fprintf(stderr, "%s: the selection pass did not run, so there is no map: %s\n", name, ex.selection.Note)
			return 1
		}
		m = ex.selection.Map
	}
	stop()
	if operatorInterrupted(nil) {
		return exitInterrupted
	}
	if m.Method == "" {
		fmt.Fprintf(stderr, "%s: the evidence built no map for %s\n", name, langName)
		return 1
	}
	printTestsMap(stdout, m)
	// Written after the summary, and only now: a pass that did not run wrote
	// nothing above, so a file on disk always holds a map that was built.
	writeEvidenceCache(ex, stderr)
	if *outPath != "" {
		if err := writeTestMapFile(*outPath, m); err != nil {
			fmt.Fprintf(stderr, "%s: writing --out %s: %v\n", name, *outPath, err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s (digest %s)\n", *outPath, m.Digest())
	}
	return 0
}

// writeTestMapFile writes m through testmap.Write — the one serializer — to a
// temporary file beside path and renames it into place, so a write that fails
// part way leaves no truncated map under the name a reader trusts.
func writeTestMapFile(path string, m testmap.Map) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if err = testmap.Write(f, m); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// printTestsMap is the summary: what the map holds, what it could not measure,
// and what no unit executes. A node is a "test" in a per-test map and a "unit"
// in a unit map, and the line says which, since the two are not the same count
// (one unit is a whole test file, package or target).
func printTestsMap(w io.Writer, m testmap.Map) {
	noun := "units"
	if m.Method == testmap.MethodCoverageContext {
		noun = "tests"
	}
	runner := m.Runner
	if runner == "" {
		runner = "none (no runner row reads this command)"
	}
	fmt.Fprintf(w, "language: %s, runner: %s\n", m.Lang, runner)
	fmt.Fprintf(w, "method: %s\n", m.Method)
	unreported := m.Unreported()
	fmt.Fprintf(w, "%d sources, %d %s (%d with no evidence)\n", len(m.Sources), len(m.Tests), noun, len(unreported))
	if m.Note != "" {
		fmt.Fprintf(w, "note: %s\n", m.Note)
	}
	uncovered, gradedByUnreported := testmap.Uncovered(m)
	listCapped(w, "uncovered", uncovered, func(s string) string { return s })
	if n := len(gradedByUnreported); n > 0 {
		// Not "uncovered": grading adds every unit with no evidence to the
		// selection of every file, so these are graded by those units.
		fmt.Fprintf(w, "%d sources executed by no unit that reported; they are graded by the %d %s with no evidence\n", n, len(unreported), noun)
	}
	listCapped(w, "no evidence", unreported, func(id string) string { return id + ": " + m.Tests[id].Err })
}

// listCapped prints one "<label>: <item>" line per item, capped as the scan's
// own exclusion lists are (maxListedExclusions) with the count withheld
// named, so a large suite that timed out does not print a line per unit.
func listCapped(w io.Writer, label string, items []string, render func(string) string) {
	for i, it := range items {
		if i == maxListedExclusions {
			fmt.Fprintf(w, "%s: ... and %d more (every one is in the file --out writes)\n", label, len(items)-i)
			return
		}
		fmt.Fprintf(w, "%s: %s\n", label, strings.ReplaceAll(render(it), "\n", " "))
	}
}
