// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// Every door that asks lang.CheckTestLanding, asked about the same three Rust
// sources: one whose own declaration loads a separate test file (admitted
// wherever the repository can be read), one whose tests are inline and one
// that declares none (still refused: their landing is the source). cgo-only
// because the declaration is read with the tree-sitter grammar.

const (
	declaredSrc = "fn private_double(x: i32) -> i32 { x * 2 }\n\n#[cfg(test)]\n#[path = \"foo_tests.rs\"]\nmod tests;\n"
	declaredTst = "use super::*;\n\n#[test]\nfn t() { assert_eq!(private_double(1), 2); }\n"
	inlineSrc   = "pub fn b() {}\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n"
	plainSrc    = "pub fn p() {}\n"
)

func landingRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	mustWrite(t, filepath.Join(root, ".config", "nextest.toml"), "[profile.default]\nretries = 1\n")
	mustWrite(t, filepath.Join(root, "src", "foo.rs"), declaredSrc)
	mustWrite(t, filepath.Join(root, "src", "foo_tests.rs"), declaredTst)
	mustWrite(t, filepath.Join(root, "src", "bar.rs"), inlineSrc)
	mustWrite(t, filepath.Join(root, "src", "plain.rs"), plainSrc)
	return root
}

// TestResolveAuditPluginAdmitsOnlyADeclaredLanding: certify --local's door,
// which every certify --repo job also goes through.
func TestResolveAuditPluginAdmitsOnlyADeclaredLanding(t *testing.T) {
	root := landingRepo(t)
	// Every toolchain check satisfied — a stock ~/.cargo rustup install, and
	// a system-wide toolchain for the jail — so the admit below is the
	// landing's answer and nothing else's.
	stockRustup(t)
	withSystemToolchain(t, true)
	in := func(code string) localAuditInput {
		return localAuditInput{repoDir: root, codePath: code, goal: "g", checkArgv: []string{"cargo", "test"}}
	}
	if _, err := resolveAuditPlugin(in("src/foo.rs")); err != nil {
		t.Fatalf("a declared Rust source with its toolchain in place must be admitted: %v", err)
	}
	for _, code := range []string{"src/bar.rs", "src/plain.rs"} {
		if _, err := resolveAuditPlugin(in(code)); err == nil || !strings.Contains(err.Error(), "not built yet") {
			t.Errorf("%s: err = %v, want the landing refusal", code, err)
		}
	}
	// No --repo-dir: single-file mode cannot compose into a project file.
	if _, err := resolveAuditPlugin(localAuditInput{codePath: filepath.Join(root, "src", "foo.rs"), goal: "g"}); err == nil || !strings.Contains(err.Error(), "--repo-dir") {
		t.Errorf("single-file mode: err = %v, want the refusal naming --repo-dir", err)
	}
	// A Python source is untouched by any of it.
	if _, err := resolveAuditPlugin(localAuditInput{codePath: "pkg/mod.py", goal: "g", checkArgv: []string{"sh"}}); err != nil && strings.Contains(err.Error(), "test landing") {
		t.Fatalf("a Python source was refused by the landing check: %v", err)
	}
}

// TestAStockRustupUserIsRefusedOnlyWhereAJailRuns: a stranger's first run.
// A stock rustup install (~/.cargo, no sudo, no system-wide toolchain) is
// exactly what --substrate workspace runs on the host, so that substrate
// admits it; the jail substrate (and --local, which is always jailed) runs
// the system-wide toolchain, so it refuses — naming the layout to install,
// and the substrate that would not need it.
func TestAStockRustupUserIsRefusedOnlyWhereAJailRuns(t *testing.T) {
	root := landingRepo(t)
	stockRustup(t)
	withSystemToolchain(t, false)
	in := localAuditInput{repoDir: root, codePath: "src/foo.rs", goal: "g", checkArgv: []string{"cargo", "test"}}

	ws := in
	ws.substrate = substrateWorkspace
	if _, err := resolveAuditPlugin(ws); err != nil {
		t.Fatalf("--substrate workspace runs the operator's own toolchain and must admit a stock rustup install: %v", err)
	}
	for _, substrate := range []string{"", substrateJail} {
		jailed := in
		jailed.substrate = substrate
		_, err := resolveAuditPlugin(jailed)
		if err == nil {
			t.Fatalf("substrate %q builds a jail, which cannot run a ~/.cargo toolchain, and must refuse", substrate)
		}
		for _, want := range []string{sandbox.SystemRustupHome, sandbox.SystemCargoHome, "--substrate workspace"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("substrate %q: the refusal must name %q, got: %v", substrate, want, err)
			}
		}
	}
}

