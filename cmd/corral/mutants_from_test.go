// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/mutantsources"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The tool-mutant fixture: a Go package whose one test asserts Max(2, 1), so
// the line `return b` never runs. The hand-made report is in StrykerJS's
// schema (the only one ReadStryker reads); its columns are 1-based and
// end-exclusive, which for this all-ASCII file are byte columns.
//
// Go, not TypeScript: the TypeScript plugin refuses a run with no tsc on the
// host (tsPlugin.Preflight), and neither this box nor the CI runner installs
// one, so a TS end-to-end would skip everywhere it is meant to prove
// something. The reader does not look at the language, so a Go file carries
// the same report through the same wiring.
const toolFixtureSrc = "package pkg\n\n// Max returns the larger of a and b.\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n"

// toolFixtureWriterTest is the fake writer's reply: a test that calls Max
// with the larger argument second, which kills `return b` -> `return a`.
const toolFixtureWriterTest = "package pkg\n\nimport \"testing\"\n\nfunc TestMaxSecondLarger(t *testing.T) {\n\tif Max(1, 2) != 2 {\n\t\tt.Fatal(\"Max(1, 2) != 2\")\n\t}\n}\n"

const toolFixtureTest = "package pkg\n\nimport \"testing\"\n\nfunc TestMax(t *testing.T) {\n\tif Max(2, 1) != 2 {\n\t\tt.Fatal(\"Max(2, 1) != 2\")\n\t}\n}\n"

// toolFixtureReport is a StrykerJS report about pkg/a.go whose source is src:
// mutant 1 (`a > b` -> `a < b`) the tool killed, and mutant 2 (`return b` ->
// `return a`) it found no test covering.
func toolFixtureReport(t *testing.T, src string) []byte {
	t.Helper()
	type pos struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	}
	type mut struct {
		ID          string `json:"id"`
		MutatorName string `json:"mutatorName"`
		Replacement string `json:"replacement"`
		Status      string `json:"status"`
		Location    struct {
			Start pos `json:"start"`
			End   pos `json:"end"`
		} `json:"location"`
	}
	m1 := mut{ID: "1", MutatorName: "EqualityOperator", Replacement: "a < b", Status: "Killed"}
	m1.Location.Start, m1.Location.End = pos{5, 5}, pos{5, 10}
	m2 := mut{ID: "2", MutatorName: "Identifier", Replacement: "a", Status: "NoCoverage"}
	m2.Location.Start, m2.Location.End = pos{8, 9}, pos{8, 10}
	b, err := json.Marshal(map[string]any{
		"schemaVersion": "2",
		"framework":     map[string]string{"name": "StrykerJS", "version": "10.0.0"},
		"files": map[string]any{
			"pkg/a.go": map[string]any{"language": "go", "source": src, "mutants": []mut{m1, m2}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toolFixtureRepo writes the fixture as a committed git repository and the
// report (made from reportSrc) outside it, and returns both paths.
func toolFixtureRepo(t *testing.T, reportSrc string) (root, report string) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), toolFixtureSrc)
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), toolFixtureTest)
	gitRun := gitCmd(t, root)
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	report = filepath.Join(t.TempDir(), "mutation.json")
	if err := os.WriteFile(report, toolFixtureReport(t, reportSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, report
}

// --mutant-model off with no --mutants-from leaves the run with no mutant
// source at all. Refused on both commands before anything is read or spent,
// and the refusal names both flags, because the fix is one or the other.
func TestMutantsFromNeedsAMutantSource(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), toolFixtureSrc)
	for name, run := range map[string]func() (int, string){
		"local": func() (int, string) {
			var out, errb bytes.Buffer
			code := runCertifyLocal([]string{"--code", filepath.Join(dir, "a.go"), "--goal", "g",
				"--writer-model", "w", "--mutant-model", "off", "--critic-model", "off", "--no-ledger"}, &out, &errb)
			return code, errb.String()
		},
		"repo": func() (int, string) {
			var out, errb bytes.Buffer
			code := runCertifyRepo([]string{"--repo", dir, "--goals", writeGoals(t, dir, `{}`),
				"--writer-model", "w", "--mutant-model", "off", "--critic-model", "off", "--no-ledger"}, &out, &errb)
			return code, errb.String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := run()
			if code != 2 {
				t.Fatalf("exit %d, want 2: %s", code, stderr)
			}
			if !strings.Contains(stderr, "--mutant-model off") || !strings.Contains(stderr, "--mutants-from") {
				t.Fatalf("the refusal must name --mutant-model off and --mutants-from, got: %s", stderr)
			}
		})
	}
	// The resolver's own refusal says it too: a seat left empty names the
	// other way to fill it.
	err := herdNotConfiguredErr("corral certify --local", "w", "", "off", false)
	if err == nil || !strings.Contains(err.Error(), "--mutants-from") {
		t.Fatalf("herdNotConfiguredErr with no mutant seat = %v, want a line naming --mutants-from", err)
	}
	if err := herdNotConfiguredErr("corral certify --local", "w", "", "off", true); err != nil {
		t.Fatalf("an empty mutant seat with a tool source is a legal run, got %v", err)
	}
	if err := herdNotConfiguredErr("corral certify --local", "", "", "off", true); err == nil || !strings.Contains(err.Error(), "--writer-model") {
		t.Fatalf("a tool source never excuses the writer seat: %v", err)
	}
}

