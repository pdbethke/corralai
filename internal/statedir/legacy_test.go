// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyOnlyStateFileIsRefused(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	writeFile(t, filepath.Join(home, ".claude", "corralai_certify_key"))

	_, err := Path(CertifyKey)
	var le *LegacyError
	if !errors.As(err, &le) {
		t.Fatalf("want *LegacyError, got %v", err)
	}
	for _, want := range []string{"corral state migrate", "CORRALAI_CERTIFY_KEY_FILE", "corralai_certify_key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(home, ".local", "state", "corral", "corralai_certify_key")); !os.IsNotExist(statErr) {
		t.Fatal("Path must never create the new file")
	}
}

func TestPerFileOverrideBypassesTheRefusal(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", map[string]string{"CORRALAI_CERTIFY_KEY_FILE": "/k"}, home)
	writeFile(t, filepath.Join(home, ".claude", "corralai_certify_key"))
	if p, err := Path(CertifyKey); err != nil || p != "/k" {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestStateDirPointedAtTheLegacyDirUsesFilesInPlace(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".claude")
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": legacy}, home)
	writeFile(t, filepath.Join(legacy, "corralai_coord.sqlite3"))
	if p, err := Path(CoordDB); err != nil || p != filepath.Join(legacy, "corralai_coord.sqlite3") {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestBothPresentResolvesToTheNewFile(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	writeFile(t, filepath.Join(home, ".claude", "corralai_coord.sqlite3"))
	newP := filepath.Join(home, ".local", "state", "corral", "corralai_coord.sqlite3")
	writeFile(t, newP)
	if p, err := Path(CoordDB); err != nil || p != newP {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestCacheRowIsNeverRefused(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	writeFile(t, filepath.Join(home, ".claude", "corralai_cache.duckdb"))
	if _, err := Path(CacheDB); err != nil {
		t.Fatalf("a cache row is rebuildable and must not be refused: %v", err)
	}
}

func TestEveryStateRowIsGuarded(t *testing.T) {
	for _, f := range Files() {
		if f.Root != State {
			continue
		}
		home := t.TempDir()
		fakeHost(t, "linux", nil, home)
		writeFile(t, filepath.Join(home, ".claude", f.Name))
		var le *LegacyError
		if _, err := Path(f); !errors.As(err, &le) {
			t.Errorf("%s: legacy-only file not refused (err=%v)", f.Name, err)
		}
	}
}

// A legacy directory corral cannot look into (root-owned after `sudo claude`,
// say) is not evidence the old key is absent. The refusal must fail CLOSED:
// treating "cannot tell" as "not there" lets the loader mint a second key.
func TestUnreadableLegacyDirIsRefusedNotTreatedAsAbsent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through mode 000")
	}
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	legacy := filepath.Join(home, ".claude")
	writeFile(t, filepath.Join(legacy, "corralai_certify_key"))
	if err := os.Chmod(legacy, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacy, 0o700) })

	_, err := Path(CertifyKey)
	if err == nil || !strings.Contains(err.Error(), "cannot tell whether") || !strings.Contains(err.Error(), legacy) {
		t.Fatalf("want a refusal naming %s, got %v", legacy, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".local", "state", "corral")); !os.IsNotExist(statErr) {
		t.Fatal("Path created something")
	}
}

// The same holds for the NEW location: an unreadable state root cannot say
// the file is absent there either.
func TestUnreadableStateRootIsRefusedNotTreatedAsAbsent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through mode 000")
	}
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	root := filepath.Join(home, ".local", "state", "corral")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, err := Path(CertifyKey); err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
		t.Fatalf("want a refusal, got %v", err)
	}
}
