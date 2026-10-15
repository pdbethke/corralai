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
	// PHP lists PHPUnit's tests (2026-10-15): without the list a critic
	// judged an arbitrary subset of a file's tests and the review read as
	// complete.
	php := RunSpec{Goal: "asserts", Lang: "php", CodePath: "src/A.php", Code: "<?php class A {}", DevTestPath: "tests/ATest.php",
		DevTestCode: "<?php\nclass ATest extends TestCase {\n    public function testA(): void {}\n    #[Test]\n    public function b(): void {}\n}\n"}
	if want := agentworker.CriticTestListBlock("tests/ATest.php", []string{"ATest::testA", "ATest::b"}); !strings.Contains(CriticInstruction(php), want) {
		t.Fatalf("PHP instruction lacks the list block %q:\n%s", want, CriticInstruction(php))
	}
	// Rust lists libtest's tests by their module path in the file.
	rust := RunSpec{Goal: "matches", Lang: "rust", CodePath: "src/fuzzy.rs", Code: "pub fn m() {}", DevTestPath: "src/fuzzy_tests.rs",
		DevTestCode: "use super::*;\n\n#[test]\nfn a() {}\n\nmod more {\n    #[tokio::test]\n    async fn b() {}\n}\n"}
	if want := agentworker.CriticTestListBlock("src/fuzzy_tests.rs", []string{"a", "more::b"}); !strings.Contains(CriticInstruction(rust), want) {
		t.Fatalf("Rust instruction lacks the list block %q:\n%s", want, CriticInstruction(rust))
	}
	rs.Lang, rs.CodePath, rs.DevTestPath, rs.DevTestCode = "ruby", "lib/p.rb", "spec/p_spec.rb", "describe 'p' do\nend\n"
	if got := CriticInstruction(rs); strings.Contains(got, "TESTS TO JUDGE") {
		t.Fatalf("a language that cannot list must get no block:\n%s", got)
	}
}
