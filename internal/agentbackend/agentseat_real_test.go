// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"testing"
)

// TestBuiltinSeatsStartCold pins the PROPERTY spec §3 rests on: an agent seat
// does not carry the operator's own setup (instructions, memory, skills, MCP
// servers) into an audit. TestBuiltinSeatDefinitionsCarryTheColdFlags pins
// the flag SPELLING; this one pins what the flags are for, by the input
// tokens the real CLI reports for a one-line prompt. A flag the CLI stops
// honouring passes the spelling test and fails this one.
//
// It runs the real CLIs on the operator's subscription, one call per agent
// per run and no retries, so it is gated behind CORRALAI_REAL_AGENT_TESTS=1
// and CI skips it with a stated reason rather than spending. The ceiling is
// per agent, data on the built-in (coldCeiling): codex's own agent prompt is
// ~14.6k tokens, so one number cannot serve both. A ceiling of 0 means the
// agent is unmeasured and is skipped. CORRALAI_COLD_SEAT_CEILING overrides
// the ceiling for every agent.
//
// A seat that cannot run (ErrSeatEnvironment) SKIPs; it never counts as a
// pass of the property.
func TestBuiltinSeatsStartCold(t *testing.T) {
	if os.Getenv("CORRALAI_REAL_AGENT_TESTS") != "1" {
		t.Skip("SKIP: CORRALAI_REAL_AGENT_TESTS unset — real agent CLIs run on the operator's subscription")
	}
	override := 0
	if v := os.Getenv("CORRALAI_COLD_SEAT_CEILING"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("CORRALAI_COLD_SEAT_CEILING=%q is not a positive integer", v)
		}
		override = n
	}
	names := make([]string, 0, len(builtinAgents))
	for name := range builtinAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := builtinAgents[name]
		t.Run(name, func(t *testing.T) {
			ceiling := b.coldCeiling
			if override > 0 {
				ceiling = override
			}
			if ceiling == 0 {
				t.Skipf("SKIP: %s has no measured cold ceiling", name)
			}
			if _, err := exec.LookPath(b.binary); err != nil {
				t.Skipf("SKIP: %s not installed", name)
			}
			// SeatOptions{} runs the seat from a fresh empty temp dir.
			seat, err := ForSeat(name, SeatOptions{})
			if err != nil {
				t.Fatalf("ForSeat(%s): %v", name, err)
			}
			m, err := seat.Chat([]Message{{Role: "user", Content: "Reply with the single word: ok"}}, nil)
			if errors.Is(err, ErrSeatEnvironment) {
				t.Skipf("SKIP: %s not logged in / environment: %v", name, err)
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if m.Usage.Unmetered {
				t.Fatalf("%s reported no token usage; the property cannot be measured", name)
			}
			t.Logf("%s: %d input tokens (ceiling %d)", name, m.Usage.InputTokens, ceiling)
			if m.Usage.InputTokens > ceiling {
				t.Errorf("%s carried %d input tokens for a one-line prompt, over its cold ceiling %d: the seat is loading the operator's own setup", name, m.Usage.InputTokens, ceiling)
			}
		})
	}
}
