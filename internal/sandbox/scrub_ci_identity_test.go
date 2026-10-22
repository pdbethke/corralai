// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
)

// ciIdentity is named here, not derived from CIIdentityTokens, so deleting
// an entry from the list fails this test instead of shrinking it.
var ciIdentity = []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_RUNTIME_TOKEN"}

func setCIIdentity(t *testing.T) {
	t.Helper()
	for _, n := range ciIdentity {
		t.Setenv(n, "zz-the-runs-identity")
	}
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("MY_SUITE_NEEDS_THIS", "yes")
}

func envHas(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// A GitHub Actions job granted id-token: write exports the means to mint an
// OIDC token for the repository. Keyless ledger signing turns that token into
// a Fulcio certificate naming the repository, so code the audit runs — the
// suite, a model-written test, a review's reproduction script — that could
// read these could sign a ledger entry as the repository. Corral's own
// signer reads them from its own process; no child may.
func TestScrubbedEnvironDropsTheCIIdentityTokens(t *testing.T) {
	setCIIdentity(t)
	got := ScrubbedEnviron()
	for _, n := range ciIdentity {
		if envHas(got, n) {
			t.Errorf("%s reached the audited suite: it could sign a ledger entry as the repository", n)
		}
	}
	if !envHas(got, "MY_SUITE_NEEDS_THIS") || !envHas(got, "PATH") {
		t.Error("the suite lost the ordinary environment it needs")
	}
}

func TestWithoutCIIdentityDropsOnlyTheCIIdentity(t *testing.T) {
	in := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=k", "ACTIONS_ID_TOKEN_REQUEST_URL=u", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=t", "ACTIONS_RUNTIME_TOKEN=r"}
	got := WithoutCIIdentity(in)
	for _, n := range ciIdentity {
		if envHas(got, n) {
			t.Errorf("%s kept", n)
		}
	}
	if !envHas(got, "PATH") || !envHas(got, "ANTHROPIC_API_KEY") {
		t.Errorf("dropped more than the CI identity: %v", got)
	}
}

// The jail's environments are allow-lists; this pins that they stay so for
// the CI identity.
func TestJailEnvironmentsNeverCarryTheCIIdentity(t *testing.T) {
	setCIIdentity(t)
	for name, env := range map[string][]string{"MinimalEnv": MinimalEnv(), "FetchEnv": FetchEnv()} {
		for _, n := range ciIdentity {
			if envHas(env, n) {
				t.Errorf("%s carries %s", name, n)
			}
		}
	}
}
