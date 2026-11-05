// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/mutantsources"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// --mutants-from: grade an established mutation tool's mutants beside the
// generator's, or instead of them (docs/superpowers/specs/
// 2026-11-01-tool-mutant-sources-design.md §3).
//
// | --mutant-model | --mutants-from | mutants graded           |
// | a model        | absent         | the model's, as before   |
// | a model        | given          | both                     |
// | off            | given          | the tool's only          |
// | off            | absent         | refused: no mutant source |
//
// No default is added anywhere: a run that names a model and no report is
// byte-for-byte the run it was before the flag existed (its cache key
// included), and "off" was already this codebase's word for a seat left
// empty on purpose. What a tool source changes is WHERE the exam comes from,
// never how it is graded: tool mutants go through the same dev pass, the same
// proof and the same compile gate as the model's.

// mutantsFromHelp is the flag's text on both commands. A function, so the
// known tools are the registry's at the moment the flag is defined.
func mutantsFromHelp() string {
	return "grade a mutation TOOL's mutants too, as `<tool>[@<version>]:<report path>` (the version is optional: the report names its own, and a declared one that contradicts it is refused). Known tools: " + strings.Join(mutantsources.Tools(), ", ") + " (stryker reads a StrykerJS JSON report). The tool's Survived and NoCoverage mutants for each audited file are graded by the same dev pass, proof and compile gate as the model's, capped by the file's mutant budget (a seeded sample, see --tool-sample-seed); its Killed, CompileError and other mutants are counted, not re-graded. A file whose report source is not byte-identical to the audited file has its tool mutants refused, and the scan line says so. With --mutant-model it adds to the model's exam; with --mutant-model off it IS the exam, and a file the report gives nothing to grade is not graded. Refused with --mutants, which already pins the exam. Repeatable, once per tool: several tools' mutants join one exam"
}

// toolSampleSeedHelp is --tool-sample-seed's text on both commands.
const toolSampleSeedHelp = "the seed of the over-budget sample of --mutants-from's tool mutants, to replay a recorded run's sample. Absent, a seed is drawn and printed. Refused without --mutants-from"

// mutantsFromFlags is the flags a tool source is resolved from, as typed.
type mutantsFromFlags struct {
	values      []string // every --mutants-from
	seed        string   // --tool-sample-seed
	mutantModel string   // --mutant-model, as typed (an alias is never "off")
	replay      string   // --mutants
}

// toolMutantSource is one run's resolved --mutants-from: the report, what
// identifies it, and whether it is the run's only mutant source.
type toolMutantSource struct {
	tool string // the --mutants-from prefix, e.g. "stryker"
	// reader is the registered mutantsources.Reader for tool.
	reader mutantsources.Reader
	// label is the tool and version the report names, e.g.
	// "stryker@10.0.0": what every record of its mutants says.
	label  string
	path   string // the report path as typed
	report []byte
	sha256 string // of report
	seed   int64
	// toolOnly is --mutant-model off: the tool's mutants are the whole exam,
	// so a file it gives nothing to grade has no exam at all.
	toolOnly bool
	// covers is the report's files, cleaned as the reader matches them.
	covers map[string]bool
}

// mutantSeatOff is whether --mutant-model names the empty seat.
func mutantSeatOff(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "off")
}

