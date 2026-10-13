// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// The tool versions a verdict signs are the seats its roster names: a
// challenger that never ran is not in the roster, so its tool is not either,
// and an API seat carries no tool.
func TestSignedSeatsCarryTheToolsOfTheSeatsTheyName(t *testing.T) {
	run := &runState{rs: RunSpec{SeatTools: map[string]string{
		RoleTestWriter:            "codex-cli 0.160.0",
		RoleMutantGeneratorShadow: "codex-cli 0.160.0",
		RoleTestCritic:            "",
	}}}
	assign := RoleAssignment{RoleTestWriter: "codex", RoleMutantGenerator: "gpt-6", RoleMutantGeneratorShadow: "codex", RoleTestCritic: "claude-sonnet-5"}
	roster, _, tools := run.signedSeats(assign)
	if _, ok := roster[RoleMutantGeneratorShadow]; ok {
		t.Fatal("an undispatched challenger is not in the roster")
	}
	if len(tools) != 1 || tools[RoleTestWriter] != "codex-cli 0.160.0" {
		t.Fatalf("tools = %v, want only the writer's", tools)
	}
}

// Observations carry whether their seat names a model, by the one recorded
// rule — an unpinned agent seat's row is marked so per-model analysis can
// leave it out; a pinned one and an API seat are not.
func TestBugCatchObservationsMarkAnUnresolvedSeat(t *testing.T) {
	run := &runState{rs: RunSpec{}}
	v := Verdict{
		ModelsByRole: map[string]string{RoleTestWriter: "codex", RoleMutantGenerator: "codex:gpt-6", RoleTestCritic: "claude-sonnet-5"},
		SeatTools:    map[string]string{RoleTestWriter: "codex-cli 1", RoleMutantGenerator: "codex-cli 1"},
	}
	for _, o := range bugCatchObservations(run, v) {
		want := o.Role == RoleTestWriter
		if o.ModelUnresolved != want {
			t.Errorf("%s (%s): ModelUnresolved = %v, want %v", o.Role, o.Model, o.ModelUnresolved, want)
		}
		if o.SeatTool != v.SeatTools[o.Role] {
			t.Errorf("%s: SeatTool = %q, want %q", o.Role, o.SeatTool, v.SeatTools[o.Role])
		}
	}
}
