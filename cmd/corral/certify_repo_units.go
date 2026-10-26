// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The unit evidence pass in certify --repo: for a language with no per-test
// selector (every language but Python), each test unit the operator's command
// collects is run alone under coverage once (reposcan.CollectUnitEvidence),
// and the result widens CANDIDACY exactly as Python's selection evidence does
// — a source file only a unit's execution pairs becomes a candidate, landing
// beside that unit. It never touches grading: selectionMethod, and so
// auditConfigKey and every verdict's cache key, read only the selection pass.
//
// Python keeps its own path because its selector already attributes
// coverage per test from one run; a unit pass there would be a second,
// coarser answer to a question already answered.

// collectUnitEvidence is reposcan.CollectUnitEvidence behind a package var,
// the seam collectSelectionEvidence is for the selection pass: the one call
// in collectUnits that runs the project's tests, so a test of what the scan
// does with unit evidence can stand in for it without a runner.
var collectUnitEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, inv lang.Invocation, modulePath string, workers int, announce func(runnable, listed int)) reposcan.UnitEvidence {
	return reposcan.CollectUnitEvidence(ctx, runner, files, p, inv, modulePath, workers, announce)
}

// runsUnitPass reports whether the unit pass is the evidence path for plug:
// a resolved language with no per-test selector. A nil plug (no language, or
// an ambiguous mixed repository) runs no pass; the selection line has
// already said why.
func runsUnitPass(plug lang.Plugin) bool {
	if plug == nil {
		return false
	}
	_, selects := plug.(lang.TestSelector)
	return !selects
}

// collectUnits runs the unit pass for the scan's language over sources (the
// enumerated source set, as collectSelection takes it), or serves a fully green
// earlier pass over a byte-identical tree from the selection cache. Every
// refusal is a Note, never fatal: the scan stays name-paired for that
// language, and says so.
//
// Its bounds are the scan's own: the whole pass runs under selectionTimeout
// (units not started by then are recorded "not run (time limit)", the ones
// that finished keep their evidence), each unit's command under the same
// per-command cap the selection run has, and l.unitWorkers units at a time.
//
// It also sets l.unitLanding, the repository a unit's authored test lands in,
// which the caller hands to reposcan.UnitTestFile — on a cache hit as on a
// fresh pass, since landing is a question about the tree, not the run.
func (l *localExecutor) collectUnits(ctx context.Context, sources []string) reposcan.UnitEvidence {
	plug, langName, testCmd, note := l.resolveSelectionPlugin(sources)
	if plug == nil {
		return reposcan.UnitEvidence{Note: note}
	}
	if !runsUnitPass(plug) {
		return reposcan.UnitEvidence{Note: fmt.Sprintf("%s grades through its own per-test selector, so it runs no unit pass", langName)}
	}
	inv, err := lang.Resolve(plug, l.repoDir, testCmd, l.runnerFor(langName))
	if err != nil {
		return reposcan.UnitEvidence{Note: fmt.Sprintf("%s: %v", langName, err)}
	}
	modulePath, err := l.coverageModulePath(langName)
	if err != nil {
		return reposcan.UnitEvidence{Note: fmt.Sprintf("%s: %v", langName, err)}
	}
	runner, files, err := l.scanRunner(langName, plug, selectionMaxOutput, selectionTimeout)
	if err != nil {
		return reposcan.UnitEvidence{Note: fmt.Sprintf("%s: unit pass: %v", langName, err)}
	}
	// The landing map. The jail's seed holds the tree's text, and the
	// runner applies it to the jail; the workspace runner applies files to
	// the REAL checkout, so it is handed none (scanRunner) and the landing
	// map is read from the checkout instead, never passed to a runner.
	if l.substrate == substrateWorkspace {
		landing, lerr := workspaceLandingFiles(l.repoDir, l.unitInventory)
		if lerr != nil {
			l.unitLandingNote = fmt.Sprintf("evidence pairing could not land tests on this substrate: %v", lerr)
		} else {
			l.unitLanding = landing
		}
	} else {
		l.unitLanding = files
	}

	treeDigest, cmdDigest, keyOK := "", "", false
	if l.selectionCache != nil {
		treeDigest, cmdDigest, keyOK = l.unitsCacheKey(plug, inv)
		if keyOK {
			raw, hit, gerr := l.selectionCache.SelectionCacheGet(ctx, treeDigest, cmdDigest, unitsCachePlugin(langName), l.substrate)
			var cached reposcan.UnitEvidence
			if gerr == nil && hit && json.Unmarshal(raw, &cached) == nil && cached.Ran {
				l.unitsReused = true
				return cached
			}
		}
	}

	passCtx, cancel := context.WithTimeout(ctx, selectionTimeout)
	defer cancel()
	start := time.Now()
	ev := collectUnitEvidence(passCtx, runner, files, plug, inv, modulePath, l.unitWorkers, func(runnable, listed int) {
		fmt.Fprintf(l.progress, "  units: running %d of %d listed test unit(s) alone under coverage, %d at a time, within %s for the whole pass…\n",
			runnable, listed, l.unitWorkers, selectionTimeout)
	})
	l.unitsDuration = time.Since(start)
	if ev.Settled() && keyOK {
		if raw, merr := json.Marshal(ev); merr == nil {
			l.pendingUnitsPut = &pendingSelectionCachePut{TreeDigest: treeDigest, CmdDigest: cmdDigest, Plugin: unitsCachePlugin(langName), Substrate: l.substrate, Raw: raw}
		}
	}
	return ev
}

