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

// certify --adversarial fixes the command on the client, where the checkout is,
// before the brain (which has none) ever sees it.
func TestAdversarialDoorSendsTheScopedCommand(t *testing.T) {
	repo := rustRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "src", "lib.rs"), []byte("pub fn f() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	f := &fakeAdvClient{runID: 7, statuses: []advStatus{certifiedStatus()}}
	var out, errBuf strings.Builder
	args := []string{"--adversarial", "--brain", "http://b", "--code", "src/lib.rs", "--test", "src/lib.rs", "--goal", "g", "--poll", "1ms", "--", "cargo", "test"}
	if rc := runCertifyAdversarial(args, f, gitStubRunner{}, noSleep, &out, &errBuf); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errBuf.String())
	}
	if f.spec.TestCmd != "cargo test -p leaf" {
		t.Errorf("the brain was sent %q, want the scoped command", f.spec.TestCmd)
	}
}

func TestAdversarialDoorRefusesWhenTheCrateCannotBeNamed(t *testing.T) {
	repo := t.TempDir() // no Cargo.toml
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
	if f.spec.TestCmd != "" || !strings.Contains(errBuf.String(), "no Cargo.toml") {
		t.Errorf("spec=%q stderr=%s", f.spec.TestCmd, errBuf.String())
	}
}
