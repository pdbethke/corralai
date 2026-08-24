// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"strings"
	"testing"
)

// A real qwen2.5-coder:14b failure, reduced: the block is perfectly formed and
// the mutation is sound, but every SEARCH line carries ONE extra leading tab.
// applyMutation correctly refuses it — the anchor must match exact bytes — but
// the operator was told only "no parseable, cleanly-applying mutations", which
// is true of a malformed block, a missing marker, and this. Diagnosing one tab
// character required replaying the prompt against the live model by hand.
const origIndented = "package p\n\nfunc f() {\n\tx := 1\n\treturn x\n}\n"

func oneTabTooDeep() string {
	return "===MUTATION_1===\n" + srSearchHead + "\n\t\tx := 1\n" + srDivider + "\n\t\tx := 2\n" + srReplaceEnd + "\n"
}

func TestParseMutantsDiag_NamesTheRejectionReason(t *testing.T) {
	_, diag := parseMutantsDiag(oneTabTooDeep(), origIndented)
	if diag.Blocks != 1 {
		t.Fatalf("Blocks = %d, want 1", diag.Blocks)
	}
	if diag.Kept != 0 {
		t.Fatalf("Kept = %d, want 0 — the anchor does not match exact bytes", diag.Kept)
	}
	if diag.AnchorNotFound != 1 {
		t.Errorf("AnchorNotFound = %d, want 1 — the block was well-formed, so it must not be counted as malformed", diag.AnchorNotFound)
	}
	if diag.Malformed != 0 {
		t.Errorf("Malformed = %d, want 0 — markers were all present", diag.Malformed)
	}
}

// The load-bearing one: the whole point of the diagnostic is to name the
// whitespace case, because that is the failure a human cannot see.
func TestParseMutantsDiag_FlagsWhitespaceOnlyMismatch(t *testing.T) {
	_, diag := parseMutantsDiag(oneTabTooDeep(), origIndented)
	if diag.WhitespaceOnly != 1 {
		t.Fatalf("WhitespaceOnly = %d, want 1 — the anchor matches once leading whitespace is normalized", diag.WhitespaceOnly)
	}
	msg := diag.Error().Error()
	for _, want := range []string{"1 block", "indentation", "whitespace"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("error message does not mention %q: %s", want, msg)
		}
	}
}

// A genuinely malformed block must NOT be reported as a whitespace problem —
// that would send the next operator chasing indentation on a model that is
// actually emitting the wrong format.
func TestParseMutantsDiag_DoesNotBlameWhitespaceForMalformedBlocks(t *testing.T) {
	bad := "===MUTATION_1===\n" + srSearchHead + "\n\tx := 1\nno divider here, no replace marker\n"
	_, diag := parseMutantsDiag(bad, origIndented)
	if diag.Malformed != 1 {
		t.Fatalf("Malformed = %d, want 1", diag.Malformed)
	}
	if diag.WhitespaceOnly != 0 {
		t.Errorf("WhitespaceOnly = %d, want 0 — a missing marker is not an indentation problem", diag.WhitespaceOnly)
	}
}

// A clean block still parses, and the diagnostic reports it as kept.
func TestParseMutantsDiag_CleanBlockIsKept(t *testing.T) {
	good := "===MUTATION_1===\n" + srSearchHead + "\n\tx := 1\n" + srDivider + "\n\tx := 2\n" + srReplaceEnd + "\n"
	muts, diag := parseMutantsDiag(good, origIndented)
	if len(muts) != 1 || diag.Kept != 1 {
		t.Fatalf("muts=%d Kept=%d, want 1/1", len(muts), diag.Kept)
	}
	if diag.Error() != nil {
		t.Errorf("Error() = %v, want nil when a mutant was kept", diag.Error())
	}
}

// The exported entry point must carry the diagnosis, not flatten it back to
// the old opaque sentence.
func TestParseMutantsOutput_ErrorCarriesTheDiagnosis(t *testing.T) {
	_, err := ParseMutantsOutput(oneTabTooDeep(), origIndented)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "indentation") {
		t.Errorf("ParseMutantsOutput error lost the whitespace diagnosis: %v", err)
	}
}
