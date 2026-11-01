// SPDX-License-Identifier: Elastic-2.0

//go:build !linux

package hoststat

import (
	"testing"
	"time"
)

// spinCPU is Linux-only: the only caller skips on other platforms first.
func spinCPU(t *testing.T, _ time.Duration) {
	t.Helper()
	t.Skip("CPU-time spin uses getrusage, read on Linux only")
}
