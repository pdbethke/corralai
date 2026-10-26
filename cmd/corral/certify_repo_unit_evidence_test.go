// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// fakeUnitPass stands in for the unit pass (collectUnitEvidence) so a test
// of what certify --repo does WITH unit evidence needs no runner, no
// coverage instrument and no jail. It records every call, and refuses —
// as the real pass does — a worker count below one, so a caller that forgot
// to pass the run's own setting is caught here rather than in production.
type fakeUnitPass struct {
	mu    sync.Mutex
	ev    reposcan.UnitEvidence
	calls []fakeUnitCall
}

type fakeUnitCall struct {
	lang       string
	argv       []string
	modulePath string
	workers    int
}

func (f *fakeUnitPass) install(t *testing.T) {
	t.Helper()
	prev := collectUnitEvidence
	collectUnitEvidence = func(_ context.Context, _ coverageRunner, _ map[string]string, p lang.Plugin, inv lang.Invocation, modulePath string, workers int, announce func(runnable, listed int)) reposcan.UnitEvidence {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, fakeUnitCall{lang: p.Name(), argv: inv.Argv, modulePath: modulePath, workers: workers})
		if workers < 1 {
			return reposcan.UnitEvidence{Note: "fake: no workers"}
		}
		if f.ev.Ran {
			announce(len(f.ev.Units), len(f.ev.Units))
		}
		return f.ev
	}
	t.Cleanup(func() { collectUnitEvidence = prev })
}

func (f *fakeUnitPass) called() []fakeUnitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeUnitCall(nil), f.calls...)
}

func unitRun(unit string, counts map[string]lang.LineCounts) reposcan.UnitResult {
	return reposcan.UnitResult{Unit: unit, Counts: counts}
}

// unitEvidenceTSFixture is a TypeScript tree whose only test sits where no
// filename convention or search pairs it with the source: a composition test
// for src/app/Service.ts under tests/composition/, naming nothing it runs.
func unitEvidenceTSFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "app", "Service.ts"), "export function serve(): number { return 1; }\n")
	mustWrite(t, filepath.Join(root, "tests", "composition", "service.test.ts"), "test('composition', () => {});\n")
	return root
}

func runUnitEvidenceScan(t *testing.T, root string, extra ...string) string {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	args := []string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--goals", writeGoals(t, root, `{}`), "--substrate", substrateWorkspace, "--no-selection-cache",
	}
	var out, errb bytes.Buffer
	runCertifyRepo(append(args, extra...), &out, &errb)
	return out.String() + "\n[stderr]\n" + errb.String()
}

// A file only a test unit's execution pairs becomes a candidate, landing
// beside the unit that ran it, and the summary counts it as evidence-paired.
// The covering count printed is the number of units (UnitsFor), and the
// candidate carries no CoveringTests (ruling R2: units are not tests).
func TestUnitEvidencePromotesAnUnpairedTSFile(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--", "vitest", "run")

	calls := fake.called()
	if len(calls) != 1 {
		t.Fatalf("unit pass called %d time(s), want once:\n%s", len(calls), s)
	}
	if c := calls[0]; c.lang != "typescript" || c.workers != 1 || c.modulePath != root || !reflect.DeepEqual(c.argv, []string{"vitest", "run"}) {
		t.Errorf("unit pass called with %+v; want typescript, the operator's argv, the repo root as module path (runPreflight's), and the workspace substrate's one worker", c)
	}
	if !strings.Contains(s, "src/app/Service.ts paired by evidence: 1 covering test unit(s), authored test lands beside tests/composition/service.test.ts") {
		t.Errorf("missing the evidence-paired disclosure for the unit-paired file:\n%s", s)
	}
	if !strings.Contains(s, "evidence-paired 1 · name-paired 0") {
		t.Errorf("the summary must count the unit-paired file as evidence-paired:\n%s", s)
	}
	if strings.Contains(s, "pairing-only candidacy") {
		t.Errorf("unit evidence ran, so candidacy is not pairing-only:\n%s", s)
	}
	if !strings.Contains(s, "units: 1 run, 0 failed") {
		t.Errorf("missing the units scan line:\n%s", s)
	}
}

