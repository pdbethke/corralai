// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/testmap"
)

// SelectionEvidence is what ONE instrumented run of the suite learned about
// which tests execute which files, held for the whole scan so each job can
// ask about its own file without re-running anything. Ran distinguishes "we
// have evidence" from "we could not get any" — and when it is false, Note
// is the reason every file in this scan grades whole-suite, disclosed.
//
// Map is the test map built from Raw once, when the evidence was collected
// (or served from the selection cache): every file's selection, the
// candidacy index and the authored pass's command read it, so the document
// is parsed in one place. Raw stays because it is what the selection cache
// stores and keys. A Ran evidence always carries the Map its Raw built;
// NewSelectionEvidence is the one constructor that pairs them.
type SelectionEvidence struct {
	Raw []byte
	Ran bool
	// Note is why the evidence did not run, as the record keeps it: it is
	// every file's TestSelection.Fallback (For), which the ledger publishes
	// on every entry, so it is one fixed phrase per reason (a plugin's own
	// fixed hint aside) and never quotes the operator's command, a runner's
	// error or what the run printed.
	Note string
	// Detail is Note as the operator's terminal says it, with the error and
	// output that explain it; never marshalled, never read into a
	// selection. "" when Note says everything (Said).
	Detail string `json:"-"`
	Map    testmap.Map
}

// Said is why the evidence did not run as the operator's terminal says it:
// Detail when there is one, else Note. Never read it into a selection.
func (e SelectionEvidence) Said() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Note
}

// selectionRow is the runner row that reads testCmd: the row whose TestUnit
// names each test's unit in the map, and whose TestsCmd and CollectionBase
// build every narrowed command from it. A plugin produces evidence only for
// a command its instrumentation accepts (Python's, a pytest command), so
// the row is the one that command names. The row is detected from the
// command alone and ignores an operator's --runner: harmless, because only a
// pytest-shaped command instruments at all, and that command names the
// pytest row whatever --runner says. A row with no TestUnit cannot name
// a node's unit, which the engine widens to over the argv cap: refused, never
// built with units left blank.
func selectionRow(p lang.Plugin, testCmd []string) (lang.Runner, error) {
	r, ok := lang.RunnerFor(p, lang.Cmd(testCmd))
	if !ok {
		// The command itself is not quoted: this error becomes a Fallback,
		// which the ledger publishes, and the operator's argv may carry a
		// secret.
		return lang.Runner{}, fmt.Errorf("%s: no runner row reads the test command, so no test map can be built for it", p.Name())
	}
	if r.TestUnit == nil {
		return lang.Runner{}, fmt.Errorf("%s: the %s runner cannot name a test's unit, so its per-test evidence cannot be built into a test map", p.Name(), r.ID)
	}
	return r, nil
}

// BuildSelectionMap reads one instrumented run's output into the test map
// the selection engine selects from. Paths are aligned as the evidence's
// own run wrote them (lang.AlignEvidencePath with no root): the run executes
// in the scan's substrate, never in the operator's checkout path, so a
// relative path is the only form a measured repo file can take — Python's
// reducer relativizes against the run's cwd and drops anything outside it.
// This is the alignment Python's Index applied; its Select also accepted an
// absolute path under the checkout, which no run ever produced.
func BuildSelectionMap(p lang.Plugin, testCmd []string, raw []byte) (testmap.Map, error) {
	te, ok := p.(lang.TestEvidence)
	if !ok {
		return testmap.Map{}, fmt.Errorf("no selector for %s", p.Name())
	}
	row, err := selectionRow(p, testCmd)
	if err != nil {
		return testmap.Map{}, err
	}
	doc, err := te.Evidence(raw)
	if err != nil {
		return testmap.Map{}, err
	}
	m := testmap.FromEvidence(doc, func(path string) (string, bool) { return lang.AlignEvidencePath(path, "") }, row.TestUnit)
	m.Lang, m.Runner, m.Command = p.Name(), row.ID, append([]string{}, testCmd...)
	markFailedTests(&m, doc.Failed, row.TestUnit)
	return m, nil
}

// failedInEvidenceRun is the reason a test that failed in the instrumented
// run carries on its node: a "run failed" reason, like a unit's whose own
// run failed, so WithoutRedUnits may retry a red baseline without it.
const failedInEvidenceRun = unitRunFailed + "failed in the instrumented evidence run"

