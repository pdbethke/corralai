// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func rustRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Cargo.toml"), []byte("[package]\nname = \"leaf\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

// scopeAuditInput is the one place a file's audit command is scoped; the
// baseline (checkArgv) and the scored runs (baseArgv) must both carry it.
func TestScopeAuditInputScopesBaselineAndScoredCommands(t *testing.T) {
	repo := rustRepo(t)
	plug, _ := lang.ByName("rust")
	in := localAuditInput{repoDir: repo, codePath: "src/lib.rs",
		checkArgv: []string{"cargo", "test"}, baseArgv: []string{"cargo", "test"}}
	got, err := scopeAuditInput(in, plug)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cargo", "test", "-p", "leaf"}
	if !reflect.DeepEqual(got.checkArgv, want) || !reflect.DeepEqual(got.baseArgv, want) {
		t.Errorf("checkArgv=%q baseArgv=%q, want both %q", got.checkArgv, got.baseArgv, want)
	}
	if !reflect.DeepEqual(in.checkArgv, []string{"cargo", "test"}) {
		t.Error("the caller's input was mutated")
	}
}

func TestScopeAuditInputRefusesAsAUsageError(t *testing.T) {
	plug, _ := lang.ByName("rust")
	_, err := scopeAuditInput(localAuditInput{repoDir: t.TempDir(), codePath: "src/lib.rs",
		checkArgv: []string{"cargo", "test"}}, plug)
	if err == nil || !strings.Contains(err.Error(), "no Cargo.toml") {
		t.Errorf("got %v", err)
	}
}

func TestScopeAuditInputIsIdentityForALanguageWithoutAScoper(t *testing.T) {
	plug, _ := lang.ByName("python")
	in := localAuditInput{repoDir: t.TempDir(), codePath: "a.py", checkArgv: []string{"pytest"}}
	got, err := scopeAuditInput(in, plug)
	if err != nil || !reflect.DeepEqual(got.checkArgv, in.checkArgv) || got.baseArgv != nil {
		t.Errorf("got (%+v, %v)", got, err)
	}
}

// certify --adversarial fixes the command on the client, where the checkout
// is, before the brain (which has none) ever sees it — and it refuses a file
// whose test landing is not built BEFORE that, before any read (final review
// I4). In Rust part 1 every .rs file is refused, so the door's scoping is
// unreachable for Rust here, exactly as it is behind certify --local's
// resolveAuditPlugin; scopeAuditInput itself is held by the tests above. This
// pins the order: a repository where scoping would fail ("no Cargo.toml")
// gets the landing refusal, and nothing is sent. When the landing for a
// Declared file exists, this door gets its scoped-command test back.
func TestAdversarialDoorRefusesTheLandingBeforeScoping(t *testing.T) {
	repo := t.TempDir() // no Cargo.toml: scoping would refuse too
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "lib.rs"), []byte("pub fn f() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf strings.Builder
	args := []string{"--adversarial", "--brain", "http://b", "--code", "src/lib.rs", "--test", "src/lib.rs", "--goal", "g", "--", "cargo", "test"}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 2 {
		t.Fatalf("exit %d, want 2", rc)
	}
	if f.spec.TestCmd != "" || !strings.Contains(errBuf.String(), "test landing is not built") || strings.Contains(errBuf.String(), "no Cargo.toml") {
		t.Errorf("spec=%q stderr=%s", f.spec.TestCmd, errBuf.String())
	}
}