// resolveRoleModels reads "off" in the mutant seat as an empty seat, so the
// role assignment never names a model called "off".
func TestMutantModelOffResolvesToAnEmptySeat(t *testing.T) {
	_, m, _, _, _ := resolveRoleModels(localAuditInput{writerModel: "w", mutantModel: " OFF ", criticModel: "off"})
	if m != "" {
		t.Fatalf("mutant seat = %q, want empty", m)
	}
}

func TestMutantsFromRefusesUnknownTool(t *testing.T) {
	report := filepath.Join(t.TempDir(), "r.json")
	mustWrite(t, report, string(toolFixtureReport(t, toolFixtureSrc)))
	for name, c := range map[string]struct {
		values []string
		want   []string
	}{
		"unknown tool":     {[]string{"pit:" + report}, []string{`"pit"`, "stryker"}},
		"no tool prefix":   {[]string{report}, []string{"<tool>:<report path>", "stryker"}},
		"empty path":       {[]string{"stryker:"}, []string{"stryker:"}},
		"one per tool":     {[]string{"stryker:" + report, "stryker:" + report}, []string{"once per tool"}},
		"unreadable":       {[]string{"stryker:" + filepath.Join(t.TempDir(), "absent.json")}, []string{"absent.json"}},
		"not a stryker js": {[]string{"stryker:" + writeTemp(t, `{"framework":{"name":"Stryker.NET","version":"4"},"files":{}}`)}, []string{"StrykerJS"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveMutantsFrom(mutantsFromFlags{values: c.values, mutantModel: "m"}, io.Discard)
			if err == nil {
				t.Fatal("accepted")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal %q does not contain %q", err, w)
				}
			}
		})
	}
	// And on the commands themselves, as a usage refusal.
	var out, errb bytes.Buffer
	if code := runCertifyLocal([]string{"--code", report, "--goal", "g", "--writer-model", "w", "--mutant-model", "m",
		"--critic-model", "off", "--no-ledger", "--mutants-from", "pit:" + report}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "stryker") {
		t.Fatalf("certify --local: exit %d, stderr %s", code, errb.String())
	}
	errb.Reset()
	if code := runCertifyRepo([]string{"--repo", t.TempDir(), "--dry-run", "--mutants-from", "pit:" + report}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "stryker") {
		t.Fatalf("certify --repo: exit %d, stderr %s", code, errb.String())
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.json")
	mustWrite(t, p, body)
	return p
}

