// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pdbethke/corralai/internal/ledger"
)

// Every reported mutant reaches the record (ledger.ReportMutant), because
// the record is the only source of what corral publishes and a field a reader
// drops cannot be recovered from it later. The exceptions are written down
// here, per tool, each with the reason the public record cannot hold it.
// TestEveryParsedFieldIsRecordedOrExcluded fails on a parsed mutant field
// that is in neither the record nor this list.
var excludedFields = map[string][]string{
	"stryker": {
		// statusReason: the tool's free text (compiler output, assertion
		// diffs naming values and host paths); no filter makes it safe.
		"statusReason",
		// description: free text the tool writes about a mutant.
		"description",
		// projectRoot: a host path on the machine that ran the tool.
		"projectRoot",
		// config: the operator's whole configuration, paths and commands.
		"config",
		// framework.branding: the tool's logo and homepage; describes no run.
		"framework.branding",
	},
}

// maxNameBytes bounds a recorded test name, so no name can be a page of text.
const maxNameBytes = 256

// recordableName is a test name as the record holds it: s when it is a
// plain line (at most maxNameBytes of printable UTF-8, no newline or tab)
// that does not look like a path (does not start with "/" or "~", contains
// no `:\`), else a digest of it. A test's name is the developer's sentence,
// worth keeping, but it sits in the operator's report and the record is
// public: a digest still tells two names apart and the same name together.
func recordableName(s string) string {
	if len(s) <= maxNameBytes && utf8.ValidString(s) &&
		!strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~") && !strings.Contains(s, `:\`) &&
		strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	return "not-a-plain-name-" + sha256Hex([]byte(s))[:16]
}

// strykerTestNames maps a report's test ids to their names in testFiles.
func strykerTestNames(rep strykerReport) map[string]string {
	out := map[string]string{}
	for _, f := range rep.TestFiles {
		for _, t := range f.Tests {
			out[t.ID] = t.Name
		}
	}
	return out
}

// recordedTests is killedBy or coveredBy as the record holds it: each id
// replaced by its test's name through recordableName, or, for an id
// testFiles does not name, "id:<id>" when the id is a plain token and a
// digest otherwise.
func recordedTests(ids []string, names map[string]string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		switch name := names[id]; {
		case name != "":
			out[i] = recordableName(name)
		case plainToken.MatchString(id):
			out[i] = "id:" + id
		default:
			out[i] = "not-a-plain-name-" + sha256Hex([]byte(id))[:16]
		}
	}
	return out
}

// strykerReported is a Stryker mutant as the record holds it, before corral
// has decided its fate. Everything here is the tool's claim except the byte
// span, which is corral's conversion of the claim's location.
func strykerReported(sm strykerReportMutant, lines []string, lineStarts []int, names map[string]string) ledger.ReportMutant {
	rm := ledger.ReportMutant{
		ID:       strykerMutantID(sm.ID),
		IDSource: "tool",
		Operator: recordable(sm.MutatorName),
		Status:   recordable(sm.Status),
		Span:     [4]int{sm.Location.Start.Line, sm.Location.Start.Column, sm.Location.End.Line, sm.Location.End.Column},
		ByteSpan: strykerByteSpan(lines, lineStarts, sm.Location.Start, sm.Location.End),
		Static:   sm.Static,
		KilledBy: recordedTests(sm.KilledBy, names),
	}
	if sm.TestsCompleted != nil {
		n := *sm.TestsCompleted
		rm.TestsCompleted = &n
	}
	// An empty coveredBy is the tool saying no test covers the mutant, so it
	// is a stated 0; an absent one states nothing and records no count.
	if sm.CoveredBy != nil {
		n := len(sm.CoveredBy)
		rm.CoveredByCount = &n
		rm.CoveredBy = recordedTests(sm.CoveredBy, names)
	}
	// A duration is recorded only as a whole number of milliseconds a
	// clock could have measured; a negative or absurd one is no measurement.
	if d := sm.Duration; d != nil && *d >= 0 && *d < math.MaxInt32 {
		ms := int(math.Round(*d))
		rm.DurationMS = &ms
	}
	return rm
}

// lineStartOffsets is the byte offset at which each of lines begins in the
// text they were split from on "\n".
func lineStartOffsets(lines []string) []int {
	out := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		out[i] = off
		off += len(l) + 1
	}
	return out
}

// strykerByteSpan converts a Stryker location (1-based lines, 1-based UTF-16
// columns, end-exclusive) to byte offsets into the file, or nil when the
// location does not fit it. It applies the same conversion strykerHunk does.
func strykerByteSpan(lines []string, lineStarts []int, start, end strykerPos) *[2]int {
	if start.Line < 1 || end.Line < start.Line || end.Line > len(lines) {
		return nil
	}
	from, ok1 := utf16ColumnToByte(lines[start.Line-1], start.Column)
	to, ok2 := utf16ColumnToByte(lines[end.Line-1], end.Column)
	if !ok1 || !ok2 {
		return nil
	}
	b := [2]int{lineStarts[start.Line-1] + from, lineStarts[end.Line-1] + to}
	if b[0] > b[1] {
		return nil
	}
	return &b
}
