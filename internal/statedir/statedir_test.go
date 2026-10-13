// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"errors"
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
	if len(Files()) != 24 {
		t.Fatalf("want 24 rows (spec Appendix A), got %d", len(Files()))
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
