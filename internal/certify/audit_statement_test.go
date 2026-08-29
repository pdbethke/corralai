// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"encoding/json"
	"testing"
)

func killRate(f float64) *float64 { return &f }

// The statement has to be a valid in-toto Statement, because GitHub's
// attestation API is the consumer and it will reject anything else.
func TestAuditAttestationIsAnInTotoStatement(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "pdbethke/sportspicker-core", Commit: "abc123",
		Files:   []AuditedFile{{Path: "a.py", KillRate: killRate(0.9), Survivors: 2, ProvenMissed: 1}},
		Audited: 1, Candidates: 11, Passed: false,
	})

	if got["_type"] != "https://in-toto.io/Statement/v1" {
		t.Errorf("_type = %v", got["_type"])
	}
	// Deliberately NOT slsa provenance: this is a test-adequacy audit, and a
	// verifier filtering by predicate type must be able to tell the two apart.
	if got["predicateType"] != AuditPredicateType {
		t.Errorf("predicateType = %v, want %s", got["predicateType"], AuditPredicateType)
	}
	subj := got["subject"].([]map[string]any)[0]
	if subj["name"] != "pdbethke/sportspicker-core" {
		t.Errorf("subject name = %v", subj["name"])
	}
	// The subject is the audited COMMIT — the revision a reviewer is being
	// asked to merge — under in-toto's own key for it.
	if subj["digest"].(map[string]string)["gitCommit"] != "abc123" {
		t.Errorf("subject digest = %v", subj["digest"])
	}
}

// The honesty flags must travel WITH the number they qualify. provenMissed: 0
// means "nothing was proven" rather than "the suite is clean" whenever one is
// set, and a consumer reading the number alone would conclude the opposite.
func TestHonestyFlagsTravelWithTheNumbers(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{{Path: "a.py", Survivors: 4, ProvenMissed: 0, TestWriterFailed: true}},
	})
	pred := got["predicate"].(map[string]any)
	f := pred["files"].([]map[string]any)[0]
	if f["testWriterFailed"] != true {
		t.Errorf("a zero that means 'nothing was proven' shipped without its qualifier: %#v", f)
	}
}

// "passed: true" is unreadable without the bar it cleared.
func TestGatesAreRecordedAlongsideTheVerdict(t *testing.T) {
	rate, gaps := 0.8, 0
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c", MinKillRate: &rate, MaxProvenMissed: &gaps, Passed: true,
	})
	gates := got["predicate"].(map[string]any)["gates"].(map[string]any)
	if gates["minKillRate"] != 0.8 || gates["maxProvenMissed"] != 0 {
		t.Errorf("gates not recorded: %#v", gates)
	}
}

// The denominator ships too: "1 file clean" reads differently out of 11.
func TestScopeCarriesTheDenominator(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{Repo: "r", Commit: "c", Audited: 1, Candidates: 11})
	scope := got["predicate"].(map[string]any)["scope"].(map[string]any)
	if scope["audited"] != 1 || scope["candidates"] != 11 {
		t.Errorf("scope = %#v", scope)
	}
}

// And it has to round-trip as JSON, since that is how it reaches the action.
func TestAuditAttestationMarshals(t *testing.T) {
	b, err := json.Marshal(BuildAuditAttestation(AuditStatement{Repo: "r", Commit: "c"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// An UNCOVERED file has no rate to sign. The report withholds it and the
// ledger stores NULL; the attestation is the one artifact a third party
// verifies, so a 0.0 signed here would be the withheld number coming back as
// a fact — with a signature on it.
func TestUncoveredFileSignsNoKillRate(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{
			{Path: "pkg/u.py", Survivors: 2, Uncovered: true, TestSelection: "coverage-context", SuiteTests: 1431},
			{Path: "pkg/a.py", KillRate: killRate(0.65), Survivors: 4, TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
		},
	})
	files := got["predicate"].(map[string]any)["files"].([]map[string]any)
	u := files[0]
	if _, ok := u["killRate"]; ok {
		t.Errorf("an uncovered file must sign NO kill rate, got %#v", u)
	}
	if u["uncovered"] != true {
		t.Errorf("the missing rate must be explained, not merely absent: %#v", u)
	}
	a := files[1]
	if a["killRate"] != 0.65 {
		t.Errorf("a measured rate must still be signed: %#v", a)
	}
	// A signed number without the question it answers is not verifiable.
	if a["testSelection"] != "coverage-context" || a["selectedTests"] != 14 || a["suiteTests"] != 1431 {
		t.Errorf("the statement must say WHICH measurement it signed: %#v", a)
	}
	// The JSON tags must agree with the map the attestation is built from.
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("statement does not marshal: %v", err)
	}
}
