// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/mutantsources"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The record half of --mutants-from (spec §3 "The record"): every mutant row
// says where its mutant came from, and each file's account of the tool's
// report rides on the scan row, so a per-source statistic is a query over
// ledger entries and never a second store.

// bothSourcesResult is one file graded on a both-sources exam: a model
// mutant the dev suite killed, a model survivor the writer proved, and a tool
// survivor the writer proved. The generator seat is gen-model, and it must
// be credited with its own two mutants only.
func bothSourcesResult() reposcan.FileResult {
	return reposcan.FileResult{
		Job:      reposcan.Job{Path: "pkg/a.ts", Lang: "typescript"},
		Gradable: true,
		Verdict: advpool.Verdict{
			ModelsByRole: map[string]string{advpool.RoleMutantGenerator: "gen-model"},
			DevKilledMutants: []advpool.MutantRef{
				{ID: "m1", ParentSHA256: "p", Span: lang.LineRange{Start: 3, End: 3}},
			},
			DevSurvivedMutants: []advpool.MutantRef{
				{ID: "m2", ParentSHA256: "p", Span: lang.LineRange{Start: 5, End: 5}},
				{ID: "t/stryker/7", ParentSHA256: "p", Span: lang.LineRange{Start: 8, End: 8}},
			},
			ProvenMutantIDs: []string{"m2", "t/stryker/7"},
			MutantSources: map[string]mutantsources.MutantSource{
				"m1": {Kind: mutantsources.KindModel, Model: "gen-model"},
				"m2": {Kind: mutantsources.KindModel, Model: "gen-model"},
				"t/stryker/7": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0",
					Operator: "ConditionalExpression", Status: "Survived"},
			},
		},
	}
}

func intP(n int) *int { return &n }

// The per-file account of the tool's report, as the scan line printed it.
func sampleToolCounts() []ledger.ToolMutantCounts {
	seed := int64(7)
	return []ledger.ToolMutantCounts{{
		Path: "pkg/a.ts", Tool: "stryker@10.0.0",
		Taken: intP(1), Survived: intP(3), NoCoverage: intP(0), Killed: intP(12), CompileError: intP(0),
		Dropped: intP(2), DroppedBy: map[string]int{"over-budget": 1, "anchor-not-unique": 1},
		Seed: &seed,
	}}
}

// checkSourceRows asserts the three rows' sources, whatever grain they were
// read back at.
func checkSourceRows(t *testing.T, where string, rows []scanstore.Mutant) {
	t.Helper()
	byID := map[string]scanstore.Mutant{}
	for _, r := range rows {
		byID[r.MutantID] = r
	}
	if len(byID) != 3 {
		t.Fatalf("%s: %d mutant row(s), want 3: %+v", where, len(byID), rows)
	}
	for _, id := range []string{"m1", "m2"} {
		m := byID[id]
		if m.Source != mutantsources.KindModel || m.GeneratorModel != "gen-model" {
			t.Errorf("%s: model row %s: Source %q GeneratorModel %q, want model / gen-model", where, id, m.Source, m.GeneratorModel)
		}
		if m.SourceTool != "" || m.SourceOperator != "" || m.SourceStatus != "" {
			t.Errorf("%s: model row %s carries a tool's fields: %+v", where, id, m)
		}
	}
	tool := byID["t/stryker/7"]
	if tool.Source != mutantsources.KindTool || tool.SourceTool != "stryker@10.0.0" ||
		tool.SourceOperator != "ConditionalExpression" || tool.SourceStatus != "Survived" {
		t.Errorf("%s: tool row = %+v, want tool / stryker@10.0.0 / ConditionalExpression / Survived", where, tool)
	}
	// Ruling: a tool mutant is never credited to the generator seat, which
	// did not plant it.
	if tool.GeneratorModel != "" {
		t.Errorf("%s: tool row credits GeneratorModel %q; the generator did not plant it", where, tool.GeneratorModel)
	}
	if !tool.Proven || tool.Outcome != "survived" {
		t.Errorf("%s: tool row outcome %q proven %v, want survived and proven", where, tool.Outcome, tool.Proven)
	}
}

