// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "testing"

func TestVerdictGenerationIsSet(t *testing.T) {
	if VerdictGeneration == "" {
		t.Fatal("VerdictGeneration is empty — every verdict would key as if the engine had no identity")
	}
}

func TestVerdictGenerationChangesTheKey(t *testing.T) {
	base := KeyInputs{SourceDigest: "s", PackageDigest: "p", GoalDigest: "g",
		TestSurfaceDigest: "t", ModelSet: "m", AuditConfig: "", Substrate: SubstrateWorkspace}
	a, b := base, base
	a.EngineVersion = "1"
	b.EngineVersion = "2"
	if a.CacheKey() == b.CacheKey() {
		t.Fatal("bumping the generation did not invalidate the key — the purge lever does not work")
	}
}
