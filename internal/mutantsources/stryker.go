// SPDX-License-Identifier: Elastic-2.0

// Package mutantsources reads mutants corral did not generate — an
// established mutation tool's report — into the same adequacy.Mutant the
// model-planted path produces, so both are graded by one exam.
//
// THE TOOL'S STATUS NEVER DECIDES AN OUTCOME. A report's "Survived" is a
// claim, recorded as declared (MutantSource.Status); what corral records as
// measured is what its own jail does with the mutant. The status is used for
// exactly one thing: choosing which mutants are worth a jail run at all
// (Survived and NoCoverage), and the rest are counted, not re-graded.
//
// It sits below advpool (which will consume it) and imports only adequacy and
// testgen, so the arrow points one way.
package mutantsources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/testgen"
)

// Mutant source kinds.
const (
	KindModel = "model"
	KindTool  = "tool"
)

// MutantSource says where one mutant came from. Kind is corral's own
// knowledge (it loaded the mutant); Tool, Operator and Status are copied from
// the report and are the tool's claims.
type MutantSource struct {
	Kind string // KindModel or KindTool
	// Tool is the producing tool and its version as the report states them,
	// e.g. "stryker@10.0.0".
	Tool string
	// Operator is the tool's name for the mutation (Stryker's mutatorName).
	Operator string
	// Status is what the tool said happened to the mutant, e.g. "Survived".
	Status string
}

// FileMutants is one audited file's tool mutants: those taken for grading,
// and an account of everything else in the report for that file.
type FileMutants struct {
	Path string
	// Taken are the mutants to grade, each proven a single-region edit of the
	// audited bytes, at most the file's budget.
	Taken []adequacy.Mutant
	// Sources is keyed by Taken's mutant IDs.
	Sources map[string]MutantSource
	// Counts holds the report's mutants by the tool's status, plus
	// "dropped:<reason>" for every taken-status mutant that was not taken.
	Counts map[string]int
	// Seed is the seed the over-budget sample was drawn with, recorded so the
	// draw can be replayed.
	Seed int64
	// Refused is one sentence saying why none of this file's tool mutants
	// can be used. Taken is empty whenever it is set.
	Refused string
}

// Statuses whose mutants are taken. Survived: tests ran the mutant and none
// failed. NoCoverage: no test even runs the line, which is often the cheapest
// real gap. Every other status (Killed, CompileError, Timeout, RuntimeError,
// Ignored, ...) is counted and left: re-grading a kill would audit the tool's
// honesty at a jail run per mutant, which is a separate question.
var takenStatuses = map[string]bool{"Survived": true, "NoCoverage": true}

// Reasons a taken-status mutant is not taken. They are fixed phrases because
// they are recorded.
const (
	dropOutOfRange = "dropped:location-out-of-range"
	dropOverBudget = "dropped:over-budget"
	// dropDuplicateID: a second mutant under an id already taken. The id is
	// the mutant's identity on the record, so two under one would be
	// indistinguishable there; the first is kept.
	dropDuplicateID = "dropped:duplicate-id"
)