// TestTheWiringCarriesTheAdmittedLandingToEveryWriter: the scorer, the
// validator and the RunSpec carry ONE AuthoredLanding — the declared test
// file, keyed repo-relative, with its current bytes — so the file the door
// admitted is the file every pass writes into. Run on the workspace
// substrate (the one Warp's acceptance uses), which needs no jail to wire.
// The runner disclosure is said on the way: this repository keeps a
// nextest.toml that `cargo test` bypasses.
func TestTheWiringCarriesTheAdmittedLandingToEveryWriter(t *testing.T) {
	root := landingRepo(t)
	plug, _ := lang.ByName("rust")
	var stdout bytes.Buffer
	prep, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir: root, codePath: "src/foo.rs", lang: "rust", goal: "g",
		checkArgv: []string{"cargo", "test"}, substrate: substrateWorkspace,
	}, plug, time.Minute, &stdout)
	if err != nil {
		t.Fatalf("prepareAuditJail: %v", err)
	}
	defer prep.cleanup()
	w := prep.wiring
	if w.landing.Path != "src/foo_tests.rs" || !w.landing.Into || w.landing.Depth != 1 || w.landing.Existing != declaredTst {
		t.Fatalf("wiring landing = %+v", w.landing)
	}
	if w.scorer.Landing.Landing.Path != w.landing.Path || w.validator.Landing.Landing.Path != w.landing.Path ||
		w.scorer.Landing.Existing != w.landing.Existing || w.validator.Landing.Existing != w.landing.Existing {
		t.Fatalf("scorer %+v / validator %+v disagree with the wiring %+v", w.scorer.Landing, w.validator.Landing, w.landing)
	}
	rs := newAuditRunSpec(localAuditInput{goal: "g"}, auditRoles{}, runSubject{codePath: w.codeKey, landing: w.landing})
	if rs.Landing.Path != w.landing.Path || lang.CheckTestLanding(rs.CodePath, rs.Landing.Landing) != nil {
		t.Fatalf("RunSpec landing %+v does not carry the admitted one", rs.Landing)
	}
	if !strings.Contains(stdout.String(), "paired by path-attr: src/foo_tests.rs") {
		t.Errorf("stdout lacks the pairing line:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), ".config/nextest.toml") || !strings.Contains(stdout.String(), "do not apply") {
		t.Errorf("stdout lacks the runner disclosure:\n%s", stdout.String())
	}
}

// TestDoctorAdmitsADeclaredRustSource: doctor reads the landing the same way
// (rooted at the working directory, as its pairing is).
func TestDoctorAdmitsADeclaredRustSource(t *testing.T) {
	t.Chdir(landingRepo(t))
	got := checkPairing("src/foo.rs", "")
	if !got.ok {
		t.Fatalf("doctor refused a declared Rust source: %+v", got)
	}
	// certify --local without --repo-dir refuses it, and doctor says so
	// rather than passing it unqualified.
	if _, bare := auditLanding(localAuditInput{codePath: "src/foo.rs"}); bare == nil {
		t.Fatal("precondition: single-file mode refuses a declared Rust source")
	}
	if !strings.Contains(got.detail, "--repo-dir") || !strings.Contains(got.detail, "path-attr") {
		t.Fatalf("doctor's [ok] does not say the file needs --repo-dir: %s", got.detail)
	}
	if got := checkPairing("src/bar.rs", ""); got.ok || !strings.Contains(got.detail, "not built yet") {
		t.Fatalf("doctor passed an inline Rust source: %+v", got)
	}
}

