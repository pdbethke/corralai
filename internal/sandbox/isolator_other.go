// SPDX-License-Identifier: Elastic-2.0

//go:build !linux

package sandbox

import "errors"

// newBwrapIsolator is a stub for non-Linux platforms. bwrap relies on Linux
// user namespaces and is not available outside Linux.
func newBwrapIsolator() (Isolator, error) {
	return nil, errors.New("bwrap backend is Linux-only; use container or none")
}
