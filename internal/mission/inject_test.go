// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"strings"
	"testing"
)

func TestInjectHerdContext(t *testing.T) {
	plan := []PhaseSpec{
		{Name: "design", Role: "designer", Instruction: "design the UI"},
		{Name: "build", Role: "builder", Instruction: "build it"},
		{Name: "scan", Role: "pentester", Instruction: "attack it"},
	}
	out := InjectHerdContext(plan, []string{"Emulate the neon dashboard mock."}, []string{"prod-db", "search"})

	// Endpoint note goes to EVERY role.
	for _, p := range out {
		if !strings.Contains(p.Instruction, "prod-db") || !strings.Contains(p.Instruction, "search") {
			t.Fatalf("%s missing endpoint note: %q", p.Role, p.Instruction)
		}
	}
	// Lookbook goes to designer/builder/reviewer, NOT pentester.
	if !strings.Contains(out[0].Instruction, "neon dashboard") {
		t.Fatalf("designer missing lookbook: %q", out[0].Instruction)
	}
	if !strings.Contains(out[1].Instruction, "neon dashboard") {
		t.Fatalf("builder missing lookbook: %q", out[1].Instruction)
	}
	if strings.Contains(out[2].Instruction, "neon dashboard") {
		t.Fatalf("pentester should NOT get the lookbook directive: %q", out[2].Instruction)
	}
	// Original instruction is preserved (prepended, not replaced).
	if !strings.Contains(out[1].Instruction, "build it") {
		t.Fatalf("original instruction lost: %q", out[1].Instruction)
	}
}

func TestInjectHerdContextNoOp(t *testing.T) {
	plan := []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}
	out := InjectHerdContext(plan, nil, nil)
	if out[0].Instruction != "build it" {
		t.Fatalf("no herd context must leave instructions untouched, got %q", out[0].Instruction)
	}
}
