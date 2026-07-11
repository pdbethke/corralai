// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

// TestDesktopBrowserURLIsLocalLoopbackNeverCarriesToken is the regression
// guard for the bearer-token-in-URL leak: the old launchApp pointed the
// browser straight at the daemon with `?token=...` appended, which put the
// bearer into browser history / referrer / daemon access logs. desktop must
// instead always point the browser at the LOCAL console host's loopback
// address, and the URL must never carry a token in any form. Written to FAIL
// against the old launchApp behavior (which appended "?token=" + token).
func TestDesktopBrowserURLIsLocalLoopbackNeverCarriesToken(t *testing.T) {
	const sampleToken = "super-secret-bearer-token"
	got := desktopBrowserURL("127.0.0.1:54321")

	if got != "http://127.0.0.1:54321" {
		t.Errorf("desktopBrowserURL(%q) = %q, want %q", "127.0.0.1:54321", got, "http://127.0.0.1:54321")
	}
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("desktopBrowserURL result %q must start with http://127.0.0.1: (loopback only)", got)
	}
	if strings.Contains(got, "token") {
		t.Errorf("desktopBrowserURL result %q must never contain %q", got, "token")
	}
	if strings.Contains(got, sampleToken) {
		t.Errorf("desktopBrowserURL result %q must never contain a bearer token value", got)
	}
}
