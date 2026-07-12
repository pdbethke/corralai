// SPDX-License-Identifier: Elastic-2.0

package testgen

import "testing"

func TestParseVerdicts(t *testing.T) {
	resp := "MUTANT m1: GAP: the test never exercises empty grants\n" +
		"MUTANT m2: equivalent: a wildcard grant is outside the goal's model\n" +
		"some preamble line that isn't a verdict\n" +
		"MUTANT m3: WISHYWASHY: unknown class is skipped\n"
	vs := parseVerdicts(resp)
	if len(vs) != 2 {
		t.Fatalf("got %d verdicts, want 2 (garbage + unknown-class skipped): %+v", len(vs), vs)
	}
	if vs[0].MutantID != "m1" || !vs[0].RealGap || vs[0].Rationale != "the test never exercises empty grants" {
		t.Errorf("v1 wrong: %+v", vs[0])
	}
	if vs[1].MutantID != "m2" || vs[1].RealGap || vs[1].Rationale != "a wildcard grant is outside the goal's model" {
		t.Errorf("v2 wrong (case-insensitive equivalent?): %+v", vs[1])
	}
}
