// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// SelectionEvidence is what ONE instrumented run of the suite learned about
// which tests execute which files, held for the whole scan so each job can
// ask about its own file without re-running anything. Ran distinguishes "we
// have evidence" from "we could not get any" — and when it is false, Note
// is the reason every file in this scan grades whole-suite, disclosed.
type SelectionEvidence struct {
	Raw  []byte
	Ran  bool
	Note string
}

// detailedRunner is commandRunner's OPTIONAL richer twin: a runner that can
// also report an instrumented run's exit status and a stderr tail, not just
// its stdout. Declared locally and structurally, exactly as commandRunner
// is (see preflight.go's own doc on why) — every existing runner, real or
// faked in a test, that does not implement it keeps compiling unchanged;
// CollectSelectionEvidence type-asserts for it and falls back to the plain
// commandRunner contract when a runner doesn't.
//
// It exists for exactly one caller's problem: a shell-wrapped instrumented
// command (python's Instrument, `sh -c "…; rc=$?; [ \"$rc\" -eq 0 ] || exit
// \"$rc\"; …"`) that fails before its own JSON-emitting step runs prints
// NOTHING on stdout and a plain Enumerate returns (out="", err=nil) — a
// non-zero exit is a RESULT, not an error, on this seam (see Enumerate's own
// doc). Without exitCode/stderr, "the suite printed nothing" and "the suite
// never got the chance to print anything, and here is why" are
// indistinguishable, and only one of those is actually "no evidence".
type detailedRunner interface {
	EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error)
}

// runSelectionCmd runs cmd via runner's detailed contract when available,
// falling back to the plain one otherwise. exitCode is -1 when unknown (no
// detailed contract, or the process did not exit normally).
func runSelectionCmd(ctx context.Context, runner commandRunner, files map[string]string, cmd []string) (out string, exitCode int, stderr string, err error) {
	if dr, ok := runner.(detailedRunner); ok {
		res, rerr := dr.EnumerateDetailed(ctx, files, cmd)
		return res.Output, res.ExitCode, res.Stderr, rerr
	}
	out, err = runner.Enumerate(ctx, files, cmd)
	return out, -1, "", err
}

// selectionFailureHint asks p (when it implements the optional
// lang.SelectionDiagnoser) to recognize its own instrumented run's failure
// text — e.g. python naming the missing pytest-cov plugin — and returns ""
// when the plugin doesn't implement it, or recognizes nothing in text.
func selectionFailureHint(p lang.Plugin, text string) string {
	d, ok := p.(lang.SelectionDiagnoser)
	if !ok {
		return ""
	}
	return d.DiagnoseSelectionFailure(text)
}

// maxSelectionNoteTail bounds how much of a captured stderr this package
// ever folds into a Note: an operator-readable disclosure line, not a log
// dump.
const maxSelectionNoteTail = 2000

func selectionNoteTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxSelectionNoteTail {
		s = "…" + s[len(s)-maxSelectionNoteTail:]
	}
	return s
}

// emptySelectionRunNote explains an instrumented run that produced NO
// usable stdout — the failure this package exists to stop mislabelling
// Ran:true. exitCode < 0 means the runner could not report one (no detailed
// contract, or a run that never exited normally).
func emptySelectionRunNote(p lang.Plugin, exitCode int, stderr string) string {
	var detail string
	switch {
	case exitCode == 0:
		detail = "exited 0 and printed nothing"
	case exitCode > 0:
		detail = fmt.Sprintf("exited %d and printed nothing", exitCode)
	default:
		detail = "printed nothing"
	}
	note := fmt.Sprintf("%s: selection evidence run %s", p.Name(), detail)
	if hint := selectionFailureHint(p, stderr); hint != "" {
		return note + " — " + hint
	}
	if tail := selectionNoteTail(stderr); tail != "" {
		return note + fmt.Sprintf(" (stderr: %s)", tail)
	}
	return note
}

