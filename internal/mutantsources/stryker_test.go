// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

const (
	reaPath = "src/domain/analysisErrorProjection.ts"
	// reaSHA is the sha256 of REA's analysisErrorProjection.ts at aa48d494e
	// (testdata/PROVENANCE.md). Asserting it keeps the fixture's source the
	// real file, so the byte-identity check below is made against real bytes.
	reaSHA = "a670809bcfcdd001fe1deb8462433fea700c502dbe0cd20e6e33e27a746b3fa7"
)

// strykerMutant is the test's own reading of the report, independent of the
// reader under test, so the oracle below is not the code it checks.
type strykerMutant struct {
	ID          string `json:"id"`
	MutatorName string `json:"mutatorName"`
	Replacement string `json:"replacement"`
	Status      string `json:"status"`
	Location    struct {
		Start struct{ Line, Column int } `json:"start"`
		End   struct{ Line, Column int } `json:"end"`
	} `json:"location"`
}

func loadREA(t *testing.T) (report []byte, source string, mutants map[string]strykerMutant) {
	t.Helper()
	report, err := os.ReadFile("testdata/stryker-rea-analysisErrorProjection.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Files map[string]struct {
			Source  string          `json:"source"`
			Mutants []strykerMutant `json:"mutants"`
		} `json:"files"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		t.Fatal(err)
	}
	f := r.Files[reaPath]
	if got := testSHA(f.Source); got != reaSHA {
		t.Fatalf("fixture source sha256 = %s, want REA's real file %s", got, reaSHA)
	}
	mutants = map[string]strykerMutant{}
	for _, m := range f.Mutants {
		mutants[m.ID] = m
	}
	return report, f.Source, mutants
}

func testSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func budgetOf(n int) func(string) int { return func(string) int { return n } }

func readREA(t *testing.T, budget int, seed int64) (FileMutants, string, map[string]strykerMutant) {
	t.Helper()
	report, src, ms := loadREA(t)
	got, err := ReadStryker(report, map[string][]byte{reaPath: []byte(src)}, budgetOf(budget), seed)
	if err != nil {
		t.Fatalf("ReadStryker: %v", err)
	}
	fm, ok := got[reaPath]
	if !ok {
		t.Fatalf("no entry for %s in %v", reaPath, got)
	}
	return fm, src, ms
}

// wholeFileSplice is the oracle: it splices Stryker's replacement into the
// WHOLE source at the reported 1-based, end-exclusive columns (the fixture is
// ASCII, so a column is a byte). The reader builds a line-bounded hunk
// instead; agreeing with this proves the hunk is Stryker's mutant.
func wholeFileSplice(t *testing.T, src string, m strykerMutant) string {
	t.Helper()
	lines := strings.SplitAfter(src, "\n")
	off := func(line, col int) int {
		n := 0
		for _, l := range lines[:line-1] {
			n += len(l)
		}
		return n + col - 1
	}
	return src[:off(m.Location.Start.Line, m.Location.Start.Column)] + m.Replacement + src[off(m.Location.End.Line, m.Location.End.Column):]
}

func strykerID(t *testing.T, id string) string {
	t.Helper()
	const p = "t/stryker/"
	if !strings.HasPrefix(id, p) {
		t.Fatalf("mutant ID %q lacks the %q prefix", id, p)
	}
	return strings.TrimPrefix(id, p)
}

// The fixture's 23 Survived/NoCoverage mutants, less the three whose anchor
// lines occur elsewhere in the file (374, 422, 593), which the patch applier
// must refuse.
var reaNonUnique = []string{"374", "422", "593"}

const reaProvable = 20

func TestStrykerSurvivorsBecomeSingleRegionMutants(t *testing.T) {
	fm, src, ms := readREA(t, 1000, 1)
	if fm.Refused != "" {
		t.Fatalf("refused: %s", fm.Refused)
	}
	if len(fm.Taken) != reaProvable {
		t.Fatalf("took %d mutants, want %d", len(fm.Taken), reaProvable)
	}
	for _, id := range reaNonUnique {
		for _, m := range fm.Taken {
			if m.ID == "t/stryker/"+id {
				t.Errorf("mutant %s's anchor occurs twice in the file, yet it was taken", id)
			}
		}
	}
	if fm.Counts["dropped:anchor-not-unique"] != len(reaNonUnique) {
		t.Errorf("dropped:anchor-not-unique = %d, want %d (Counts %v)", fm.Counts["dropped:anchor-not-unique"], len(reaNonUnique), fm.Counts)
	}
	sawMultiLine := false
	for _, m := range fm.Taken {
		sm, ok := ms[strykerID(t, m.ID)]
		if !ok {
			t.Fatalf("%s is not a mutant in the report", m.ID)
		}
		if sm.Status != "Survived" && sm.Status != "NoCoverage" {
			t.Errorf("%s has status %s and was taken", m.ID, sm.Status)
		}
		if m.ParentSHA256 != reaSHA {
			t.Errorf("%s ParentSHA256 = %s, want %s", m.ID, m.ParentSHA256, reaSHA)
		}
		want := lang.LineRange{Start: sm.Location.Start.Line, End: sm.Location.End.Line}
		if m.Span != want {
			t.Errorf("%s Span = %+v, want the location's lines %+v", m.ID, m.Span, want)
		}
		if want.End > want.Start {
			sawMultiLine = true
		}
		got, err := m.Apply(src)
		if err != nil {
			t.Fatalf("%s does not apply: %v", m.ID, err)
		}
		if exp := wholeFileSplice(t, src, sm); got != exp {
			t.Errorf("%s: applying the hunk does not reproduce Stryker's replacement %q at its columns", m.ID, sm.Replacement)
		}
		src := fm.Sources[m.ID]
		if src != (MutantSource{Kind: "tool", Tool: "stryker@10.0.0", Operator: sm.MutatorName, Status: sm.Status}) {
			t.Errorf("%s source = %+v", m.ID, src)
		}
	}
	if !sawMultiLine {
		t.Fatal("no multi-line mutant was taken; the fixture keeps four")
	}
	// One known by eye: id 518 turns `typeof primary === "object"` on line
	// 578 into `!==`.
	for _, m := range fm.Taken {
		if m.ID == "t/stryker/518" {
			if !strings.Contains(m.Search, `typeof primary === "object"`) || !strings.Contains(m.Replace, `typeof primary !== "object"`) {
				t.Fatalf("518: Search %q Replace %q", m.Search, m.Replace)
			}
			return
		}
	}
	t.Fatal("518 was not taken")
}

func TestStrykerNonSurvivorsAreCountedNotTaken(t *testing.T) {
	fm, _, ms := readREA(t, 1000, 1)
	for status, want := range map[string]int{"Killed": 4, "CompileError": 4, "Survived": 15, "NoCoverage": 8} {
		if fm.Counts[status] != want {
			t.Errorf("Counts[%s] = %d, want %d", status, fm.Counts[status], want)
		}
	}
	for _, m := range fm.Taken {
		if s := ms[strykerID(t, m.ID)].Status; s == "Killed" || s == "CompileError" {
			t.Errorf("%s (%s) was taken", m.ID, s)
		}
	}

	// The statuses the REA report does not carry are counted the same way.
	rep := synthReport(t, "a();\nb();\nc();\nd();\n", []synthMutant{
		{"1", "Timeout", 1, 1, 1, 4, "x()"},
		{"2", "RuntimeError", 2, 1, 2, 4, "x()"},
		{"3", "Ignored", 3, 1, 3, 4, "x()"},
		{"4", "Survived", 4, 1, 4, 4, "x()"},
		{"4", "Survived", 3, 1, 3, 4, "y()"},
	})
	got, err := ReadStryker(rep, map[string][]byte{"f.js": []byte("a();\nb();\nc();\nd();\n")}, budgetOf(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	f := got["f.js"]
	if len(f.Taken) != 1 || f.Taken[0].ID != "t/stryker/4" {
		t.Fatalf("Taken = %+v, want only the survivor", f.Taken)
	}
	if f.Counts["dropped:duplicate-id"] != 1 {
		t.Errorf("a second mutant under id 4 was not dropped as a duplicate: %v", f.Counts)
	}
	for s, want := range map[string]int{"Timeout": 1, "RuntimeError": 1, "Ignored": 1, "Survived": 2} {
		if f.Counts[s] != want {
			t.Errorf("Counts[%s] = %d, want %d", s, f.Counts[s], want)
		}
	}
}

// Review Focus 1: a report about other bytes is refused, never re-applied.
func TestStrykerReportForOtherBytesIsRefused(t *testing.T) {
	report, src, _ := loadREA(t)
	edited := src + "// one more line\n"
	got, err := ReadStryker(report, map[string][]byte{reaPath: []byte(edited)}, budgetOf(1000), 1)
	if err != nil {
		t.Fatalf("a sha mismatch refuses the file, it is not a run error: %v", err)
	}
	fm := got[reaPath]
	if len(fm.Taken) != 0 || len(fm.Sources) != 0 {
		t.Fatalf("took %d mutants from a report about other bytes", len(fm.Taken))
	}
	if fm.Refused == "" {
		t.Fatal("Refused is empty")
	}
	for _, want := range []string{"sha256", reaSHA[:12], testSHA(edited)[:12]} {
		if !strings.Contains(fm.Refused, want) {
			t.Errorf("Refused %q does not name %q", fm.Refused, want)
		}
	}
	if strings.Count(strings.TrimSuffix(fm.Refused, "."), ". ") != 0 {
		t.Errorf("Refused is meant to be one sentence: %q", fm.Refused)
	}
}

type synthMutant struct {
	id, status     string
	sl, sc, el, ec int
	replacement    string
}

func synthReport(t *testing.T, source string, ms []synthMutant) []byte {
	t.Helper()
	type pos struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	}
	var out []map[string]any
	for _, m := range ms {
		out = append(out, map[string]any{
			"id": m.id, "mutatorName": "Synthetic", "replacement": m.replacement, "status": m.status,
			"location": map[string]pos{"start": {m.sl, m.sc}, "end": {m.el, m.ec}},
		})
	}
	b, err := json.Marshal(map[string]any{
		"schemaVersion": "1.0",
		"framework":     map[string]any{"name": "StrykerJS", "version": "10.0.0"},
		"files":         map[string]any{"f.js": map[string]any{"language": "javascript", "source": source, "mutants": out}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Review Focus 2: an empty replacement, a multi-line location, a multi-line
// replacement, a column past a non-ASCII character, and a location that does
// not fit the file.
func TestStrykerEmptyAndMultiLineReplacements(t *testing.T) {
	const src = "const a = f(1, 2);\nif (x) {\n  y();\n}\nconst s = \"hé\U0001F600\"; g(s);\nlast()"
	rep := synthReport(t, src, []synthMutant{
		// `(1, 2)` -> empty: an empty replacement is a deletion, not a no-op.
		{"1", "Survived", 1, 12, 1, 18, ""},
		// The block `{\n  y();\n}` (line 2 col 8 .. line 4 col 2) -> `{}`.
		{"2", "Survived", 2, 8, 4, 2, "{}"},
		// `f(1, 2)` -> a two-line replacement.
		{"3", "NoCoverage", 1, 11, 1, 18, "f(\n  9)"},
		// `g(s)` on line 5. Columns are UTF-16 units: é is one, 😀 is two,
		// so `g` is at column 19 although it starts at byte 22.
		{"4", "Survived", 5, 19, 5, 23, "h(s)"},
		// The last line has no newline after it.
		{"5", "Survived", 6, 1, 6, 5, "next"},
		// Line 9 does not exist; column 99 is past line 1's end; a start
		// after its end; and a column that splits 😀's surrogate pair.
		{"6", "Survived", 9, 1, 9, 2, "x"},
		{"7", "Survived", 1, 1, 1, 99, "x"},
		{"8", "Survived", 1, 5, 1, 3, "x"},
		{"9", "Survived", 5, 15, 5, 16, "x"},
	})
	got, err := ReadStryker(rep, map[string][]byte{"f.js": []byte(src)}, budgetOf(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	fm := got["f.js"]
	want := map[string]string{
		"t/stryker/1": "const a = f;\nif (x) {\n  y();\n}\nconst s = \"hé\U0001F600\"; g(s);\nlast()",
		"t/stryker/2": "const a = f(1, 2);\nif (x) {}\nconst s = \"hé\U0001F600\"; g(s);\nlast()",
		"t/stryker/3": "const a = f(\n  9);\nif (x) {\n  y();\n}\nconst s = \"hé\U0001F600\"; g(s);\nlast()",
		"t/stryker/4": "const a = f(1, 2);\nif (x) {\n  y();\n}\nconst s = \"hé\U0001F600\"; h(s);\nlast()",
		"t/stryker/5": "const a = f(1, 2);\nif (x) {\n  y();\n}\nconst s = \"hé\U0001F600\"; g(s);\nnext()",
	}
	wantSpan := map[string]lang.LineRange{
		"t/stryker/1": {Start: 1, End: 1}, "t/stryker/2": {Start: 2, End: 4},
		"t/stryker/3": {Start: 1, End: 1}, "t/stryker/4": {Start: 5, End: 5}, "t/stryker/5": {Start: 6, End: 6},
	}
	if len(fm.Taken) != len(want) {
		t.Fatalf("took %d, want %d: %+v (Counts %v)", len(fm.Taken), len(want), fm.Taken, fm.Counts)
	}
	for _, m := range fm.Taken {
		got, err := m.Apply(src)
		if err != nil {
			t.Fatalf("%s: %v", m.ID, err)
		}
		if got != want[m.ID] {
			t.Errorf("%s applied =\n%q\nwant\n%q", m.ID, got, want[m.ID])
		}
		if m.Span != wantSpan[m.ID] {
			t.Errorf("%s Span = %+v, want %+v", m.ID, m.Span, wantSpan[m.ID])
		}
	}
	if fm.Counts["dropped:location-out-of-range"] != 4 {
		t.Errorf("dropped:location-out-of-range = %d, want 4 (Counts %v)", fm.Counts["dropped:location-out-of-range"], fm.Counts)
	}
}

func takenIDs(fm FileMutants) []string {
	var ids []string
	for _, m := range fm.Taken {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

func distinctStartLines(ms []adequacy.Mutant) map[int]int {
	lines := map[int]int{}
	for _, m := range ms {
		lines[m.Span.Start]++
	}
	return lines
}

// Review Focus 3: over budget, the sample is a pure function of the seed, and
// it takes one mutant per distinct line before it takes a second from any.
func TestStrykerSampleIsSeededAndSpreadsLines(t *testing.T) {
	all, _, _ := readREA(t, 1000, 1)
	lines := len(distinctStartLines(all.Taken))
	if lines >= reaProvable || lines < 6 {
		t.Fatalf("the fixture should crowd some lines: %d provable mutants on %d lines", reaProvable, lines)
	}

	for _, budget := range []int{5, lines} {
		fm, _, _ := readREA(t, budget, 42)
		if len(fm.Taken) != budget {
			t.Fatalf("budget %d: took %d", budget, len(fm.Taken))
		}
		for line, n := range distinctStartLines(fm.Taken) {
			if n != 1 {
				t.Errorf("budget %d: line %d got %d mutants while other lines got none", budget, line, n)
			}
		}
		if fm.Seed != 42 {
			t.Errorf("Seed = %d, want the seed passed, 42", fm.Seed)
		}
		if fm.Counts["dropped:over-budget"] != reaProvable-budget {
			t.Errorf("budget %d: dropped:over-budget = %d, want %d", budget, fm.Counts["dropped:over-budget"], reaProvable-budget)
		}
		again, _, _ := readREA(t, budget, 42)
		if !reflect.DeepEqual(takenIDs(fm), takenIDs(again)) || !reflect.DeepEqual(fm.Taken, again.Taken) {
			t.Errorf("budget %d: the same seed drew different mutants", budget)
		}
	}

	// A budget past one-per-line takes a second mutant from some line, and
	// still from every line once.
	fm, _, _ := readREA(t, lines+2, 42)
	if got := len(distinctStartLines(fm.Taken)); got != lines {
		t.Errorf("budget %d covered %d lines, want all %d", lines+2, got, lines)
	}

	seen := map[string]bool{}
	for seed := int64(1); seed <= 20; seed++ {
		fm, _, _ := readREA(t, 5, seed)
		seen[strings.Join(takenIDs(fm), ",")] = true
	}
	if len(seen) < 2 {
		t.Error("twenty seeds drew the same five mutants: the seed does not reach the draw")
	}
}

// Review Focus 4.
func TestStrykerReportMatchingNoAuditedFileIsAnError(t *testing.T) {
	report, src, _ := loadREA(t)
	_, err := ReadStryker(report, map[string][]byte{"src/other.ts": []byte("x")}, budgetOf(10), 1)
	if err == nil {
		t.Fatal("a report about none of the audited files was accepted")
	}
	if !strings.Contains(err.Error(), reaPath) {
		t.Errorf("the error does not name the report's paths: %v", err)
	}

	// An audited file the report does not mention gets no entry.
	got, err := ReadStryker(report, map[string][]byte{reaPath: []byte(src), "src/other.ts": []byte("x")}, budgetOf(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["src/other.ts"]; ok {
		t.Error("an audited file absent from the report got an entry")
	}
	if _, ok := got[reaPath]; !ok {
		t.Error("the reported file got no entry")
	}
}

// What the reader cannot read with certainty it refuses, naming what to fix,
// rather than substituting a value.
func TestStrykerRefusesWhatItCannotRead(t *testing.T) {
	report, src, _ := loadREA(t)
	files := map[string][]byte{reaPath: []byte(src)}
	if _, err := ReadStryker(report, files, nil, 1); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("nil budget: %v", err)
	}
	if _, err := ReadStryker(report, files, budgetOf(0), 1); err == nil || !strings.Contains(err.Error(), reaPath) {
		t.Errorf("zero budget: %v", err)
	}
	if _, err := ReadStryker([]byte("{"), files, budgetOf(5), 1); err == nil {
		t.Error("malformed JSON was accepted")
	}
	for name, fw := range map[string]string{
		"no version":   `{"name":"StrykerJS"}`,
		"another tool": `{"name":"Stryker.NET","version":"4.0.0"}`,
		"no framework": `null`,
	} {
		rep := []byte(`{"framework":` + fw + `,"files":{"f.js":{"source":"a\n","mutants":[]}}}`)
		if _, err := ReadStryker(rep, map[string][]byte{"f.js": []byte("a\n")}, budgetOf(5), 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Babel ends a line at a lone \r, U+2028 or U+2029; corral splits on \n. A
// file holding one would have every later line numbered differently by the
// two, so it is refused rather than mis-anchored. \r\n is fine.
func TestStrykerFileWithLineBreaksCorralCannotCountIsRefused(t *testing.T) {
	for name, src := range map[string]string{
		"lone CR": "a();\rb();\nc();\n",
		"U+2028":  "const s = \"\u2028\";\nc();\n",
		"U+2029":  "const s = \"\u2029\";\nc();\n",
	} {
		rep := synthReport(t, src, []synthMutant{{"1", "Survived", 2, 1, 2, 4, "x()"}})
		got, err := ReadStryker(rep, map[string][]byte{"f.js": []byte(src)}, budgetOf(5), 1)
		if err != nil {
			t.Fatal(err)
		}
		if f := got["f.js"]; f.Refused == "" || len(f.Taken) != 0 {
			t.Errorf("%s: Refused %q, took %d", name, f.Refused, len(f.Taken))
		}
	}
	const crlf = "a();\r\nb();\r\n"
	rep := synthReport(t, crlf, []synthMutant{{"1", "Survived", 2, 1, 2, 4, "x()"}})
	got, err := ReadStryker(rep, map[string][]byte{"f.js": []byte(crlf)}, budgetOf(5), 1)
	if err != nil {
		t.Fatal(err)
	}
	f := got["f.js"]
	if f.Refused != "" || len(f.Taken) != 1 {
		t.Fatalf("CRLF: Refused %q, took %d", f.Refused, len(f.Taken))
	}
	if out, _ := f.Taken[0].Apply(crlf); out != "a();\r\nx();\r\n" {
		t.Errorf("CRLF applied = %q", out)
	}
}
