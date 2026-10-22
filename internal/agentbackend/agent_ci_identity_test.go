// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"context"
	"strings"
	"testing"
)

// An agent seat is a model with tools, run on the host. It inherits corral's
// environment because the CLI needs its own login and token variables — but
// never the GitHub Actions OIDC request credentials, with which it could
// mint a certificate for the repository and sign a ledger entry as it.
func TestAgentCommandNeverCarriesTheCIIdentity(t *testing.T) {
	for _, n := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_RUNTIME_TOKEN"} {
		t.Setenv(n, "zz-the-runs-identity")
	}
	t.Setenv("AGENT_NEEDS_THIS", "yes")
	out, err := agentCommand(context.Background(), []string{"sh", "-c", "env"}).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "zz-the-runs-identity") || !strings.Contains(string(out), "AGENT_NEEDS_THIS=yes") {
		t.Errorf("the agent seat received the CI identity, or lost its environment:\n%s", out)
	}
}
