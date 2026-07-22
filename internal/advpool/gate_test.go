// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// advPoolJailSkipUnlessGoWorks resolves a real bwrap backend and skips the
// caller's test unless the `go` toolchain is actually reachable INSIDE the
// jail — bwrap only binds /usr into the sandbox (see
// internal/sandbox/isolator_linux.go), so a host where `go` is installed
// outside /usr (e.g. a snap-packaged go, common on Ubuntu dev boxes: `go` at
// /snap/bin -> /usr/bin/snap) has a working bwrap backend that can still
// never actually run `go` — a plain "no backend, skip" check would silently
// pass over that case instead of skipping it. Returns the resolved backend,
// or skips via t.Skip. (Relocated from internal/brain/advpool_test.go along
// with JailValidator itself.)
func advPoolJailSkipUnlessGoWorks(t *testing.T) sandbox.Isolator {
	t.Helper()
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise go vet")
	}
	jail := adequacy.NewJail(backend, 30*time.Second)
	pass, rerr := jail.RunTest(context.Background(), nil, []string{"go", "version"})
	if rerr != nil || !pass {
		t.Skipf("go toolchain not reachable inside the bwrap jail on this host (rerr=%v pass=%v) — likely a snap-packaged go outside /usr", rerr, pass)
	}
	return backend
}

// fakeReportJail is a deterministic stand-in for adequacy.Jail, keyed by the
// code content it receives at codePath (mirrors adequacy's own score_test.go
// fakeJail, relocated here since JailScorer.ScoreReport is exercised at this
// package's boundary, not adequacy's).
type fakeReportJail struct {
	passOn map[string]bool
}

func (f *fakeReportJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	return f.passOn[files["target.go"]], nil
}

// TestJailScorerReport proves ScoreReport surfaces the full adequacy.Report
// (not just a collapsed kill rate): a compliant baseline that passes plus one
// killed mutant must come back with CompliantPass=true and exactly one
// Killed entry.
func TestJailScorerReport(t *testing.T) {
	jail := &fakeReportJail{passOn: map[string]bool{
		"COMPLIANT": true,  // baseline passes
		"MUTANT":    false, // test fails on the mutant -> killed
	}}
	s := JailScorer{Jail: jail}

	mutants := []adequacy.Mutant{{ID: "m1", Code: "MUTANT"}}
	rep, err := s.ScoreReport(context.Background(), "target.go", "COMPLIANT", "<test>", mutants, "go test ./...")
	if err != nil {
		t.Fatalf("ScoreReport: %v", err)
	}
	if !rep.CompliantPass {
		t.Fatalf("rep.CompliantPass = false, want true")
	}
	if len(rep.Killed) != 1 {
		t.Fatalf("len(rep.Killed) = %d, want 1; rep=%+v", len(rep.Killed), rep)
	}
}

// TestJailValidatorCompileTest_SubdirectoryCodePath is I-1's regression test
// (relocated from internal/brain/advpool_test.go's
// TestAdvPoolValidatorCompileTest_SubdirectoryCodePath): a SUBDIRECTORY
// code_path (e.g. internal/auth/login.go, the common case — control-plane
// targets are rarely at the module root) must not have its compiling test
// wrongly rejected. Before the fix, CompileTest ran `go vet ./` (module
// root, non-recursive) while the candidate files landed under
// internal/auth/ — the root package has no .go files, so vet always errored
// and every authored test was rejected regardless of whether it actually
// compiled, wedging the run forever. This exercises the REAL JailValidator
// over a real jail (never a stub).
func TestJailValidatorCompileTest_SubdirectoryCodePath(t *testing.T) {
	backend := advPoolJailSkipUnlessGoWorks(t)
	jail := adequacy.NewJail(backend, 30*time.Second)
	v := JailValidator{Jail: jail}

	code := "package auth\n\nfunc ValidatePassword(pw string) error { return nil }\n"
	test := "package auth\n\nimport \"testing\"\n\nfunc TestValidatePassword(t *testing.T) {\n\tif err := ValidatePassword(\"x\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n"

	if err := v.CompileTest(context.Background(), "internal/auth/login.go", code, test); err != nil {
		t.Fatalf("CompileTest rejected a compiling test under a subdirectory code_path: %v", err)
	}
}

