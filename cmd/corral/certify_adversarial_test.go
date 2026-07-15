// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"testing"
)

func TestAdvVerdictDecodesToolPayload(t *testing.T) {
	// Exactly what get_adversarial_run marshals (advpool.Verdict has no json
	// tags -> capitalized keys; VacuousFindings elements use queue.Finding's
	// lowercase tags).
	payload := `{
	  "run_id": 7, "found": true, "converged": true,
	  "verdict": {
	    "Repo": "pdbethke/corralai", "Commit": "88b6ff7",
	    "DevKillRate": 0.5, "MutantsTotal": 8, "Survivors": 4, "ProvenMissed": 2,
	    "VacuousFindings": [
	      {"type": "note", "severity": "high", "target": "TestValidatePassword",
	       "evidence": "calls ValidatePassword without checking its input"}
	    ],
	    "ModelsByRole": {"test-writer": "qwen2.5-coder:7b", "test-critic": "llama3.2:3b"},
	    "Status": "needs-review", "RecordID": 41, "RecordHead": "head41"
	  }
	}`
	var st advStatus
	if err := json.Unmarshal([]byte(payload), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.Converged || st.Verdict == nil {
		t.Fatalf("converged=%v verdict=%v", st.Converged, st.Verdict)
	}
	v := st.Verdict
	if v.DevKillRate != 0.5 || v.MutantsTotal != 8 || v.Survivors != 4 || v.ProvenMissed != 2 {
		t.Fatalf("numbers wrong: %+v", v)
	}
	if v.Status != "needs-review" || v.RecordID != 41 || v.RecordHead != "head41" {
		t.Fatalf("status/record wrong: %+v", v)
	}
	if len(v.VacuousFindings) != 1 || v.VacuousFindings[0].Target != "TestValidatePassword" {
		t.Fatalf("findings wrong: %+v", v.VacuousFindings)
	}
	if v.ModelsByRole["test-writer"] != "qwen2.5-coder:7b" {
		t.Fatalf("models wrong: %+v", v.ModelsByRole)
	}
}
