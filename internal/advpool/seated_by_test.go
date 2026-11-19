// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"testing"
)

// How each seat was chosen (operator flag, ranking, pool order) and the
// evidence the ranking read travel RunSpec -> Verdict on BOTH construction
// paths, narrowed to the seats the roster names: a critic that is "off" was
// chosen by nothing, so it carries no basis.
func TestVerdictCarriesSeatedByOnBothConstructionPaths(t *testing.T) {
	const evidence = "the machine ledger (31 scans)"
	rs := RunSpec{Repo: "r", Commit: "c", Lang: "go",
		SeatedBy: map[string]string{
			RoleTestWriter: "evidence", RoleTestCritic: "order", RoleMutantGenerator: "operator",
		},
		SeatEvidence: evidence,
	}
	assign := RoleAssignment{RoleTestWriter: "w", RoleTestCritic: "off", RoleMutantGenerator: "g"}
	d := &Driver{Assign: assign}
	converged := Verdict{}
	(&runState{rs: rs}).signedSeats(assign).stamp(&converged)
	timeout := d.timeoutVerdict(&runState{rs: rs})
	for name, v := range map[string]Verdict{"converged": converged, "timeout": timeout} {
		if len(v.SeatedBy) != 2 || v.SeatedBy[RoleTestWriter] != "evidence" || v.SeatedBy[RoleMutantGenerator] != "operator" {
			t.Errorf("%s: SeatedBy = %v, want writer=evidence and generator=operator only (the off critic dropped)", name, v.SeatedBy)
		}
		if v.SeatEvidence != evidence {
			t.Errorf("%s: SeatEvidence = %q, want %q", name, v.SeatEvidence, evidence)
		}
		b, _ := json.Marshal(v)
		var doc struct {
			SeatedBy     map[string]string `json:"seated_by"`
			SeatEvidence string            `json:"seat_evidence"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.SeatedBy[RoleTestWriter] != "evidence" || doc.SeatEvidence != evidence {
			t.Errorf("%s: serialized %s", name, b)
		}
	}
	// Without --models nothing is set, and the verdict serializes as before.
	b, _ := json.Marshal(d.timeoutVerdict(&runState{rs: RunSpec{Repo: "r"}}))
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"seated_by", "seat_evidence"} {
		if _, ok := raw[k]; ok {
			t.Errorf("a verdict with no seating basis serialized %s", k)
		}
	}
}