// A file name pairing already paired keeps its own test, byte-identical,
// even when unit evidence says another unit executes most of it: evidence
// only ADDS candidates (Review Focus 4).
func TestUnitEvidenceNeverMovesANamePairedCandidate(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "a.ts"), "export function a(): number { return 1; }\n")
	mustWrite(t, filepath.Join(root, "test", "a.test.ts"), "import { a } from '../src/a';\ntest('a', () => { expect(a()).toBe(1); });\n")
	mustWrite(t, filepath.Join(root, "tests", "other.test.ts"), "test('other', () => {});\n")
	ev := reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("test/a.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 10}}),
		unitRun("tests/other.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 9, Executable: 10}}),
	}}

	without := &fakeUnitPass{ev: reposcan.UnitEvidence{Note: "fake: no unit evidence"}}
	without.install(t)
	before := runUnitEvidenceScan(t, root, "--", "vitest", "run")
	with := &fakeUnitPass{ev: ev}
	with.install(t)
	after := runUnitEvidenceScan(t, root, "--", "vitest", "run")

	if !strings.Contains(before, "name-paired 1") {
		t.Fatalf("fixture: src/a.ts must be name-paired to test/a.test.ts:\n%s", before)
	}
	if diff := diffLines(unitCandidacyLines(before), unitCandidacyLines(after)); diff != "" {
		t.Fatalf("src/a.ts's candidacy moved when unit evidence arrived:\n%s\nwithout:\n%s\nwith:\n%s", diff, before, after)
	}
	if strings.Contains(after, "paired by evidence") {
		t.Errorf("a name-paired candidate is never disclosed as evidence-paired:\n%s", after)
	}

	// The job the scan would grade, built from the same candidates: one
	// Enumerate, widened by the same unit index, emits the job pairing
	// alone emits — TestPath, CoveringTestPath, CoveringTests and the cache
	// key all equal. The one field unit evidence adds is CoveringUnits
	// (ruling R3), ledger metadata in its own column: both units executed
	// src/a.ts, so 2.
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := lang.ByName("typescript")
	landing := map[string]string{"test/a.test.ts": "", "tests/other.test.ts": ""}
	idx, ok := reposcan.IndexFromUnits(ev, reposcan.UnitFilesFor(ts, landing))
	if !ok {
		t.Fatal("fixture: no unit index")
	}
	plainJobs := emitTestJobs(t, cands, root)
	widened, _, _ := reposcan.WidenCandidacyByEvidence(append([]reposcan.Candidate(nil), cands...), excl, idx, ok)
	got := emitTestJobs(t, widened, root)
	if len(got) != 1 || got[0].CoveringUnits == nil || *got[0].CoveringUnits != 2 {
		t.Fatalf("widened job's CoveringUnits = %+v, want 2", got)
	}
	got[0].CoveringUnits = nil
	if !reflect.DeepEqual(got, plainJobs) {
		t.Errorf("jobs moved:\n got %+v\nwant %+v", got, plainJobs)
	}
}

