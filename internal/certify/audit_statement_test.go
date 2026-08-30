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

// TestPerMutantEntrySignsTheSpread pins the signed artifact at the grain the
// grading happens. Once each mutant is graded by the tests that reach its own
// lines, "234 of 620" is the file's union and no mutant's own denominator —
// so the statement carries the spread, and a file NOT graded per mutant must
// not carry a claim it cannot support.
func TestPerMutantEntrySignsTheSpread(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{
			{Path: "src/flask/cli.py", KillRate: killRate(0.65), Survivors: 4,
				TestSelection: "coverage-lines", SelectedTests: 234, SuiteTests: 620,
				PerMutant: true, TestsPerMutant: &TestsPerMutantSpread{Min: 3, Median: 9, Max: 41}},
			{Path: "pkg/a.py", KillRate: killRate(0.9), Survivors: 1,
				TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
		},
	})
	files := got["predicate"].(map[string]any)["files"].([]map[string]any)
	pm := files[0]
	if pm["perMutant"] != true {
		t.Errorf("a per-mutant run must say so: %#v", pm)
	}
	if pm["testsPerMutantMin"] != 3 || pm["testsPerMutantMedian"] != 9 || pm["testsPerMutantMax"] != 41 {
		t.Errorf("the spread must be signed with the rate: %#v", pm)
	}
	shared := files[1]
	if _, ok := shared["perMutant"]; ok {
		t.Errorf("a file graded by one shared command must not claim per-mutant grading: %#v", shared)
	}
	for _, k := range []string{"testsPerMutantMin", "testsPerMutantMedian", "testsPerMutantMax"} {
		if _, ok := shared[k]; ok {
			t.Errorf("%s must be absent, not zero-filled, on a shared-command file: %#v", k, shared)
		}
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("statement does not marshal: %v", err)
	}
}

// TestPerMutantWithNoGradedMutantSignsNoSpread pins the reachable state the
// first pass got wrong: a per-mutant run whose every mutant was rejected by
// the compile gate has PerMutant set and a spread of {0,0,0}. Signing
// "3 to 0" — or a min/median/max of zero — would put a measurement nobody
// made over a signature.
func TestPerMutantWithNoGradedMutantSignsNoSpread(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{{Path: "pkg/none.py", KillRate: killRate(0), TestSelection: "coverage-lines", PerMutant: true}},
	})
	files := got["predicate"].(map[string]any)["files"].([]map[string]any)
	f := files[0]
	if f["perMutant"] != true {
		t.Errorf("the run DID grade per mutant and must say so: %#v", f)
	}
	for _, k := range []string{"testsPerMutantMin", "testsPerMutantMedian", "testsPerMutantMax"} {
		if _, ok := f[k]; ok {
			t.Errorf("%s must be ABSENT when no mutant was graded, not signed as 0: %#v", k, f)
		}
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("statement does not marshal: %v", err)
	}
}

func TestProvenByTheAuthoredTestAloneIsSigned(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{
			{Path: "pkg/a.py", KillRate: killRate(0.55), Survivors: 18, ProvenMissed: 18, TestSelection: "coverage-lines", ProvenByAuthoredAlone: true},
			{Path: "pkg/b.py", KillRate: killRate(0.55), Survivors: 2, ProvenMissed: 1},
		},
	})
	files := got["predicate"].(map[string]any)["files"].([]map[string]any)
	if v, ok := files[0]["provenByAuthoredAlone"]; !ok || v != true {
		t.Errorf("per-selection file must sign provenByAuthoredAlone=true: %v", files[0])
	}
	if _, ok := files[1]["provenByAuthoredAlone"]; ok {
		t.Errorf("a whole-suite file must not carry the key: %v", files[1])
	}
}

// TestConcurrencyIsSigned pins the attestation's half of "every reader says
// how many trees scored the file, or why one": trees is signed on EVERY
// file (a verifier must be able to see the exam ran at concurrency 1
// without inferring it from an absent key), while concurrencyNote signs
// only when the substrate actually had one to give.
func TestConcurrencyIsSigned(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		Files: []AuditedFile{
			{Path: "pkg/a.py", KillRate: killRate(0.55), Survivors: 4, Trees: 6, SharedDirs: []string{".venv"}},
			{Path: "pkg/b.py", KillRate: killRate(0.9), Survivors: 1, Trees: 1,
				ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"},
			// Trees 0 is "not recorded" — the jail substrate builds no trees,
			// and a verdict served from a pre-concurrency cache row carries
			// none. Signing "trees": 0 would be a number no measurement
			// supports, so the key is ABSENT instead.
			{Path: "pkg/c.py", KillRate: killRate(0.4), Survivors: 2},
		},
	})
	files := got["predicate"].(map[string]any)["files"].([]map[string]any)
	if v, ok := files[0]["trees"]; !ok || v != 6 {
		t.Errorf("trees must always be signed: %v", files[0])
	}
	if _, ok := files[0]["concurrencyNote"]; ok {
		t.Errorf("a file with no note must not carry the key: %v", files[0])
	}
	if v, ok := files[1]["trees"]; !ok || v != 1 {
		t.Errorf("trees must always be signed: %v", files[1])
	}
	if v, ok := files[1]["concurrencyNote"]; !ok || v != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("a downgraded file must sign its note: %v", files[1])
	}
	if _, ok := files[2]["trees"]; ok {
		t.Errorf("an unrecorded concurrency must sign NO trees key: %v", files[2])
	}
	// The dep dirs every tree shared are signed too: they are the one thing
	// the trees did not hold privately, so a verifier reading "6 trees" must
	// also be able to read what those 6 had in common.
	if v, ok := files[0]["sharedDirs"]; !ok || len(v.([]string)) != 1 || v.([]string)[0] != ".venv" {
		t.Errorf("shared dep dirs must be signed: %v", files[0])
	}
	if _, ok := files[1]["sharedDirs"]; ok {
		t.Errorf("a file that shared nothing must not carry the key: %v", files[1])
	}
}
