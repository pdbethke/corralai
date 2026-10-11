// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"strings"
	"testing"
)

// TestMutantSystemGolden pins every registered plugin's MutantSystem prompt
// byte for byte. The prompts are measured model inputs: a refactor that
// shares their common paragraphs must not change one byte any plugin sends.
// Generated deliberately with UPDATE_GOLDEN=1; a missing golden fails.
func TestMutantSystemGolden(t *testing.T) {
	var b strings.Builder
	for _, name := range Names() {
		b.WriteString("=== " + name + "\n")
		b.WriteString(registry[name].MutantSystem())
		b.WriteString("\n")
	}
	got := b.String()
	const path = "testdata/mutant-system.golden.txt"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden missing (generate deliberately with UPDATE_GOLDEN=1): %v", err)
	}
	if got != string(want) {
		t.Fatalf("MutantSystem output changed; diff testdata/mutant-system.golden.txt against a regenerated copy")
	}
}