// strykerReport is the part of the mutation-testing-report-schema JSON this
// reader uses. The shape was read from a real StrykerJS 10.0.0 report
// (testdata/PROVENANCE.md), not assumed.
type strykerReport struct {
	Framework *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"framework"`
	Files map[string]struct {
		Source  string `json:"source"`
		Mutants []struct {
			ID          string `json:"id"`
			MutatorName string `json:"mutatorName"`
			Replacement string `json:"replacement"`
			Status      string `json:"status"`
			Location    struct {
				Start strykerPos `json:"start"`
				End   strykerPos `json:"end"`
			} `json:"location"`
		} `json:"mutants"`
	} `json:"files"`
}

type strykerPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// strykerJS is the one framework name whose location semantics this reader
// has verified. Others (Stryker.NET, Stryker4s) write the same schema, but
// their columns come from other parsers and nobody here has checked them.
const strykerJS = "StrykerJS"

// ReadStryker reads a StrykerJS JSON report into mutants for the audited
// files. files maps repo-relative path to the exact bytes corral audits;
// budget gives each file's mutant cap (the run's per-file budget — there is
// no default); seed drives the over-budget sample.
//
// An audited file the report does not mention gets no entry. A report that
// mentions none of them is an error naming the report's paths, because that
// is a run that would silently grade nothing from the tool. A file whose
// report source is not byte-identical to its audited bytes is Refused, never
// re-applied: a mutant is an edit of specific bytes.
//
// LOCATIONS. A location is 1-based lines and 1-based columns, end-exclusive,
// in UTF-16 code units. That is not assumed: StrykerJS takes Babel's node
// locations (0-based columns indexing a JavaScript string, so UTF-16 units),
// and core adds one to each column (@stryker-mutator/instrumenter
// dist/src/mutant.js toPosition, core dist/src/utils/object-utils.js), and
// stryker_test.go checks every taken mutant against a whole-file splice at
// those columns on REA's real report.
func ReadStryker(reportJSON []byte, files map[string][]byte, budget func(path string) int, seed int64) (map[string]FileMutants, error) {
	if budget == nil {
		return nil, errors.New("mutantsources: no mutant budget given; pass the run's per-file budget")
	}
	var rep strykerReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return nil, fmt.Errorf("mutantsources: the Stryker report is not readable JSON: %w", err)
	}
	if rep.Framework == nil || rep.Framework.Name != strykerJS {
		name := "none"
		if rep.Framework != nil {
			name = strconv.Quote(rep.Framework.Name)
		}
		return nil, fmt.Errorf("mutantsources: the report's framework is %s; only %s reports are read, because only their column semantics have been verified", name, strykerJS)
	}
	if rep.Framework.Version == "" {
		return nil, errors.New("mutantsources: the report's framework names no version, and every tool mutant records the version that produced it")
	}
	tool := "stryker@" + rep.Framework.Version

	audited := make(map[string][]byte, len(files))
	for p, b := range files {
		audited[path.Clean(p)] = b
	}

	out := map[string]FileMutants{}
	for rp, rf := range rep.Files {
		p := path.Clean(rp)
		bytes, ok := audited[p]
		if !ok {
			continue
		}
		n := budget(p)
		if n < 1 {
			return nil, fmt.Errorf("mutantsources: the mutant budget for %s is %d; it must be at least 1", p, n)
		}
		fm := FileMutants{Path: p, Seed: seed}
		if refusal := refuseSource(p, rf.Source, bytes); refusal != "" {
			fm.Refused = refusal
			out[p] = fm
			continue
		}
		original := rf.Source
		lines := strings.Split(original, "\n")
		fm.Counts = map[string]int{}
		var candidates []adequacy.Mutant
		sources := map[string]MutantSource{}
		// seen holds every taken-status id, proven or not, so the first
		// mutant under an id is the one kept or dropped and a later one
		// cannot stand in for a first that failed its proof.
		seen := map[string]bool{}
		for _, sm := range rf.Mutants {
			fm.Counts[sm.Status]++
			if !takenStatuses[sm.Status] {
				continue
			}
			id := "t/stryker/" + sm.ID
			if seen[id] {
				fm.Counts[dropDuplicateID]++
				continue
			}
			seen[id] = true
			search, replace, ok := strykerHunk(lines, sm.Location.Start, sm.Location.End, sm.Replacement)
			if !ok {
				fm.Counts[dropOutOfRange]++
				continue
			}
			m, why := testgen.ProveHunk(original, id, search, replace)
			if why != testgen.HunkProven {
				fm.Counts["dropped:"+string(why)]++
				continue
			}
			candidates = append(candidates, m)
			sources[id] = MutantSource{Kind: KindTool, Tool: tool, Operator: sm.MutatorName, Status: sm.Status}
		}
		fm.Taken = spreadSample(candidates, n, seed)
		if d := len(candidates) - len(fm.Taken); d > 0 {
			fm.Counts[dropOverBudget] = d
		}
		fm.Sources = make(map[string]MutantSource, len(fm.Taken))
		for _, m := range fm.Taken {
			fm.Sources[m.ID] = sources[m.ID]
		}
		out[p] = fm
	}
	if len(out) == 0 {
		paths := make([]string, 0, len(rep.Files))
		for p := range rep.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return nil, fmt.Errorf("mutantsources: the Stryker report covers none of the audited files; it reports on %s", strings.Join(paths, ", "))
	}
	return out, nil
}

// refuseSource returns why a file's tool mutants cannot be used, or "".
func refuseSource(p, reported string, audited []byte) string {
	rs, as := sha256Hex([]byte(reported)), sha256Hex(audited)
	if rs != as {
		return fmt.Sprintf("the Stryker report's source for %s has sha256 %s but the audited file has sha256 %s, so its tool mutants are refused rather than applied to bytes they were not made from", p, rs[:12], as[:12])
	}
	// Babel ends a line at a lone \r, U+2028 and U+2029 as well as \n, so in
	// a file holding one, Stryker's line numbers and corral's disagree.
	// Refused rather than remapped: no report with one has been read.
	if strings.ContainsAny(strings.ReplaceAll(reported, "\r\n", "\n"), "\r\u2028\u2029") {
		return fmt.Sprintf("%s contains a line break other than \\n or \\r\\n, where Stryker's line numbers and corral's differ, so its tool mutants are refused", p)
	}
	return ""
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// strykerHunk builds the SEARCH/REPLACE hunk for one Stryker mutant: SEARCH
// is the whole original lines the location spans (with the newline that ends
// the last of them, when there is one), and REPLACE is those lines with the
// located columns replaced. ok is false when the location does not fit the
// file.
//
// THE ANCHOR IS LINE-BOUNDED. Below line 1, SEARCH and REPLACE both begin
// with the newline that ends the line before. Without it,
// `    x: 1,` also matches the tail of `        x: 1,` elsewhere, and a line
// that occurs once would be refused as ambiguous — 2 of the 31 such refusals
// on REA's full report were this, not a repeated line. The borrowed newline
// is unchanged on both sides, so the edit is still the located columns, and
// adequacy.EditSpan (which testgen.ProveHunk records) starts the span after it.
func strykerHunk(lines []string, start, end strykerPos, replacement string) (search, replace string, ok bool) {
	if start.Line < 1 || end.Line < start.Line || end.Line > len(lines) {
		return "", "", false
	}
	first, last := lines[start.Line-1], lines[end.Line-1]
	from, ok1 := utf16ColumnToByte(first, start.Column)
	to, ok2 := utf16ColumnToByte(last, end.Column)
	if !ok1 || !ok2 || (start.Line == end.Line && from > to) {
		return "", "", false
	}
	nl := ""
	if end.Line < len(lines) {
		nl = "\n"
	}
	pre := ""
	if start.Line > 1 {
		pre = "\n"
	}
	search = pre + strings.Join(lines[start.Line-1:end.Line], "\n") + nl
	replace = pre + first[:from] + replacement + last[to:] + nl
	return search, replace, true
}

// utf16ColumnToByte converts a 1-based column counted in UTF-16 code units
// to a byte offset into line. Column len+1 (just past the end, where an
// end-exclusive location may stop) is valid. ok is false for a column outside
// the line or one that falls between the two halves of a surrogate pair.
func utf16ColumnToByte(line string, column int) (int, bool) {
	want := column - 1
	if want < 0 {
		return 0, false
	}
	units := 0
	for i, r := range line {
		if units == want {
			return i, true
		}
		if units > want {
			return 0, false
		}
		// An invalid UTF-8 byte ranges as U+FFFD, one unit, as a
		// JavaScript decoder would make it.
		units += utf16.RuneLen(r)
	}
	if units == want {
		return len(line), true
	}
	return 0, false
}

// spreadSample takes at most n of candidates, deterministically for a seed.
// When there are more than n it spreads across distinct lines first: the
// lines (by each mutant's first line) are put in a seeded order, and rounds
// take one not-yet-taken mutant per line, in that order, until n are taken.
// A crowded line — Stryker plants a dozen mutants in one boolean expression —
// cannot use up the budget while other lines go untested.
//
// The result is ordered by span and then by the tool's own id, so the same
// seed yields the same slice, not merely the same set.
func spreadSample(candidates []adequacy.Mutant, n int, seed int64) []adequacy.Mutant {
	taken := candidates
	if len(candidates) > n {
		// The int64 seed is reinterpreted as uint64 bits, which is lossless
		// and the same draw every time for the same recorded seed.
		u := uint64(seed)                                     // #nosec G115 -- a bit reinterpretation of the seed, not arithmetic
		rng := rand.New(rand.NewPCG(u, u^0x746f6f6c6d757473)) // #nosec G404 -- not security: a reproducible draw from a recorded seed
		byLine := map[int][]adequacy.Mutant{}
		var order []int
		for _, m := range candidates {
			if _, seen := byLine[m.Span.Start]; !seen {
				order = append(order, m.Span.Start)
			}
			byLine[m.Span.Start] = append(byLine[m.Span.Start], m)
		}
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, l := range order {
			ms := byLine[l]
			rng.Shuffle(len(ms), func(i, j int) { ms[i], ms[j] = ms[j], ms[i] })
		}
		taken = make([]adequacy.Mutant, 0, n)
		for round := 0; len(taken) < n; round++ {
			for _, l := range order {
				if ms := byLine[l]; round < len(ms) && len(taken) < n {
					taken = append(taken, ms[round])
				}
			}
		}
	} else {
		taken = append([]adequacy.Mutant(nil), candidates...)
	}
	sort.SliceStable(taken, func(i, j int) bool {
		if taken[i].Span.Start != taken[j].Span.Start {
			return taken[i].Span.Start < taken[j].Span.Start
		}
		return lessToolID(taken[i].ID, taken[j].ID)
	})
	return taken
}

// lessToolID orders "t/stryker/9" before "t/stryker/10": numerically when
// both ids are numbers, as Stryker's are, and as strings otherwise.
func lessToolID(a, b string) bool {
	ai, aerr := strconv.Atoi(a[strings.LastIndexByte(a, '/')+1:])
	bi, berr := strconv.Atoi(b[strings.LastIndexByte(b, '/')+1:])
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}
