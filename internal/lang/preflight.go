// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
)

// toolOnPath reports a fail-closed error if the named executable is not on
// PATH — the toolchain a plugin needs to grade in the jail.
func toolOnPath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("lang: required tool %q not found on PATH: %w", name, err)
	}
	return nil
}
