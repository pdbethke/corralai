// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/creds"
)

// TestFetchEnvCarriesRoutesNotLocationsOrSecrets: the networked vendoring
// call needs the operator's proxy and module-proxy routes, must not be
// pointed at host directories the jail does not mount, and — like every
// jailed command — must never see corral's own credentials.
func TestFetchEnvCarriesRoutesNotLocationsOrSecrets(t *testing.T) {
	for _, k := range fetchVars {
		t.Setenv(k, "route-"+k)
	}
	locations := []string{"GOPATH", "GOMODCACHE", "GOCACHE", "CARGO_HOME", "RUSTUP_HOME"}
	for _, k := range locations {
		t.Setenv(k, "/host/"+k)
	}
	secrets := append(append([]string{}, CorralOwnSecrets...), creds.CanonicalNames...)
	for _, k := range secrets {
		t.Setenv(k, "leaked-"+k)
	}

	env := FetchEnv()
	for _, k := range fetchVars {
		if !slices.Contains(env, k+"=route-"+k) {
			t.Errorf("%s must reach the fetch", k)
		}
	}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(locations, k) {
			t.Errorf("%s names a host location the jail does not mount; it must not be forwarded", k)
		}
		if slices.Contains(secrets, k) {
			t.Errorf("%s is a corral secret and reached the fetch", k)
		}
	}
	for _, kv := range MinimalEnv() {
		if !slices.Contains(env, kv) {
			t.Errorf("FetchEnv must extend MinimalEnv; missing %s", kv)
		}
	}
}

// TestFetchEnvDisablesRustupAutoInstall: in the networked step a repository's
// rust-toolchain.toml must not make the rustup proxy download and run a
// toolchain of the repository's choosing. RUSTUP_AUTO_INSTALL=0 (rustup
// >= 1.28; measured on 1.28.2: "toolchain '1.80.0-…' is not installed"
// instead of "syncing channel updates") turns that into a refusal. The
// operator's own value never overrides it.
func TestFetchEnvDisablesRustupAutoInstall(t *testing.T) {
	t.Setenv("RUSTUP_AUTO_INSTALL", "1")
	env := FetchEnv()
	if !slices.Contains(env, "RUSTUP_AUTO_INSTALL=0") || slices.Contains(env, "RUSTUP_AUTO_INSTALL=1") {
		t.Errorf("FetchEnv must pin RUSTUP_AUTO_INSTALL=0; got %v", env)
	}
}
