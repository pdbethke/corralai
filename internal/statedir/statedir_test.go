// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHost(t *testing.T, goos string, env map[string]string, home string) {
	t.Helper()
	old := host
	host = platform{
		goos:   goos,
		getenv: func(k string) string { return env[k] },
		home: func() (string, error) {
			if home == "" {
				return "", errors.New("no home")
			}
			return home, nil
		},
	}
	t.Cleanup(func() { host = old })
}

func TestRootDefaults(t *testing.T) {
	cases := []struct {
		goos string
		env  map[string]string
		root Root
		want string
	}{
		{"linux", nil, State, "/h/.local/state/corral"},
		{"linux", map[string]string{"XDG_STATE_HOME": "/x"}, State, "/x/corral"},
		{"linux", map[string]string{"XDG_STATE_HOME": "rel"}, State, "/h/.local/state/corral"},
		{"linux", map[string]string{"CORRALAI_STATE_DIR": "/s", "XDG_STATE_HOME": "/x"}, State, "/s"},
		{"linux", map[string]string{"CORRALAI_STATE_DIR": "   "}, State, "/h/.local/state/corral"},
		{"darwin", nil, State, "/h/Library/Application Support/corral"},
		{"windows", map[string]string{"LocalAppData": `C:\L`}, State, filepath.Join(`C:\L`, "corral", "state")},
		{"linux", nil, Cache, "/h/.cache/corral"},
		{"linux", map[string]string{"XDG_CACHE_HOME": "/c"}, Cache, "/c/corral"},
		{"linux", map[string]string{"XDG_CACHE_HOME": "rel"}, Cache, "/h/.cache/corral"},
		{"linux", map[string]string{"CORRALAI_CACHE_DIR": "/cc"}, Cache, "/cc"},
		{"darwin", nil, Cache, "/h/Library/Caches/corral"},
		{"windows", map[string]string{"LocalAppData": `C:\L`}, Cache, filepath.Join(`C:\L`, "corral", "cache")},
		{"linux", nil, Config, "/h/.config/corral"},
		{"linux", map[string]string{"XDG_CONFIG_HOME": "/g"}, Config, "/g/corral"},
		{"linux", map[string]string{"CORRAL_CREDS_DIR": "/cr"}, Config, "/cr"},
		{"darwin", nil, Config, "/h/Library/Application Support/corral"},
		{"windows", map[string]string{"AppData": `C:\R`}, Config, filepath.Join(`C:\R`, "corral")},
	}
	for _, c := range cases {
		fakeHost(t, c.goos, c.env, "/h")
		got, err := Dir(c.root)
		if err != nil || got != c.want {
			t.Errorf("%s %v %s: Dir = %q, %v; want %q", c.goos, c.env, c.root, got, err, c.want)
		}
	}
}

func TestNoHomeNoXDGIsAnErrorNamingTheOverride(t *testing.T) {
	for _, r := range []Root{State, Cache, Config} {
		fakeHost(t, "linux", nil, "")
		_, err := Dir(r)
		if err == nil {
			t.Fatalf("%s: want error with no HOME and no XDG", r)
		}
		if want := r.OverrideVar(); !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not name %s", r, err, want)
		}
	}
}

