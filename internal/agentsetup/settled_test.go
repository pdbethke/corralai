// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import "testing"

// A noop is either a satisfied end state (Settled) or Blocked: callers decide
// exit codes from the field, never from Note's wording.
func TestNoopSettledVersusBlocked(t *testing.T) {
	settled := func(name string, p Plan, want bool) {
		t.Helper()
		if len(p.Actions) == 0 || p.Actions[0].Kind != ActionNoop {
			t.Fatalf("%s: not a noop: %+v", name, p.Actions)
		}
		if p.Actions[0].Settled != want {
			t.Errorf("%s: Settled = %v, want %v (%q)", name, p.Actions[0].Settled, want, p.Actions[0].Note)
		}
	}
	settled("already registered", plan(t, []Status{st(t, "codex", func(s *Status) { s.Registered, s.Command = true, []string{tbin, "mcp"} })}, []string{"codex"}, false), true)
	settled("plugin", plan(t, []Status{st(t, "claude_code", func(s *Status) { s.ViaPlugin = true })}, []string{"claude_code"}, true), true)
	settled("not installed", plan(t, []Status{st(t, "codex", func(s *Status) { s.Installed = false })}, []string{"codex"}, true), false)
	settled("unreadable", plan(t, []Status{st(t, "codex", func(s *Status) { s.Unknown = "timed out" })}, []string{"codex"}, true), false)

	settled("uninstall: not added", uninstall(t, []Status{st(t, "codex", reg(tbin, "mcp"))}, []string{"codex"}, recorded()), true)
	settled("uninstall: unreadable", uninstall(t, []Status{st(t, "codex", func(s *Status) { s.Unknown = "timed out" })}, []string{"codex"}, recorded("codex")), false)
	settled("uninstall: gone already", uninstall(t, []Status{st(t, "codex", nil)}, []string{"codex"}, recorded("codex")), true)
}
