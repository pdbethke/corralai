// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
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
	in := func(code string) localAuditInput {
		return localAuditInput{repoDir: root, codePath: code, goal: "g", checkArgv: []string{"cargo", "test"}}
	}
	if _, err := resolveAuditPlugin(in("src/foo.rs")); err != nil && strings.Contains(err.Error(), "test landing") {
		t.Fatalf("a declared Rust source was refused for its landing: %v", err)
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
	if got := checkPairing("src/foo.rs", ""); !got.ok {
		t.Fatalf("doctor refused a declared Rust source: %+v", got)
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