// --mutants pins the whole exam already, so adding a tool source to it is a
// contradiction, refused on both commands.
func TestMutantsFromWithReplayIsRefused(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	set := filepath.Join(t.TempDir(), "set.json")
	if err := adequacy.WriteMutantSet(set, adequacy.MutantSetFile{Format: adequacy.MutantSetFormat, Files: map[string]adequacy.MutantSetEntry{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, replay: set, mutantModel: "m"}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "--mutants") || !strings.Contains(err.Error(), "--mutants-from") {
		t.Fatalf("resolveMutantsFrom with a replay = %v, want a refusal naming both flags", err)
	}
	var out, errb bytes.Buffer
	if code := runCertifyLocal([]string{"--code", report, "--goal", "g", "--writer-model", "w", "--mutant-model", "m",
		"--critic-model", "off", "--no-ledger", "--mutants", set, "--mutants-from", "stryker:" + report}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--mutants-from") {
		t.Fatalf("certify --local: exit %d, stderr %s", code, errb.String())
	}
	errb.Reset()
	if code := runCertifyRepo([]string{"--repo", t.TempDir(), "--dry-run", "--mutants", set, "--mutants-from", "stryker:" + report}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--mutants-from") {
		t.Fatalf("certify --repo: exit %d, stderr %s", code, errb.String())
	}
}

// The sample seed: named, it is used; absent, one is drawn and printed so
// the run can be replayed; given with no tool source, it is refused like a
// --shadow-seed with no pool.
func TestToolSampleSeedIsUsedOrDrawnAndPrinted(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	var out bytes.Buffer
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "42", mutantModel: "m"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if s.seed != 42 {
		t.Fatalf("seed = %d, want 42", s.seed)
	}
	if !strings.Contains(out.String(), "--tool-sample-seed 42") {
		t.Errorf("the seed in use must be printed, got %q", out.String())
	}
	out.Reset()
	s, err = resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, mutantModel: "m"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "drawn") || !strings.Contains(out.String(), "--tool-sample-seed "+formatToolSeed(s.seed)) {
		t.Errorf("a drawn seed must be printed with the flag that replays it, got %q", out.String())
	}
	if _, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "nope", mutantModel: "m"}, io.Discard); err == nil {
		t.Error("an unparseable seed was accepted")
	}
	if _, err := resolveMutantsFrom(mutantsFromFlags{seed: "42", mutantModel: "m"}, io.Discard); err == nil || !strings.Contains(err.Error(), "--mutants-from") {
		t.Errorf("a seed with no tool source = %v, want a refusal naming --mutants-from", err)
	}
	if s, err := resolveMutantsFrom(mutantsFromFlags{mutantModel: "m"}, io.Discard); s != nil || err != nil {
		t.Errorf("no tool source = %v, %v; want nil, nil", s, err)
	}
}

// The verdict cache key carries the tool source: two runs that differ only
// in --mutants-from (a report or not, two reports, two seeds) sit different
// exams and must never share a cached verdict.
func TestMutantsFromKeysTheVerdictCache(t *testing.T) {
	a := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	b := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc+"\n")))
	src := func(path, seed string) *toolMutantSource {
		s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + path}, seed: seed, mutantModel: "m"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	keys := map[string]string{
		"model only":     auditConfigKey(false, "", nil, "", "", (*toolMutantSource)(nil).cacheKey()),
		"report a":       auditConfigKey(false, "", nil, "", "", src(a, "1").cacheKey()),
		"report b":       auditConfigKey(false, "", nil, "", "", src(b, "1").cacheKey()),
		"report a seed2": auditConfigKey(false, "", nil, "", "", src(a, "2").cacheKey()),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if other, dup := seen[k]; dup {
			t.Errorf("%s and %s share the cache key %q", name, other, k)
		}
		seen[k] = name
	}
	if keys["model only"] != auditConfigKey(false, "", nil, "", "", "") {
		t.Error("a run with no tool source must key exactly as it did before the flag existed")
	}
	if src(a, "1").cacheKey() != src(a, "1").cacheKey() {
		t.Error("the same report and seed must key the same")
	}
}

