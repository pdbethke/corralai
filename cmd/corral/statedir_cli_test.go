// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pdbethke/corralai/internal/statedir"
)

// isolateStateHome points every default-path input at a temp dir, so the test
// neither sees nor is spoiled by the developer's real ~/.claude.
func isolateStateHome(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("expected paths are Linux's; macOS is covered by internal/statedir")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("CORRALAI_STATE_DIR", "")
	t.Setenv("CORRALAI_CACHE_DIR", "")
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	return home
}

// The regression the whole move turns on: with the ledger key only at the
// old location, the CLI must refuse — and must not create a key anywhere.
func TestLocalCertifyKeyRefusesALegacyOnlyKeyAndCreatesNone(t *testing.T) {
	home := isolateStateHome(t)
	legacy := filepath.Join(home, ".claude", "corralai_certify_key")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadLocalCertifyKey()
	var le *statedir.LegacyError
	if !errors.As(err, &le) {
		t.Fatalf("want the legacy refusal, got %v", err)
	}
	newKey := filepath.Join(home, ".local", "state", "corral", "corralai_certify_key")
	if _, err := os.Stat(newKey); !os.IsNotExist(err) {
		t.Fatalf("a key was created at %s", newKey)
	}
}

func TestLocalCertifyKeyIsCreatedUnderStateOnAFreshMachine(t *testing.T) {
	home := isolateStateHome(t)
	if _, err := loadLocalCertifyKey(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "corral", "corralai_certify_key")); err != nil {
		t.Fatalf("fresh key not under the state root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("corral created ~/.claude")
	}
}

// The transparency guard used to Stat the legacy key path; a legacy-only key
// must now surface the refusal rather than "no key is configured".
func TestLoadLocalCertifyKeyIfConfiguredRefusesLegacyOnlyKey(t *testing.T) {
	home := isolateStateHome(t)
	legacy := filepath.Join(home, ".claude", "corralai_certify_key")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadLocalCertifyKeyIfConfigured()
	var le *statedir.LegacyError
	if !errors.As(err, &le) {
		t.Fatalf("want the legacy refusal, got %v", err)
	}
}
