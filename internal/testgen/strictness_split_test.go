// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"strings"
	"testing"
)

// The two seats have OPPOSITE constraints on imports and one shared core.
//
// The mutant-generator edits through a minimal hunk and genuinely cannot reach
// the import block. The test-writer authors a WHOLE FILE and must declare its
// own imports. A single shared note told both "you cannot add an import", which
// is true for one and actively wrong for the other — and the writer was
// observed failing with `undefined: models` on a dependency-heavy file.
func TestMutantNoteForbidsAddingImports(t *testing.T) {
	n := strings.ToLower(MutantStrictnessNote())
	if !strings.Contains(n, "cannot add an import") {
		t.Error("the mutant note must say a partial edit cannot add an import — that is its real constraint")
	}
}

func TestWriterNoteRequiresDeclaringImports(t *testing.T) {
	n := strings.ToLower(WriterStrictnessNote())
	if strings.Contains(n, "cannot add an import") {
		t.Fatal("the writer note must NOT forbid adding imports: it authors a whole file and must declare every import it uses")
	}
	if !strings.Contains(n, "import") {
		t.Error("the writer note must tell it to declare the imports its test uses")
	}
}

// The genuinely shared rules stay shared — that half of the DRY move was right.
func TestBothNotesShareTheCoreRules(t *testing.T) {
	m, w := strings.ToLower(MutantStrictnessNote()), strings.ToLower(WriterStrictnessNote())
	for _, want := range []string{"declared variable", ":="} {
		if !strings.Contains(m, want) {
			t.Errorf("mutant note lost the shared rule %q", want)
		}
		if !strings.Contains(w, want) {
			t.Errorf("writer note lost the shared rule %q", want)
		}
	}
}

// Brace balance is a HUNK concern: a whole authored file is parsed as a unit.
func TestOnlyTheMutantNoteMentionsBraceBalance(t *testing.T) {
	if !strings.Contains(strings.ToLower(MutantStrictnessNote()), "brace") {
		t.Error("the mutant note must keep the brace-balance rule — a dropped closing brace was a measured failure")
	}
}

// THE WIRE: each prompt must carry ITS OWN note.
func TestMutantPromptCarriesTheMutantNote(t *testing.T) {
	_, usr := GenerateMutantsPrompt("", "goal", "package p\n", nil, 3)
	if !strings.Contains(strings.ToLower(usr), "cannot add an import") {
		t.Fatal("the mutant instruction does not carry the mutant-specific note")
	}
	if strings.Contains(usr, "%!") {
		t.Errorf("format verb mismatch: %s", usr[:200])
	}
}