// resolveMutantsFrom validates the tool-source flags against each other and
// reads the report. It returns nil, nil when no tool source is named and the
// mutant seat is not off: the run as it always was. Every error is a usage
// error, refused before anything is spent. The seed in use is printed to
// stdout, drawn or given, as --shadow-seed's draw is.
func resolveMutantsFrom(f mutantsFromFlags, stdout io.Writer) (toolSources, error) {
	seedText := strings.TrimSpace(f.seed)
	if len(f.values) == 0 {
		if mutantSeatOff(f.mutantModel) {
			return nil, fmt.Errorf("--mutant-model off leaves the run with no mutant source: name a mutant-generator model, or pass --mutants-from <tool>:<report> to grade a mutation tool's mutants instead (known tools: %s)", strings.Join(mutantsources.Tools(), ", "))
		}
		if seedText != "" {
			return nil, fmt.Errorf("--tool-sample-seed is set but --mutants-from is not: there is no tool sample to seed")
		}
		return nil, nil
	}
	if strings.TrimSpace(f.replay) != "" {
		return nil, fmt.Errorf("--mutants and --mutants-from both set: --mutants replays a recorded exam whole, so a tool's mutants cannot be added to it — pass one or the other")
	}
	var srcs toolSources
	seen := map[string]bool{}
	for _, v := range f.values {
		// <tool>[@<version>]:<report path>. The first ":" ends the tool part,
		// so a path containing "@" or ":" is the path's; the version is
		// optional because the report names its own (spec §4.5).
		head, p, ok := strings.Cut(v, ":")
		tool, declared, at := strings.Cut(strings.TrimSpace(head), "@")
		declared = strings.TrimSpace(declared)
		if !ok || tool == "" || strings.ContainsAny(tool, `/\.`) {
			return nil, fmt.Errorf("--mutants-from %q: want <tool>[@<version>]:<report path>, with a known tool (%s)", v, strings.Join(mutantsources.Tools(), ", "))
		}
		// An "@" with no version after it declares nothing yet reads as if
		// it did, so it is refused rather than taken as "no version".
		if at && declared == "" {
			return nil, fmt.Errorf("--mutants-from %q: the version after %s@ is empty; pass %s@<version>:<report path> to declare the version the report must state, or %s:<report path> to take the report's own", v, tool, tool, tool)
		}
		reader, known := mutantsources.Lookup(tool)
		if !known {
			return nil, fmt.Errorf("--mutants-from %q: unknown tool %q; known tools: %s", v, tool, strings.Join(mutantsources.Tools(), ", "))
		}
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("--mutants-from %q: no report path after %s:", v, tool)
		}
		if seen[tool] {
			return nil, fmt.Errorf("--mutants-from names %s twice: phase 1 reads one report per tool, given once per tool", tool)
		}
		report, err := os.ReadFile(p) // #nosec G304 -- operator-supplied report path, same contract as --mutants
		if err != nil {
			return nil, fmt.Errorf("--mutants-from %s: %v", v, err)
		}
		rpt := mutantsources.Report{Bytes: report}
		paths, err := reader.Paths(rpt)
		if err != nil {
			return nil, fmt.Errorf("--mutants-from %s: %v", v, err)
		}
		label, err := reader.Label(rpt, declared)
		if err != nil {
			return nil, fmt.Errorf("--mutants-from %s: %v", v, err)
		}
		covers := make(map[string]bool, len(paths))
		for _, rp := range paths {
			covers[rp] = true
		}
		seen[tool] = true
		srcs = append(srcs, &toolMutantSource{tool: tool, reader: reader, label: label, path: p, report: report, sha256: reportSHA256(report), covers: covers, toolOnly: mutantSeatOff(f.mutantModel)})
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].tool < srcs[j].tool })
	if seedText != "" {
		seed, err := strconv.ParseInt(seedText, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("--tool-sample-seed %q is not an integer: %v", seedText, err)
		}
		for _, src := range srcs {
			src.seed = seed
		}
		fmt.Fprintf(stdout, "  tool sample seed: %s (given; --tool-sample-seed %s replays the sample)\n", formatToolSeed(seed), formatToolSeed(seed))
		return srcs, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("drawing a tool sample seed: %v", err)
	}
	seed := int64(binary.LittleEndian.Uint64(b[:]) & math.MaxInt64)
	for _, src := range srcs {
		src.seed = seed
	}
	fmt.Fprintf(stdout, "  tool sample seed: %s (drawn; --tool-sample-seed %s replays the sample)\n", formatToolSeed(seed), formatToolSeed(seed))
	return srcs, nil
}

// reportKey is how the record names the report: "sha256:<hex>" of its
// bytes (ToolMutantCounts.Report), never its path.
func (s *toolMutantSource) reportKey() string { return "sha256:" + s.sha256 }

func reportSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// formatToolSeed is the one spelling of a sample seed, in the print and in
// the flag that replays it.
func formatToolSeed(seed int64) string { return strconv.FormatInt(seed, 10) }

// cacheKey is the tool source's part of a verdict's identity: the tool, the
// report's sha256 and the sample seed, so a verdict is never reused across a
// different report, a different sample of the same report, or the presence
// of a tool source at all. Empty for no tool source, so a model-only run
// keys exactly as it did before the flag existed. Separated by "/", which
// CanonicalKV does not escape and none of the three parts contains.
func (s *toolMutantSource) cacheKey() string {
	if s == nil {
		return ""
	}
	return s.tool + "/" + s.sha256 + "/" + formatToolSeed(s.seed)
}

// checkCoversSome refuses a report that covers none of the files a run is
// about to audit: every one of them would get no tool mutants, which is the
// run silently ignoring the flag.
func (s *toolMutantSource) checkCoversSome(paths []string) error {
	for _, p := range paths {
		if s.covers[path.Clean(p)] {
			return nil
		}
	}
	reported := make([]string, 0, len(s.covers))
	for p := range s.covers {
		reported = append(reported, p)
	}
	sort.Strings(reported)
	return fmt.Errorf("--mutants-from %s:%s covers none of the %d file(s) this run audits (%s); it reports on %s", s.tool, s.path, len(paths), strings.Join(paths, ", "), strings.Join(reported, ", "))
}

// toolFileMutants is one file's tool mutants, decided before its run starts.
type toolFileMutants struct {
	src *toolMutantSource
	// fm is what the reader made of the file; zero when not covered.
	fm      mutantsources.FileMutants
	covered bool
	// line is the scan line: what was taken and what was not, or why none.
	line string
	// counts is the same account for the record (ScanRow.ToolMutants): the
	// line is rendered from it when the file was counted.
	counts ledger.ToolMutantCounts
	// none is why the tool gives this file nothing to grade, "" when it
	// does, as the RECORD says it (ScanRow.ToolMutants, Row.Detail): the
	// report is named by its tool and sha256, never by the path the
	// operator typed, which is theirs and may name their home directory.
	none string
	// noneSaid is none as the terminal says it, naming the report by its
	// path so the operator can find it. Never recorded.
	noneSaid string
}

// forFile reads the file's tool mutants under its budget (the per-file
// mutant budget advpool.PlanShards decides, computed before generation).
func (s *toolMutantSource) forFile(p string, code []byte, budget int) (toolFileMutants, error) {
	r := toolFileMutants{src: s}
	if !s.covers[path.Clean(p)] {
		r.none = fmt.Sprintf("the %s report with sha256 %s has no entry for %s", s.label, s.sha256, p)
		r.noneSaid = fmt.Sprintf("the %s report %s has no entry for %s", s.label, s.path, p)
		r.line = fmt.Sprintf("tool mutants (%s): none — %s", s.label, r.noneSaid)
		r.counts = ledger.ToolMutantCounts{Path: path.Clean(p), Tool: s.label, Report: s.reportKey(), Refused: r.none}
		return r, nil
	}
	got, err := s.reader.Read(mutantsources.Report{Bytes: s.report}, map[string][]byte{p: code}, func(string) int { return budget }, s.seed)
	if err != nil {
		return r, recordedError{
			msg:      fmt.Sprintf("--mutants-from %s:%s: %v", s.tool, s.path, err),
			recorded: fmt.Sprintf("--mutants-from: the %s report sha256:%s: %v", s.label, s.sha256, err),
			cause:    err,
		}
	}
	r.covered, r.fm = true, got[path.Clean(p)]
	r.counts = toolMutantCounts(r.fm)
	r.counts.Report = s.reportKey()
	switch {
	case r.fm.Refused != "":
		r.none = r.fm.Refused
		r.noneSaid = r.none
		r.line = fmt.Sprintf("tool mutants (%s): refused — %s", r.fm.Tool, r.noneSaid)
		return r, nil
	case len(r.fm.Taken) == 0:
		r.none = fmt.Sprintf("the %s report leaves nothing to grade in %s: no Survived or NoCoverage mutant corral could apply", s.label, p)
		r.noneSaid = r.none
	}
	r.line = toolMutantsLine(r.counts)
	if r.noneSaid != "" {
		r.line += " — " + r.noneSaid
	}
	return r, nil
}