// unitsCacheKey is the selection cache key a unit pass is stored under: the
// tree (treeDigestOnce, as selection's), and a digest over the two command
// shapes that produced the evidence — the unit listing argv, and the
// coverage instrument wrapped around the operator's command, which is the
// wrapper every one-unit command is run under. ok is false when either
// cannot be named: no listing (the pass refuses before running anything,
// so there is nothing to cache) or a tree outside git.
func (l *localExecutor) unitsCacheKey(plug lang.Plugin, inv lang.Invocation) (treeDigest, cmdDigest string, ok bool) {
	listCmd, ok := lang.ListUnitsCmd(plug, inv)
	if !ok {
		return "", "", false
	}
	reporter, ok := plug.(lang.CoverageReporter)
	if !ok {
		return "", "", false
	}
	covCmd, ok := reporter.CoverageCmd(inv.Argv)
	if !ok {
		return "", "", false
	}
	td, ok := l.treeDigestOnce()
	if !ok {
		return "", "", false
	}
	return td, unitsCmdDigest(listCmd, covCmd), true
}

// unitsCmdDigest is selectionCmdDigest over two argv lists, each prefixed by
// its own length so no two different pairs fold to the same bytes.
func unitsCmdDigest(listCmd, covCmd []string) string {
	h := sha256.New()
	for _, argv := range [][]string{listCmd, covCmd} {
		fmt.Fprintf(h, "%d#", len(argv))
		for _, a := range argv {
			fmt.Fprintf(h, "%d:%s|", len(a), a)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkoutInventory is every file in the checkout the scan's enumeration
// classified — candidates, their paired tests, and every exclusion naming a
// file of the repository's own (a test, a manifest, a source pairing missed)
// — and none of those it walked past as not the repository's: skipped
// dependency trees, gitignored output, non-regular files. Sorted, once each.
func checkoutInventory(cands []reposcan.Candidate, excl []reposcan.Exclusion) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range cands {
		add(c.Path)
		add(c.TestPath)
	}
	for _, e := range excl {
		switch e.Reason {
		case reposcan.ReasonSkippedDir, reposcan.ReasonGitignored, reposcan.ReasonNotRegularFile:
			continue
		}
		add(e.Path)
	}
	return out
}

// workspaceLandingFiles is the landing map reposcan.UnitTestFile needs, read
// from the checkout itself, for the workspace substrate (whose runner is
// handed no files, since it applies them to the real checkout). Every path in
// inventory is present; text is read only for what landing reads — go.mod
// and Cargo.toml (what a unit name means on disk) and Go files (their
// package clause) — and every other file is present with empty text, which
// UnitTestFile checks for existence only. It is never handed to a runner.
//
// Reads go through os.OpenRoot, so a link in the checkout cannot make it read
// outside the repository. Any read error refuses the map rather than
// leaving out a manifest: a missing go.mod would land nothing, silently.
func workspaceLandingFiles(repoDir string, inventory []string) (map[string]string, error) {
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	files := make(map[string]string, len(inventory))
	for _, p := range inventory {
		base := path.Base(p)
		if base != "go.mod" && base != "Cargo.toml" && !strings.HasSuffix(base, ".go") {
			files[p] = ""
			continue
		}
		b, rerr := root.ReadFile(p)
		if rerr != nil {
			return nil, fmt.Errorf("reading %s: %w", p, rerr)
		}
		files[p] = string(b)
	}
	return files, nil
}

// unitPassFailed counts the units that contributed no evidence.
func unitPassFailed(ev reposcan.UnitEvidence) int {
	n := 0
	for _, u := range ev.Units {
		if u.Err != "" {
			n++
		}
	}
	return n
}

// unitPassUncounted is how many file measurements the pass's units could
// not count, summed over the units: the phase_units event's number, whose
// list is the --dry-run --json inventory's.
func unitPassUncounted(ev reposcan.UnitEvidence) int {
	n := 0
	for _, u := range ev.Units {
		n += len(u.Uncounted)
	}
	return n
}

// printUnitPass is the scan's units line: how many units the pass listed and
// how many contributed nothing, then each of those once with its reason;
// when the pass never ran, its Note. A landing map that could not be built
// is said here too, since the pass then adds no candidate however it ran.
func printUnitPass(w io.Writer, ex *localExecutor) {
	ev := ex.units
	if !ev.Ran {
		fmt.Fprintf(w, "  units: %s\n", ev.Note)
		return
	}
	// No duration here: the scan's report is compared run against run
	// (the preflight byte-identity test), and the time is the phase_units
	// event's to record.
	basis := fmt.Sprintf("%d at a time", ex.unitWorkers)
	if ex.unitsReused {
		basis = "reused — tree unchanged since an earlier scan (cached evidence)"
	}
	fmt.Fprintf(w, "  units: %d run, %d failed (%s)\n", len(ev.Units), unitPassFailed(ev), basis)
	// Capped as printEvidencePairings caps its list: on a large suite that
	// hits the time limit every unit has a line, and the full list is the
	// count line above plus --dry-run --json (ruling R30).
	listed, withheld := 0, 0
	for _, u := range ev.Units {
		if u.Err == "" && len(u.Uncounted) == 0 {
			continue
		}
		if listed == maxListedExclusions {
			withheld++
			continue
		}
		listed++
		if u.Err != "" {
			// A failed unit's reason ends with its output tail, which can
			// run over several lines; each is indented under the unit so
			// it reads as that unit's, not as a line of the scan.
			fmt.Fprintf(w, "    %s: %s\n", u.Unit, strings.ReplaceAll(u.Err, "\n", "\n      "))
		}
		// The files a unit measured and could not count are a COUNT here
		// and a list in the --dry-run --json inventory (ruling R22): listed
		// on the scan line, REA's 25 units made it 1.4 MB.
		if n := len(u.Uncounted); n > 0 {
			fmt.Fprintf(w, "    %s: %d file(s) measured and not counted (listed in --dry-run --json)\n", u.Unit, n)
		}
	}
	if withheld > 0 {
		fmt.Fprintf(w, "    ... and %d more unit(s) that failed or left files uncounted (every one is in --dry-run --json)\n", withheld)
	}
	if ex.unitLandingNote != "" {
		fmt.Fprintf(w, "  units: %s\n", ex.unitLandingNote)
	}
}
