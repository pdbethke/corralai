// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"encoding/json"
	"os"
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
	if want := []string{"220"}; !reflect.DeepEqual(one.KilledBy, want) {
		t.Errorf("mutant 1 KilledBy = %q, want the tool's test id %q", one.KilledBy, want)
	}
	if fm.Tests["220"] != names["220"] || names["220"] == "" {
		t.Errorf("Tests[220] = %q, want testFiles' name %q", fm.Tests["220"], names["220"])
	}
	// Tests names exactly the ids the recorded mutants refer to.
	referenced := map[string]bool{}
	for _, rm := range fm.Reported {
		for _, id := range append(append([]string(nil), rm.KilledBy...), rm.CoveredBy...) {
			referenced[id] = true
		}
	}
	if len(fm.Tests) != len(referenced) {
		t.Errorf("Tests names %d tests, the mutants refer to %d", len(fm.Tests), len(referenced))
	}
	for id := range fm.Tests {
		if !referenced[id] {
			t.Errorf("Tests names %s, which no mutant refers to", id)
		}
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
	var seq struct {
		Files map[string]struct {
			Mutants []struct {
				ID string `json:"id"`
			} `json:"mutants"`
		} `json:"files"`
	}
	if err := json.Unmarshal(report, &seq); err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, m := range seq.Files[reaPath].Mutants {
		want = append(want, "t/stryker/"+m.ID)
	}
	for _, rm := range fm.Reported {
		got = append(got, rm.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recorded order %v, want the report's %v", got, want)
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

// Every field the reader parses from a Stryker report reaches the record or
// is excluded in writing. A field in neither is the dropped-on-the-floor bug
// shape, and this names it. The walk covers every struct the reader parses,
// by JSON path, so a field added anywhere in strykerReport is caught.
func TestEveryParsedFieldIsRecordedOrExcluded(t *testing.T) {
	recordedAs := map[string]string{
		"framework.name":                      "ToolMutantCounts.Tool", // only StrykerJS is read; the label says stryker
		"framework.version":                   "ToolMutantCounts.Tool",
		"files.source":                        "ToolMutantCounts.Match", // tied to the audited bytes by sha256
		"files.mutants.id":                    "ReportMutant.ID",
		"files.mutants.mutatorName":           "ReportMutant.Operator",
		"files.mutants.replacement":           "ReportMutant.ReplacementSHA256",
		"files.mutants.status":                "ReportMutant.Status",
		"files.mutants.location.start.line":   "ReportMutant.Span",
		"files.mutants.location.start.column": "ReportMutant.Span",
		"files.mutants.location.end.line":     "ReportMutant.Span",
		"files.mutants.location.end.column":   "ReportMutant.Span",
		"files.mutants.static":                "ReportMutant.Static",
		"files.mutants.testsCompleted":        "ReportMutant.TestsCompleted",
		"files.mutants.killedBy":              "ReportMutant.KilledBy",
		"files.mutants.coveredBy":             "ReportMutant.CoveredBy",
		"files.mutants.duration":              "ReportMutant.DurationMS",
		"testFiles.tests.id":                  "ToolMutantCounts.Tests",
		"testFiles.tests.name":                "ToolMutantCounts.Tests",
		"schemaVersion":                       "ToolMutantCounts.SchemaVersion",
		"thresholds.high":                     "ToolThresholds.High",
		"thresholds.low":                      "ToolThresholds.Low",
		"thresholds.break":                    "ToolThresholds.Break",
	}
	ledgerTypes := map[string]reflect.Type{
		"ReportMutant":     reflect.TypeOf(ledger.ReportMutant{}),
		"ToolMutantCounts": reflect.TypeOf(ledger.ToolMutantCounts{}),
		"ToolThresholds":   reflect.TypeOf(ledger.ToolThresholds{}),
	}
	excluded := map[string]bool{}
	for _, f := range excludedFields["stryker"] {
		excluded[f] = true
	}
	parsed := strykerParsedFields()
	if len(parsed) < 20 {
		t.Fatalf("the walk found only %d parsed fields: %v", len(parsed), parsed)
	}
	for _, name := range parsed {
		target, mapped := recordedAs[name]
		switch {
		case mapped && excluded[name]:
			t.Errorf("%q is both recorded and excluded", name)
		case mapped:
			row, field, _ := strings.Cut(target, ".")
			if _, ok := ledgerTypes[row].FieldByName(field); !ok {
				t.Errorf("%q maps to %s, which does not exist", name, target)
			}
		case !excluded[name]:
			t.Errorf("the reader parses the Stryker field %q but neither records it nor lists it in excludedFields[\"stryker\"]", name)
		}
	}
}

// strykerParsedFields is every leaf field of the report the Stryker reader
// parses, as its JSON path in the report (map levels, which are keyed by a
// file path, and slice levels add nothing to the path).
func strykerParsedFields() []string {
	var parsed []string
	var walk func(prefix string, ty reflect.Type)
	walk = func(prefix string, ty reflect.Type) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct {
			parsed = append(parsed, prefix)
			return
		}
		for i := 0; i < ty.NumField(); i++ {
			name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
			p := name
			if prefix != "" {
				p = prefix + "." + name
			}
			walk(p, ty.Field(i).Type)
		}
	}
	walk("", reflect.TypeOf(strykerReport{}))
	return parsed
}

// reportLeafKeys is every leaf key path of a report as it is, walked
// generically: an object's keys extend the path, except where the reader's
// own type has a map there (files, testFiles: keyed by file path), and an
// array's elements share its path. An empty object or array is a leaf.
func reportLeafKeys(report []byte) (map[string]bool, error) {
	var v any
	if err := json.Unmarshal(report, &v); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	var walk func(prefix string, v any, ty reflect.Type)
	walk = func(prefix string, v any, ty reflect.Type) {
		for ty != nil && ty.Kind() == reflect.Pointer {
			ty = ty.Elem()
		}
		switch x := v.(type) {
		case map[string]any:
			if len(x) == 0 {
				out[prefix] = true
			}
			for k, val := range x {
				if ty != nil && ty.Kind() == reflect.Map {
					walk(prefix, val, ty.Elem())
					continue
				}
				var ft reflect.Type
				if ty != nil && ty.Kind() == reflect.Struct {
					for i := 0; i < ty.NumField(); i++ {
						if name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ","); name == k {
							ft = ty.Field(i).Type
						}
					}
				}
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, val, ft)
			}
		case []any:
			if len(x) == 0 {
				out[prefix] = true
			}
			var et reflect.Type
			if ty != nil && ty.Kind() == reflect.Slice {
				et = ty.Elem()
			}
			for _, e := range x {
				walk(prefix, e, et)
			}
		default:
			out[prefix] = true
		}
	}
	walk("", v, reflect.TypeOf(strykerReport{}))
	return out, nil
}

