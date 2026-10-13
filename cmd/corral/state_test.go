// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/statedir"
)

func TestStateMigratePrintsTheSamePublicKeyBeforeAndAfter(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "st")
	t.Setenv("HOME", home)
	t.Setenv("CORRALAI_STATE_DIR", state)
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	pub := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	legacy := filepath.Join(home, ".claude")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "corralai_certify_key"), []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Count(out.String(), pub) != 2 {
		t.Fatalf("want the certify public key printed before and after:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "→") {
		t.Fatalf("want one `old → new` line per move:\n%s", out.String())
	}
}

func TestStateMigrateDryRunMovesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CORRALAI_STATE_DIR", filepath.Join(home, "st"))
	legacy := filepath.Join(home, ".claude", "corralai_coord.sqlite3")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("--dry-run moved a file")
	}
}

func TestStatePathsListsEveryRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a real ~/.claude would turn rows into REFUSED lines
	t.Setenv("CORRALAI_STATE_DIR", "/s")
	t.Setenv("CORRALAI_CACHE_DIR", "/c")
	var out, errb bytes.Buffer
	if code := runState([]string{"paths"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"CORRALAI_DB", "/s/corralai_coord.sqlite3", "CORRALAI_CACHE_DB", "/c/corralai_cache.duckdb"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("paths output missing %q", want)
		}
	}
}

func TestStateMigrateFromMovesAnExplicitDirectory(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "st")
	t.Setenv("HOME", home)
	t.Setenv("CORRALAI_STATE_DIR", state)
	src := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "corralai_coord.sqlite3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate", "--from", src}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(state, "corralai_coord.sqlite3")); err != nil {
		t.Fatalf("--from directory's row was not moved: %v", err)
	}
}

func TestStateMigrateConflictExitsOneAndTouchesNothing(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "st")
	t.Setenv("HOME", home)
	t.Setenv("CORRALAI_STATE_DIR", state)
	for _, p := range []string{
		filepath.Join(home, ".claude", "corralai_certify_key"),
		filepath.Join(home, ".claude", "corralai_coord.sqlite3"),
		filepath.Join(state, "corralai_certify_key"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() string {
		var b strings.Builder
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				c, _ := os.ReadFile(p)
				fi, _ := d.Info()
				b.WriteString(p + " " + fi.Mode().String() + " " + string(c) + "\n")
			}
			return nil
		})
		return b.String()
	}
	before := snap()
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "conflict") {
		t.Errorf("want a conflict line:\n%s", errb.String())
	}
	if snap() != before {
		t.Fatal("a conflicted migrate changed the tree")
	}
}

func TestCompareKeysRequiresEveryMovedKeyToSurvive(t *testing.T) {
	moved := []statedir.Move{{File: statedir.CertifyKey, To: "/new/key"}, {File: statedir.CoordDB}}
	if err := compareKeys(moved, map[string]string{"corralai_certify_key": "aa"}, map[string]string{"corralai_certify_key": "aa"}); err != nil {
		t.Errorf("identical keys: %v", err)
	}
	err := compareKeys(moved, map[string]string{"corralai_certify_key": "aa"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "corralai_certify_key") {
		t.Errorf("a moved key unreadable at its destination must fail, got %v", err)
	}
	if compareKeys(moved, map[string]string{"corralai_certify_key": "aa"}, map[string]string{"corralai_certify_key": "bb"}) == nil {
		t.Error("a changed key must fail")
	}
	if err := compareKeys(nil, map[string]string{"corralai_certify_key": "aa"}, nil); err != nil {
		t.Errorf("a key row that never moved is not compared: %v", err)
	}
}

// "must equal LEDGER_PUBKEY" is an instruction about the certify key; said
// after a migration that moved no certify key it points at a key line that
// is not there.
func TestStateMigrateMentionsLedgerPubkeyOnlyWhenTheCertifyKeyMoved(t *testing.T) {
	home := isolateStateHome(t)
	legacy := filepath.Join(home, ".claude")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "corralai_coord.sqlite3"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "LEDGER_PUBKEY") {
		t.Fatalf("no certify key moved, yet:\n%s", out.String())
	}

	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	if err := os.WriteFile(filepath.Join(legacy, "corralai_certify_key"), []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runState([]string{"migrate"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "LEDGER_PUBKEY") {
		t.Fatalf("the certify key moved and the LEDGER_PUBKEY check was not said:\n%s", out.String())
	}
}

// A destination side file with its database still at the source is refused
// without --resume, and adopted — and said to be — with it.
func TestStateMigrateResumeAdoptsAndSaysSo(t *testing.T) {
	home := isolateStateHome(t)
	legacy := filepath.Join(home, ".claude")
	state := filepath.Join(home, ".local", "state", "corral")
	for _, p := range []string{filepath.Join(legacy, "corralai_coord.sqlite3"), filepath.Join(state, "corralai_coord.sqlite3-wal")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	if code := runState([]string{"migrate"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "--resume") {
		t.Fatalf("without --resume: exit %d, want 1 naming --resume\n%s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(legacy, "corralai_coord.sqlite3")); err != nil {
		t.Fatal("a refused plan moved the database")
	}
	out.Reset()
	errb.Reset()
	if code := runState([]string{"migrate", "--resume"}, &out, &errb); code != 0 {
		t.Fatalf("--resume: exit %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "adopting existing "+filepath.Join(state, "corralai_coord.sqlite3-wal")) {
		t.Fatalf("the adopted side file was not listed:\n%s", out.String())
	}
}
