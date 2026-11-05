// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/mutantsources"
)

// fakeReader is a test-only mutantsources.Reader covering a.ts and taking
// `take` mutants from it, with ids carrying the tool's "t/<tool>/" prefix as
// a real reader's do.
type fakeReader struct {
	tool string
	take int
	// idsOf overrides the ids, to force a collision.
	idsOf func(i int) string
}

func (f fakeReader) Tool() string                                       { return f.tool }
func (f fakeReader) Paths(mutantsources.Report) ([]string, error)       { return []string{"a.ts"}, nil }
func (f fakeReader) Label(mutantsources.Report, string) (string, error) { return f.tool + "@1", nil }
func (f fakeReader) Read(_ mutantsources.Report, files map[string][]byte, _ func(string) int, seed int64) (map[string]mutantsources.FileMutants, error) {
	fm := mutantsources.FileMutants{Path: "a.ts", Tool: f.tool + "@1", Seed: seed,
		Sources: map[string]mutantsources.MutantSource{}, Counts: map[string]int{"Survived": f.take}}
	for i := 0; i < f.take; i++ {
		id := fmt.Sprintf("t/%s/%d", f.tool, i)
		if f.idsOf != nil {
			id = f.idsOf(i)
		}
		fm.Taken = append(fm.Taken, adequacy.Mutant{ID: id})
		fm.Sources[id] = mutantsources.MutantSource{Kind: mutantsources.KindTool, Tool: f.tool + "@1"}
	}
	return map[string]mutantsources.FileMutants{"a.ts": fm}, nil
}

// registerFakes registers readers for the test and returns --mutants-from
// values naming each, in the order given.
func registerFakes(t *testing.T, rs ...fakeReader) []string {
	t.Helper()
	report := writeTemp(t, "{}")
	var vals []string
	for _, r := range rs {
		t.Cleanup(mutantsources.Register(r))
		vals = append(vals, r.tool+":"+report)
	}
	return vals
}

