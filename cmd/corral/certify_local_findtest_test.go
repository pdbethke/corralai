// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestPrepareAuditJailErrorListsWhereItLooked pins the rehearsal fix
// directly: `--local` with no --test, against an itsdangerous-shaped repo
// that has NO test anywhere, must not just say "no test found" — it must
// name every convention candidate it already ruled out and every root its
// recursive search already covered, so a stranger never has to guess a
// third time. Reaches this error BEFORE any sandbox/jail work, so it needs
// no bwrap on the host.
func TestPrepareAuditJailErrorListsWhereItLooked(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "src", "itsdangerous"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "src", "itsdangerous", "signer.py"), []byte("class Signer: pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plug, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	var stdout bytes.Buffer
	_, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir:  repoDir,
		codePath: "src/itsdangerous/signer.py",
		lang:     "python",
	}, plug, time.Minute, &stdout)
	if err == nil {
		t.Fatal("expected an error — no test exists anywhere in this fixture")
	}
	msg := err.Error()
	if !strings.Contains(msg, "src/itsdangerous/signer.py") {
		t.Errorf("error %q does not name the code file", msg)
	}
	if !strings.Contains(msg, "Looked for:") {
		t.Errorf("error %q does not disclose what it tried", msg)
	}
	// At least one convention candidate must be listed by name.
	if !strings.Contains(msg, "test_signer.py") {
		t.Errorf("error %q does not name a tried convention candidate", msg)
	}
	if !strings.Contains(msg, "searched") || !strings.Contains(msg, "tests") {
		t.Errorf("error %q does not disclose the roots it searched", msg)
	}
}

// TestPrepareAuditJailFindsTestByRecursiveSearch is the itsdangerous-shaped
// fixture end to end through `--local`'s own --test-default resolution: the
// real test sits one directory level deeper than any convention TestRules
// derives (tests/test_itsdangerous/test_signer.py, not
// tests/itsdangerous/test_signer.py), and FindTest's recursive fallback must
// still find it and disclose the pairing on stdout.
func TestPrepareAuditJailFindsTestByRecursiveSearch(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "src", "itsdangerous"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "tests", "test_itsdangerous"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "src", "itsdangerous", "signer.py"), []byte("class Signer: pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "tests", "test_itsdangerous", "test_signer.py"), []byte("def test_x(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plug, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	var stdout bytes.Buffer
	prep, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir:   repoDir,
		codePath:  "src/itsdangerous/signer.py",
		lang:      "python",
		checkArgv: []string{"python3", "-m", "pytest", "tests/test_itsdangerous/test_signer.py"},
	}, plug, time.Minute, &stdout)
	// The disclosure line is written BEFORE any jail/sandbox work, so it must
	// be present regardless of whether this host can go on to actually run
	// the jail.
	if !strings.Contains(stdout.String(), "paired by search: tests/test_itsdangerous/test_signer.py") {
		t.Errorf("stdout = %q, want a line disclosing the search pairing", stdout.String())
	}
	if err != nil {
		if _, serr := resolveLocalJail(""); serr != nil {
			t.Skipf("no sandbox backend on this host: %v", serr)
		}
		t.Fatalf("prepareAuditJail: %v", err)
	}
	defer prep.cleanup()

	if prep.testPath != "tests/test_itsdangerous/test_signer.py" {
		t.Errorf("testPath = %q, want tests/test_itsdangerous/test_signer.py", prep.testPath)
	}
}

// TestCertifyRepoDisclosesSearchPairing is the same itsdangerous-shaped
// fixture through `certify --repo --dry-run`: the human report's
// language-detection block must name the pairing as coming from search,
// not present it as an ordinary convention match.
func TestCertifyRepoDisclosesSearchPairing(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "itsdangerous", "signer.py"), "class Signer: pass\n")
	mustWrite(t, filepath.Join(root, "tests", "test_itsdangerous", "test_signer.py"), "def test_x(): pass\n")

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant,
		"--critic-model", "off", "--dry-run",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "src/itsdangerous/signer.py paired by search: tests/test_itsdangerous/test_signer.py"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout does not disclose the search pairing (%q):\n%s", want, out.String())
	}
}

// TestResolveDevTestPrintsHowItPaired: every pairing --local finds is
// disclosed with the Resolution's Label — the convention's shape, or for
// Rust what the file itself declared ("inline" means the tests are IN the
// audited file, which an operator must know before reading a verdict on it).
// A Rust source that declares no test module is not paired to itself, and
// the miss says there was nothing to look for rather than listing nothing
// and claiming a search (Review Focus 1).
func TestResolveDevTestPrintsHowItPaired(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"pkg/mod.py":      "x = 1\n",
		"pkg/test_mod.py": "def test_x(): pass\n",
		"src/lib.rs":      "pub fn f() {}\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n",
		"src/plain.rs":    "pub fn g() {}\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ lang, code, want string }{
		{"python", "pkg/mod.py", "paired by sibling: pkg/test_mod.py"},
		{"rust", "src/lib.rs", "paired by inline: src/lib.rs"},
	} {
		plug, ok := lang.ByName(tc.lang)
		if !ok {
			t.Fatalf("%s plugin not registered", tc.lang)
		}
		var stdout bytes.Buffer
		if _, err := resolveDevTest(plug, root, tc.code, &stdout); err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s: stdout = %q, want %q", tc.code, stdout.String(), tc.want)
		}
	}

	plug, _ := lang.ByName("rust")
	var stdout bytes.Buffer
	_, err := resolveDevTest(plug, root, "src/plain.rs", &stdout)
	if err == nil {
		t.Fatalf("src/plain.rs declares no test module, yet paired: %q", stdout.String())
	}
	msg := err.Error()
	if !strings.Contains(msg, "declares no test of its own") || strings.Contains(msg, "searched") {
		t.Errorf("miss = %q, want it to say there was nothing to look for, and claim no search", msg)
	}
}

// TestResolveAuditPluginRefusesAFileWhoseTestWouldOverwriteIt: certify
// --local (and every certify --repo job, which routes through the same
// function) refuses a Rust source before preflight, jail or spend when its
// test would be written over the source itself — here because, with no
// --repo-dir, nothing can read the test module it declares (landing_door_test
// covers the repository cases). A Python source is resolved as before.
func TestResolveAuditPluginRefusesAFileWhoseTestWouldOverwriteIt(t *testing.T) {
	_, err := resolveAuditPlugin(localAuditInput{codePath: "src/lib.rs", goal: "g"})
	if err == nil || !strings.Contains(err.Error(), "test landing is not built") || !strings.Contains(err.Error(), "--repo-dir") {
		t.Fatalf("resolveAuditPlugin(src/lib.rs) err = %v, want the landing refusal", err)
	}
	if _, err := resolveAuditPlugin(localAuditInput{codePath: "pkg/mod.py", goal: "g", checkArgv: []string{"sh"}}); err != nil && strings.Contains(err.Error(), "not built yet") {
		t.Fatalf("a Python source was refused by the landing check: %v", err)
	}
}