// TestAdversarialStillRefusesADeclaredRustSource: certify --adversarial hands
// the brain code and test to grade single-file, where nothing can be composed
// into a project file — so even a declared source is refused there, before
// anything is read or sent, and the refusal says why.
func TestAdversarialStillRefusesADeclaredRustSource(t *testing.T) {
	root := landingRepo(t)
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf bytes.Buffer
	args := []string{"--adversarial", "--brain", "http://b", "--code", filepath.Join(root, "src", "foo.rs"), "--goal", "g", "--poll", "1ms", "--", "cargo", "test"}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", rc, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "test landing is not built") || f.spec.Code != "" {
		t.Fatalf("want the landing refusal before anything is sent, got:\n%s", errBuf.String())
	}
}

// flakyBaseline is a stable, passing baseline that leaned on retries.
type flakyBaseline struct{ flaky []string }

func (flakyBaseline) RunBaseline() (bool, error) { return true, nil }
func (f flakyBaseline) BaselineFlaky() []string  { return f.flaky }

// TestLocalExecutorNamesAFlakyBaseline: the scan line names the tests a
// passing baseline passed only on a retry — graded as passed, never silently.
func TestLocalExecutorNamesAFlakyBaseline(t *testing.T) {
	var buf bytes.Buffer
	ex := localExecutor{
		baselineRuns: 2,
		progress:     &buf,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return flakyBaseline{flaky: []string{"warp_core tests::t"}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) { return advpool.Verdict{}, nil },
	}
	if _, err := ex.Execute(context.Background(), reposcan.Job{Path: "src/foo.rs", Goal: reposcan.Goal{Text: "g"}}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "src/foo.rs: the baseline passed 1 test(s) only on a retry (flaky), graded as passed: warp_core tests::t") {
		t.Errorf("progress lacks the flaky line:\n%s", got)
	}
}

// TestCertifyRepoSaysTheRunnerDisclosureOncePerLanguage: the scan header
// carries the plugin's runner disclosure once per language in the job set,
// from the command the jobs are graded with.
func TestCertifyRepoSaysTheRunnerDisclosureOncePerLanguage(t *testing.T) {
	root := landingRepo(t)
	ex := &localExecutor{repoDir: root, checkArgv: []string{"cargo", "test"}}
	var buf bytes.Buffer
	printRunDisclosures(&buf, ex, []reposcan.Job{{Path: "src/foo.rs", Lang: "rust"}, {Path: "src/baz.rs", Lang: "rust"}})
	if got := buf.String(); strings.Count(got, ".config/nextest.toml") != 1 || !strings.Contains(got, "do not apply") {
		t.Errorf("want one runner disclosure line, got:\n%s", got)
	}
}

