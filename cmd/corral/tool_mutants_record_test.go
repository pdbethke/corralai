// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	if err := db.QueryRow(`SELECT tool_mutants FROM corral_scans`).Scan(&tm); err != nil || tm != string(b.Scan.ToolMutants) {
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
	reportBytes, _ := os.ReadFile(report) // #nosec G304 -- test temp path
	if c.Report != "sha256:"+reportSHA256(reportBytes) {
		t.Errorf("recorded Report = %q, want the graded report's sha256:<hex>", c.Report)
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
			if acc.Report != "sha256:"+reportSHA256(src) {
				t.Errorf("a refused file's account names no report: Report = %q", acc.Report)
			}
			if a, ok := accountFor(counts, "pkg/a.go"); !ok || a.Report != acc.Report {
				t.Errorf("pkg/a.go account %+v does not name the same report", a)
			}
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

// --- final review, Important 1 -------------------------------------------

// detailOf is what reposcan.Scan records as a file's Detail (the source of
// the signed Row.Detail) when its executor returns err.
func detailOf(t *testing.T, err error) string {
	t.Helper()
	out := reposcan.Scan(context.Background(), []reposcan.Job{{Path: "pkg/a.go"}}, errExecutor{err}, nil, 1)
	return out[0].Detail
}

type errExecutor struct{ err error }

func (e errExecutor) Execute(context.Context, reposcan.Job) (reposcan.FileResult, error) {
	return reposcan.FileResult{}, e.err
}

// failingReader is stryker's Paths and Label with a Read that always fails:
// a reader error on a file the report covers, which no fixture reaches.
type failingReader struct{}

func (failingReader) Tool() string { return "failing" }
func (failingReader) Paths(r mutantsources.Report) ([]string, error) {
	s, _ := mutantsources.Lookup("stryker")
	return s.Paths(r)
}
func (failingReader) Label(r mutantsources.Report, declared string) (string, error) {
	s, _ := mutantsources.Lookup("stryker")
	return s.Label(r, declared)
}
func (failingReader) Read(mutantsources.Report, map[string][]byte, func(string) int, int64) (map[string]mutantsources.FileMutants, error) {
	return nil, errors.New("mutantsources: forced reader failure")
}

// A reader error on a covered file goes through Execute to the signed
// entry naming the report by tool and sha256, in both shapes of run: beside
// a model (auditOneFile's door) and tool-only (the pre-check's door). The
// terminal still names the path.
func TestAToolReaderErrorNeverRecordsTheReportPath(t *testing.T) {
	for _, mode := range []string{"both", "tool-only"} {
		t.Run(mode, func(t *testing.T) {
			noKeys(t)
			withMachineLedger(t)
			wiredAgent(t, "fakew", "", toolFixtureWriterTest)
			wiredAgent(t, "fakem", "", "NONE")
			t.Cleanup(mutantsources.Register(failingReader{}))
			root, report, goals := twoFileToolRepo(t)
			mutant := "fakem"
			if mode == "tool-only" {
				mutant = "off"
			}
			var out, errb bytes.Buffer
			runCertifyRepo([]string{
				"--repo", root, "--goals", goals, "--all",
				"--writer-model", "fakew", "--mutant-model", mutant, "--critic-model", "off",
				"--mutants-from", "failing:" + report, "--tool-sample-seed", "7",
				"--substrate", substrateWorkspace, "--whole-suite", "--no-verdict-cache",
				"--", "go", "test", "./...",
			}, &out, &errb)
			said := out.String() + errb.String()
			if !strings.Contains(said, "forced reader failure") || !strings.Contains(said, report) {
				t.Errorf("the terminal does not say the failure with the report path:\n%s", said)
			}
			raw := rawLedgerBytes(t, filepath.Join(root, ".corral", "ledger"))
			for _, frag := range pathFragments(report) {
				if strings.Contains(raw, frag) {
					t.Errorf("the signed entry carries %q, a fragment of the operator's report path", frag)
				}
			}
			src, _ := os.ReadFile(report) // #nosec G304 -- test temp path
			entries, err := auditpush.ReadLedgerDir(filepath.Join(root, ".corral", "ledger"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("want one entry, got %d (%v)\n%s", len(entries), err, said)
			}
			var a ledger.Row
			for _, r := range entries[0].Bundle.Files {
				if r.Path == "pkg/a.go" {
					a = r
				}
			}
			if !strings.Contains(a.Detail, "forced reader failure") || !strings.Contains(a.Detail, "sha256:"+reportSHA256(src)) {
				t.Errorf("pkg/a.go Row.Detail = %q, want the failure naming the report by sha256", a.Detail)
			}
		})
	}
}

// apply's tool-only refusal of a file the report has no entry for: the
// terminal names the report path, the recorded detail its sha256.
func TestApplysNoEntryRefusalRecordsNoPath(t *testing.T) {
	_, report, _ := twoFileToolRepo(t)
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "1", mutantModel: "off"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.forFile("pkg/b.go", []byte("package pkg\n"), 5)
	if err != nil {
		t.Fatal(err)
	}
	aerr := r.apply(&advpool.RunSpec{})
	if aerr == nil || !strings.Contains(aerr.Error(), report) {
		t.Fatalf("the terminal refusal %v does not name the report path", aerr)
	}
	d := detailOf(t, aerr)
	for _, frag := range pathFragments(report) {
		if strings.Contains(d, frag) {
			t.Errorf("recorded detail %q carries %q", d, frag)
		}
	}
	if !strings.Contains(d, s.sha256) || !strings.Contains(d, "no entry for pkg/b.go") {
		t.Errorf("recorded detail %q does not name the report by sha256 and the file", d)
	}
}

// toolNoSource's checkout-open failure: the absolute repo directory is the
// terminal's, never the record's.
func TestToolNoSourceOpenErrorRecordsNoRepoDir(t *testing.T) {
	_, report, _ := twoFileToolRepo(t)
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "1", mutantModel: "off"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(t.TempDir(), pathMarker, "gone")
	l := &localExecutor{repoDir: repoDir, toolSource: s}
	_, terr := l.toolNoSource("pkg/a.go")
	if terr == nil || !strings.Contains(terr.Error(), repoDir) {
		t.Fatalf("the terminal error %v does not name the checkout", terr)
	}
	d := detailOf(t, terr)
	if strings.Contains(d, pathMarker) || strings.Contains(d, filepath.Dir(repoDir)) {
		t.Errorf("recorded detail %q carries the checkout directory", d)
	}
	if !strings.Contains(d, "pkg/a.go") {
		t.Errorf("recorded detail %q does not name the file", d)
	}
}

// --- final review, Minor 1 -----------------------------------------------

// A both-sources exam can be up to twice the fitted budget, because the
// tool's sample uses the same per-file cap. Every surface that prints the
// budget says so: the scan line beside the model, the per-file report line
// and the --local verdict block. A tool-only scan line does not, because
// there is no model share to add to.
func TestTheCombinedExamSizeIsSaid(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	for _, model := range []string{"m", "off"} {
		s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "1", mutantModel: model}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.forFile("pkg/a.go", []byte(toolFixtureSrc), 5)
		if err != nil {
			t.Fatal(err)
		}
		said := strings.Contains(r.line, "5 model + 1 tool") && strings.Contains(r.line, "up to 6")
		if model == "m" && !said {
			t.Errorf("both sources: scan line %q does not say the combined exam size", r.line)
		}
		if model == "off" && strings.Contains(r.line, "model +") {
			t.Errorf("tool-only: scan line %q claims a model share", r.line)
		}
	}

	budget := advpool.MutantBudget{Rule: advpool.BudgetRuleComplexity, Total: 5, Complexity: 3, Floor: 4, Ceiling: 40}
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.go", MutantBudget: budget, ToolShare: 2})
	if !strings.Contains(b.String(), "mutants: 5 model + 2 tool") {
		t.Errorf("the report line does not say the combined exam:\n%s", b.String())
	}
	v := advpool.Verdict{MutantBudget: budget, RegionsTotal: 1, MutantSources: map[string]mutantsources.MutantSource{
		"t/stryker/1": {Kind: mutantsources.KindTool}, "t/stryker/2": {Kind: mutantsources.KindTool}, "m1": {Kind: mutantsources.KindModel},
	}}
	b.Reset()
	renderAdvVerdict(&b, "pkg/a.go", advVerdictFromPool(v))
	if !strings.Contains(b.String(), "5 model + 2 tool") {
		t.Errorf("the verdict block does not say the combined exam:\n%s", b.String())
	}
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{{Job: reposcan.Job{Path: "pkg/a.go"}, Gradable: true, Verdict: v}}, nil)
	if len(rep.Weakest) == 0 || rep.Weakest[0].ToolShare != 2 {
		t.Errorf("the report's file does not carry the verdict's tool share: %+v", rep.Weakest)
	}
}

