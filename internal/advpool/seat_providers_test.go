// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"testing"
)

// The providers a verdict signs are those of the seats its roster names: a
// challenger never dispatched is not in the roster, so where it would have
// gone is not either, and a seat whose provider was never resolved is absent,
// never filled in.
func TestSignedSeatsCarryTheProvidersOfTheSeatsTheyName(t *testing.T) {
	run := &runState{rs: RunSpec{SeatProviders: map[string]string{
		RoleTestWriter:            "openai",
		RoleMutantGenerator:       "ollama",
		RoleMutantGeneratorShadow: "anthropic",
		RoleTestCritic:            "",
	}}}
	assign := RoleAssignment{RoleTestWriter: "gpt-x", RoleMutantGenerator: "qwen3:8b", RoleMutantGeneratorShadow: "claude-sonnet-5", RoleTestCritic: "gemini-3.6-pro"}
	seated := run.signedSeats(assign)
	want := map[string]string{RoleTestWriter: "openai", RoleMutantGenerator: "ollama"}
	if len(seated.providers) != len(want) {
		t.Fatalf("providers = %v, want %v", seated.providers, want)
	}
	for role, p := range want {
		if seated.providers[role] != p {
			t.Errorf("providers[%s] = %q, want %q", role, seated.providers[role], p)
		}
	}
}

// Both verdict construction sites carry the providers, and they reach the
// serialized verdict (the document a ledger row's VerdictJSON holds) under
// seat_providers. The timeout path is the one this package has forgotten a
// field on before.
func TestBothVerdictPathsSerializeTheSeatProviders(t *testing.T) {
	rs := RunSpec{Repo: "r", Commit: "c", Lang: "go", SeatProviders: map[string]string{RoleTestWriter: "openai", RoleMutantGenerator: "ollama"}}
	assign := RoleAssignment{RoleTestWriter: "gpt-x", RoleMutantGenerator: "qwen3:8b"}
	d := &Driver{Assign: assign}
	timeout := d.timeoutVerdict(&runState{rs: rs})
	converged := Verdict{}
	(&runState{rs: rs}).signedSeats(assign).stamp(&converged)
	for name, v := range map[string]Verdict{"timeout": timeout, "converged": converged} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SeatProviders map[string]string `json:"seat_providers"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.SeatProviders[RoleTestWriter] != "openai" || doc.SeatProviders[RoleMutantGenerator] != "ollama" {
			t.Errorf("%s verdict serialized seat_providers = %v, want the writer on openai and the generator on ollama", name, doc.SeatProviders)
		}
	}
	// A run whose seats resolved no provider (the brain's daemon path)
	// serializes no key at all: absent, never an empty claim.
	b, _ := json.Marshal((&Driver{Assign: assign}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r"}}))
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["seat_providers"]; ok {
		t.Errorf("a verdict with no resolved provider serialized seat_providers: %s", raw["seat_providers"])
	}
}
