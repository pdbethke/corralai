// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
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

// plantLegacyCertifyKey puts a certify key ONLY at the pre-statedir location
// of an isolated HOME — the workstation state the refusal exists for.
func plantLegacyCertifyKey(t *testing.T, home string) {
	t.Helper()
	legacy := filepath.Join(home, ".claude", "corralai_certify_key")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ledgerEntryCount(t *testing.T, dir string) int {
	t.Helper()
	names, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

// A signer that tolerated ANY key error turned the legacy refusal into a
// permanent UNSIGNED entry in the public hash-linked ledger. Only "no key is
// configured" may write unsigned; a refused or unreadable key must stop the
// write.
func TestLedgerWritersRefuseALegacyOnlyKeyAndWriteNothingUnsigned(t *testing.T) {
	home := isolateStateHome(t)
	// Staged with no key anywhere: unsigned is the honest, allowed outcome.
	staging := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "bbbb2222", ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}
	if _, err := pushBundle(staging+"/", b, io.Discard); err != nil {
		t.Fatalf("no key configured must still write (unsigned): %v", err)
	}
	names, _ := os.ReadDir(filepath.Join(staging, auditpush.ScansSubdir))
	staged := filepath.Join(staging, auditpush.ScansSubdir, names[0].Name())

	plantLegacyCertifyKey(t, home)
	target := t.TempDir()

	if _, err := pushBundle(target+"/", b, io.Discard); err == nil {
		t.Error("pushBundle wrote a ledger entry with the key refused")
	} else if !strings.Contains(err.Error(), "state migrate") {
		t.Errorf("pushBundle's error does not carry the refusal: %v", err)
	}
	for _, args := range [][]string{
		{"append", staged, target},
		{"checkpoint", target},
	} {
		var out, errb bytes.Buffer
		code := runLedger(args, &out, &errb)
		if code == 0 || !strings.Contains(errb.String(), "state migrate") {
			t.Errorf("ledger %s: exit %d, want non-zero naming the refusal\nstdout: %s\nstderr: %s", args[0], code, out.String(), errb.String())
		}
	}
	if n := ledgerEntryCount(t, target); n != 0 {
		t.Fatalf("%d entr(ies) written to the ledger with the signing key refused", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "corral", "corralai_certify_key")); !os.IsNotExist(err) {
		t.Fatal("a fresh certify key was minted beside the legacy one")
	}
}

// The sentinel is the ONLY tolerable error: it must mean "not configured"
// and nothing else.
func TestLoadLocalCertifyKeyIfConfiguredSentinelMeansNotConfigured(t *testing.T) {
	home := isolateStateHome(t)
	if _, err := loadLocalCertifyKeyIfConfigured(); !errors.Is(err, errNoLocalKey) {
		t.Fatalf("fresh machine: want errNoLocalKey, got %v", err)
	}
	plantLegacyCertifyKey(t, home)
	if _, err := loadLocalCertifyKeyIfConfigured(); err == nil || errors.Is(err, errNoLocalKey) {
		t.Fatalf("legacy-only key: want a non-sentinel error, got %v", err)
	}
}

// certify --repo writes a ledger entry by default, at the END of a paid run.
// A key the run cannot use must refuse it BEFORE anything is spent, not
// after the herd ran and the entry could not be signed.
func TestCertifyRepoRefusesALegacyOnlyKeyBeforeAnySpend(t *testing.T) {
	home := isolateStateHome(t)
	plantLegacyCertifyKey(t, home)
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", t.TempDir()}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "state migrate") || !strings.Contains(errb.String(), "nothing was spent") {
		t.Fatalf("exit %d, want 2 naming the refusal before spend\nstderr: %.600s", code, errb.String())
	}
}

// A fresh machine has no state root. `corral models rank` (and every other
// CLI store opener) must create it rather than fail "No such file or
// directory" opening its DuckDB file.
func TestStoreOpenersWorkOnAFreshMachine(t *testing.T) {
	home := isolateStateHome(t)
	if _, err := defaultRankLoader(""); err != nil {
		t.Fatalf("models rank on a fresh machine: %v", err)
	}
	root := filepath.Join(home, ".local", "state", "corral")
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("state root not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("state root mode %v, want 0700", fi.Mode().Perm())
	}
	p, err := cacheDSNOr("")
	if err != nil {
		t.Fatal(err)
	}
	st, err := scanstore.Open(p)
	if err != nil {
		t.Fatalf("the cache store does not open on a fresh machine: %v", err)
	}
	_ = st.Close()
}

// The critic join is best-effort, but a REFUSED path is not "no critic
// store": with the critic DB only at the legacy location the C-PREC column
// silently vanished. The refusal must be said, once, on stderr.
func TestBestEffortCriticStoreSaysWhyItIsMissing(t *testing.T) {
	home := isolateStateHome(t)
	legacy := filepath.Join(home, ".claude", "corralai_criticscore.duckdb")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	cs, closeFn := openCriticStoreBestEffort("corral scorecard", &errb)
	defer closeFn()
	if cs != nil {
		t.Fatal("a refused path opened a store")
	}
	if got := errb.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "state migrate") || !strings.Contains(got, "corral scorecard") {
		t.Fatalf("want one stderr line naming the refusal, got %q", got)
	}
}
