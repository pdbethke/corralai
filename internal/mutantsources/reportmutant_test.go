// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// reaTestNames is the fixture's own testFiles table, id to name, read
// independently of the reader under test.
func reaTestNames(t *testing.T, report []byte) map[string]string {
	t.Helper()
	var r struct {
		TestFiles map[string]struct {
			Tests []struct{ ID, Name string } `json:"tests"`
		} `json:"testFiles"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range r.TestFiles {
		for _, tc := range f.Tests {
			out[tc.ID] = tc.Name
		}
	}
	return out
}

// Every mutant the report holds for the file is on the record, whatever
// happened to it, with what the tool said about it.
func TestEveryReportedMutantIsRecorded(t *testing.T) {
	fm, src, ms := readREA(t, 1000, 1)
	report, _, _ := loadREA(t)
	names := reaTestNames(t, report)
	if len(fm.Reported) != 31 {
		t.Fatalf("Reported has %d mutants, want all 31 the report holds", len(fm.Reported))
	}
	fates := map[string]int{}
	byID := map[string]ledger.ReportMutant{}
	for _, rm := range fm.Reported {
		fates[rm.Fate]++
		byID[rm.ID] = rm
		if rm.IDSource != "tool" {
			t.Errorf("%s: IDSource %q, want tool", rm.ID, rm.IDSource)
		}
	}
	for _, want := range []string{"graded", "dropped:anchor-not-unique", "not-taken:Killed", "not-taken:CompileError"} {
		if fates[want] == 0 {
			t.Errorf("no mutant has fate %q: %v", want, fates)
		}
	}
	if fates["graded"] != len(fm.Taken) {
		t.Errorf("%d graded, want the %d taken", fates["graded"], len(fm.Taken))
	}
	one, ok := byID["t/stryker/1"]
	if !ok {
		t.Fatal("mutant 1 is not recorded")
	}
	if want := []string{names["220"]}; !reflect.DeepEqual(one.KilledBy, want) || names["220"] == "" {
		t.Errorf("mutant 1 KilledBy = %q, want %q", one.KilledBy, want)
	}
	if one.TestsCompleted == nil || *one.TestsCompleted != 10 {
		t.Errorf("mutant 1 TestsCompleted = %v, want 10", one.TestsCompleted)
	}
	if one.Static == nil || *one.Static {
		t.Errorf("mutant 1 Static = %v, want false", one.Static)
	}
	if one.Operator != "BooleanLiteral" || one.Status != "Killed" || one.Fate != "not-taken:Killed" {
		t.Errorf("mutant 1 = %+v", one)
	}
	if one.Span != [4]int{60, 51, 60, 55} {
		t.Errorf("mutant 1 Span = %v, want the report's 60:51-60:55", one.Span)
	}
	if one.CoveredByCount == nil || *one.CoveredByCount != 118 || len(one.CoveredBy) != 118 {
		t.Errorf("mutant 1 covered by %d names, count %v; want 118", len(one.CoveredBy), one.CoveredByCount)
	}
	if one.ReplacementSHA256 != "" {
		t.Error("a Killed mutant has a replacement hash; corral built no hunk for it")
	}
	if one.DurationMS != nil {
		t.Error("StrykerJS 10 states no per-mutant duration, so none is recorded")
	}
	// Every ByteSpan, where set, is the reported location in the audited
	// bytes: the oracle splices the replacement at the reported columns.
	for _, rm := range fm.Reported {
		m := ms[strykerID(t, rm.ID)]
		if rm.ByteSpan == nil {
			t.Errorf("%s: no ByteSpan for a location inside an ASCII file", rm.ID)
			continue
		}
		b := *rm.ByteSpan
		if got, want := src[:b[0]]+m.Replacement+src[b[1]:], wholeFileSplice(t, src, m); got != want {
			t.Errorf("%s: ByteSpan %v is not the reported location", rm.ID, b)
		}
	}
	// A taken mutant's replacement hash is the hash of the hunk it is graded by.
	for _, m := range fm.Taken {
		if rm := byID[m.ID]; rm.ReplacementSHA256 != sha256Hex([]byte(m.Replace)) || rm.Fate != "graded" {
			t.Errorf("%s: fate %q hash %q, want graded and the taken hunk's REPLACE hash", m.ID, rm.Fate, rm.ReplacementSHA256)
		}
	}
	// The report's order is the record's.
	for i := 1; i < len(fm.Reported); i++ {
		if fm.Reported[i-1].ID == fm.Reported[i].ID {
			t.Errorf("mutant %s recorded twice", fm.Reported[i].ID)
		}
	}
}

// An over-budget mutant is recorded as dropped:over-budget, not as graded.
func TestOverBudgetMutantsAreRecordedAsDropped(t *testing.T) {
	fm, _, _ := readREA(t, 3, 1)
	n := 0
	for _, rm := range fm.Reported {
		if rm.Fate == dropOverBudget {
			n++
		}
	}
	if n == 0 || n != fm.Counts[dropOverBudget] {
		t.Errorf("%d recorded over-budget, Counts says %d", n, fm.Counts[dropOverBudget])
	}
}

// Every field the reader parses from a Stryker mutant reaches the record or
// is excluded in writing. A field in neither is the dropped-on-the-floor bug
// shape, and this names it.
func TestEveryParsedFieldIsRecordedOrExcluded(t *testing.T) {
	recordedAs := map[string]string{
		"id":             "ID",
		"mutatorName":    "Operator",
		"replacement":    "ReplacementSHA256",
		"status":         "Status",
		"location":       "Span",
		"static":         "Static",
		"testsCompleted": "TestsCompleted",
		"killedBy":       "KilledBy",
		"coveredBy":      "CoveredBy",
		"duration":       "DurationMS",
	}
	excluded := map[string]bool{}
	for _, f := range excludedFields["stryker"] {
		excluded[f] = true
	}
	rmType := reflect.TypeOf(ledger.ReportMutant{})
	ty := reflect.TypeOf(strykerReportMutant{})
	for i := 0; i < ty.NumField(); i++ {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		field, mapped := recordedAs[name]
		switch {
		case mapped && excluded[name]:
			t.Errorf("%q is both recorded and excluded", name)
		case mapped:
			if _, ok := rmType.FieldByName(field); !ok {
				t.Errorf("%q maps to ReportMutant.%s, which does not exist", name, field)
			}
		case !excluded[name]:
			t.Errorf("the reader parses the Stryker mutant field %q but neither records it nor lists it in excludedFields[\"stryker\"]", name)
		}
	}
}

func TestRecordableNameRefusesPathsAndNewlines(t *testing.T) {
	for _, s := range []string{"a\nb", "a\tb", "/home/x/t.ts", "~/t.ts", `C:\x`, strings.Repeat("n", 300), "bad\xffutf8"} {
		got := recordableName(s)
		if got != "not-a-plain-name-"+sha256Hex([]byte(s))[:16] {
			t.Errorf("recordableName(%q) = %q, want the digest", s, got)
		}
	}
	for _, s := range []string{"suite > reports EACCES", strings.Repeat("n", 256), "distinguishes ünicode names"} {
		if got := recordableName(s); got != s {
			t.Errorf("recordableName(%q) = %q, want it unchanged", s, got)
		}
	}
}

// A test id with no name in testFiles is recorded by its id when that is a
// plain token, and by a digest otherwise; a hostile name is digested.
func TestTestIDsWithoutAPlainNameAreNotRecordedRaw(t *testing.T) {
	src := "a();\n"
	rep, err := json.Marshal(map[string]any{
		"schemaVersion": "1.0",
		"framework":     map[string]any{"name": "StrykerJS", "version": "10.0.0"},
		"testFiles": map[string]any{"t.test.js": map[string]any{"tests": []map[string]string{
			{"id": "9", "name": "/home/someone/zz-marker"},
		}}},
		"files": map[string]any{"f.js": map[string]any{"language": "javascript", "source": src, "mutants": []map[string]any{{
			"id": "1", "mutatorName": "X", "replacement": "b()", "status": "Killed",
			"killedBy":  []string{"9", "7", "x\n/home/someone/zz-marker"},
			"coveredBy": []string{},
			"location":  map[string]any{"start": map[string]int{"line": 1, "column": 1}, "end": map[string]int{"line": 1, "column": 4}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readStryker(rep, map[string][]byte{"f.js": []byte(src)}, budgetOf(10), 1)
	if err != nil {
		t.Fatal(err)
	}
	rm := got["f.js"].Reported[0]
	want := []string{
		"not-a-plain-name-" + sha256Hex([]byte("/home/someone/zz-marker"))[:16],
		"id:7",
		"not-a-plain-name-" + sha256Hex([]byte("x\n/home/someone/zz-marker"))[:16],
	}
	if !reflect.DeepEqual(rm.KilledBy, want) {
		t.Errorf("KilledBy = %q, want %q", rm.KilledBy, want)
	}
	// An empty coveredBy is a claim (no test covers it), kept as a 0 count;
	// an absent one would be no count at all.
	if rm.CoveredByCount == nil || *rm.CoveredByCount != 0 {
		t.Errorf("CoveredByCount = %v, want a stated 0", rm.CoveredByCount)
	}
	if rm.Static != nil || rm.TestsCompleted != nil {
		t.Errorf("Static %v TestsCompleted %v: the report states neither, so neither is recorded", rm.Static, rm.TestsCompleted)
	}
}

// statusReason is the tool's free text — compiler output, assertion diffs,
// host paths — and never reaches the record.
func TestStatusReasonNeverReachesTheRecord(t *testing.T) {
	fm, _, _ := readREA(t, 1000, 1)
	report, _, _ := loadREA(t)
	var r struct {
		Files map[string]struct {
			Mutants []struct {
				StatusReason string `json:"statusReason"`
			} `json:"mutants"`
		} `json:"files"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		t.Fatal(err)
	}
	text, err := json.Marshal(ledger.ToolMutantCounts{Path: fm.Path, Tool: fm.Tool, Mutants: fm.Reported})
	if err != nil {
		t.Fatal(err)
	}
	reasons := 0
	for _, m := range r.Files[reaPath].Mutants {
		if m.StatusReason == "" {
			continue
		}
		reasons++
		enc, _ := json.Marshal(m.StatusReason)
		if strings.Contains(string(text), strings.Trim(string(enc), `"`)) {
			t.Errorf("the record holds a statusReason: %.80q", m.StatusReason)
		}
	}
	if reasons == 0 {
		t.Fatal("the fixture has no statusReason to look for")
	}
}

// The exclusion list is sorted per tool and names only what the record
// deliberately leaves out; each entry has its reason in the source.
func TestExcludedFieldsAreListed(t *testing.T) {
	got := append([]string(nil), excludedFields["stryker"]...)
	sort.Strings(got)
	want := []string{"config", "description", "framework.branding", "projectRoot", "statusReason"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("excludedFields[stryker] = %v, want %v", got, want)
	}
}