// Ruling R10 b: every key of the committed real report is parsed (and so,
// by TestEveryParsedFieldIsRecordedOrExcluded, recorded or excluded) or is
// excluded in writing, itself or under an excluded ancestor. The keys come
// from the report, not from a list here, so a key the tool adds is caught.
func TestEveryFixtureKeyIsParsedOrExcluded(t *testing.T) {
	parsed := map[string]bool{}
	for _, p := range strykerParsedFields() {
		parsed[p] = true
	}
	excluded := map[string]bool{}
	for _, f := range excludedFields["stryker"] {
		excluded[f] = true
	}
	underExcluded := func(k string) bool {
		for p := k; ; {
			if excluded[p] {
				return true
			}
			i := strings.LastIndex(p, ".")
			if i < 0 {
				return false
			}
			p = p[:i]
		}
	}
	for _, name := range []string{"testdata/stryker-rea-analysisErrorProjection.json"} {
		report, err := os.ReadFile(name) // #nosec G304 -- committed testdata
		if err != nil {
			t.Fatal(err)
		}
		keys, err := reportLeafKeys(report)
		if err != nil {
			t.Fatal(err)
		}
		// Negative controls: the walk reached a leaf deep in a mutant and
		// one deep in an excluded subtree.
		if !keys["files.mutants.location.start.line"] || !keys["config.thresholds.high"] {
			t.Fatalf("%s: the walk missed known leaves: %v", name, keys)
		}
		for k := range keys {
			if !parsed[k] && !underExcluded(k) {
				t.Errorf("%s: the report key %q is neither parsed nor listed in excludedFields[\"stryker\"] with a reason", name, k)
			}
		}
	}
}