// A tool-only file with nothing to grade — no report entry, a report made
// from other bytes, or nothing the tool left alive — is not graded on an
// empty exam: it is refused with the reason, as no-mutant-source. With a
// model seat it is not refused: the model's mutants still run, and the scan
// line says why the tool added none.
func TestToolMutantsForAFileWithNoSource(t *testing.T) {
	staleReport := writeTemp(t, string(toolFixtureReport(t, strings.Replace(toolFixtureSrc, "larger", "bigger", 1))))
	killedOnly := strings.Replace(string(toolFixtureReport(t, toolFixtureSrc)), `"NoCoverage"`, `"Killed"`, 1)
	cases := map[string]struct {
		report, path, want string
	}{
		"stale":       {staleReport, "pkg/a.go", "sha256"},
		"absent":      {staleReport, "pkg/other.go", "no entry for pkg/other.go"},
		"killed only": {writeTemp(t, killedOnly), "pkg/a.go", "nothing to grade"},
	}
	for name, c := range cases {
		for _, toolOnly := range []bool{true, false} {
			model := "m"
			if toolOnly {
				model = "off"
			}
			s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + c.report}, seed: "1", mutantModel: model}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.forFile(c.path, []byte(toolFixtureSrc), 5)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(r.line, c.want) {
				t.Errorf("%s: scan line %q does not say %q", name, r.line, c.want)
			}
			var rs advpool.RunSpec
			err = r.apply(&rs)
			if !toolOnly {
				if err != nil || rs.PresetMutants != nil || rs.ToolMutants != nil || rs.MutantSources != nil {
					t.Errorf("%s with a model seat: err %v, spec %+v; want the model's exam untouched", name, err, rs)
				}
				continue
			}
			var lae localAuditError
			if !errors.As(err, &lae) || lae.ScanReason() != reposcan.ReasonNoMutantSource || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s tool-only: err %v, want a no-mutant-source refusal saying %q", name, err, c.want)
			}
			if rs.PresetMutants != nil {
				t.Errorf("%s tool-only: an empty exam was set", name)
			}
			if pre, perr := s.noSourceReason(c.path, []byte(toolFixtureSrc)); perr != nil || !strings.Contains(pre, c.want) {
				t.Errorf("%s: the pre-check %q does not say %q", name, pre, c.want)
			}
		}
	}
	// A usable file passes the pre-check, and lands on the door TM-R1 names:
	// PresetMutants tool-only, ToolMutants beside a model.
	good := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	for _, model := range []string{"off", "m"} {
		s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + good}, seed: "1", mutantModel: model}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if why, perr := s.noSourceReason("pkg/a.go", []byte(toolFixtureSrc)); why != "" || perr != nil {
			t.Fatalf("pre-check refused a usable file: %s %v", why, perr)
		}
		r, err := s.forFile("pkg/a.go", []byte(toolFixtureSrc), 5)
		if err != nil {
			t.Fatal(err)
		}
		var rs advpool.RunSpec
		if err := r.apply(&rs); err != nil {
			t.Fatal(err)
		}
		exam := rs.ToolMutants
		if model == "off" {
			exam = rs.PresetMutants
			if rs.ToolMutants != nil {
				t.Error("tool-only must use PresetMutants alone")
			}
		} else if rs.PresetMutants != nil {
			t.Error("both sources must use ToolMutants, never PresetMutants")
		}
		if len(exam) != 1 || exam[0].ID != "t/stryker/2" || rs.MutantSources["t/stryker/2"].Kind != mutantsources.KindTool {
			t.Errorf("model %q: exam %+v sources %+v", model, exam, rs.MutantSources)
		}
		const line = "tool mutants (stryker@10.0.0): taken 1 of 0 survivors + 1 no-coverage, 1 killed by the tool and not re-graded, 0 compile errors, 0 dropped"
		if r.line != line {
			t.Errorf("scan line =\n%q\nwant\n%q", r.line, line)
		}
	}
}

