// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
)

// The workspace substrate runs the audited suite on the HOST, once per
// mutant. Handed the operator's desktop session, a suite with UI tests opens
// real windows on their screen, over and over: Warp's integration tests did
// exactly that through its own presubmit on 2026-10-21 ("its spawning a real
// term window", "i thought i was being hacked"). The session's handles are
// dropped; a suite that needs a display brings its own (xvfb-run in the test
// command sets DISPLAY inside it, after this environment is built).
func TestScrubbedEnvironDropsTheDesktopSession(t *testing.T) {
	// Named explicitly, not derived from the list under test (see
	// TestScrubbedEnvironDropsEveryCorralCredential for why).
	mustDrop := []string{"DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"}
	for _, name := range mustDrop {
		t.Setenv(name, "the-operators-desktop")
	}
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000") // not a display by itself; tools need it for their own runtime files

	joined := "\n" + strings.Join(ScrubbedEnviron(), "\n") + "\n"
	for _, name := range mustDrop {
		if strings.Contains(joined, "\n"+name+"=") {
			t.Errorf("%s reached the audited suite: it can draw on the operator's screen", name)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "XDG_RUNTIME_DIR=/run/user/1000"} {
		if !strings.Contains(joined, "\n"+keep+"\n") {
			t.Errorf("%s was dropped; the suite needs it", keep)
		}
	}
}
