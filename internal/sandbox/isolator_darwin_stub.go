//go:build !darwin

package sandbox

import "errors"

func newSandboxExecIsolator() (Isolator, error) {
	return nil, errors.New("sandbox-exec is macOS-only")
}