// End to end on certify --repo, workspace substrate, tool only: the
// mutant seat is off, the one tool mutant the report left alive is graded by
// the dev suite and survives, the writer's test proves it by the same rule a
// model mutant meets, and the source line is printed.
func TestMutantsFromPrintsTheSourceLine(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--goals", writeGoals(t, t.TempDir(), `{"pkg/a.go": "Max must return the larger argument"}`),
		"--writer-model", "fakew", "--mutant-model", "off", "--critic-model", "off",
		"--mutants-from", "stryker:" + report, "--tool-sample-seed", "7",
		"--substrate", substrateWorkspace, "--whole-suite", "--no-ledger", "--no-verdict-cache",
		"--", "go", "test", "./...",
	}, &out, &errb)
	s := out.String()
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, s, errb.String())
	}
	for _, want := range []string{
		"tool sample seed: 7",
		"pkg/a.go: tool mutants (stryker@10.0.0): taken 1 of 0 survivors + 1 no-coverage, 1 killed by the tool and not re-graded, 0 compile errors, 0 dropped",
		"pkg/a.go: kill rate 0.00 (1 survivor(s), 1 proven missed)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("stdout lacks %q:\n%s\nstderr:\n%s", want, s, errb.String())
		}
	}
	if strings.Contains(s, "COULD-NOT-GRADE") {
		t.Errorf("the tool mutant was not graded:\n%s", s)
	}
}

// The same tool-only scan with a report made from other bytes grades
// nothing: the file is reported not graded, with the refusal sentence, and
// never as a kill rate over an empty exam.
func TestToolOnlyScanReportsAStaleReportUngraded(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, strings.Replace(toolFixtureSrc, "larger", "bigger", 1))
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--goals", writeGoals(t, t.TempDir(), `{"pkg/a.go": "Max must return the larger argument"}`),
		"--writer-model", "fakew", "--mutant-model", "off", "--critic-model", "off",
		"--mutants-from", "stryker:" + report, "--tool-sample-seed", "7",
		"--substrate", substrateWorkspace, "--whole-suite", "--no-ledger", "--no-verdict-cache",
		"--", "go", "test", "./...",
	}, &out, &errb)
	s := out.String()
	if !strings.Contains(s, reposcan.ReasonNoMutantSource) || !strings.Contains(s, "sha256") {
		t.Fatalf("want the file reported %s with the refusal sentence (exit %d):\n%s\nstderr:\n%s", reposcan.ReasonNoMutantSource, code, s, errb.String())
	}
	if strings.Contains(s, "pkg/a.go: kill rate") {
		t.Errorf("a kill rate was printed for a file with no exam:\n%s", s)
	}
	if !strings.Contains(s, "COULD-NOT-GRADE") {
		t.Errorf("a scan that graded nothing must say so:\n%s", s)
	}
}

// The --local half (TM-R4): auditOneFile, the code certify --local runs,
// grades a tool-only exam on the workspace substrate, prints the source
// line, and the verdict carries every mutant's source.
func TestCertifyLocalAuditGradesToolMutants(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", toolFixtureWriterTest)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "7", mutantModel: "off"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	v, err := auditOneFile(ctx, localAuditInput{
		repoDir: root, codePath: "pkg/a.go", testPath: "pkg/a_test.go",
		goal: "Max must return the larger argument", substrate: substrateWorkspace,
		checkArgv:   []string{"go", "test", "./..."},
		writerModel: "fakew", mutantModel: "off", criticModel: "off",
		writerMode: advpool.WriterModeBatched,
		toolSource: s,
		stdout:     &out, stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("auditOneFile: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "tool mutants (stryker@10.0.0): taken 1 of 0 survivors") {
		t.Errorf("no source line:\n%s", out.String())
	}
	if v.MutantsTotal != 1 || v.Survivors != 1 || v.ProvenMissed != 1 {
		t.Errorf("graded %d mutant(s), %d survivor(s), %d proven; want the one tool mutant, surviving, proven by the writer's test", v.MutantsTotal, v.Survivors, v.ProvenMissed)
	}
	if got := v.MutantSources["t/stryker/2"]; got.Kind != mutantsources.KindTool || got.Status != "NoCoverage" || got.Tool != "stryker@10.0.0" {
		t.Errorf("verdict source = %+v", got)
	}
}