// TestTheLandingReachesTheLedgerAndTheRepoOutput: the file a landed block
// must be appended to, as the verdict carries it (advpool's
// TestBothVerdictPathsCarryTheLanding pins RunSpec.Landing -> Verdict through
// both construction sites), survives every hop after it — the scan's file
// row, the bundle a ledger entry holds (marshalled and read back as the
// entry's own JSON), and the --repo report — for a converged and a
// timed-out verdict alike, and --repo says "append it to <file>" in the same
// words --local does.
func TestTheLandingReachesTheLedgerAndTheRepoOutput(t *testing.T) {
	landing := advpool.AuthoredLanding{Landing: pairing.Landing{Path: "src/foo_tests.rs", Into: true, Depth: 1}}
	for name, v := range map[string]advpool.Verdict{
		"converged": {Lang: "rust", ProvenMissed: 1, PoolScored: true, DevScored: true, AuthoredTest: "#[cfg(test)]\nmod corral_tests {\nuse super::super::*;\n}\n"},
		"timed out": {Lang: "rust", ProvenMissed: 1, PoolScored: true, DevScored: true, TimedOut: true, AuthoredTest: "#[cfg(test)]\nmod corral_tests {\nuse super::super::*;\n}\n"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			v.AuthoredLanding = landing.AppendTo()
			results := []reposcan.FileResult{{Job: reposcan.Job{Path: "src/foo.rs", Lang: "rust"}, Gradable: true, Verdict: v}}

			files := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
			b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c"}, 1, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
			raw, err := json.Marshal(auditpush.LedgerEntry{Format: "test", Bundle: b})
			if err != nil {
				t.Fatal(err)
			}
			var back auditpush.LedgerEntry
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatal(err)
			}
			if len(back.Bundle.Files) != 1 || back.Bundle.Files[0].AuthoredLanding != "src/foo_tests.rs" {
				t.Fatalf("ledger entry file rows = %+v, want authored landing src/foo_tests.rs", back.Bundle.Files)
			}
			if f := fileFromRow(back.Bundle.Files[0]); f.AuthoredLanding != "src/foo_tests.rs" {
				t.Errorf("reading the entry back dropped the landing: %+v", f)
			}

			rep := reposcan.Aggregate("o", "r", "c", 1, 1, results, nil)
			if len(rep.Weakest) != 1 || rep.Weakest[0].AuthoredLanding != "src/foo_tests.rs" {
				t.Fatalf("report = %+v", rep.Weakest)
			}
			var out bytes.Buffer
			printWeakFile(&out, rep.Weakest[0])
			if !strings.Contains(out.String(), "— "+authoredTestDestination("src/foo_tests.rs", "your suite")+":") ||
				!strings.Contains(out.String(), "append it to src/foo_tests.rs") {
				t.Errorf("--repo output does not say where the block goes:\n%s", out.String())
			}
		})
	}
}

// seqVerboseJail answers each verbose run with the next scripted output,
// the last repeating, always passing — a baseline that is green every time but leaned on a
// retry only sometimes.
type seqVerboseJail struct {
	outs []string
	n    int
}

func (j *seqVerboseJail) RunTest(context.Context, map[string]string, []string) (bool, error) {
	return true, nil
}

func (j *seqVerboseJail) RunTestVerbose(context.Context, map[string]string, []string) (bool, string, error) {
	// The scripted outputs in order, the last one repeating: a run is
	// whatever number of verbose calls one baseline makes.
	out := j.outs[min(j.n, len(j.outs)-1)]
	j.n++
	return true, out, nil
}

// TestBaselineFlakyIsTheUnionOfTheRuns: CheckBaselineStable runs the baseline
// repeatedly; a test that needed a retry in run 1 and not in run 2 still
// leaned on the retry policy, so it is still named after run 2.
func TestBaselineFlakyIsTheUnionOfTheRuns(t *testing.T) {
	flakyOut, err := os.ReadFile("../../internal/lang/testdata/rust/nextest/flaky.txt")
	if err != nil {
		t.Fatal(err)
	}
	cleanOut, err := os.ReadFile("../../internal/lang/testdata/rust/nextest/pass.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := lang.ByName("rust")
	fp := lang.FailureParserFor(p, lang.Cmd(p.TestCmd()))
	b := jailBaselineRunner{
		ctx:     context.Background(),
		scorer:  advpool.JailScorer{Jail: &seqVerboseJail{outs: []string{string(flakyOut), string(cleanOut)}}, BaseFiles: map[string]string{}, FailureParser: fp},
		codeKey: "src/a.rs", code: "fn a() {}", testCmd: "cargo nextest run",
		lastOutput: new(string), flaky: new([]string),
	}
	stable, err := reposcan.CheckBaselineStable(b, 2)
	if err != nil || !stable {
		t.Fatalf("stable=%v err=%v", stable, err)
	}
	if got := b.BaselineFlaky(); len(got) != 1 || got[0] != "fuzzy_match tests::planted_flaky" {
		t.Fatalf("BaselineFlaky after a flaky run then a clean one = %q, want the flaky test kept", got)
	}
}

// TestLocalReadoutNamesFlakyTestsAndTheLandingAsRepoDoes: certify --local's
// readout names the baseline's flaky tests and where the authored block
// goes, in the same rendering certify --repo uses.
func TestLocalReadoutNamesFlakyTestsAndTheLandingAsRepoDoes(t *testing.T) {
	var out bytes.Buffer
	v := advpool.Verdict{BaselineFlaky: []string{"crate tests::f"}, AuthoredLanding: "src/foo_tests.rs"}
	st := advpool.RunState{AuthoredTest: "#[cfg(test)]\nmod corral_tests {\n}\n", AuthoredExtra: []lang.AuthoredPart{{MutantID: "m2", Source: "x"}}}
	printLocalReadout(&out, v, st, "src/foo_tests.rs")
	got := out.String()
	for _, want := range []string{
		flakyBaselineNote([]string{"crate tests::f"}),
		"— " + authoredTestDestination("src/foo_tests.rs", "src/foo_tests.rs") + ":",
		"append it to src/foo_tests.rs too",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("readout lacks %q:\n%s", want, got)
		}
	}
	out.Reset()
	printLocalReadout(&out, advpool.Verdict{}, advpool.RunState{AuthoredTest: "def test_x(): pass\n"}, "tests/test_a.py")
	if !strings.Contains(out.String(), "add it to tests/test_a.py") || strings.Contains(out.String(), "flaky") {
		t.Errorf("a path landing's readout changed:\n%s", out.String())
	}
}