// toolMutantCounts is a covered file's account of the tool's report, for the
// record and for the scan line rendered from it. A refused file was never
// counted, so it records the tool and the refusal and no count (R-T4); a
// counted file's zeros are measured and kept. The tool's statuses are its
// claims; Taken and the drops are corral's.
func toolMutantCounts(fm mutantsources.FileMutants) ledger.ToolMutantCounts {
	c := ledger.ToolMutantCounts{Path: fm.Path, Tool: fm.Tool, Refused: fm.Refused,
		SchemaVersion: fm.SchemaVersion, Thresholds: fm.Thresholds}
	if fm.Refused != "" {
		return c
	}
	c.Match, c.Units = fm.Match, fm.Units
	n := func(v int) *int { return &v }
	seed := fm.Seed
	c.Seed = &seed
	c.Taken = n(len(fm.Taken))
	c.Survived, c.NoCoverage = n(fm.Counts["Survived"]), n(fm.Counts["NoCoverage"])
	c.Killed, c.CompileError = n(fm.Counts["Killed"]), n(fm.Counts["CompileError"])
	dropped := 0
	for k, v := range fm.Counts {
		switch {
		case strings.HasPrefix(k, "dropped:"):
			if c.DroppedBy == nil {
				c.DroppedBy = map[string]int{}
			}
			c.DroppedBy[strings.TrimPrefix(k, "dropped:")] = v
			dropped += v
		case k == "Survived", k == "NoCoverage", k == "Killed", k == "CompileError":
		default:
			if c.NotRegraded == nil {
				c.NotRegraded = map[string]int{}
			}
			c.NotRegraded[k] = v
		}
	}
	c.Dropped = n(dropped)
	c.Mutants, c.MutantsTruncated = boundReportMutants(fm.Reported)
	c.Tests = referencedTests(c.Mutants, fm.Tests)
	return c
}

// referencedTests is the names of the tests the recorded mutants refer to:
// the reader's names less any whose every reference the bound removed, so
// the record names no test it does not use.
func referencedTests(ms []ledger.ReportMutant, names map[string]string) map[string]string {
	var out map[string]string
	add := func(ids []string) {
		for _, id := range ids {
			if name, ok := names[id]; ok {
				if out == nil {
					out = map[string]string{}
				}
				out[id] = name
			}
		}
	}
	for _, m := range ms {
		add(m.KilledBy)
		add(m.CoveredBy)
	}
	return out
}

// maxReportMutantsBytes bounds one file's encoded ToolMutantCounts.Mutants.
// It is measured, not chosen: on 2026-11-05 the account of REA's
// src/domain/analysisErrorProjection.ts from its full StrykerJS 10.0.0 report
// (662 mutants naming 118 tests, report sha256 8496b0c1...,
// testdata/PROVENANCE.md), with mutants referring to tests by id and the
// names held once in Tests, encoded to 292,956 bytes raw and 25,006 gzipped
// (281,492 for Mutants alone; 165,446 with covered_by lists as counts); the
// bound is the next power of two above twice the raw size, so a file twice
// REA's records whole. (Under the earlier shape, names repeated on every
// mutant, the same file was 1,801,228 raw and the bound 1<<22.)
// mutantsources.TestFullReportRecordSize repeats the measurement. The bound
// is measured on json.Marshal of the list alone, so it is approximate
// relative to the signed entry's bytes, which add the rest of the row, the
// Tests names and the entry's own encoding.
const maxReportMutantsBytes = 1 << 20