// TestJailValidatorCompileTest_SubdirectoryNonCompilingTest proves the fix
// didn't just widen the check into a rubber stamp: a genuinely non-compiling
// test under the SAME subdirectory code_path must still be rejected.
func TestJailValidatorCompileTest_SubdirectoryNonCompilingTest(t *testing.T) {
	backend := advPoolJailSkipUnlessGoWorks(t)
	jail := adequacy.NewJail(backend, 30*time.Second)
	v := JailValidator{Jail: jail}

	code := "package auth\n\nfunc ValidatePassword(pw string) error { return nil }\n"
	badTest := "package auth\n\nimport \"testing\"\n\nfunc TestValidatePassword(t *testing.T) {\n\tValidatePassword(123)\n}\n" // wrong arg type

	err := v.CompileTest(context.Background(), "internal/auth/login.go", code, badTest)
	if err == nil {
		t.Fatal("expected CompileTest to reject a non-compiling test, got nil error")
	}
	// The corrective-retry loop depends on this error being a *CompileError that
	// actually carries the jail's compiler output — an empty Output silently
	// reverts the driver to the pre-fix BLIND retry (bare "does not compile" fed
	// back, model repeats the mistake to exhaustion). Assert both.
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *CompileError, got %T: %v", err, err)
	}
	if strings.TrimSpace(ce.Output) == "" {
		t.Fatal("CompileError.Output is empty — the real compiler diagnostic was not surfaced")
	}
	if !strings.Contains(ce.Output, "ValidatePassword") && !strings.Contains(ce.Output, "123") {
		t.Fatalf("CompileError.Output does not carry the real go-vet diagnostic:\n%s", ce.Output)
	}
}

// TestPluginForFailsClosedOnUnknownExt proves pluginFor fail-closes on an
// unrecognized code extension (the gate must never grade a language it
// cannot run) while still resolving the go plugin for a .go path. (Relocated
// from internal/brain/advpool_test.go.)
func TestPluginForFailsClosedOnUnknownExt(t *testing.T) {
	if _, err := pluginFor("weird.cobol"); err == nil {
		t.Fatal("pluginFor(.cobol) must error — fail closed")
	}
	p, err := pluginFor("internal/sqlguard/sqlguard.go")
	if err != nil || p.Name() != "go" {
		t.Fatalf("pluginFor(.go) = %v,%v; want go,nil", p, err)
	}
}

// TestAdvPoolBaseGoUnchanged proves advPoolBase's go path is unchanged after
// its move: the go.mod scaffold and the recursive `go test ./...` default
// must be byte-identical to the prior go-only behavior. (Relocated from
// internal/brain/advpool_test.go.)
func TestAdvPoolBaseGoUnchanged(t *testing.T) {
	base, cmd := advPoolBase("x/y.go")
	if base["go.mod"] == "" || cmd[0] != "go" {
		t.Fatalf("go base/cmd regressed: %v %v", base, cmd)
	}
}

// TestScoreWorkspace_RepoModeSeedsRepoAndSkipsDevTestOverlay proves --repo-dir
// mode: the WHOLE repo is the jail base, the project's own command is
// authoritative (no synthetic default), and the dev-test arg is NOT overlaid
// (the real suite already lives in the repo). It also proves the returned map
// is a COPY, so a mutant overlay never mutates the shared BaseFiles.
func TestScoreWorkspace_RepoModeSeedsRepoAndSkipsDevTestOverlay(t *testing.T) {
	repo := map[string]string{
		"more_itertools/recipes.py":  "def f():\n    return 1\n",
		"more_itertools/__init__.py": "from .recipes import *\n",
		"tests/test_recipes.py":      "import more_itertools\n",
	}
	s := JailScorer{BaseFiles: repo}
	ws, cmd := s.scoreWorkspace("more_itertools/recipes.py", "DEV-TEST-MUST-NOT-APPEAR", "python3 -m pytest tests/test_recipes.py -q")

	if len(ws) != len(repo) {
		t.Fatalf("repo-mode base must equal the repo (%d files), got %d", len(repo), len(ws))
	}
	if got := strings.Join(cmd, " "); got != "python3 -m pytest tests/test_recipes.py -q" {
		t.Fatalf("repo-mode must use the project command verbatim, got %q", got)
	}
	for k, v := range ws {
		if strings.Contains(v, "DEV-TEST-MUST-NOT-APPEAR") {
			t.Fatalf("repo mode must not overlay the dev-test arg (found at %q)", k)
		}
	}
	ws["injected"] = "x"
	if _, ok := repo["injected"]; ok {
		t.Fatal("scoreWorkspace must copy BaseFiles, not alias it")
	}
}