// TestScansShowSaysWhereALandedTestGoes: `corral scans show --evidence`
// prints a landed block with the file it must be appended to, through the
// shared rendering; a path landing's header is unchanged.
func TestScansShowSaysWhereALandedTestGoes(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "src/foo.rs", Disposition: "audited", Survivors: 1, ProvenMissed: 1, AuthoredTest: "#[cfg(test)]\nmod corral_tests {\n}\n", AuthoredLanding: "src/foo_tests.rs"},
		{Path: "a.py", Disposition: "audited", Survivors: 1, ProvenMissed: 1, AuthoredTest: "def test_x(): pass\n"},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7", "--evidence"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "--- src/foo.rs: the pool's authored test"+authoredTestWhere("src/foo_tests.rs")+" ---") ||
		!strings.Contains(got, "append it to src/foo_tests.rs") {
		t.Errorf("scans show does not say where the block goes:\n%s", got)
	}
	if !strings.Contains(got, "--- a.py: the pool's authored test ---") {
		t.Errorf("a path landing's header changed:\n%s", got)
	}
}

// TestBriefSaysWhereALandedTestGoes: `corral brief` carries the landing from
// the ledger row and prints it with the test that closes a proven gap, in
// the text and in the JSON.
func TestBriefSaysWhereALandedTestGoes(t *testing.T) {
	dir := t.TempDir()
	kr := 0.5
	if _, err := auditpush.PushBundle(dir+"/", auditpush.Bundle{
		SourcePushed: true,
		Scan:         auditpush.ScanRow{Repo: "o/r", ScanID: 1, Commit: "c1c1c1c1c1c1c1c1", Host: "h", CorralVersion: "v", Substrate: "workspace", SourcePushed: true},
		Files: []auditpush.Row{{Repo: "o/r", Commit: "c1c1c1c1c1c1c1c1", ScanID: 1, Path: "src/foo.rs", Disposition: "audited", KillRate: &kr, Survivors: 1, ProvenMissed: 1,
			AuthoredTest: "#[cfg(test)]\nmod corral_tests {\nuse super::super::*;\n}\n", AuthoredLanding: "src/foo_tests.rs"}},
		Mutants: []auditpush.MutantRow{{Repo: "o/r", ScanID: 1, Path: "src/foo.rs", MutantID: "s0/m1", Outcome: "survived", Proven: true, SpanStart: 1, SpanEnd: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runBrief([]string{"--ledger", dir, "--scope", "src"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "the test that closes it"+authoredTestWhere("src/foo_tests.rs")+":") {
		t.Errorf("brief does not say where the block goes:\n%s", out.String())
	}
	out.Reset()
	if code := runBrief([]string{"--ledger", dir, "--scope", "src", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"authored_landing": "src/foo_tests.rs"`) && !strings.Contains(out.String(), `"authored_landing":"src/foo_tests.rs"`) {
		t.Errorf("brief --json lacks the landing:\n%s", out.String())
	}
}

// TestBaselineFlakyIsOneValueFromTheRunnerToTheWarehouse: the tests a
// passing baseline passed only on a retry are measured twice — by the
// stability runs before the audit and by the audit's own dev pass — and are
// ONE value from there on: Execute folds the runner's union into the
// verdict, de-duplicated, the scan line is rendered from that verdict, and
// the same list survives the scan's file row, the ledger entry (marshalled
// and read back as the entry's own JSON) and the warehouse row, all WITHOUT
// --push-source, which withholds the verdict document that used to be the
// only place the dev pass's half was kept.
func TestBaselineFlakyIsOneValueFromTheRunnerToTheWarehouse(t *testing.T) {
	var progress bytes.Buffer
	ex := localExecutor{
		baselineRuns: 2,
		progress:     &progress,
		newBaseline: func(context.Context, localAuditInput) (reposcan.BaselineRunner, func(), error) {
			return flakyBaseline{flaky: []string{"c tests::stability_only", "c tests::both"}}, func() {}, nil
		},
		audit: func(context.Context, localAuditInput) (advpool.Verdict, error) {
			return advpool.Verdict{Lang: "rust", DevScored: true, DevKillRate: 0.5, BaselineFlaky: []string{"c tests::both", "c tests::dev_pass_only"}}, nil
		},
	}
	job := reposcan.Job{Path: "src/foo.rs", Lang: "rust", Goal: reposcan.Goal{Text: "g"}}
	res, err := ex.Execute(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c tests::stability_only", "c tests::both", "c tests::dev_pass_only"}
	if !slices.Equal(res.Verdict.BaselineFlaky, want) {
		t.Fatalf("Verdict.BaselineFlaky = %q, want the runner's union folded in, de-duplicated: %q", res.Verdict.BaselineFlaky, want)
	}
	if got := progress.String(); strings.Count(got, "only on a retry") != 1 || !strings.Contains(got, flakyBaselineNote(want)) {
		t.Errorf("the scan line must say the one folded value, once:\n%s", got)
	}

	res.Job = job
	files := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c"}, 1, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
	raw, err := json.Marshal(auditpush.LedgerEntry{Format: "test", Bundle: b})
	if err != nil {
		t.Fatal(err)
	}
	var back auditpush.LedgerEntry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	wantCol := strings.Join(want, "\n")
	if len(back.Bundle.Files) != 1 || back.Bundle.Files[0].BaselineFlaky != wantCol {
		t.Fatalf("ledger entry file rows = %+v, want baseline_flaky %q", back.Bundle.Files, wantCol)
	}
	if f := fileFromRow(back.Bundle.Files[0]); f.BaselineFlaky != wantCol {
		t.Errorf("reading the entry back dropped the flaky tests: %+v", f)
	}

	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := pushBundle(target, b, io.Discard); err != nil {
		t.Fatalf("pushBundle: %v", err)
	}
	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var col, verdictJSON sql.NullString
	if err := db.QueryRow(`SELECT baseline_flaky, verdict_json FROM corral_audits WHERE path = 'src/foo.rs'`).Scan(&col, &verdictJSON); err != nil {
		t.Fatal(err)
	}
	if verdictJSON.Valid {
		t.Fatal("precondition: without --push-source the verdict document is withheld")
	}
	if col.String != wantCol {
		t.Errorf("warehouse baseline_flaky = %+v, want %q", col, wantCol)
	}
	readBack, err := auditpush.ReadBundle(db, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(readBack.Files) != 1 || readBack.Files[0].BaselineFlaky != wantCol {
		t.Errorf("the warehouse reader dropped the flaky tests: %+v", readBack.Files)
	}
}