// boundReportMutants is the file's mutants as the record holds them, within
// maxReportMutantsBytes, and a sentence saying what the bound did ("" when
// the whole list fit). Nothing is dropped silently: first every covered_by
// list gives way to its count (CoveredByCount is already set wherever the
// tool stated a list), and if that is still over, mutants are cut, the ones
// corral took to grade or dropped kept before those it never took, each
// group in the report's order. The reader's slice is not modified.
func boundReportMutants(rs []ledger.ReportMutant) ([]ledger.ReportMutant, string) {
	if len(rs) == 0 {
		return nil, ""
	}
	sizes := make([]int, len(rs))
	total := 1 // "[" ... "]", less the comma the first element does not have
	for i, r := range rs {
		sizes[i] = reportMutantLen(r) + 1
		total += sizes[i]
	}
	if total <= maxReportMutantsBytes {
		return append([]ledger.ReportMutant(nil), rs...), ""
	}
	counted := make([]ledger.ReportMutant, len(rs))
	for i, r := range rs {
		r.CoveredBy = nil
		counted[i] = r
		sizes[i] = reportMutantLen(r) + 1
	}
	// Taken-status mutants first, then the rest, each in report order.
	order := make([]int, 0, len(rs))
	for pass := 0; pass < 2; pass++ {
		for i, r := range counted {
			if strings.HasPrefix(r.Fate, "not-taken:") == (pass == 1) {
				order = append(order, i)
			}
		}
	}
	keep := make([]bool, len(rs))
	kept, size := 0, 1
	for _, i := range order {
		if size+sizes[i] > maxReportMutantsBytes {
			break
		}
		keep[i], size, kept = true, size+sizes[i], kept+1
	}
	out := make([]ledger.ReportMutant, 0, kept)
	for i, r := range counted {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out, fmt.Sprintf("covered_by lists replaced by counts; %d of %d mutants recorded", kept, len(rs))
}

// reportMutantLen is r's encoded length in a JSON array.
func reportMutantLen(r ledger.ReportMutant) int {
	b, _ := json.Marshal(r) // strings, ints, bools and arrays of them: always marshals
	return len(b)
}

// toolMutantsLine is the scan line for a counted file, rendered from the
// counts the record carries so the screen and the record cannot disagree:
// "tool mutants (stryker@10.0.0): taken T of S survivors + N no-coverage, K
// killed by the tool and not re-graded, C compile errors, D dropped:
// <reasons>", with any other status the tool reported counted after it
// rather than left out. Only a counted file has a line from its counts (a
// refused or uncovered one is a sentence, see forFile), and every count of
// one is set.
func toolMutantsLine(c ledger.ToolMutantCounts) string {
	s := fmt.Sprintf("tool mutants (%s): taken %d of %d survivors + %d no-coverage, %d killed by the tool and not re-graded, %d compile errors, %d dropped",
		c.Tool, *c.Taken, *c.Survived, *c.NoCoverage, *c.Killed, *c.CompileError, *c.Dropped)
	if len(c.DroppedBy) > 0 {
		s += ": " + countsByName(c.DroppedBy)
	}
	if len(c.NotRegraded) > 0 {
		s += "; not re-graded: " + countsByName(c.NotRegraded)
	}
	if c.Match != "" {
		s += " — match: " + c.Match
	}
	return s
}

// countsByName is "name n, name n", sorted by name.
func countsByName(m map[string]int) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

// toolCountsCollector gathers the files' accounts as a repo scan's
// concurrent audits report them. The zero value is ready to use.
type toolCountsCollector struct {
	mu     sync.Mutex
	counts []ledger.ToolMutantCounts
}

func (c *toolCountsCollector) add(tc ledger.ToolMutantCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = append(c.counts, tc)
}

func (c *toolCountsCollector) all() []ledger.ToolMutantCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ledger.ToolMutantCounts(nil), c.counts...)
}