// pathologicalSelectionDocumentNote explains a well-formed, non-empty
// evidence document that nonetheless measured NO source file under the repo
// root — the src-layout/editable-install failure mode: coverage.py resolves
// a file's path against wherever it was actually imported from, and an
// editable install (`pip install -e .`) commonly imports the package's real
// sources from OUTSIDE the checked-out tree (a build dir, site-packages'
// __editable__ shim), so the reducer's own repo-root filter drops every one
// of them while the in-tree test files — which really do live under root —
// survive. The document is then unusable for candidacy or grading: it
// "measures" every source file as absent, which this package never reads as
// uncovered (see python.go's Select), but leaving each file to discover
// that alone, one confusing per-file error at a time, buries the actual,
// scan-wide cause. Caught once, here, instead.
func pathologicalSelectionDocumentNote(p lang.Plugin) string {
	return fmt.Sprintf(
		"%s: the instrumented run's evidence measured no source file under the repo root (only test files, or none at all) — "+
			"this is the signature of an editable/src-layout install, whose sources coverage measures OUTSIDE the repo root and so drops entirely; "+
			"try running the suite against the in-tree sources (e.g. `pip install -e . --config-settings editable_mode=compat`, or PYTHONPATH=src) or pass --whole-suite — "+
			"grading by the whole suite and pairing by name only until then", p.Name())
}

// hasMeasuredSourceFile reports whether measured — one instrumented run's
// full per-file readout (lang.TestSelector.Index) — contains at least one
// file that is not itself a test, by the SAME markers isTestFile uses for
// candidacy. Zero means the document is unusable (see
// pathologicalSelectionDocumentNote): a document that measured nothing but
// its own test files cannot answer "does a test cover this SOURCE file" for
// any file that matters.
func hasMeasuredSourceFile(p lang.Plugin, measured map[string]lang.FileCoverage) bool {
	for path := range measured {
		if !isTestFile(p, path) {
			return true
		}
	}
	return false
}