// The scan line says how the report was tied to the file, rendered from the
// recorded Match like every other number on it.
func TestToolMutantsLineSaysTheMatch(t *testing.T) {
	c := toolMutantCounts(mutantsources.FileMutants{Path: "pkg/a.ts", Tool: "stryker@10.0.0", Seed: 7,
		Match: mutantsources.MatchEmbeddedSource, Units: mutantsources.UnitsUTF16,
		Counts: map[string]int{"Survived": 1}})
	if c.Match != "embedded-source" || c.Units != "utf16" {
		t.Errorf("counts Match, Units = %q, %q", c.Match, c.Units)
	}
	if got := toolMutantsLine(c); !strings.HasSuffix(got, " — match: embedded-source") {
		t.Errorf("line = %q", got)
	}
}

// reportMutantsOf is n recorded mutants, each covered by k test names.
func reportMutantsOf(n, k int) []ledger.ReportMutant {
	out := make([]ledger.ReportMutant, n)
	for i := range out {
		names := make([]string, k)
		for j := range names {
			names[j] = "a test whose name is long enough to weigh something " + strings.Repeat("x", j%7)
		}
		c := k
		out[i] = ledger.ReportMutant{ID: "t/stryker/" + strings.Repeat("9", 1+i%5), IDSource: "tool", Status: "Survived",
			Span: [4]int{i + 1, 1, i + 1, 4}, CoveredBy: names, CoveredByCount: &c, Fate: "graded"}
	}
	return out
}

