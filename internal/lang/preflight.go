// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
	"strings"
)

// toolOnPath reports a fail-closed error if the named executable is not on
// PATH — the toolchain a plugin needs to grade in the jail. exec.LookPath
// already does the right thing for BOTH shapes this is called with: a bare
// name ("python3") is searched on PATH, and a name containing a path
// separator (".venv/bin/python", "/abs/path/to/ruby") is tried directly, PATH
// not consulted — exactly "is this binary the operator NAMED runnable",
// whichever form they gave it in.
func toolOnPath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("lang: required tool %q not found on PATH: %w", name, err)
	}
	return nil
}

// preflightBin picks which binary Preflight checks presence of: the
// operator's own test command's first token when testCmd is non-empty — it
// names exactly what will run, see Plugin.Preflight's doc comment — else
// fallback, the plugin's stock default. Used by every plugin whose
// Preflight only needs a presence check (go, ruby, javascript); python and
// typescript additionally probe deeper (pytest importability / tsc).
func preflightBin(testCmd []string, fallback string) string {
	if len(testCmd) > 0 && strings.TrimSpace(testCmd[0]) != "" {
		return testCmd[0]
	}
	return fallback
}