// markFailedTests makes every test the evidence run saw fail a node whose
// own run failed, with no edge. A failing test can stop before the code it
// would have run, so its coverage is not evidence — the unit pass's rule
// for a red unit — and as an unreported node it joins every selection and
// every mutant's command (testmap.Select, Selection.Always), so it never
// silently leaves a narrowed baseline (#164).
func markFailedTests(m *testmap.Map, failed []string, unitOf func(string) string) {
	if len(failed) == 0 {
		return
	}
	red := make(map[string]bool, len(failed))
	for _, id := range failed {
		red[id] = true
		m.Tests[id] = testmap.Node{ID: id, Grain: testmap.GrainTest, Unit: unitOf(id), Err: failedInEvidenceRun}
	}
	kept := m.Edges[:0]
	for _, e := range m.Edges {
		if !red[e.Test] {
			kept = append(kept, e)
		}
	}
	m.Edges = kept
}

// NewSelectionEvidence is evidence a caller already holds — the selection
// cache's stored Raw, served as Ran — with its map built. Raw that no longer
// builds a map (an entry stored in a format this build refuses) keeps no
// map, and every file's selection falls back to the whole suite with the
// reason the build gave (For): what a served entry grades by must not
// depend on whether it was served or freshly run.
func NewSelectionEvidence(p lang.Plugin, testCmd []string, raw []byte) SelectionEvidence {
	m, _ := BuildSelectionMap(p, testCmd, raw) // a build error is For's to disclose, per file
	return SelectionEvidence{Raw: raw, Ran: true, Map: m}
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
//
// tail false is the note as the record keeps it: the plugin's fixed hint
// but never the stderr tail, which can quote anything the run printed.
func emptySelectionRunNote(p lang.Plugin, exitCode int, stderr string, tail bool) string {
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
	if !tail {
		return note
	}
	if t := selectionNoteTail(stderr); t != "" {
		return note + fmt.Sprintf(" (stderr: %s)", t)
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
// uncovered (see testmap.Select), but leaving each file to discover
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
// full per-file readout (testmap.Map.Coverage) — contains at least one
// file that is not itself a test, by the SAME markers pairing.IsTest uses for
// candidacy. Zero means the document is unusable (see
// pathologicalSelectionDocumentNote): a document that measured nothing but
// its own test files cannot answer "does a test cover this SOURCE file" for
// any file that matters.
func hasMeasuredSourceFile(p lang.Plugin, measured map[string]lang.FileCoverage) bool {
	for path := range measured {
		if !pairing.IsTest(p.TestRules(), path) {
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
		if pairing.IsTest(p.TestRules(), path) {
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
func instrumentCmd(p lang.Plugin, sel lang.TestEvidence, testCmd []string, sourceRoots []string) (cmd []string, ok bool) {
	if ri, ok := p.(lang.SourceRootInstrumenter); ok {
		return ri.InstrumentSourceRoots(testCmd, sourceRoots)
	}
	return sel.Instrument(testCmd)
}

// InstrumentedCommand is THE command CollectSelectionEvidence runs for
// (p, testCmd, sourcePaths): the plugin's instrumentation, with the source
// roots derived from the scan's own file list where the plugin takes them.
// Exported so a caller keying a cache on "the instrumented command that
// produced this evidence" digests this command and not a different one —
// the selection cache used to key on sel.Instrument(testCmd) (`--cov`)
// while the run used InstrumentSourceRoots (`--cov=pkg_a`), so two scans of
// one tree over different source roots shared a key, and the second was
// served evidence collected over roots it never asked about.
func InstrumentedCommand(p lang.Plugin, testCmd []string, sourcePaths []string) (cmd []string, ok bool) {
	cmd, note := SelectionCommand(p, testCmd, sourcePaths)
	return cmd, note == ""
}

// SelectionCommand is InstrumentedCommand with the reason it has no command:
// note is empty exactly when cmd is the command CollectSelectionEvidence
// would run. Exported so a caller can ask BEFORE preparing the substrate the
// command would run in — jail preparation is a tree copy and a networked
// dependency fetch, and paying for it for a plugin that can instrument
// nothing (no TestEvidence, a test command it cannot instrument, or one no
// runner row can build a map for) bought only the note below.
func SelectionCommand(p lang.Plugin, testCmd []string, sourcePaths []string) (cmd []string, note string) {
	sel, ok := p.(lang.TestEvidence)
	if !ok {
		return nil, fmt.Sprintf("no selector for %s", p.Name())
	}
	cmd, ok = instrumentCmd(p, sel, testCmd, sourceRootsFor(p, sourcePaths))
	if !ok {
		// Not quoted: the note becomes a Fallback the ledger publishes.
		return nil, fmt.Sprintf("%s: cannot instrument the test command", p.Name())
	}
	if _, err := selectionRow(p, testCmd); err != nil {
		return nil, err.Error()
	}
	return cmd, ""
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
	cmd, note := SelectionCommand(p, testCmd, sourcePaths)
	if note != "" {
		return SelectionEvidence{Note: note}
	}
	// Every failure below is recorded by its fixed reason, with the
	// plugin's own fixed hint, in Note; the error and the output that
	// explain it are the operator's, in Detail.
	out, exitCode, stderr, err := runSelectionCmd(ctx, runner, files, cmd)
	if err != nil {
		hint := selectionFailureHint(p, err.Error()+" "+stderr+" "+out)
		return SelectionEvidence{Note: withHint(fmt.Sprintf("%s: selection evidence run failed: the jail could not start it", p.Name()), hint),
			Detail: withHint(fmt.Sprintf("%s: selection evidence run failed: %v", p.Name(), err), hint)}
	}
	if strings.TrimSpace(out) == "" {
		return SelectionEvidence{Note: emptySelectionRunNote(p, exitCode, stderr, false), Detail: emptySelectionRunNote(p, exitCode, stderr, true)}
	}
	// The document parsed and is non-empty; check it is not the
	// src-layout/editable-install pathology before trusting it as Ran.
	m, mErr := BuildSelectionMap(p, testCmd, []byte(out))
	if mErr != nil {
		// UNPARSEABLE IS NOT EVIDENCE. This used to fall through to Ran:
		// true with the raw bytes attached, so an instrumented run whose
		// only output was `sh: 1: …/venv/bin/python: not found` was recorded
		// as selection evidence — then CACHED, keyed on (tree, command,
		// plugin, substrate), none of which changes when the operator fixes
		// their environment, and served on every later scan as "reused".
		// The report's one line that could have said why never printed,
		// because Ran was true. A document the plugin cannot read says
		// nothing about any test; it is a failure to measure, with the
		// reason and the first line of what came back.
		//
		// A run that EXITED NON-ZERO never reached the reducer (python's
		// script exits with the suite's code), so its last line is whatever
		// stopped it: the runner's own summary when the suite failed, a shell
		// error when the interpreter is missing. Handing that line to the JSON
		// reader read pylint's "2 failed, 440 passed" as the number 2
		// followed by an f, and the note named a JSON error instead of the
		// failure (2026-10-21). Say how it exited and how it ended.
		if exitCode > 0 {
			hint := selectionFailureHint(p, stderr+" "+out)
			return SelectionEvidence{Note: withHint(fmt.Sprintf("%s: selection evidence run exited %d, so its output is unparseable as evidence and no test can be selected", p.Name(), exitCode), hint),
				Detail: withHint(fmt.Sprintf("%s: selection evidence run exited %d, so its output is unparseable as evidence and no test can be selected (it ended: %q)", p.Name(), exitCode, lastLine(out)), hint)}
		}
		return SelectionEvidence{Note: fmt.Sprintf("%s: selection evidence unparseable", p.Name()),
			Detail: fmt.Sprintf("%s: selection evidence unparseable: %v (output began: %q)", p.Name(), mErr, firstLine(out))}
	}
	if !hasMeasuredSourceFile(p, m.Coverage()) {
		return SelectionEvidence{Note: pathologicalSelectionDocumentNote(p)}
	}
	return SelectionEvidence{Raw: []byte(out), Ran: true, Map: m}
}

// withHint is note followed by a plugin's fixed hint (selectionFailureHint),
// or note alone when there is none.
func withHint(note, hint string) string {
	if hint == "" {
		return note
	}
	return note + " — " + hint
}

// firstLine is a short excerpt of a document that failed to parse — enough to
// recognise a `command not found` where a JSON report was expected.
// lastLine is firstLine's counterpart for the END of an output, where a test
// runner prints its summary; bounded the same way.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// For answers one file from the evidence's map, through the selection
// engine (testmap.Select). A whole-suite answer is a Selection with Cmd nil
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
	if _, ok := p.(lang.TestEvidence); !ok {
		return lang.Selection{Fallback: fmt.Sprintf("no selector for %s", p.Name())}
	}
	if e.Map.Method == "" {
		// Ran with no map: either Raw does not build one (say why, in the
		// build's own words — the same text every file read when each one
		// parsed Raw itself), or the evidence was assembled field by field
		// rather than by NewSelectionEvidence or CollectSelectionEvidence.
		// Either way there is nothing to select from, so the file grades
		// whole-suite and says so, never silently.
		if _, err := BuildSelectionMap(p, testCmd, e.Raw); err != nil {
			// corral's own refusal of a document its own reducer wrote
			// (the format it names, the row selectionRow found none for),
			// which quotes no command, path or run output.
			return lang.Selection{Fallback: err.Error()}
		}
		return lang.Selection{Fallback: fmt.Sprintf("%s: selection evidence ran but no test map was built from it", p.Name())}
	}
	row, err := selectionRow(p, testCmd)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	s, err := testmap.Select(e.Map, row, testCmd, repoRoot, codePath, testPath)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	return s
}