// TestToolMutantRowsNameTheirSource follows a both-sources file from the
// verdict to the ledger entry and back, through every converter on the way:
// the scan_mutants row, the bundle row, the entry written to a ledger
// directory, the `corral scans` reader, and the warehouse view a ledger
// directory loads as. A converter that forgets a field fails here.
func TestToolMutantRowsNameTheirSource(t *testing.T) {
	results := []reposcan.FileResult{bothSourcesResult()}
	rows := buildScanMutantRows(1, results)
	checkSourceRows(t, "scan rows", rows)

	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}, 1, nil, rows, nil, nil,
		auditpush.Link{}, true, "o/r", "deadbeef", "", bundleMeta{ToolMutants: toolMutantsJSON(sampleToolCounts())})
	for _, m := range b.Mutants {
		if m.MutantID == "t/stryker/7" && (m.Source != "tool" || m.SourceStatus != "Survived" || m.GeneratorModel != "") {
			t.Errorf("bundle row = %+v", m)
		}
	}
	if b.Scan.ToolMutants == "" {
		t.Fatal("the scan row carries no tool-mutant counts")
	}

	dir := filepath.Join(t.TempDir(), "ledger")
	if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
		t.Fatalf("writing the entry: %v", err)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLedgerDir: %d entries, %v", len(entries), err)
	}
	l := &ledgerScans{entries: entries}
	read, err := l.MutantsForScan(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	checkSourceRows(t, "ledger entry", read)

	var got []ledger.ToolMutantCounts
	if err := json.Unmarshal([]byte(entries[0].Bundle.Scan.ToolMutants), &got); err != nil {
		t.Fatalf("the entry's tool-mutant counts are not JSON: %v", err)
	}
	want, _ := json.Marshal(sampleToolCounts())
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(want) {
		t.Errorf("tool-mutant counts after the round trip:\n%s\nwant\n%s", gotJSON, want)
	}

	// The warehouse view of the same directory: the per-source query the
	// spec promises reads these columns.
	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_mutants WHERE source = 'tool' AND source_tool = 'stryker@10.0.0'
		AND source_operator = 'ConditionalExpression' AND source_status = 'Survived' AND generator_model IS NULL`).Scan(&n); err != nil {
		t.Fatalf("querying the view by source: %v", err)
	}
	if n != 1 {
		t.Errorf("the view holds %d tool row(s) with their source, want 1", n)
	}
	var tm string
	if err := db.QueryRow(`SELECT tool_mutants FROM corral_scans`).Scan(&tm); err != nil || tm != b.Scan.ToolMutants {
		t.Errorf("the view's tool_mutants = %q (%v), want the entry's %q", tm, err, b.Scan.ToolMutants)
	}
	wb, err := auditpush.ReadBundleForScan(db, auditpush.ScanRow{Repo: "o/r", ScanID: 1, ScanUID: entries[0].ScanUID})
	if err != nil {
		t.Fatal(err)
	}
	var whRows []scanstore.Mutant
	for _, m := range wb.Mutants {
		whRows = append(whRows, scanstore.Mutant{MutantID: m.MutantID, Outcome: m.Outcome, Proven: m.Proven,
			Source: m.Source, SourceTool: m.SourceTool, SourceOperator: m.SourceOperator, SourceStatus: m.SourceStatus,
			GeneratorModel: m.GeneratorModel})
	}
	checkSourceRows(t, "warehouse read-back", whRows)
}

// A row whose verdict names no source for its mutant (a --mutants replay
// handed no sources) says nothing about where it came from: no Source, and
// no generator credited for a mutant no generator was seen to plant.
func TestAMutantWithNoRecordedSourceClaimsNone(t *testing.T) {
	r := bothSourcesResult()
	r.Verdict.MutantSources = nil
	for _, m := range buildScanMutantRows(1, []reposcan.FileResult{r}) {
		if m.Source != "" || m.GeneratorModel != "" || m.SourceTool != "" || m.SourceStatus != "" {
			t.Errorf("row %s claims a source the verdict never recorded: %+v", m.MutantID, m)
		}
	}
}

// R-T4: a count nothing measured is left off, never written as 0. A file
// whose report source is other bytes was never counted; a covered file's
// zeros are measured and kept.
func TestToolMutantCountsOmitWhatWasNotMeasured(t *testing.T) {
	refused := toolMutantCounts(mutantsources.FileMutants{Path: "pkg/a.ts", Tool: "stryker@10.0.0", Seed: 7,
		Refused: "the report's source for pkg/a.ts is not the audited bytes (sha256 a != b)"})
	raw, _ := json.Marshal(refused)
	for _, k := range []string{"taken", "survived", "no_coverage", "killed", "compile_error", "dropped", "seed"} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("a refused file records %s, which nothing measured: %s", k, raw)
		}
	}
	if refused.Refused == "" || refused.Tool != "stryker@10.0.0" {
		t.Errorf("refused counts = %s", raw)
	}

	covered := toolMutantCounts(mutantsources.FileMutants{Path: "pkg/a.ts", Tool: "stryker@10.0.0", Seed: 7,
		Counts: map[string]int{"NoCoverage": 1, "Killed": 1}})
	raw, _ = json.Marshal(covered)
	for _, k := range []string{`"taken":0`, `"survived":0`, `"compile_error":0`, `"dropped":0`, `"no_coverage":1`, `"killed":1`, `"seed":7`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("a covered file's measured %s is missing: %s", k, raw)
		}
	}
}

// The scan line is rendered FROM the recorded counts, so the screen and the
// record cannot disagree; every number the line prints is a field.
func TestTheToolMutantsLineIsTheRecordedCounts(t *testing.T) {
	fm := mutantsources.FileMutants{Path: "pkg/a.ts", Tool: "stryker@10.0.0", Seed: 7,
		Counts: map[string]int{"Survived": 3, "NoCoverage": 2, "Killed": 9, "CompileError": 1, "Timeout": 4,
			"dropped:over-budget": 2, "dropped:anchor-not-unique": 1}}
	c := toolMutantCounts(fm)
	const want = "tool mutants (stryker@10.0.0): taken 0 of 3 survivors + 2 no-coverage, 9 killed by the tool and not re-graded, 1 compile errors, 3 dropped: anchor-not-unique 1, over-budget 2; not re-graded: Timeout 4"
	if got := toolMutantsLine(c); got != want {
		t.Errorf("line =\n%q\nwant\n%q", got, want)
	}
	if c.NotRegraded["Timeout"] != 4 || c.DroppedBy["over-budget"] != 2 || *c.Dropped != 3 {
		t.Errorf("counts = %+v", c)
	}
}

// End to end on certify --repo, tool only: the signed entry carries the tool
// mutant's source on its row and the file's account of the report on the
// scan row, with the seed that replays the sample, and `corral verify`
// still passes on the entry and on the warehouse view of it.
func TestRepoEntryRecordsToolMutantSources(t *testing.T) {
	noKeys(t)
	withMachineLedger(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	attest := filepath.Join(t.TempDir(), "statement.json")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--goals", writeGoals(t, t.TempDir(), `{"pkg/a.go": "Max must return the larger argument"}`),
		"--writer-model", "fakew", "--mutant-model", "off", "--critic-model", "off",
		"--mutants-from", "stryker:" + report, "--tool-sample-seed", "7",
		"--substrate", substrateWorkspace, "--whole-suite", "--no-verdict-cache", "--attest", attest,
		"--", "go", "test", "./...",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errb.String())
	}
	dir := filepath.Join(root, ".corral", "ledger")
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one entry, got %d (%v)", len(entries), err)
	}
	b := entries[0].Bundle
	if len(b.Mutants) != 1 {
		t.Fatalf("mutant rows = %+v, want the one tool mutant", b.Mutants)
	}
	m := b.Mutants[0]
	if m.Source != "tool" || m.SourceTool != "stryker@10.0.0" || m.SourceOperator != "Identifier" ||
		m.SourceStatus != "NoCoverage" || m.GeneratorModel != "" || !m.Proven {
		t.Errorf("tool mutant row = %+v", m)
	}
	var counts []ledger.ToolMutantCounts
	if err := json.Unmarshal([]byte(b.Scan.ToolMutants), &counts); err != nil || len(counts) != 1 {
		t.Fatalf("scan row tool_mutants = %q (%v)", b.Scan.ToolMutants, err)
	}
	c := counts[0]
	if c.Path != "pkg/a.go" || c.Tool != "stryker@10.0.0" || c.Taken == nil || *c.Taken != 1 ||
		c.NoCoverage == nil || *c.NoCoverage != 1 || c.Killed == nil || *c.Killed != 1 ||
		c.Seed == nil || *c.Seed != 7 || c.Refused != "" {
		raw, _ := json.Marshal(c)
		t.Errorf("recorded counts = %s", raw)
	}
	// The line on screen is the line the record renders.
	if !strings.Contains(out.String(), "pkg/a.go: "+toolMutantsLine(c)) {
		t.Errorf("the printed line is not the recorded counts' line %q:\n%s", toolMutantsLine(c), out.String())
	}
	var vout, verr bytes.Buffer
	if c := runVerifyAttest([]string{"--ledger", dir}, &vout, &verr); c != 0 {
		t.Errorf("verify --ledger: exit %d\n%s%s", c, vout.String(), verr.String())
	}
	vout.Reset()
	verr.Reset()
	if c := runVerifyAttest([]string{"--attest", attest, "--db", dir + "/"}, &vout, &verr); c != 0 {
		t.Errorf("verify --attest --db <ledger>: exit %d\n%s%s", c, vout.String(), verr.String())
	}
}

// The --local half: auditOneFile hands the file's counts to its sink, and
// the --local entry records them on its scan row.
func TestLocalEntryRecordsToolMutantCounts(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "7", mutantModel: "off"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var sunk []ledger.ToolMutantCounts
	in := localAuditInput{
		repoDir: root, codePath: "pkg/a.go", testPath: "pkg/a_test.go",
		goal: "Max must return the larger argument", substrate: substrateWorkspace,
		checkArgv:   []string{"go", "test", "./..."},
		writerModel: "fakew", mutantModel: "off", criticModel: "off",
		writerMode: advpool.WriterModeBatched,
		toolSource: s,
		toolSink:   func(c ledger.ToolMutantCounts) { sunk = append(sunk, c) },
		stdout:     io.Discard, stderr: io.Discard,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	v, err := auditOneFile(ctx, in)
	if err != nil {
		t.Fatalf("auditOneFile: %v", err)
	}
	if len(sunk) != 1 || sunk[0].Taken == nil || *sunk[0].Taken != 1 || sunk[0].Seed == nil || *sunk[0].Seed != 7 {
		t.Fatalf("sunk counts = %+v", sunk)
	}
	b, err := localEntryBundle(in, v, advpool.RunTelemetry{}, time.Now(), localRunFacts{toolMutants: toolMutantsJSON(sunk)})
	if err != nil {
		t.Fatal(err)
	}
	if b.Scan.ToolMutants == "" || b.Scan.ToolMutants != toolMutantsJSON(sunk) {
		t.Errorf("--local scan row tool_mutants = %q", b.Scan.ToolMutants)
	}
	if len(b.Mutants) != 1 || b.Mutants[0].Source != "tool" || b.Mutants[0].SourceStatus != "NoCoverage" {
		t.Errorf("--local mutant rows = %+v", b.Mutants)
	}
}

// TestScansReaderShowsToolMutantSource: `corral scans show` says where each
// mutant came from — a count per source, and for each tool mutant the
// tool's own status beside corral's outcome, which is the agreement figure.
func TestScansReaderShowsToolMutantSource(t *testing.T) {
	rows := buildScanMutantRows(1, []reposcan.FileResult{bothSourcesResult()})
	r := &fakeScansReader{
		files:   []scanstore.File{{Path: "pkg/a.ts", Disposition: "audited", Survivors: 2, ProvenMissed: 2}},
		mutants: rows,
	}
	var out, errb bytes.Buffer
	if code := runScansShow([]string{"1"}, openFake(r), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"mutant sources:",
		"pkg/a.ts  model gen-model  graded 2  survived 1  proven 1",
		"pkg/a.ts  tool stryker@10.0.0  graded 1  survived 1  proven 1",
		"pkg/a.ts  t/stryker/7  ConditionalExpression  tool said Survived  corral: survived, proven",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("scans show lacks %q:\n%s", want, s)
		}
	}

	// A scan with no recorded source prints no section at all.
	r.mutants = buildScanMutantRows(1, []reposcan.FileResult{func() reposcan.FileResult {
		x := bothSourcesResult()
		x.Verdict.MutantSources = nil
		return x
	}()})
	out.Reset()
	runScansShow([]string{"1"}, openFake(r), &out, &errb)
	if strings.Contains(out.String(), "mutant sources:") {
		t.Errorf("a scan with no recorded sources printed a sources section:\n%s", out.String())
	}
}

// --- fix round 1 ---------------------------------------------------------

// pathMarker is a directory name no recorded sentence can contain by chance:
// the report sits under it, so any fragment of the operator's path on the
// record is found by looking for it.
const pathMarker = "zz-operator-home-marker-q7"

// twoFileToolRepo is the tool fixture plus pkg/b.go, which the report does
// not mention, with the report under pathMarker. It returns the repo, the
// report path and the goals file.
func twoFileToolRepo(t *testing.T) (root, report, goals string) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), toolFixtureSrc)
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), toolFixtureTest)
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n\n// Min returns the smaller of a and b.\nfunc Min(a, b int) int {\n\tif a < b {\n\t\treturn a\n\t}\n\treturn b\n}\n")
	mustWrite(t, filepath.Join(root, "pkg", "b_test.go"), "package pkg\n\nimport \"testing\"\n\nfunc TestMin(t *testing.T) {\n\tif Min(1, 2) != 1 {\n\t\tt.Fatal(\"Min\")\n\t}\n}\n")
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	report = filepath.Join(t.TempDir(), pathMarker, "reports", "mutation.json")
	mustWrite(t, report, string(toolFixtureReport(t, toolFixtureSrc)))
	goals = writeGoals(t, t.TempDir(), `{"pkg/a.go": "Max must return the larger argument", "pkg/b.go": "Min must return the smaller argument"}`)
	return root, report, goals
}

// rawLedgerBytes is every entry file of a ledger directory, decompressed:
// what is published, byte for byte.
func rawLedgerBytes(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, auditpush.ScansSubdir, "*.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no entry files in %s (%v)", dir, err)
	}
	var all strings.Builder
	for _, f := range files {
		fh, err := os.Open(f) // #nosec G304 -- test temp path
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(fh)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(zr)
		_ = fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

// pathFragments are the pieces of a report path that must never reach the
// record: the marker directory, the temp parent, the whole path.
func pathFragments(report string) []string {
	return []string{pathMarker, filepath.Dir(filepath.Dir(report)), report}
}

func repoEntryCounts(t *testing.T, root string) (ledger.Row, []ledger.ToolMutantCounts, auditpush.LedgerEntry) {
	t.Helper()
	entries, err := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one entry, got %d (%v)", len(entries), err)
	}
	e := entries[0]
	var b ledger.Row
	for _, r := range e.Bundle.Files {
		if r.Path == "pkg/b.go" {
			b = r
		}
	}
	var counts []ledger.ToolMutantCounts
	if err := json.Unmarshal([]byte(e.Bundle.Scan.ToolMutants), &counts); err != nil {
		t.Fatalf("scan row tool_mutants = %q: %v", e.Bundle.Scan.ToolMutants, err)
	}
	return b, counts, e
}

func accountFor(counts []ledger.ToolMutantCounts, p string) (ledger.ToolMutantCounts, bool) {
	for _, c := range counts {
		if c.Path == p {
			return c, true
		}
	}
	return ledger.ToolMutantCounts{}, false
}

// Fix round 1, findings 1, 2 and 5. A file the report does not mention is
// accounted for on the scan row in both shapes of run (beside a model, and
// tool-only, where the pre-check refuses it before any baseline), and no
// recorded sentence names the report by the path the operator typed: the
// record names it by tool and sha256. The terminal still says the path, and
// the pre-check's line names the versioned tool.
func TestRecordedToolReasonsNeverNameTheReportPath(t *testing.T) {
	for _, mode := range []string{"both", "tool-only"} {
		t.Run(mode, func(t *testing.T) {
			noKeys(t)
			withMachineLedger(t)
			wiredAgent(t, "fakew", "", toolFixtureWriterTest)
			wiredAgent(t, "fakem", "", "NONE")
			root, report, goals := twoFileToolRepo(t)
			mutant := "fakem"
			if mode == "tool-only" {
				mutant = "off"
			}
			var out, errb bytes.Buffer
			runCertifyRepo([]string{
				"--repo", root, "--goals", goals, "--all",
				"--writer-model", "fakew", "--mutant-model", mutant, "--critic-model", "off",
				"--mutants-from", "stryker:" + report, "--tool-sample-seed", "7",
				"--substrate", substrateWorkspace, "--whole-suite", "--no-verdict-cache",
				"--", "go", "test", "./...",
			}, &out, &errb)
			said := out.String() + errb.String()
			if !strings.Contains(said, report) {
				t.Errorf("the terminal no longer names the report path %s:\n%s", report, said)
			}
			raw := rawLedgerBytes(t, filepath.Join(root, ".corral", "ledger"))
			for _, frag := range pathFragments(report) {
				if strings.Contains(raw, frag) {
					t.Errorf("the signed entry carries %q, a fragment of the operator's report path", frag)
				}
			}
			bRow, counts, _ := repoEntryCounts(t, root)
			if strings.Contains(bRow.Detail, pathMarker) {
				t.Errorf("Row.Detail names the report path: %q", bRow.Detail)
			}
			acc, ok := accountFor(counts, "pkg/b.go")
			if !ok {
				t.Fatalf("pkg/b.go has no account on the scan row: %+v\n%s", counts, said)
			}
			if acc.Tool != "stryker@10.0.0" || !strings.Contains(acc.Refused, "no entry for pkg/b.go") || acc.Taken != nil {
				t.Errorf("pkg/b.go account = %+v", acc)
			}
			src, _ := os.ReadFile(report) // #nosec G304 -- test temp path
			if !strings.Contains(acc.Refused, reportSHA256(src)) {
				t.Errorf("the recorded reason %q does not name the report by its sha256", acc.Refused)
			}
			if mode == "tool-only" {
				if bRow.Reason != reposcan.ReasonNoMutantSource || !strings.Contains(bRow.Detail, reportSHA256(src)) {
					t.Errorf("pkg/b.go row reason %q detail %q", bRow.Reason, bRow.Detail)
				}
				if !strings.Contains(out.String(), "pkg/b.go: tool mutants (stryker@10.0.0): none to grade") {
					t.Errorf("the pre-check line does not name the versioned tool:\n%s", out.String())
				}
			}
		})
	}
}

// Fix round 1, finding 3: a file served from the verdict cache ran no audit,
// so no account was handed to the sink, yet its tool-sourced mutant rows are
// written. The account comes from the entry that earned the verdict, so the
// second run's scan row equals the first's.
func TestAVerdictCacheHitKeepsTheToolAccount(t *testing.T) {
	noKeys(t)
	withMachineLedger(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	goals := writeGoals(t, t.TempDir(), `{"pkg/a.go": "Max must return the larger argument"}`)
	// One prior, the same (empty) one both times: unset, the second run is
	// primed by the first run's entry, which is a different exam and so a
	// different cache key.
	noPrior := t.TempDir()
	run := func() (string, string) {
		var out, errb bytes.Buffer
		code := runCertifyRepo([]string{
			"--repo", root, "--goals", goals,
			"--writer-model", "fakew", "--mutant-model", "off", "--critic-model", "off",
			"--mutants-from", "stryker:" + report, "--tool-sample-seed", "7",
			"--substrate", substrateWorkspace, "--whole-suite", "--prior", noPrior,
			"--", "go", "test", "./...",
		}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d\n%s%s", code, out.String(), errb.String())
		}
		return out.String(), errb.String()
	}
	run()
	second, _ := run()
	entries, err := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("want two entries, got %d (%v)", len(entries), err)
	}
	first, again := entries[0].Bundle, entries[1].Bundle
	hit := false
	for _, f := range again.Files {
		hit = hit || f.CacheHit
	}
	if !hit {
		t.Fatalf("the second run was not a cache hit:\n%s", second)
	}
	if first.Scan.ToolMutants == "" || again.Scan.ToolMutants != first.Scan.ToolMutants {
		t.Errorf("cache-hit tool_mutants = %q, want the first run's %q", again.Scan.ToolMutants, first.Scan.ToolMutants)
	}
	if len(again.Mutants) != 1 || again.Mutants[0].Source != "tool" {
		t.Errorf("cache-hit mutant rows = %+v", again.Mutants)
	}
}