// toolMutantsJSON is ScanRow.ToolMutants: every file's account, sorted by
// path, as JSON text. "" when the run had no tool source.
func toolMutantsJSON(counts []ledger.ToolMutantCounts) ledger.JSONText[[]ledger.ToolMutantCounts] {
	if len(counts) == 0 {
		return ""
	}
	sorted := append([]ledger.ToolMutantCounts(nil), counts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	text, _ := ledger.EncodeJSONText(sorted) // strings, ints and string-keyed maps: always marshals
	return text
}

// preCheck is forFile's verdict on whether the tool gives this file
// anything to grade, asked before any baseline runs so a tool-only scan
// spends nothing on a file it cannot grade. It reads the report UNCAPPED,
// because emptiness does not depend on the budget (any budget of at least
// one takes something from a non-empty candidate set); the sample a real
// run takes is drawn by forFile under the real budget.
//
// The result's none is "" when there is something to grade. When it is set,
// the result's counts are the file's whole account for the record: nothing
// was taken, so no over-budget drop exists and the uncapped read counts
// exactly what a capped one would. A report corral could not READ for the
// file comes back as err, never as a reason: a reason is recorded as
// no-mutant-source, which says the tool had nothing to offer, and that is
// not what an unreadable report measured.
func (s *toolMutantSource) preCheck(p string, code []byte) (toolFileMutants, error) {
	return s.forFile(p, code, math.MaxInt32)
}

// toolSources is a run's tool sources, sorted by tool: every mutation tool
// named by a --mutants-from, whose mutants join one exam so they can be
// compared on it. nil is a run with no tool source.
type toolSources []*toolMutantSource

// toolOnly is whether the tools' mutants are the whole exam
// (--mutant-model off). It is one flag for all sources, set from the one
// --mutant-model.
func (ts toolSources) toolOnly() bool { return len(ts) > 0 && ts[0].toolOnly }

// names is the tools, sorted, joined by " + ": "alpha + zeta".
func (ts toolSources) names() string {
	n := make([]string, len(ts))
	for i, s := range ts {
		n[i] = s.tool
	}
	return strings.Join(n, " + ")
}

// labels is the tools with their versions as their reports name them,
// sorted by tool, joined by "+".
func (ts toolSources) labels() string {
	n := make([]string, len(ts))
	for i, s := range ts {
		n[i] = s.label
	}
	return strings.Join(n, "+")
}

// cacheKey is the sources' part of a verdict's identity: "" for none (a
// model-only run keys as it always did), exactly the source's own key for
// one (so cached single-tool verdicts keep being served), and for several
// the sorted keys joined by "+", which no key contains, so the order the
// flags were typed in does not change what a verdict is.
func (ts toolSources) cacheKey() string {
	keys := make([]string, len(ts))
	for i, s := range ts {
		keys[i] = s.cacheKey()
	}
	sort.Strings(keys)
	return strings.Join(keys, "+")
}

// checkCoversSome refuses any source whose report covers none of the files
// the run audits: that tool would be silently ignored.
func (ts toolSources) checkCoversSome(paths []string) error {
	for _, s := range ts {
		if err := s.checkCoversSome(paths); err != nil {
			return err
		}
	}
	return nil
}

// forFile is each source's account of the file, in tool order, under the
// same per-file budget each. Beside a generator the exam is the model's
// share plus every tool's, said once: "with the model's B under the same
// per-file cap: B model + T1 alpha@1 + T2 zeta@1, an exam of up to B+T1+T2"
// (a lone tool is "B model + T1 tool"). The labels match advpool.WithTools,
// which says the same on the report surfaces.
func (ts toolSources) forFile(p string, code []byte, budget int) ([]toolFileMutants, error) {
	out := make([]toolFileMutants, 0, len(ts))
	for _, s := range ts {
		r, err := s.forFile(p, code, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if !ts.toolOnly() {
		// The shares are all known only now, so the sentence goes on the last
		// line that counted something, never on a refusal's line.
		last, total, named := -1, budget, 0
		for i, r := range out {
			if t := len(r.fm.Taken); t > 0 {
				last, total, named = i, total+t, named+1
			}
		}
		if last >= 0 {
			parts := fmt.Sprintf("%d model", budget)
			for _, r := range out {
				t := len(r.fm.Taken)
				switch {
				case t == 0:
				case named == 1:
					parts += fmt.Sprintf(" + %d tool", t)
				default:
					parts += fmt.Sprintf(" + %d %s", t, r.src.label)
				}
			}
			out[last].line += fmt.Sprintf(" — with the model's %d under the same per-file cap: %s, an exam of up to %d", budget, parts, total)
		}
	}
	return out, nil
}

// preCheck is each source's pre-check of the file (toolMutantSource.preCheck).
func (ts toolSources) preCheck(p string, code []byte) ([]toolFileMutants, error) {
	out := make([]toolFileMutants, 0, len(ts))
	for _, s := range ts {
		r, err := s.preCheck(p, code)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// nothingToGrade is why NO source gives the file anything to grade, as the
// record says it and as the terminal does; both "" when any source does.
// One reason per source, joined by "; " (a lone source's reason is its own).
func nothingToGrade(files []toolFileMutants) (none, noneSaid string) {
	var rec, said []string
	for _, f := range files {
		if f.none == "" {
			return "", ""
		}
		rec, said = append(rec, f.none), append(said, f.noneSaid)
	}
	return strings.Join(rec, "; "), strings.Join(said, "; ")
}

// applyAll puts the files' tool mutants on the file's RunSpec through the
// door TM-R1 names: PresetMutants when the tools are the only source (the
// replay door, whose bytes-must-match and no-generator behaviour already
// exist), and ToolMutants beside a generator, every source's Taken
// concatenated in tool order and one MutantSources map merged from them. A
// tool-only file is refused as no-mutant-source only when EVERY source gives
// it nothing, never handed an empty exam to grade as a measured 1.0 or 0.0;
// with a generator seat such a file just runs the model's exam. A mutant id
// taken twice is an error naming both tools: ids carry their tool's
// "t/<tool>/" prefix, so it cannot happen, and this pins that.
func applyAll(rs *advpool.RunSpec, files []toolFileMutants) error {
	if len(files) == 0 {
		return nil
	}
	toolOnly := files[0].src.toolOnly
	if none, said := nothingToGrade(files); none != "" {
		if toolOnly {
			const why = "COULD-NOT-GRADE: --mutant-model off makes the tool the only mutant source, and "
			return localAuditError{usage: true, scanReason: reposcan.ReasonNoMutantSource,
				msg: why + said, recorded: why + none}
		}
		return nil
	}
	var taken []adequacy.Mutant
	var sources map[string]mutantsources.MutantSource
	owner := map[string]string{}
	for _, f := range files {
		if f.none != "" {
			continue
		}
		for _, m := range f.fm.Taken {
			if other, dup := owner[m.ID]; dup {
				msg := fmt.Sprintf("mutant id %q is taken from both %s and %s", m.ID, other, f.src.tool)
				return localAuditError{usage: true, scanReason: reposcan.ReasonExecutorError, msg: msg, recorded: msg}
			}
			owner[m.ID] = f.src.tool
		}
		taken = append(taken, f.fm.Taken...)
		for id, src := range f.fm.Sources {
			if sources == nil {
				sources = map[string]mutantsources.MutantSource{}
			}
			sources[id] = src
		}
	}
	if toolOnly {
		rs.PresetMutants = taken
	} else {
		rs.ToolMutants = taken
	}
	rs.MutantSources = sources
	return nil
}

// recordedError is an error whose terminal text (msg) names something the
// record must not hold, a host path the operator typed or the absolute
// checkout, with the text the record holds instead (reposcan.RecordedDetailer:
// FileResult.Detail, and from it the signed Row.Detail). The record names
// the report by tool and sha256 and a file by its repo-relative path.
type recordedError struct {
	msg, recorded string
	cause         error
}

func (e recordedError) Error() string          { return e.msg }
func (e recordedError) RecordedDetail() string { return e.recorded }
func (e recordedError) Unwrap() error          { return e.cause }

// pathlessErr is err's text without the path an *os.PathError (or
// *os.LinkError) names: its operation and the underlying error ("open: no
// such file or directory"). Any other error is its own text.
func pathlessErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return err.Error()
}
