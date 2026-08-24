// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"strings"
	"testing"
)

// Both generating seats were observed failing on the SAME class of rule, so the
// warning is shared rather than copied. These assert the note actually names
// each failure that was MEASURED, so a future edit cannot quietly drop one:
//
//	mutant-generator: `"fmt" imported and not used`  (removed the last use)
//	mutant-generator: `no new variables on left side of :=`
//	test-writer:      `declared and not used: l2`
func TestStrictnessNoteNamesEveryObservedFailure(t *testing.T) {
	note := strings.ToLower(StrictnessNote())
	for _, want := range []string{"import", "declared variable", ":=", "already exist"} {
		if !strings.Contains(note, want) {
			t.Errorf("StrictnessNote does not cover %q — a measured failure mode is unaddressed:\n%s", want, StrictnessNote())
		}
	}
}

// THE WIRE: the note must reach the mutant prompt, not merely exist.
func TestMutantPromptCarriesTheStrictnessNote(t *testing.T) {
	_, usr := GenerateMutantsPrompt("", "goal", "package p\n", nil, 3)
	if !strings.Contains(usr, "LANGUAGE STRICTNESS") {
		t.Fatal("the mutant instruction does not carry the strictness note")
	}
	// And it must not have been mangled into a literal %!s(MISSING).
	if strings.Contains(usr, "%!") {
		t.Errorf("format verb mismatch in the mutant instruction: %s", usr[:200])
	}
}
