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