// TestScoreWorkspace_SingleFileModeUnchanged pins the original behavior: the
// dev test IS overlaid at the plugin's synthetic test path and the command
// defaults from the plugin when the run carries none.
func TestScoreWorkspace_SingleFileModeUnchanged(t *testing.T) {
	s := JailScorer{} // no BaseFiles -> single-file mode
	ws, cmd := s.scoreWorkspace("passwd.py", "DEVTEST", "")
	tp := advPoolTestPath("passwd.py")
	if ws[tp] != "DEVTEST" {
		t.Fatalf("single-file mode must overlay the dev test at %q; keys=%v", tp, ws)
	}
	if len(cmd) == 0 {
		t.Fatal("single-file mode must default the test command from the plugin")
	}
}

// TestCertSigner_SignVerdict_ProducesVerifiableRecord proves CertSigner (the
// relocated, brain.Options-decoupled Signer) signs a Verdict into a
// buildstore record that independently verifies — the same certify chain
// check internal/brain/advpool_integration_test.go runs over the
// brain-hosted path, now exercised directly against this leaf package's own
// Signer, with no *brain.Options in sight. Models the buildstore/key setup
// on that integration test's setupIntegrationDriver helper.
func TestCertSigner_SignVerdict_ProducesVerifiableRecord(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	s := CertSigner{Key: priv, Store: bs}

	v := Verdict{
		Repo: "example/repo", Commit: "deadbeef01",
		Status:      StatusCertified,
		DevKillRate: 0.9,
		ModelsByRole: RoleAssignment{
			RoleMutantGenerator: "model-gen",
			RoleTestWriter:      "model-writer",
			RoleTestCritic:      "model-critic",
		},
	}

	id, head, err := s.SignVerdict(context.Background(), v)
	if err != nil {
		t.Fatalf("SignVerdict: %v", err)
	}
	if id <= 0 {
		t.Fatalf("SignVerdict record id = %d, want > 0", id)
	}
	if head == "" {
		t.Fatal("SignVerdict returned an empty head")
	}

	rec, found, err := bs.Get(id)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", id, found, err)
	}
	sig, _ := rec["signature"].(string)
	stmt, ok, verr := certify.VerifyDSSE([]byte(sig), pub)
	if verr != nil {
		t.Fatalf("VerifyDSSE: %v", verr)
	}
	if !ok || stmt == nil {
		t.Fatal("VerifyDSSE must succeed over the signed verdict record under the returned public key")
	}
}

// TestSignVerdictMarksShadowProducerNonGating proves the signed record tells a
// reader which models SET the exam and which merely measured. produced_by
// lists every entry in ModelsByRole as "role:model"; the challenger seat is in
// that map (certify --local adds it to the assignment) but its mutants never
// entered the set the dev suite was graded against. An unmarked entry would
// read as a model that helped certify.
func TestSignVerdictMarksShadowProducerNonGating(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	id, _, err := CertSigner{Key: priv, Store: bs}.SignVerdict(context.Background(), Verdict{
		Repo: "example/repo", Commit: "deadbeef02", Status: StatusCertified,
		ModelsByRole: RoleAssignment{
			RoleMutantGenerator:       "model-gen",
			RoleTestWriter:            "model-writer",
			RoleTestCritic:            "model-critic",
			RoleMutantGeneratorShadow: "challenger-model",
		},
	})
	if err != nil {
		t.Fatalf("SignVerdict: %v", err)
	}
	rec, found, err := bs.Get(id)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", id, found, err)
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	produced := string(blob)
	if !strings.Contains(produced, RoleMutantGeneratorShadow+":challenger-model (non-gating)") {
		t.Errorf("the shadow producer must be marked non-gating in the signed record, got:\n%s", produced)
	}
	// The gating seats must NOT pick up the marker.
	if strings.Contains(produced, "model-gen (non-gating)") {
		t.Error("the PRIMARY mutant-generator was marked non-gating — it sets the exam")
	}
}
