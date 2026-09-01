// SPDX-License-Identifier: Elastic-2.0

//go:build !windows

package sandbox

import "errors"

func newWindowsJobIsolator() (Isolator, error) {
	return nil, errors.New("windows-job is Windows-only")
}
