// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
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

// knownMutantTools are the tools --mutants-from reads, by prefix.
var knownMutantTools = []string{"stryker"}

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
	tool   string // the --mutants-from prefix, e.g. "stryker"
	path   string // the report path as typed
	report []byte
	sha256 string // of report
	seed   int64
	// toolOnly is --mutant-model off: the tool's mutants are the whole exam,
	// so a file it gives nothing to grade has no exam at all.
	toolOnly bool
	// covers is the report's files, cleaned as ReadStryker matches them.
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
			return nil, fmt.Errorf("--mutant-model off leaves the run with no mutant source: name a mutant-generator model, or pass --mutants-from <tool>:<report> to grade a mutation tool's mutants instead (known tools: %s)", strings.Join(knownMutantTools, ", "))
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
		tool, p, ok := strings.Cut(v, ":")
		tool = strings.TrimSpace(tool)
		if !ok || tool == "" || strings.ContainsAny(tool, `/\.`) {
			return nil, fmt.Errorf("--mutants-from %q: want <tool>:<report path>, with a known tool (%s)", v, strings.Join(knownMutantTools, ", "))
		}
		if !knownTool(tool) {
			return nil, fmt.Errorf("--mutants-from %q: unknown tool %q; known tools: %s", v, tool, strings.Join(knownMutantTools, ", "))
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
		paths, err := mutantsources.StrykerPaths(report)
		if err != nil {
			return nil, fmt.Errorf("--mutants-from %s: %v", v, err)
		}
		covers := make(map[string]bool, len(paths))
		for _, rp := range paths {
			covers[rp] = true
		}
		src = &toolMutantSource{tool: tool, path: p, report: report, sha256: reportSHA256(report), covers: covers, toolOnly: mutantSeatOff(f.mutantModel)}
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

func reportSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func knownTool(tool string) bool {
	for _, k := range knownMutantTools {
		if tool == k {
			return true
		}
	}
	return false
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
	// none is why the tool gives this file nothing to grade, "" when it
	// does.
	none string
}

// forFile reads the file's tool mutants under its budget (the per-file
// mutant budget advpool.PlanShards decides, computed before generation).
func (s *toolMutantSource) forFile(p string, code []byte, budget int) (toolFileMutants, error) {
	r := toolFileMutants{src: s}
	if !s.covers[path.Clean(p)] {
		r.none = fmt.Sprintf("the %s report %s has no entry for %s", s.tool, s.path, p)
		r.line = fmt.Sprintf("tool mutants (%s): none — %s", s.tool, r.none)
		return r, nil
	}
	got, err := mutantsources.ReadStryker(s.report, map[string][]byte{p: code}, func(string) int { return budget }, s.seed)
	if err != nil {
		return r, fmt.Errorf("--mutants-from %s:%s: %v", s.tool, s.path, err)
	}
	r.covered, r.fm = true, got[path.Clean(p)]
	switch {
	case r.fm.Refused != "":
		r.none = r.fm.Refused
		r.line = fmt.Sprintf("tool mutants (%s): refused — %s", s.tool, r.none)
		return r, nil
	case len(r.fm.Taken) == 0:
		r.none = fmt.Sprintf("the %s report leaves nothing to grade in %s: no Survived or NoCoverage mutant corral could apply", s.tool, p)
	}
	r.line = toolMutantsLine(r.fm, s.tool)
	if r.none != "" {
		r.line += " — " + r.none
	}
	return r, nil
}

// toolMutantsLine is the scan line for a covered file: "tool mutants
// (stryker@10.0.0): taken T of S survivors (+N no-coverage), K killed by the
// tool and not re-graded, C compile errors, D dropped: <reasons>", with any
// other status the tool reported counted after it rather than left out.
// tool names the tool when no taken mutant's source carries its version.
func toolMutantsLine(fm mutantsources.FileMutants, tool string) string {
	for _, src := range fm.Sources {
		if src.Tool != "" {
			tool = src.Tool
			break
		}
	}
	c := fm.Counts
	var dropped []string
	droppedN := 0
	var other []string
	for k, n := range c {
		switch {
		case strings.HasPrefix(k, "dropped:"):
			dropped = append(dropped, fmt.Sprintf("%s %d", strings.TrimPrefix(k, "dropped:"), n))
			droppedN += n
		case k == "Survived", k == "NoCoverage", k == "Killed", k == "CompileError":
		default:
			other = append(other, fmt.Sprintf("%s %d", k, n))
		}
	}
	sort.Strings(dropped)
	sort.Strings(other)
	s := fmt.Sprintf("tool mutants (%s): taken %d of %d survivors (+%d no-coverage), %d killed by the tool and not re-graded, %d compile errors, %d dropped",
		tool, len(fm.Taken), c["Survived"], c["NoCoverage"], c["Killed"], c["CompileError"], droppedN)
	if len(dropped) > 0 {
		s += ": " + strings.Join(dropped, ", ")
	}
	if len(other) > 0 {
		s += "; not re-graded: " + strings.Join(other, ", ")
	}
	return s
}

// noSourceReason is forFile's verdict on whether the tool gives this file
// anything to grade, asked before any baseline runs so a tool-only scan
// spends nothing on a file it cannot grade. It reads the report UNCAPPED,
// because emptiness does not depend on the budget (any budget of at least
// one takes something from a non-empty candidate set); the sample a real
// run takes is drawn by forFile under the real budget.
func (s *toolMutantSource) noSourceReason(p string, code []byte) string {
	r, err := s.forFile(p, code, math.MaxInt32)
	if err != nil {
		return err.Error()
	}
	return r.none
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
			return localAuditError{usage: true, scanReason: reposcan.ReasonNoMutantSource,
				msg: "COULD-NOT-GRADE: --mutant-model off makes the tool the only mutant source, and " + r.none}
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