func encodedLen(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// A file's mutant list is bounded, and nothing is dropped silently: first
// every covered_by list gives way to its count, then the list itself is cut,
// and MutantsTruncated says which happened and how many mutants remain.
func TestReportMutantsBound(t *testing.T) {
	fm := func(rs []ledger.ReportMutant) mutantsources.FileMutants {
		return mutantsources.FileMutants{Path: "pkg/a.ts", Tool: "stryker@10.0.0", Seed: 7, Counts: map[string]int{"Survived": len(rs)}, Reported: rs}
	}

	small := reportMutantsOf(3, 2)
	f := fm(small)
	f.Tests = map[string]string{small[0].CoveredBy[0]: "a named test"}
	c := toolMutantCounts(f)
	if len(c.Mutants) != 3 || c.MutantsTruncated != "" || len(c.Mutants[0].CoveredBy) != 2 {
		t.Errorf("a small list was changed: %d mutants, %q", len(c.Mutants), c.MutantsTruncated)
	}
	if len(c.Tests) != 1 {
		t.Errorf("Tests = %v, want the one named test the mutants refer to", c.Tests)
	}

	// Over the bound with names, under it with counts.
	n := 50
	k := maxReportMutantsBytes / (n * 40)
	lists := reportMutantsOf(n, k)
	if encodedLen(t, lists) <= maxReportMutantsBytes {
		t.Fatalf("fixture too small: %d bytes", encodedLen(t, lists))
	}
	f = fm(lists)
	f.Tests = map[string]string{lists[0].CoveredBy[0]: "only covers"}
	c = toolMutantCounts(f)
	if c.Tests != nil {
		t.Errorf("Tests = %v: a test only a removed covered_by list referred to is still named", c.Tests)
	}
	if len(c.Mutants) != n {
		t.Fatalf("%d of %d mutants kept, want all once the lists are counts", len(c.Mutants), n)
	}
	for _, m := range c.Mutants {
		if m.CoveredBy != nil || m.CoveredByCount == nil || *m.CoveredByCount != k {
			t.Fatalf("covered_by not replaced by its count: %d names, count %v", len(m.CoveredBy), m.CoveredByCount)
		}
	}
	if want := "covered_by lists replaced by counts; 50 of 50 mutants recorded"; c.MutantsTruncated != want {
		t.Errorf("MutantsTruncated = %q, want %q", c.MutantsTruncated, want)
	}
	if lists[0].CoveredBy == nil {
		t.Error("bounding mutated the reader's own slice")
	}

	// Over the bound even with counts: the list is cut, and says so.
	many := reportMutantsOf(maxReportMutantsBytes/40, 1)
	c = toolMutantCounts(fm(many))
	if got := encodedLen(t, c.Mutants); got > maxReportMutantsBytes {
		t.Errorf("bounded list encodes to %d bytes, over %d", got, maxReportMutantsBytes)
	}
	if len(c.Mutants) == 0 || len(c.Mutants) >= len(many) {
		t.Fatalf("kept %d of %d", len(c.Mutants), len(many))
	}
	if want := "covered_by lists replaced by counts; " + itoa(int64(len(c.Mutants))) + " of " + itoa(int64(len(many))) + " mutants recorded"; c.MutantsTruncated != want {
		t.Errorf("MutantsTruncated = %q, want %q", c.MutantsTruncated, want)
	}
}
