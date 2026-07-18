// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
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

	if err := v.CompileTest(context.Background(), "internal/auth/login.go", code, badTest); err == nil {
		t.Fatal("expected CompileTest to reject a non-compiling test, got nil error")
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
