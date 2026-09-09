// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/creds"
)

// The workspace substrate hands the AUDITED repository's own test command
// this environment. Every credential corral sets or reads must be gone from
// it — including corral's OWN bearer token, which the provider-key scrub
// left behind because the drop set was an enumeration.
func TestScrubbedEnvironDropsEveryCorralCredential(t *testing.T) {
	// NAMED explicitly, not derived from the list under test. Deriving the
	// expectation from CorralOwnSecrets makes this vacuous: delete an entry
	// and the test stops checking for it and still passes — which is what a
	// negative control caught this test doing on its first version. The
	// derived set below is a second, weaker assertion on top.
	mustDrop := []string{
		"CORRAL_TOKEN", // the agent launcher's BEARER token
		"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "OPENAI_API_KEY",
		"GOOGLE_API_KEY", "MOTHERDUCK_TOKEN",
	}
	var want []string
	want = append(want, mustDrop...)
	want = append(want, CorralOwnSecrets...)
	want = append(want, creds.CanonicalNames...)

	for _, name := range want {
		t.Setenv(name, "sk-should-never-survive")
	}
	// and something the suite legitimately needs, which must survive
	t.Setenv("PROJECT_OWN_API_KEY", "the audited repo's own")

	got := ScrubbedEnviron()
	joined := strings.Join(got, "\n")
	for _, name := range want {
		if strings.Contains(joined, name+"=") {
			t.Errorf("%s survived into the environment handed to the audited repo's test command", name)
		}
	}
	if !strings.Contains(joined, "PROJECT_OWN_API_KEY=") {
		t.Error("PROJECT_OWN_API_KEY was dropped — the suite needs its own environment; this must be a named denylist, not a heuristic over key-shaped names")
	}
}
