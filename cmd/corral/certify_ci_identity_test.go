// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// `corral certify -- <cmd>` runs the project's check command on the host;
// it never needs, and must never see, the GitHub Actions OIDC request
// credentials corral's own keyless signer reads.
func TestCertifyCheckCommandNeverCarriesTheCIIdentity(t *testing.T) {
	for _, n := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_RUNTIME_TOKEN"} {
		t.Setenv(n, "zz-the-runs-identity")
	}
	t.Setenv("CHECK_NEEDS_THIS", "yes")
	var out bytes.Buffer
	if _, _, _, err := (realRunner{}).RunCommand([]string{"sh", "-c", "env"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "zz-the-runs-identity") || !strings.Contains(out.String(), "CHECK_NEEDS_THIS=yes") {
		t.Errorf("the check command received the CI identity, or lost its environment:\n%s", out.String())
	}
}