func TestPathPrecedence(t *testing.T) {
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s"}, "/h")
	if got, _ := Path(CoordDB); got != "/s/corralai_coord.sqlite3" {
		t.Errorf("root override: got %q", got)
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s", "CORRALAI_DB": "/own.sqlite3"}, "/h")
	if got, _ := Path(CoordDB); got != "/own.sqlite3" {
		t.Errorf("per-file override must win: got %q", got)
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s", "CORRALAI_DB": "  "}, "/h")
	if got, _ := Path(CoordDB); got != "/s/corralai_coord.sqlite3" {
		t.Errorf("whitespace override is unset: got %q", got)
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_CACHE_DIR": "/c"}, "/h")
	if got, _ := Path(CacheDB); got != "/c/corralai_cache.duckdb" {
		t.Errorf("CacheDB lives in Cache: got %q", got)
	}
}

func TestTableIsWellFormed(t *testing.T) {
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s", "CORRALAI_CACHE_DIR": "/c"}, "/h")
	if len(Files()) != 25 {
		t.Fatalf("want 25 rows (spec Appendix A plus the machine ledger), got %d", len(Files()))
	}
	seenPath, seenVar := map[string]File{}, map[string]File{}
	for _, f := range Files() {
		if f.Name == "" || f.Override == "" || f.Root == Config {
			t.Errorf("row %+v: every row has a name and an override and lives in State or Cache", f)
		}
		p, err := Path(f)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if prev, dup := seenPath[p]; dup {
			t.Errorf("%s and %s resolve to the same path %s", prev.Name, f.Name, p)
		}
		if prev, dup := seenVar[f.Override]; dup {
			t.Errorf("%s and %s share override %s", prev.Name, f.Name, f.Override)
		}
		seenPath[p], seenVar[f.Override] = f, f
	}
}

// On a fresh machine the state root does not exist, and DuckDB will not
// create a file in a missing directory: every CLI store failed to open (or
// silently dropped its rows). PathForWrite is the one door that makes the
// parent, 0700, after — and only after — the refusal has passed.
func TestPathForWriteCreatesTheParentPrivately(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	p, err := PathForWrite(BugCatchDB)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatalf("parent not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode %v, want 0700", fi.Mode().Perm())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("PathForWrite must create the directory, never the file")
	}
}

func TestPathForWriteCreatesNothingWhenRefused(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	writeFile(t, filepath.Join(home, ".claude", "corralai_bugcatch.duckdb"))
	if _, err := PathForWrite(BugCatchDB); err == nil {
		t.Fatal("want the legacy refusal")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "corral")); !os.IsNotExist(err) {
		t.Fatal("a refused PathForWrite created the state root")
	}
}

// Path is the read side and stays free of side effects.
func TestPathCreatesNothing(t *testing.T) {
	home := t.TempDir()
	fakeHost(t, "linux", nil, home)
	if _, err := Path(BugCatchDB); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatal("Path created a directory")
	}
}

func TestMachineLedgerDeclared(t *testing.T) {
	found := false
	for _, f := range Files() {
		if f == MachineLedger {
			found = true
		}
	}
	if !found {
		t.Fatal("MachineLedger is not in the table, so `corral state paths` would not list it")
	}
	if !MachineLedger.IsDir() {
		t.Error("MachineLedger is a directory and must say so")
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s"}, "/h")
	if got, _ := Path(MachineLedger); got != "/s/ledger" {
		t.Errorf("default location: got %q, want /s/ledger", got)
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": "/s", "CORRALAI_MACHINE_LEDGER": "/elsewhere/l"}, "/h")
	if got, _ := Path(MachineLedger); got != "/elsewhere/l" {
		t.Errorf("CORRALAI_MACHINE_LEDGER must override: got %q", got)
	}
}

// A directory named "ledger" in the legacy location must not be planned as a
// move (rename of a directory would skip the lock probe and sidecar logic
// built for files) nor treated as a legacy-only state file.
func TestMigrateAndLegacyIgnoreTheMachineLedgerDirectory(t *testing.T) {
	_, legacy, _ := setupMigration(t)
	writeFile(t, filepath.Join(legacy, "ledger", "entry.json"))
	moves, conflicts, err := PlanMigration(legacy, PlanOptions{})
	if err != nil || len(conflicts) != 0 || len(moves) != 2 {
		t.Fatalf("moves=%v conflicts=%v err=%v; want the 2 file rows only", moves, conflicts, err)
	}
	for _, m := range moves {
		if m.File.IsDir() {
			t.Errorf("planned a move of the directory row: %+v", m)
		}
	}
	if _, err := Path(MachineLedger); err != nil {
		t.Errorf("a legacy dir named ledger must not refuse the machine ledger: %v", err)
	}
}
