// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pytest probe runs the operator's interpreter on the host, inside the
// repository, before any jail — and pytest loads the repository's conftest.py
// even for --version. It must not hand that code the GitHub Actions OIDC
// request credentials. The fake interpreter prints its environment and
// fails, so Preflight's error carries what it saw.
func TestPythonPreflightProbeNeverCarriesTheCIIdentity(t *testing.T) {
	for _, n := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_RUNTIME_TOKEN"} {
		t.Setenv(n, "zz-the-runs-identity")
	}
	t.Setenv("PROBE_SEES_THIS", "yes")
	fake := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nenv\nexit 1\n"), 0o755); err != nil { // #nosec G306 -- a test's own executable stub
		t.Fatal(err)
	}
	err := pyPlugin{}.Preflight("", []string{fake, "-m", "pytest", "-q"})
	if err == nil {
		t.Fatal("the failing probe returned no error")
	}
	if strings.Contains(err.Error(), "zz-the-runs-identity") || !strings.Contains(err.Error(), "PROBE_SEES_THIS=yes") {
		t.Errorf("the probe received the CI identity, or lost its environment: %v", err)
	}
}