func resolveFakes(t *testing.T, model string, vals []string) toolSources {
	t.Helper()
	ts, err := resolveMutantsFrom(mutantsFromFlags{values: vals, seed: "1", mutantModel: model}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestTwoToolsOneFileOneExam(t *testing.T) {
	vals := registerFakes(t, fakeReader{tool: "zeta", take: 2}, fakeReader{tool: "alpha", take: 2})
	ts := resolveFakes(t, "m", vals)
	if len(ts) != 2 || ts[0].tool != "alpha" || ts[1].tool != "zeta" {
		t.Fatalf("sources not sorted by tool: %v", ts.names())
	}
	files, err := ts.forFile("a.ts", []byte("x"), 5)
	if err != nil {
		t.Fatal(err)
	}
	var rs advpool.RunSpec
	if err := applyAll(&rs, files); err != nil {
		t.Fatal(err)
	}
	if len(rs.ToolMutants) != 4 || rs.PresetMutants != nil {
		t.Fatalf("ToolMutants %d PresetMutants %v; want 4 and nil", len(rs.ToolMutants), rs.PresetMutants)
	}
	tools := map[string]int{}
	for _, m := range rs.ToolMutants {
		tools[rs.MutantSources[m.ID].Tool]++
	}
	if len(rs.MutantSources) != 4 || tools["alpha@1"] != 2 || tools["zeta@1"] != 2 {
		t.Errorf("MutantSources %v; want 2 each from alpha@1 and zeta@1", rs.MutantSources)
	}
	// Tool-only puts the same four on the replay door.
	only := resolveFakes(t, "off", vals)
	files, _ = only.forFile("a.ts", []byte("x"), 5)
	rs = advpool.RunSpec{}
	if err := applyAll(&rs, files); err != nil || len(rs.PresetMutants) != 4 || rs.ToolMutants != nil {
		t.Errorf("tool-only: %d preset, err %v", len(rs.PresetMutants), err)
	}
}

func TestSingleToolCacheKeyUnchanged(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	ts := resolveFakes(t, "m", []string{"stryker:" + report})
	s := ts[0]
	want := "stryker/" + s.sha256 + "/1" // today's literal format
	if s.cacheKey() != want || ts.cacheKey() != want {
		t.Errorf("single-tool key = %q (source %q), want %q", ts.cacheKey(), s.cacheKey(), want)
	}
	if toolSources(nil).cacheKey() != "" {
		t.Error("no sources must key as the empty string")
	}
}

func TestTwoToolCacheKeyIsOrderFree(t *testing.T) {
	vals := registerFakes(t, fakeReader{tool: "a1"}, fakeReader{tool: "b1"})
	ab := resolveFakes(t, "m", vals)
	ba := resolveFakes(t, "m", []string{vals[1], vals[0]})
	if ab.cacheKey() != ba.cacheKey() || !strings.Contains(ab.cacheKey(), "+") {
		t.Errorf("keys %q vs %q: want equal and joined by +", ab.cacheKey(), ba.cacheKey())
	}
	if ab.cacheKey() == ab[0].cacheKey() {
		t.Error("two tools must not key as one")
	}
}

func TestSameToolTwiceStillRefused(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	_, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report, "stryker:" + report}, mutantModel: "m"}, io.Discard)
	const want = "--mutants-from names stryker twice: phase 1 reads one report per tool, given once per tool"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestToolOnlyFileGradesWhenAnySourceGivesSomething(t *testing.T) {
	vals := registerFakes(t, fakeReader{tool: "alpha", take: 0}, fakeReader{tool: "zeta", take: 2})
	ts := resolveFakes(t, "off", vals)
	files, err := ts.forFile("a.ts", []byte("x"), 5)
	if err != nil {
		t.Fatal(err)
	}
	var rs advpool.RunSpec
	if err := applyAll(&rs, files); err != nil || len(rs.PresetMutants) != 2 {
		t.Fatalf("one source with mutants must grade: err %v, %d mutants", err, len(rs.PresetMutants))
	}
	if none, _ := nothingToGrade(files); none != "" {
		t.Errorf("nothingToGrade = %q, want empty", none)
	}
	// Every source empty: COULD-NOT-GRADE.
	t.Cleanup(func() {})
	empty := resolveFakes(t, "off", vals[:1])
	files, _ = empty.forFile("a.ts", []byte("x"), 5)
	if err := applyAll(&advpool.RunSpec{}, files); err == nil || !strings.Contains(err.Error(), "COULD-NOT-GRADE") {
		t.Errorf("all sources empty: err %v, want COULD-NOT-GRADE", err)
	}
}

func TestEachSourceGetsItsOwnScanLine(t *testing.T) {
	vals := registerFakes(t, fakeReader{tool: "alpha", take: 2}, fakeReader{tool: "zeta", take: 3})
	files, err := resolveFakes(t, "m", vals).forFile("a.ts", []byte("x"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("%d files, want one per source", len(files))
	}
	if !strings.Contains(files[0].line, "(alpha@1)") || !strings.Contains(files[1].line, "(zeta@1)") {
		t.Errorf("lines: %q / %q", files[0].line, files[1].line)
	}
	if strings.Contains(files[0].line, "an exam of up to") {
		t.Errorf("the exam sentence belongs after the LAST line: %q", files[0].line)
	}
	if want := "5 model + 2 tool + 3 tool, an exam of up to 10"; !strings.Contains(files[1].line, want) {
		t.Errorf("last line %q does not say %q", files[1].line, want)
	}
}

func TestSameMutantIDFromTwoToolsIsRefused(t *testing.T) {
	same := func(int) string { return "t/shared/0" }
	vals := registerFakes(t, fakeReader{tool: "alpha", take: 1, idsOf: same}, fakeReader{tool: "zeta", take: 1, idsOf: same})
	files, _ := resolveFakes(t, "m", vals).forFile("a.ts", []byte("x"), 5)
	err := applyAll(&advpool.RunSpec{}, files)
	if err == nil || !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "zeta") {
		t.Errorf("err = %v, want one naming both tools", err)
	}
}