func emitTestJobs(t *testing.T, cands []reposcan.Candidate, root string) []reposcan.Job {
	t.Helper()
	gs, err := reposcan.NewFileGoalSource(writeGoals(t, root, `{"src/a.ts": "a() must return 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	jobs, _, err := reposcan.EmitJobs(reposcan.EmitConfig{Root: root}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

// unitCandidacyLines is candidacyLines for this file's fixtures: the count
// and summary lines (their trailing basis disclosure cut, which legitimately
// differs between a run with evidence and one without) and every line that
// names src/a.ts.
func unitCandidacyLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.Contains(line, "candidate(s)"), strings.Contains(line, "src/a.ts"):
			out = append(out, line)
		case strings.Contains(line, "evidence-paired"):
			if i := strings.Index(line, " — "); i >= 0 {
				line = line[:i]
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// The scan's audit config — and so every verdict's cache key — says how the
// scan GRADES, and the unit pass changes no grading: the method stays what
// selection evidence alone decides.
func TestUnitEvidenceLeavesAuditConfigKeyAlone(t *testing.T) {
	without := &localExecutor{}
	with := &localExecutor{units: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("test/a.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 1}}),
	}}}
	if got := with.selectionMethod(); got != "" {
		t.Fatalf("selectionMethod with unit evidence = %q, want \"\"", got)
	}
	argv := []string{"vitest", "run"}
	if a, b := auditConfigKey(false, without.selectionMethod(), argv, "", ""), auditConfigKey(false, with.selectionMethod(), argv, "", ""); a != b {
		t.Errorf("auditConfigKey moved with unit evidence:\n without %s\n    with %s", a, b)
	}
	sel := &localExecutor{selection: reposcan.SelectionEvidence{Ran: true}}
	if got := sel.selectionMethod(); got != "coverage-context" {
		t.Errorf("negative control: selection evidence must still set the method, got %q", got)
	}
}

// A command with no one-unit form (`npm test` with no script corral can
// read) leaves the language name-paired for the run, and the scan says why
// on the units line and on the summary line — never a silent zero (Review
// Focus 2). The REAL pass runs here: it refuses before running anything.
func TestUnitPassDisclosesACommandWithNoUnitForm(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	s := runUnitEvidenceScan(t, root, "--", "npm", "test")

	const note = "typescript: `npm test` has no one-unit form corral can list and run, so no unit evidence"
	if !strings.Contains(s, "units: "+note) {
		t.Errorf("missing the units line with the pass's own note:\n%s", s)
	}
	if !strings.Contains(s, "pairing-only candidacy (no selection evidence: "+note+")") {
		t.Errorf("the summary must say candidacy is pairing-only, and why:\n%s", s)
	}
	if !strings.Contains(s, "evidence-paired 0") || strings.Contains(s, "paired by evidence") {
		t.Errorf("nothing may be promoted without unit evidence:\n%s", s)
	}
}

// --whole-suite is a GRADING choice; the unit pass is candidacy, so it still
// runs and still promotes.
func TestWholeSuiteStillRunsTheUnitPass(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--whole-suite", "--", "vitest", "run")

	if len(fake.called()) != 1 {
		t.Fatalf("--whole-suite must not disable the unit pass:\n%s", s)
	}
	if !strings.Contains(s, "src/app/Service.ts paired by evidence") {
		t.Errorf("the unit-paired file must still be promoted under --whole-suite:\n%s", s)
	}
}

// Python grades through its own per-test selector, and that path is
// unchanged: the unit pass never runs for it, with or without --whole-suite.
func TestPythonDoesNotRunTheUnitPass(t *testing.T) {
	root := t.TempDir()
	preflightUnpairedPythonFixture(t, root)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Note: "fake"}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--whole-suite")

	if calls := fake.called(); len(calls) != 0 {
		t.Fatalf("the unit pass ran for Python: %+v\n%s", calls, s)
	}
	if strings.Contains(s, "units:") {
		t.Errorf("no units line for a language with a per-test selector:\n%s", s)
	}
}

// A unit that failed contributes nothing and is listed once, by name and
// reason; a partial pass says so on the summary line.
func TestUnitPassListsEachFailedUnit(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	mustWrite(t, filepath.Join(root, "tests", "broken.test.ts"), "test('broken', () => {});\n")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
		{Unit: "tests/broken.test.ts", Err: "coverage unavailable: llvm-tools missing"},
	}}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--", "vitest", "run")

	if !strings.Contains(s, "units: 2 run, 1 failed") {
		t.Errorf("missing the units count line:\n%s", s)
	}
	if strings.Count(s, "tests/broken.test.ts: coverage unavailable: llvm-tools missing") != 1 {
		t.Errorf("the failed unit must be listed exactly once with its reason:\n%s", s)
	}
	if !strings.Contains(s, "unit evidence is partial: 1 of 2 unit(s) contributed none") {
		t.Errorf("the summary line must say the evidence is partial:\n%s", s)
	}
}

// The workspace substrate passes the runner no files (it runs in the
// checkout itself), so the landing map is built from the checkout: every
// enumerated path, and the text only of what UnitTestFile reads.
func TestWorkspaceUnitLandingReadsTheCheckout(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc A() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n")
	mustWrite(t, filepath.Join(root, "README.md"), "hello\n")
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := workspaceLandingFiles(root, checkoutInventory(cands, excl))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"go.mod", "pkg/a.go", "pkg/a_test.go", "README.md"} {
		if _, ok := files[p]; !ok {
			t.Errorf("landing map lacks %s: %v", p, files)
		}
	}
	if files["README.md"] != "" {
		t.Errorf("README.md's text was read; only what landing reads is: %q", files["README.md"])
	}
	gp, _ := lang.ByName("go")
	if got := reposcan.UnitTestFile(gp, files)("example.com/m/pkg", "pkg/a.go"); got != "pkg/a_test.go" {
		t.Errorf("a Go unit lands on %q from the workspace map, want pkg/a_test.go", got)
	}
}

// fakeSelectionCache is an in-memory selectionCacheStore keyed on all four
// key columns.
type fakeSelectionCache struct {
	rows map[[4]string][]byte
	gets [][4]string
}

func (c *fakeSelectionCache) SelectionCacheGet(_ context.Context, tree, cmd, plugin, substrate string) ([]byte, bool, error) {
	k := [4]string{tree, cmd, plugin, substrate}
	c.gets = append(c.gets, k)
	raw, ok := c.rows[k]
	return raw, ok, nil
}

func (c *fakeSelectionCache) SelectionCachePut(_ context.Context, tree, cmd, plugin, substrate string, raw []byte, _ string) error {
	c.rows[[4]string{tree, cmd, plugin, substrate}] = raw
	return nil
}

// The unit pass is cached under its own namespaced plugin key, only when it
// is settled, and a hit is used exactly as a fresh pass — the seam is not
// called, and the evidence and landing map are the same.
func TestUnitPassIsCachedUnderItsOwnKey(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	ev := reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}
	cache := &fakeSelectionCache{rows: map[[4]string][]byte{}}
	newEx := func() *localExecutor {
		cands, excl, err := reposcan.Enumerate(root)
		if err != nil {
			t.Fatal(err)
		}
		return &localExecutor{repoDir: root, substrate: substrateWorkspace, checkArgv: []string{"vitest", "run"}, progress: io.Discard,
			selectionCache: cache, treeDigest: "tree", treeDigestComputed: true, unitWorkers: 1,
			unitInventory: checkoutInventory(cands, excl)}
	}
	sources := []string{"src/app/Service.ts"}

	fresh := &fakeUnitPass{ev: ev}
	fresh.install(t)
	first := newEx()
	got := first.collectUnits(context.Background(), sources)
	if len(fresh.called()) != 1 || !reflect.DeepEqual(got, ev) || first.unitsReused {
		t.Fatalf("a miss must run the pass once and return its evidence: calls=%d got=%+v reused=%v", len(fresh.called()), got, first.unitsReused)
	}
	p := first.pendingUnitsPut
	if p == nil || p.Plugin != "typescript/units" || p.TreeDigest != "tree" || p.Substrate != substrateWorkspace {
		t.Fatalf("pendingUnitsPut = %+v, want a typescript/units row for this tree and substrate", p)
	}
	_ = cache.SelectionCachePut(context.Background(), p.TreeDigest, p.CmdDigest, p.Plugin, p.Substrate, p.Raw, "")

	cached := &fakeUnitPass{ev: reposcan.UnitEvidence{Note: "must not run"}}
	cached.install(t)
	second := newEx()
	hit := second.collectUnits(context.Background(), sources)
	if len(cached.called()) != 0 || !second.unitsReused {
		t.Fatalf("a hit must not run the pass: calls=%d reused=%v", len(cached.called()), second.unitsReused)
	}
	if !reflect.DeepEqual(hit, ev) || !reflect.DeepEqual(second.unitLanding, first.unitLanding) || second.pendingUnitsPut != nil {
		t.Errorf("a hit must be used as the fresh pass was:\n hit %+v landing %v put %+v\nfresh %+v landing %v", hit, second.unitLanding, second.pendingUnitsPut, ev, first.unitLanding)
	}

	// An unsettled pass (a unit stopped by the time limit) is evidence for
	// this scan and is never kept.
	partial := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{{Unit: "tests/composition/service.test.ts", Err: "not run (time limit)"}}}}
	partial.install(t)
	third := newEx()
	third.selectionCache = &fakeSelectionCache{rows: map[[4]string][]byte{}}
	third.collectUnits(context.Background(), sources)
	if third.pendingUnitsPut != nil {
		t.Errorf("an unsettled pass was queued for the cache: %+v", third.pendingUnitsPut)
	}
	// --no-selection-cache: no cache, no lookup, no put.
	none := newEx()
	none.selectionCache = nil
	fresh.install(t)
	none.collectUnits(context.Background(), sources)
	if none.pendingUnitsPut != nil {
		t.Errorf("no cache wired, yet a put was queued: %+v", none.pendingUnitsPut)
	}
}

// A pass that ran and had every unit fail widens nothing, and the summary
// says that is why candidacy is pairing-only — not the selection pass's
// "no selector" note, which is about grading.
func TestUnitPassWithNoGoodUnitSaysSo(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		{Unit: "tests/composition/service.test.ts", Err: "run failed: exit 1"},
	}}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--", "vitest", "run")

	if !strings.Contains(s, "pairing-only candidacy (no selection evidence: no unit of 1 contributed evidence)") {
		t.Errorf("the summary must name the unit pass as the reason:\n%s", s)
	}
	if !strings.Contains(s, "tests/composition/service.test.ts: run failed: exit 1") {
		t.Errorf("the failed unit must be listed:\n%s", s)
	}
}

// Ruling R15: only a pass in which every unit succeeded is cached. A pass
// with an unavailable instrument, a red unit, or nothing but failures is
// evidence for its own scan, never Put, and the next scan of the same tree
// runs the collector again — so an operator who fixes the environment is not
// served the old failure.
func TestUnitPassCachesOnlyAFullyGreenPass(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	green := unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}})
	sources := []string{"src/app/Service.ts"}
	newEx := func(cache selectionCacheStore) *localExecutor {
		cands, excl, err := reposcan.Enumerate(root)
		if err != nil {
			t.Fatal(err)
		}
		return &localExecutor{repoDir: root, substrate: substrateWorkspace, checkArgv: []string{"vitest", "run"}, progress: io.Discard,
			selectionCache: cache, treeDigest: "tree", treeDigestComputed: true, unitWorkers: 1,
			unitInventory: checkoutInventory(cands, excl)}
	}
	// scan is one scan's collect plus the recording sequence's Put.
	scan := func(cache *fakeSelectionCache) *localExecutor {
		ex := newEx(cache)
		ex.units = ex.collectUnits(context.Background(), sources)
		if p := ex.pendingUnitsPut; p != nil {
			_ = cache.SelectionCachePut(context.Background(), p.TreeDigest, p.CmdDigest, p.Plugin, p.Substrate, p.Raw, "")
		}
		return ex
	}
	failing := map[string]reposcan.UnitEvidence{
		"one unavailable unit": {Ran: true, Units: []reposcan.UnitResult{green, {Unit: "tests/b.test.ts", Err: "coverage unavailable: llvm-tools missing"}}},
		"one red unit":         {Ran: true, Units: []reposcan.UnitResult{green, {Unit: "tests/b.test.ts", Err: "run failed: exit 1"}}},
		"all failed": {Ran: true, Units: []reposcan.UnitResult{
			{Unit: "tests/composition/service.test.ts", Err: "run failed: exit 1"},
			{Unit: "tests/b.test.ts", Err: "run failed: exit 1"},
		}},
	}
	for name, ev := range failing {
		t.Run(name, func(t *testing.T) {
			cache := &fakeSelectionCache{rows: map[[4]string][]byte{}}
			fake := &fakeUnitPass{ev: ev}
			fake.install(t)
			first := scan(cache)
			if first.pendingUnitsPut != nil || len(cache.rows) != 0 {
				t.Fatalf("a pass with a failed unit was cached: put=%+v rows=%d", first.pendingUnitsPut, len(cache.rows))
			}
			if !reflect.DeepEqual(first.units, ev) {
				t.Errorf("the scan that made it must still use it: %+v", first.units)
			}
			second := scan(cache)
			if n := len(fake.called()); n != 2 || second.unitsReused {
				t.Errorf("the next scan must run the collector again: calls=%d reused=%v", n, second.unitsReused)
			}
		})
	}
	t.Run("fully green", func(t *testing.T) {
		ev := reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{green}}
		cache := &fakeSelectionCache{rows: map[[4]string][]byte{}}
		fake := &fakeUnitPass{ev: ev}
		fake.install(t)
		first := scan(cache)
		if len(cache.rows) != 1 {
			t.Fatalf("a fully green pass must be cached, rows=%d", len(cache.rows))
		}
		second := scan(cache)
		if n := len(fake.called()); n != 1 || !second.unitsReused {
			t.Fatalf("the second scan must be served from the cache: calls=%d reused=%v", n, second.unitsReused)
		}
		if !reflect.DeepEqual(second.units, first.units) || !reflect.DeepEqual(second.units, ev) {
			t.Errorf("the reused pass must be byte-identical:\n got %+v\nwant %+v", second.units, ev)
		}
	})
}

// A crate whose every source is refused by the landing (inline tests only,
// semver's shape, Task 11a) leaves the evidence passes no source to run for.
// They do not run, and the summary says why: before this the scan printed
// "pairing-only candidacy (no selection evidence)" with no reason at all and
// no units line, a zero nobody explained.
func TestEvidencePassThatCannotRunSaysWhy(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"inl\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	mustWrite(t, filepath.Join(root, "src", "lib.rs"), "pub fn one() -> u8 { 1 }\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() { assert_eq!(super::one(), 1); }\n}\n")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Note: "fake"}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--", "cargo", "test")

	if calls := fake.called(); len(calls) != 0 {
		t.Fatalf("no source can be audited, so no unit pass may run: %+v\n%s", calls, s)
	}
	if strings.Contains(s, "(no selection evidence)") {
		t.Errorf("the summary must say why no evidence pass ran:\n%s", s)
	}
	if !strings.Contains(s, "no selection evidence: no source file can be audited yet (1 rust source(s) test landing not built), so no evidence pass ran") {
		t.Errorf("missing the reason on the summary line:\n%s", s)
	}
}

// A unit pass's negative findings reach the scan line as what they are
// (ruling R24): a file a unit loaded with nothing of it run counts as
// import-only, and one no unit loaded counts as uncovered under the reason
// scoped to the command's units.
func TestUnitPassNegativeFindingsAreScopedAndCounted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loaded []string
		want   []string
	}{
		{"loaded", []string{"src/app/Service.ts"}, []string{"uncovered 0 · import-only 1", "src/app/Service.ts (" + reposcan.ReasonImportOnly + ")"}},
		{"not loaded", nil, []string{"uncovered 1 · import-only 0", "src/app/Service.ts (" + reposcan.ReasonUncoveredByUnits + ")"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := unitEvidenceTSFixture(t)
			fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
				{Unit: "tests/composition/service.test.ts", Counts: map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 0, Executable: 3}}, Loaded: tc.loaded},
			}}}
			fake.install(t)
			s := runUnitEvidenceScan(t, root, "--", "vitest", "run")
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("missing %q:\n%s", w, s)
				}
			}
			if strings.Contains(s, reposcan.ReasonUncovered+")") {
				t.Errorf("a unit pass must never make the whole-suite claim %q:\n%s", reposcan.ReasonUncovered, s)
			}
		})
	}
}

// EVIDENCE DOES NOT DEPEND ON HOW NARROW --path IS (B5). --path bounds which
// files are audited, never where their tests may live: the unit
// tests/composition/service.test.ts is outside every scope below and is the
// landing of src/app/Service.ts under each. On REA, --path
// src/application/reverse ran 25 green units and paired nothing, while
// --path src paired ReverseEngineeringService.ts: the workspace landing map
// was built from the SCOPED inventory, so a test outside the scope did not
// exist to UnitTestFile.
func TestUnitEvidenceIsTheSameUnderAPathAndItsParent(t *testing.T) {
	for _, scope := range []string{"", "src", "src/app"} {
		t.Run("path="+scope, func(t *testing.T) {
			root := unitEvidenceTSFixture(t)
			fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
				unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
			}}}
			fake.install(t)
			args := []string{"--", "vitest", "run"}
			if scope != "" {
				args = append([]string{"--path", scope}, args...)
			}
			s := runUnitEvidenceScan(t, root, args...)
			if !strings.Contains(s, "src/app/Service.ts paired by evidence: 1 covering test unit(s), authored test lands beside tests/composition/service.test.ts") {
				t.Errorf("--path %q: src/app/Service.ts must be paired by evidence as it is with no --path:\n%s", scope, s)
			}
		})
	}
}

// recordingDeriver counts the files it was asked about and answers NONE, so
// the scan stops at goal derivation without any grading seat running.
type recordingDeriver struct {
	mu    sync.Mutex
	asked []string
}

func (d *recordingDeriver) Derive(_ context.Context, c reposcan.Candidate, _ string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, c.Path)
	return "", false, nil
}

// A FILE ONLY EVIDENCE PAIRED STILL GETS ITS GOAL DERIVED (B6). The goal
// source was resolved before the evidence pass, from the name-paired
// selection; on a scan where name pairing found nothing (REA with --path
// src/application/reverse) that was the no-goals source, so the promoted
// file was excluded "ungoaled — purely declarative" with no model ever
// asked (ModelCalls=0), and the paid audit ended COULD-NOT-GRADE.
func TestAnEvidenceOnlyCandidateIsAskedForItsGoal(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)
	d := &recordingDeriver{}
	prev := certifyRepoDeriver
	t.Cleanup(func() { certifyRepoDeriver = prev })
	certifyRepoDeriver = func(string, string) (reposcan.Deriver, error) { return d, nil }
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")

	var out, errb bytes.Buffer
	runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--derive-model", testHerdWriter, "--no-goal-cache", "--no-ledger", "--substrate", substrateWorkspace, "--no-selection-cache",
		"--", "vitest", "run",
	}, &out, &errb)
	s := out.String() + "\n[stderr]\n" + errb.String()
	if !strings.Contains(s, "src/app/Service.ts paired by evidence") {
		t.Fatalf("fixture: the file must be paired by evidence:\n%s", s)
	}
	if !slices.Equal(d.asked, []string{"src/app/Service.ts"}) {
		t.Errorf("deriver asked about %q, want exactly the evidence-paired src/app/Service.ts:\n%s", d.asked, s)
	}
}

// A PR THAT CHANGES A NON-LANDING GO TEST FILE puts the evidence-only
// source its package unit ran in scope (final review I2). pkg/c.go has no
// c_test.go, so only unit evidence pairs it, landing on pkg/a_test.go. The
// PR weakens pkg/b_test.go, which the same `go test` unit compiled: before
// the index recorded every test file a unit compiled, CoveredByAny saw only
// the landing and the gate scoped c.go out — failing open on exactly the
// change the diff block exists to catch.
func TestDiffBaseWidensAGoSourceWhenAnotherTestFileOfItsPackageChanges(t *testing.T) {
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "go.mod"), "module m\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(root, "pkg", "c.go"), "package pkg\n\nfunc C() int { return 1 }\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "x_test.go"), "package pkg_test\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg // assertions removed\n")
	gitRun("add", "pkg/b_test.go")
	gitRun("commit", "-q", "-m", "weaken b_test.go", "--no-gpg-sign")

	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("m/pkg", map[string]lang.LineCounts{"pkg/c.go": {Executed: 1, Executable: 1}}),
	}}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--diff-base", base, "--dry-run", "--evidence", "--", "go", "test", "./...")
	if len(fake.called()) != 1 {
		t.Fatalf("unit pass called %d time(s), want once — a changed test file runs it:\n%s", len(fake.called()), s)
	}
	if !strings.Contains(s, "widened by evidence: +1 in scope — pkg/c.go") {
		t.Errorf("a PR that weakened pkg/b_test.go must put evidence-only pkg/c.go in scope:\n%s", s)
	}
}

// THE FAILED-UNIT LIST IS CAPPED as printEvidencePairings caps its list
// (maxListedExclusions, then "... and N more"), ruling R30 / Task 8 m1. On
// a large suite that hits the time limit every unit has a line, and the
// uncapped list was the 1.4 MB scan output shape again. The count line
// still says how many failed, and --dry-run --json lists every one.
func TestUnitPassCapsTheFailedUnitList(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	units := []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
	}
	failed := maxListedExclusions + 5
	for i := range failed {
		units = append(units, reposcan.UnitResult{Unit: fmt.Sprintf("tests/t%02d.test.ts", i), Err: "not run (time limit)"})
	}
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: units}}
	fake.install(t)

	s := runUnitEvidenceScan(t, root, "--", "vitest", "run")

	if !strings.Contains(s, fmt.Sprintf("units: %d run, %d failed", failed+1, failed)) {
		t.Errorf("missing the full count line:\n%s", s)
	}
	if got := strings.Count(s, ": not run (time limit)"); got != maxListedExclusions {
		t.Errorf("%d failed-unit lines printed, want %d:\n%s", got, maxListedExclusions, s)
	}
	if !strings.Contains(s, "    ... and 5 more unit(s) that failed or left files uncounted (every one is in --dry-run --json)") {
		t.Errorf("the cap must say how many lines it withheld:\n%s", s)
	}
}
