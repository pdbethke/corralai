// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import "testing"

// TestSelfTestRealBackendPassesViaResolve proves the REAL self-test passes
// through Resolve (the path every caller takes) on this host. It skips when
// sandbox-exec is unavailable (a CI runner without it); a self-test failure on a host
// that has it is a real jail defect and fails loudly.
func TestSelfTestRealBackendPassesViaResolve(t *testing.T) {
	if err := (sandboxExecIsolator{}).Preflight(); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	resetSelfTestCache()
	t.Cleanup(resetSelfTestCache)
	if _, err := Resolve(Config{Backend: "sandbox-exec"}); err != nil {
		t.Fatalf("Resolve(sandbox-exec) = %v, want the real self-test to pass", err)
	}
}
