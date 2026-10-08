// SPDX-License-Identifier: Elastic-2.0

package lang

import "path/filepath"

// cmdIsShellWrapped reports the `sh -c '<script>'` shape, where argv is not
// the runner's argv at all and nothing may be appended to it.
//
// This is the single home for "is this argv a shell -c wrapper?". The shell
// is matched by basename, so `/bin/sh`, `/usr/bin/bash -e -c` and `dash -c`
// are all wrappers, and `-c` may sit anywhere after argv[0] to allow shell
// flags before it. Callers that need the script itself take the token after
// `-c` from the same argv; this function deliberately answers only the yes/no
// question so that no caller can disagree with another about it.
func cmdIsShellWrapped(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	switch filepath.Base(cmd[0]) {
	case "sh", "bash", "zsh", "dash":
		for _, a := range cmd[1:] {
			if a == "-c" {
				return true
			}
		}
	}
	return false
}
