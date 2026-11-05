// SPDX-License-Identifier: Elastic-2.0

package mutantsources

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files from the current reader")

// fullReportGz is the full REA StrykerJS report the golden below was
// captured from, scrubbed of its host path and configuration and gzipped
// (testdata/PROVENANCE.md), committed so the golden and size tests run in CI.
const fullReportGz = "testdata/stryker-rea-full.json.gz"

// fullReportEnv overrides fullReportGz with the absolute path of an
// uncompressed report, to check the golden against another capture.
const fullReportEnv = "CORRAL_STRYKER_FULL_REPORT"

// fullReport is the full report's bytes: the committed copy, or the file
// fullReportEnv names. It never skips: a missing report is a failure.
func fullReport(t *testing.T) []byte {
	t.Helper()
	if p := os.Getenv(fullReportEnv); p != "" {
		b, err := os.ReadFile(p) // #nosec G304 -- an operator-named test input
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f, err := os.Open(fullReportGz)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const goldenPath = "testdata/stryker-rea-full.golden.json"

// goldenMutant is one taken mutant as the golden holds it: identity and
// hashes of the edit, never the edit's text.
type goldenMutant struct {
	ID           string `json:"id"`
	ParentSHA256 string `json:"parent_sha256"`
	SpanStart    int    `json:"span_start"`
	SpanEnd      int    `json:"span_end"`
	SearchSHA256 string `json:"search_sha256"`
	ReplaceSHA   string `json:"replace_sha256"`
}

type goldenFile struct {
	Tool    string                  `json:"tool"`
	Refused string                  `json:"refused"`
	Seed    int64                   `json:"seed"`
	Taken   []goldenMutant          `json:"taken"`
	Sources map[string]MutantSource `json:"sources"`
	Counts  map[string]int          `json:"counts"`
}

// goldenOf renders what a reader returned for the full report.
func goldenOf(got map[string]FileMutants) map[string]goldenFile {
	out := map[string]goldenFile{}
	for p, fm := range got {
		g := goldenFile{Tool: fm.Tool, Refused: fm.Refused, Seed: fm.Seed, Sources: fm.Sources, Counts: fm.Counts, Taken: []goldenMutant{}}
		for _, m := range fm.Taken {
			g.Taken = append(g.Taken, goldenMutant{ID: m.ID, ParentSHA256: m.ParentSHA256,
				SpanStart: m.Span.Start, SpanEnd: m.Span.End,
				SearchSHA256: sha256Hex([]byte(m.Search)), ReplaceSHA: sha256Hex([]byte(m.Replace))})
		}
		sort.Slice(g.Taken, func(i, j int) bool { return g.Taken[i].ID < g.Taken[j].ID })
		out[p] = g
	}
	return out
}

// TestStrykerFullReportGolden pins the Stryker reader's whole output over a
// real 662-mutant report (budget 40, seed 20261101), so moving Stryker behind
// the Reader interface is provably a no-change. Run with -update on
// unmodified code to capture it.
func TestStrykerFullReportGolden(t *testing.T) {
	report := fullReport(t)
	var r struct {
		Files map[string]struct {
			Source string `json:"source"`
		} `json:"files"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for p, f := range r.Files {
		files[p] = []byte(f.Source)
	}
	rd, _ := Lookup("stryker")
	got, err := rd.Read(Report{Bytes: report}, files, budgetOf(40), 20261101)
	if err != nil {
		t.Fatal(err)
	}
	text, err := json.MarshalIndent(goldenOf(got), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	text = append(text, '\n')
	if *updateGolden {
		if err := os.WriteFile(goldenPath, text, 0o644); err != nil { // #nosec G306 -- a test golden
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(text) {
		t.Fatalf("the Stryker reader's output over the full report differs from %s; if the change is intended run with -update and read the diff", goldenPath)
	}
}

func TestRegistryKnowsStryker(t *testing.T) {
	r, ok := Lookup("stryker")
	if !ok || r.Tool() != "stryker" {
		t.Fatalf("Lookup(stryker) = %v, %v", r, ok)
	}
	if got := Tools(); !reflect.DeepEqual(got, []string{"stryker"}) {
		t.Fatalf("Tools() = %v, want [stryker]", got)
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("Lookup(nope) found a reader")
	}
}

type fakeReader struct{ name string }

func (f fakeReader) Tool() string                       { return f.name }
func (fakeReader) Paths(Report) ([]string, error)       { return nil, nil }
func (fakeReader) Label(Report, string) (string, error) { return "", nil }
func (fakeReader) Read(Report, map[string][]byte, func(string) int, int64) (map[string]FileMutants, error) {
	return nil, nil
}

func TestRegisterAddsAndRemovesAReader(t *testing.T) {
	unregister := Register(fakeReader{"zz-fake"})
	if _, ok := Lookup("zz-fake"); !ok {
		t.Fatal("registered reader not found")
	}
	if got := Tools(); !reflect.DeepEqual(got, []string{"stryker", "zz-fake"}) {
		t.Fatalf("Tools() = %v, want sorted keys", got)
	}
	unregister()
	if _, ok := Lookup("zz-fake"); ok {
		t.Fatal("reader still registered after unregister")
	}
}

func TestRegisterPanicsOnADuplicateNamingIt(t *testing.T) {
	defer func() {
		v := recover()
		if v == nil || !strings.Contains(fmt.Sprint(v), "stryker") {
			t.Fatalf("recover = %v, want a panic naming stryker", v)
		}
	}()
	Register(fakeReader{"stryker"})
}

func TestStrykerLabelIsTheReportsVersion(t *testing.T) {
	report, _, _ := loadREA(t)
	r, _ := Lookup("stryker")
	for declared, wantErr := range map[string]bool{"": false, "10.0.0": false, "9.0.0": true} {
		got, err := r.Label(Report{Bytes: report}, declared)
		if wantErr {
			if err == nil || !strings.Contains(err.Error(), "9.0.0") || !strings.Contains(err.Error(), "10.0.0") {
				t.Errorf("declared %q: err = %v, want one naming both versions", declared, err)
			}
			continue
		}
		if err != nil || got != "stryker@10.0.0" {
			t.Errorf("declared %q: Label = %q, %v", declared, got, err)
		}
	}
}

// TestFullReportRecordSize measures what the record of every reported mutant
// weighs on the full REA report, raw and gzipped (the ledger stores entries
// gzipped). cmd/corral's maxReportMutantsBytes was derived from this
// measurement; rerun it when the record's shape changes.
func TestFullReportRecordSize(t *testing.T) {
	report := fullReport(t)
	var r struct {
		Files map[string]struct {
			Source string `json:"source"`
		} `json:"files"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for p, f := range r.Files {
		files[p] = []byte(f.Source)
	}
	rd, ok := Lookup("stryker")
	if !ok {
		t.Fatal("no stryker reader registered")
	}
	got, err := rd.Read(Report{Bytes: report}, files, budgetOf(40), 20261101)
	if err != nil {
		t.Fatal(err)
	}
	for p, fm := range got {
		raw, err := json.Marshal(ledger.ToolMutantCounts{Path: fm.Path, Tool: fm.Tool, Match: fm.Match, Units: fm.Units, Mutants: fm.Reported, Tests: fm.Tests})
		if err != nil {
			t.Fatal(err)
		}
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		mutants, err := json.Marshal(fm.Reported)
		if err != nil {
			t.Fatal(err)
		}
		counted := append([]ledger.ReportMutant(nil), fm.Reported...)
		for i := range counted {
			counted[i].CoveredBy = nil
		}
		countsOnly, err := json.Marshal(counted)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d mutants, %d tests named; ToolMutantCounts %d bytes raw, %d gzipped; Mutants alone %d raw, %d with covered_by as counts", p, len(fm.Reported), len(fm.Tests), len(raw), gz.Len(), len(mutants), len(countsOnly))
	}
}