func TestRecordableNameRefusesPathsAndNewlines(t *testing.T) {
	for _, s := range []string{"a\nb", "a\tb", "/home/x/t.ts", "~/t.ts", `C:\x`, strings.Repeat("n", 300), "bad\xffutf8",
		"loads /home/peter/x/a.json", "user alice@corp.example logs in", "reads ~/cfg", `opens D:\data`, "opens D:/data", "a\x00b",
		// Ruling R10, I2: path shapes the start-of-path rule missed.
		"loads file:///home/alice/x.json", "reads `/home/alice/cfg`", "reads */home/alice/x", `\\server\share\x`,
		"opens FILE:x", "glued foo/home/x/y", "rel/path/to/x is relative"} {
		got := recordableName(s)
		if got != "not-a-plain-name-"+sha256Hex([]byte(s))[:16] {
			t.Errorf("recordableName(%q) = %q, want the digest", s, got)
		}
	}
	for _, s := range []string{"suite > reports EACCES", strings.Repeat("n", 256), "distinguishes ünicode names", "a/b split",
		"fetches http://example.test", "keeps @scope/pkg names",
		// One segment after a slash and no second slash: not caught, a
		// disclosed limit (docs/corral/mutant-sources.md).
		"reads /etc"} {
		if got := recordableName(s); got != s {
			t.Errorf("recordableName(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Mutants refer to tests by the tool's id, through recordable; the file's
// Tests names each id testFiles names, through recordableName. An id with no
// name stays on the mutant and is absent from Tests.
func TestTestIDsWithoutAPlainNameAreNotRecordedRaw(t *testing.T) {
	src := "a();\n"
	hostileID := "x\n/home/someone/zz-marker"
	rep, err := json.Marshal(map[string]any{
		"schemaVersion": "1.0",
		"framework":     map[string]any{"name": "StrykerJS", "version": "10.0.0"},
		"testFiles": map[string]any{"t.test.js": map[string]any{"tests": []map[string]string{
			{"id": "9", "name": "/home/someone/zz-marker"},
			{"id": "8", "name": "a plain name"},
			{"id": "6", "name": "never referenced"},
			{"id": hostileID, "name": "named hostile"},
		}}},
		"files": map[string]any{"f.js": map[string]any{"language": "javascript", "source": src, "mutants": []map[string]any{{
			"id": "1", "mutatorName": "X", "replacement": "b()", "status": "Killed",
			"killedBy":  []string{"9", "7", hostileID},
			"coveredBy": []string{"8"},
			"location":  map[string]any{"start": map[string]int{"line": 1, "column": 1}, "end": map[string]int{"line": 1, "column": 4}},
		}, {
			"id": "2", "mutatorName": "X", "replacement": "c()", "status": "NoCoverage",
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
	fm := got["f.js"]
	rm := fm.Reported[0]
	hostileKey := NotAPlainToken + "-" + sha256Hex([]byte(hostileID))[:16]
	if want := []string{"9", "7", hostileKey}; !reflect.DeepEqual(rm.KilledBy, want) {
		t.Errorf("KilledBy = %q, want %q", rm.KilledBy, want)
	}
	wantTests := map[string]string{
		"9":        "not-a-plain-name-" + sha256Hex([]byte("/home/someone/zz-marker"))[:16],
		"8":        "a plain name",
		hostileKey: "named hostile",
	}
	if !reflect.DeepEqual(fm.Tests, wantTests) {
		t.Errorf("Tests = %q, want %q (7 has no name; 6 is referenced by no mutant)", fm.Tests, wantTests)
	}
	// An empty coveredBy is a claim (no test covers it), kept as a 0 count;
	// an absent one would be no count at all.
	if c := fm.Reported[1].CoveredByCount; c == nil || *c != 0 {
		t.Errorf("CoveredByCount = %v, want a stated 0", c)
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
	text, err := json.Marshal(ledger.ToolMutantCounts{Path: fm.Path, Tool: fm.Tool, Mutants: fm.Reported, Tests: fm.Tests})
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
	want := []string{"config", "files.language", "files.mutants.description", "files.mutants.statusReason", "framework.branding", "framework.dependencies", "projectRoot", "testFiles.source"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("excludedFields[stryker] = %v, want %v", got, want)
	}
}

// Ruling R10 b: the report's schemaVersion and thresholds are the tool's own
// values and reach every file's account, the refused included (they describe
// the report, not the file). A null break is recorded absent, never 0; a
// schemaVersion that is not a plain token is not recorded as written.
func TestTheReportsSchemaVersionAndThresholdsAreRead(t *testing.T) {
	report, src, _ := loadREA(t)
	got, err := readStryker(report, map[string][]byte{reaPath: []byte(src)}, budgetOf(5), 1)
	if err != nil {
		t.Fatal(err)
	}
	fm := got[reaPath]
	th := fm.Thresholds
	if fm.SchemaVersion != "1.0" || th == nil || th.High == nil || *th.High != 80 || th.Low == nil || *th.Low != 60 || th.Break != nil {
		raw, _ := json.Marshal(th)
		t.Errorf("SchemaVersion %q Thresholds %s, want 1.0 and high 80, low 60, break null", fm.SchemaVersion, raw)
	}
	raw, err := json.Marshal(th)
	if err != nil || string(raw) != `{"high":80,"low":60}` {
		t.Errorf("thresholds encode as %s (%v), want a null break absent", raw, err)
	}
	refused, err := readStryker(report, map[string][]byte{reaPath: []byte(src + "x")}, budgetOf(5), 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := refused[reaPath]; r.Refused == "" || r.SchemaVersion != "1.0" || r.Thresholds == nil {
		t.Errorf("a refused file's account = %+v, want the report's schemaVersion and thresholds", r)
	}
	var m map[string]any
	if err := json.Unmarshal(report, &m); err != nil {
		t.Fatal(err)
	}
	m["schemaVersion"] = "1.0 /home/alice"
	delete(m, "thresholds")
	hostile, _ := json.Marshal(m)
	got, err = readStryker(hostile, map[string][]byte{reaPath: []byte(src)}, budgetOf(5), 1)
	if err != nil {
		t.Fatal(err)
	}
	if fm := got[reaPath]; fm.SchemaVersion != NotAPlainToken || fm.Thresholds != nil {
		t.Errorf("hostile schemaVersion recorded as %q, absent thresholds as %+v", fm.SchemaVersion, fm.Thresholds)
	}
}