// Fix round 1, finding 1: a tool source never fills a mutant seat the
// operator left UNNAMED. Only "off" says the seat is empty on purpose; with
// --mutant-model omitted the no-default-models refusal stands, on both
// commands, and still names the way out.
func TestMutantsFromDoesNotExcuseAnOmittedMutantModel(t *testing.T) {
	noKeys(t)
	root, report := toolFixtureRepo(t, toolFixtureSrc)
	for name, run := range map[string]func() (int, string){
		"local": func() (int, string) {
			var out, errb bytes.Buffer
			code := runCertifyLocal([]string{"--repo-dir", root, "--code", "pkg/a.go", "--goal", "g",
				"--writer-model", "w", "--critic-model", "off", "--no-ledger",
				"--mutants-from", "stryker:" + report, "--", "go", "test", "./..."}, &out, &errb)
			return code, errb.String()
		},
		"repo": func() (int, string) {
			var out, errb bytes.Buffer
			code := runCertifyRepo([]string{"--repo", root, "--goals", writeGoals(t, t.TempDir(), `{"pkg/a.go": "g"}`),
				"--writer-model", "w", "--critic-model", "off", "--no-ledger", "--substrate", substrateWorkspace,
				"--mutants-from", "stryker:" + report, "--", "go", "test", "./..."}, &out, &errb)
			return code, errb.String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := run()
			if code != 2 {
				t.Fatalf("exit %d, want 2 — an unnamed mutant seat with a tool source must still be refused: %s", code, stderr)
			}
			for _, w := range []string{"--mutant-model (mutant-generator)", "--mutant-model off", "--mutants-from"} {
				if !strings.Contains(stderr, w) {
					t.Errorf("refusal lacks %q: %s", w, stderr)
				}
			}
		})
	}
}

// The guard under it: no role whose assignment is empty is ever served by
// the run's base backend, whatever path reaches it. An unassigned seat gets
// no chatter, so its task fails loudly instead of being answered by a model
// nobody named.
func TestAnUnassignedRoleIsNeverServedByTheBaseBackend(t *testing.T) {
	noKeys(t)
	wiredAgent(t, "fakew", "", "x")
	chatterFor, err := localChatterFor(advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "", advpool.RoleTestWriter: "fakew",
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{advpool.RoleMutantGenerator, advpool.RoleTestCritic, advpool.RoleMutantGeneratorShadow, advpool.RoleTestWriterShadow} {
		if c := chatterFor(role); c != nil {
			t.Errorf("role %s has no model assigned and was handed a chatter (%T)", role, c)
		}
	}
	if chatterFor(advpool.RoleTestWriter) == nil {
		t.Error("the assigned writer seat lost its chatter")
	}
}

// Finding 2: a report that cannot be read for a file is an error, never a
// no-mutant-source disposition, which would say the tool had nothing to
// offer when corral could not tell.
func TestNoSourceReasonKeepsReaderErrorsApart(t *testing.T) {
	report := writeTemp(t, string(toolFixtureReport(t, toolFixtureSrc)))
	s, err := resolveMutantsFrom(mutantsFromFlags{values: []string{"stryker:" + report}, seed: "1", mutantModel: "off"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// Swap in a report ReadStryker refuses after the flag check passed it.
	s.report = []byte(`{"framework":{"name":"StrykerJS","version":"10.0.0"},"files":{"pkg/a.go":{"source":7}}}`)
	why, err := s.noSourceReason("pkg/a.go", []byte(toolFixtureSrc))
	if err == nil || why != "" {
		t.Fatalf("noSourceReason = %q, %v; want no reason and the reader's error", why, err)
	}
	// And the scan reports it as an executor error, not no-mutant-source.
	root, _ := toolFixtureRepo(t, toolFixtureSrc)
	l := &localExecutor{repoDir: root, toolSource: s}
	res, err := l.Execute(context.Background(), reposcan.Job{Path: "pkg/a.go", Lang: "go"})
	if err == nil || res.Reason == reposcan.ReasonNoMutantSource {
		t.Fatalf("Execute = %+v, %v; want an error, not a no-mutant-source disposition", res, err)
	}
}
