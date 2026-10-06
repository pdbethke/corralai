// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentworker"
)

// The critic's task carries the test list when the language can list the
// file's tests from source, written by the same helper the critic reads it
// back with; a language that cannot list gets no block, and the critic
// answers in the unkeyed shape as before.
func TestCriticInstructionCarriesTheTestList(t *testing.T) {
	rs := RunSpec{Goal: "adds", CodePath: "p/p.go", Code: "package p", DevTestPath: "p/p_test.go",
		DevTestCode: "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"}
	got := CriticInstruction(rs)
	if want := agentworker.CriticTestListBlock("p/p_test.go", []string{"TestA", "TestB"}); !strings.Contains(got, want) {
		t.Fatalf("instruction lacks the list block %q:\n%s", want, got)
	}
	rs.Lang, rs.CodePath, rs.DevTestPath, rs.DevTestCode = "ruby", "lib/p.rb", "spec/p_spec.rb", "describe 'p' do\nend\n"
	if got := CriticInstruction(rs); strings.Contains(got, "TESTS TO JUDGE") {
		t.Fatalf("a language that cannot list must get no block:\n%s", got)
	}
}