// sourceRootsFor derives the distinct top-level directories p's OWN,
// non-test source files live under, from sourcePaths — the scan's full
// enumerated file list (every language mixed together; filtered here to p
// by lang.Detect, the same oracle Enumerate itself uses). "." names a file
// that sits directly at the repo root, with no directory component at all.
// Sorted for a STABLE result independent of sourcePaths' own iteration
// order, since callers key an instrumented run's identity off the command
// this feeds (see selectionCmdDigest) — a nondeterministic root order would
// make the SAME tree hash to two different cache keys.
//
// This is advisory input for lang.SourceRootInstrumenter (python.go's
// InstrumentSourceRoots): scoping an instrumented run's coverage collection
// to a project's REAL source root(s), rather than coverage.py's whole-cwd
// default, is what makes "uncovered" reachable at all for a
// src-layout/editable install — see that interface's own doc for why the
// default makes it unreachable instead.
func sourceRootsFor(p lang.Plugin, sourcePaths []string) []string {
	seen := map[string]bool{}
	var roots []string
	for _, path := range sourcePaths {
		detected, ok := lang.Detect(path)
		if !ok || detected.Name() != p.Name() {
			continue
		}
		if isTestFile(p, path) {
			continue
		}
		root := "."
		if i := strings.Index(path, "/"); i >= 0 {
			root = path[:i]
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots
}

// instrumentCmd asks sel for the instrumented command, preferring the
// OPTIONAL richer lang.SourceRootInstrumenter contract (with sourceRoots)
// when p implements it, falling back to the plain Instrument(testCmd)
// every other plugin, and any test fake, still gets.
func instrumentCmd(p lang.Plugin, sel lang.TestSelector, testCmd []string, sourceRoots []string) (cmd []string, ok bool) {
	if ri, ok := p.(lang.SourceRootInstrumenter); ok {
		return ri.InstrumentSourceRoots(testCmd, sourceRoots)
	}
	return sel.Instrument(testCmd)
}

// CollectSelectionEvidence runs the plugin's Instrument command once in the
// scan's substrate. Never fatal: any refusal or failure becomes a Note,
// because a scan that cannot select still has a real (whole-suite)
// measurement to make — it just has to say which one it made.
//
// sourcePaths is the scan's full enumerated source list (every language
// mixed, whatever enumeratedSourcePaths built) — used only to DERIVE source
// roots (sourceRootsFor) for a plugin that can use them
// (lang.SourceRootInstrumenter); nil is a legitimate "none known", never an
// error, and every plugin that does not implement that optional interface
// ignores it entirely.
//
// An instrumented run that produced no usable stdout — whitespace-only or
// genuinely empty — is NEVER Ran:true: a shell-wrapped instrumented command
// that fails before its own evidence-emitting step (e.g. python's pytest
// missing the pytest-cov plugin Instrument requires) exits non-zero and
// prints nothing, which Enumerate reports as (out="", err=nil) — a
// non-zero exit is a RESULT there, not an error — and treating that as a
// successful, empty-but-real run would record it as a Ran:true evidence
// document that measured NOTHING, indistinguishable on its face from a
// suite that genuinely covers zero files.
func CollectSelectionEvidence(ctx context.Context, runner commandRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) SelectionEvidence {
	sel, ok := p.(lang.TestSelector)
	if !ok {
		return SelectionEvidence{Note: fmt.Sprintf("no selector for %s", p.Name())}
	}
	cmd, ok := instrumentCmd(p, sel, testCmd, sourceRootsFor(p, sourcePaths))
	if !ok {
		return SelectionEvidence{Note: fmt.Sprintf("%s: cannot instrument test command %v", p.Name(), testCmd)}
	}
	out, exitCode, stderr, err := runSelectionCmd(ctx, runner, files, cmd)
	if err != nil {
		note := fmt.Sprintf("%s: selection evidence run failed: %v", p.Name(), err)
		if hint := selectionFailureHint(p, err.Error()+" "+stderr+" "+out); hint != "" {
			note += " — " + hint
		}
		return SelectionEvidence{Note: note}
	}
	if strings.TrimSpace(out) == "" {
		return SelectionEvidence{Note: emptySelectionRunNote(p, exitCode, stderr)}
	}
	// The document parsed and is non-empty; check it is not the
	// src-layout/editable-install pathology before trusting it as Ran. A
	// parse failure here is NOT this function's business — sel.Index
	// erroring just means Select will error identically per file, disclosed
	// exactly as it always has been; only a document that parses AND turns
	// out to measure no source file gets the more specific note.
	if measured, mErr := sel.Index([]byte(out)); mErr == nil && !hasMeasuredSourceFile(p, measured) {
		return SelectionEvidence{Note: pathologicalSelectionDocumentNote(p)}
	}
	return SelectionEvidence{Raw: []byte(out), Ran: true}
}

// For answers one file. A whole-suite answer is a Selection with Cmd nil
// and a non-empty Fallback; the caller runs testCmd and records Fallback.
func (e SelectionEvidence) For(p lang.Plugin, repoRoot, codePath, testPath string, testCmd []string) lang.Selection {
	if !e.Ran {
		// A zero-value evidence (never collected at all) has no Note, and an
		// empty Fallback would be an UNDISCLOSED whole-suite grade — the one
		// outcome this type exists to make impossible. Say it structurally
		// rather than relying on every caller to have set a Note.
		if e.Note == "" {
			return lang.Selection{Fallback: "no selection evidence was collected"}
		}
		return lang.Selection{Fallback: e.Note}
	}
	sel, ok := p.(lang.TestSelector)
	if !ok {
		return lang.Selection{Fallback: fmt.Sprintf("no selector for %s", p.Name())}
	}
	s, err := sel.Select(e.Raw, repoRoot, codePath, testPath, testCmd)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	return s
}
