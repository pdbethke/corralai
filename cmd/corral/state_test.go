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
