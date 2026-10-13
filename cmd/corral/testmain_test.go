// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
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
	home, err := os.MkdirTemp("", "corral-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "CORRALAI_STATE_DIR", "CORRALAI_CACHE_DIR"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
