// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import "testing"

// TestSelfTestRealBackendPassesViaResolve proves the REAL self-test passes
// through Resolve (the path every caller takes) on this host.
func TestSelfTestRealBackendPassesViaResolve(t *testing.T) {
	resetSelfTestCache()
	t.Cleanup(resetSelfTestCache)
	backend := "bwrap"
	if "linux" == "darwin" {
		backend = "sandbox-exec"
	}
	if _, err := Resolve(Config{Backend: backend}); err != nil {
		t.Fatalf("Resolve(%s) = %v, want the real self-test to pass", backend, err)
	}
}
