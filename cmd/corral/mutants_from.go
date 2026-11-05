// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// mutantsFromHelp is the flag's text on both commands.
const mutantsFromHelp = "grade a mutation TOOL's mutants too, as <tool>:<report path> (phase 1 knows `stryker`: a StrykerJS JSON report). The tool's Survived and NoCoverage mutants for each audited file are graded by the same dev pass, proof and compile gate as the model's, capped by the file's mutant budget (a seeded sample, see --tool-sample-seed); its Killed, CompileError and other mutants are counted, not re-graded. A file whose report source is not byte-identical to the audited file has its tool mutants refused, and the scan line says so. With --mutant-model it adds to the model's exam; with --mutant-model off it IS the exam, and a file the report gives nothing to grade is not graded. Refused with --mutants, which already pins the exam. Repeatable, once per tool"

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
func resolveMutantsFrom(f mutantsFromFlags, stdout io.Writer) (*toolMutantSource, error) {
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
	var src *toolMutantSource
	for _, v := range f.values {
		// <tool>[@<version>]:<report path>. The first ":" ends the tool part,
		// so a path containing "@" or ":" is the path's; the version is
		// optional because the report names its own (spec §4.5).
		head, p, ok := strings.Cut(v, ":")
		tool, declared, _ := strings.Cut(strings.TrimSpace(head), "@")
		if !ok || tool == "" || strings.ContainsAny(tool, `/\.`) {
			return nil, fmt.Errorf("--mutants-from %q: want <tool>[@<version>]:<report path>, with a known tool (%s)", v, strings.Join(mutantsources.Tools(), ", "))
		}
		reader, known := mutantsources.Lookup(tool)
		if !known {
			return nil, fmt.Errorf("--mutants-from %q: unknown tool %q; known tools: %s", v, tool, strings.Join(mutantsources.Tools(), ", "))
		}
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("--mutants-from %q: no report path after %s:", v, tool)
		}
		if src != nil {
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
		src = &toolMutantSource{tool: tool, reader: reader, label: label, path: p, report: report, sha256: reportSHA256(report), covers: covers, toolOnly: mutantSeatOff(f.mutantModel)}
	}
	if seedText != "" {
		seed, err := strconv.ParseInt(seedText, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("--tool-sample-seed %q is not an integer: %v", seedText, err)
		}
		src.seed = seed
		fmt.Fprintf(stdout, "  tool sample seed: %s (given; --tool-sample-seed %s replays the sample)\n", formatToolSeed(seed), formatToolSeed(seed))
		return src, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("drawing a tool sample seed: %v", err)
	}
	src.seed = int64(binary.LittleEndian.Uint64(b[:]) & math.MaxInt64)
	fmt.Fprintf(stdout, "  tool sample seed: %s (drawn; --tool-sample-seed %s replays the sample)\n", formatToolSeed(src.seed), formatToolSeed(src.seed))
	return src, nil
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
	// Beside a generator the tool's sample uses the model's own per-file
	// cap, so the exam is the two together: said here, where the tool's
	// share is first known (advpool.MutantBudget.WithTool says it again).
	if t := len(r.fm.Taken); t > 0 && !s.toolOnly {
		r.line += fmt.Sprintf(" — with the model's %d under the same per-file cap: %d model + %d tool, an exam of up to %d", budget, budget, t, budget+t)
	}
	return r, nil
}

// toolMutantCounts is a covered file's account of the tool's report, for the
// record and for the scan line rendered from it. A refused file was never
// counted, so it records the tool and the refusal and no count (R-T4); a
// counted file's zeros are measured and kept. The tool's statuses are its
// claims; Taken and the drops are corral's.
func toolMutantCounts(fm mutantsources.FileMutants) ledger.ToolMutantCounts {
	c := ledger.ToolMutantCounts{Path: fm.Path, Tool: fm.Tool, Refused: fm.Refused}
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
	return c
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

// apply puts the file's tool mutants on its RunSpec through the door TM-R1
// names: PresetMutants when the tool is the only source (the replay door,
// whose bytes-must-match and no-generator behaviour already exist), and
// ToolMutants beside a generator. One MutantSources map either way. A
// tool-only file with nothing to grade is refused as no-mutant-source,
// never handed an empty exam to grade as a measured 1.0 or 0.0. With a
// generator seat such a file just runs the model's exam.
func (r toolFileMutants) apply(rs *advpool.RunSpec) error {
	if r.none != "" {
		if r.src.toolOnly {
			const why = "COULD-NOT-GRADE: --mutant-model off makes the tool the only mutant source, and "
			return localAuditError{usage: true, scanReason: reposcan.ReasonNoMutantSource,
				msg: why + r.noneSaid, recorded: why + r.none}
		}
		return nil
	}
	if r.src.toolOnly {
		rs.PresetMutants = r.fm.Taken
	} else {
		rs.ToolMutants = r.fm.Taken
	}
	rs.MutantSources = r.fm.Sources
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
