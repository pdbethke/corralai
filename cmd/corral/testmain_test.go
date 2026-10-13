// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain gives every test in the package a throwaway HOME and a clean
// state-dir environment. Default paths now resolve through internal/statedir,
// which REFUSES a file that exists only at the legacy ~/.claude location. A
// developer's real HOME still holds exactly that, so without this the suite
// would pass in CI and fail on every workstation that has not migrated — and
// a test that resolved a default would be reading, or worse writing, real
// state. Individual tests that need a specific layout still t.Setenv over it.
//
// The Go toolchain's own caches are pinned to their real locations first:
// several tests run `go test` inside a fixture, and a fresh HOME would
// otherwise send it to an empty module cache.
// testHome is the throwaway HOME TestMain created; TestHomeIsIsolated asserts
// it is still in force so removing or bypassing the net is caught.
var testHome string

func TestMain(m *testing.M) {
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOFLAGS"} {
		if os.Getenv(k) != "" {
			continue
		}
		if out, err := exec.Command("go", "env", k).Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				_ = os.Setenv(k, v)
			}
		}
	}
	// The Rust toolchain is found under $HOME/.cargo and $HOME/.rustup when
	// these are unset (internal/adequacy/toolchain.go); pin them to the real
	// directories, if present, before HOME is replaced.
	if realHome, herr := os.UserHomeDir(); herr == nil {
		for k, d := range map[string]string{"CARGO_HOME": ".cargo", "RUSTUP_HOME": ".rustup"} {
			if os.Getenv(k) != "" {
				continue
			}
			p := filepath.Join(realHome, d)
			if fi, serr := os.Stat(p); serr == nil && fi.IsDir() {
				_ = os.Setenv(k, p)
			}
		}
	}
	home, err := os.MkdirTemp("", "corral-test-home-")
	if err != nil {
		panic(err)
	}
	testHome = home
	_ = os.Setenv("HOME", home)
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "CORRALAI_STATE_DIR", "CORRALAI_CACHE_DIR"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// TestHomeIsIsolated makes the safety net observable: a package whose TestMain
// stopped isolating HOME would otherwise pass silently on CI and fail only on
// workstations holding legacy state.
func TestHomeIsIsolated(t *testing.T) {
	if testHome == "" {
		t.Fatal("TestMain did not record an isolated HOME")
	}
	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != testHome || !strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("os.UserHomeDir() = %q, want the TestMain temp HOME %q", got, testHome)
	}
}
