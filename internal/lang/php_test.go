// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestPHPPlugin(t *testing.T) {
	p, ok := ByName("php")
	if !ok {
		t.Fatal("php plugin not registered")
	}
	if !p.Detect("app/Invoice.php") || p.Detect("app/Invoice.rb") {
		t.Fatal("Detect must match .php only")
	}
	if got := p.TestPaths("app/Invoice.php")[0]; got.Path != "app/InvoiceTest.php" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {app/InvoiceTest.php, 0}", got)
	}
	if got := p.TestPaths("Invoice.php")[0]; got.Path != "InvoiceTest.php" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {InvoiceTest.php, 0}", got)
	}
	// A two-command SEQUENCE, not a single argv element: `php -l` only
	// checks one file per invocation, and the workspace substrate execs
	// argv directly with no shell to splice a `&&` for it. argv[0] is the
	// DERIVED interpreter (phpInterpreter's own resolution), never the bare
	// literal "php" — see php_interpreter_test.go: on Debian/Ubuntu that
	// name is commonly a symlink through /etc/alternatives, invisible from
	// inside the sandbox even though it resolves fine on the host.
	cc := p.CompileCheck("Invoice.php", "InvoiceTest.php")
	if len(cc) != 3 {
		t.Fatalf("CompileCheck = %v, want a 3-command sequence (two lints and a load)", cc)
	}
	wantInterp, interpErr := phpInterpreter(nil)
	if interpErr != nil {
		t.Skipf("no php on PATH — cannot derive the expected interpreter on this host: %v", interpErr)
	}
	for i, want := range []string{"Invoice.php", "InvoiceTest.php"} {
		if !reflect.DeepEqual(cc[i], []string{wantInterp, "-l", want}) {
			t.Fatalf("CompileCheck()[%d] = %v, want [%q -l %q]", i, cc[i], wantInterp, want)
		}
	}
	if cmd := p.TestCmd(); len(cmd) == 0 || cmd[0] != "vendor/bin/phpunit" {
		t.Fatalf("TestCmd = %v, want vendor/bin/phpunit", cmd)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "PHPUnit") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("php system prompts must be language-appropriate")
	}
	if p.PromptLang() != "PHP" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestPHPTestRoots pins the additional recursive-search roots (beyond
// reposcan's generic "tests" default) named in the design doc.
func TestPHPTestRoots(t *testing.T) {
	p, _ := ByName("php")
	tr, ok := p.(TestRooter)
	if !ok {
		t.Fatal("php plugin does not implement TestRooter")
	}
	got := tr.TestRoots()
	want := []string{"tests", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TestRoots() = %v, want %v", got, want)
	}
}

// TestPHPTestPathsOrder pins the ordered-candidate-list contract: sibling
// FooTest.php first, then the tests/ and test/ mirrors (leading directory
// replaced, matching the shipped plugins' parallel-tree convention).
func TestPHPTestPathsOrder(t *testing.T) {
	p, _ := ByName("php")
	cases := []struct {
		name string
		in   string
		want []TestCandidate
	}{
		{
			name: "top-level file",
			in:   "Invoice.php",
			want: []TestCandidate{
				{Path: "InvoiceTest.php", Rank: 0},
				{Path: "tests/InvoiceTest.php", Rank: 1},
				{Path: "test/InvoiceTest.php", Rank: 1},
			},
		},
		{
			name: "single-segment src dir",
			in:   "src/Invoice.php",
			want: []TestCandidate{
				{Path: "src/InvoiceTest.php", Rank: 0},
				{Path: "tests/InvoiceTest.php", Rank: 1},
				{Path: "test/InvoiceTest.php", Rank: 1},
			},
		},
		{
			name: "nested src dir",
			in:   "src/Billing/Invoice.php",
			want: []TestCandidate{
				{Path: "src/Billing/InvoiceTest.php", Rank: 0},
				{Path: "tests/Billing/InvoiceTest.php", Rank: 1},
				{Path: "test/Billing/InvoiceTest.php", Rank: 1},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.TestPaths(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("TestPaths(%q) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("TestPaths(%q)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// TestPHPPreflight exercises the "php + test-command argv[0] runnable"
// requirement from the design doc: Preflight must fail closed when php
// itself is unavailable, independent of whatever the test command names.
func TestPHPPreflight(t *testing.T) {
	p, _ := ByName("php")
	// A nonsense binary name can never resolve on any host: Preflight must
	// report an error, not a silent pass.
	if err := p.Preflight([]string{"totally-not-a-real-binary-xyz"}); err == nil {
		t.Fatal("Preflight with an unresolvable test command binary must fail closed")
	}
}

// TestPHPCoverageCmdDisablesTheProjectsOwnCoverage pins the sixth review's
// M3: a phpunit.xml that requests its own coverage report makes
// php-code-coverage's PCOV driver \pcov\clear() before every test, so
// corral's shutdown snapshot held nothing — a header-only report and a
// diagnosis blaming the suite. PHPUnit's own --no-coverage is appended when
// the runner is phpunit; a composer script is left alone.
func TestPHPCoverageCmdDisablesTheProjectsOwnCoverage(t *testing.T) {
	cmd, ok := phpPlugin{}.CoverageCmd([]string{"vendor/bin/phpunit", "-c", "phpunit.xml"})
	if !ok {
		t.Fatal("no coverage command")
	}
	if !strings.Contains(cmd[2], "'phpunit.xml' '--no-coverage'") {
		t.Errorf("phpunit run must carry --no-coverage: %s", cmd[2])
	}
	cmd, ok = phpPlugin{}.CoverageCmd([]string{"composer", "test"})
	if !ok {
		t.Fatal("no coverage command for composer")
	}
	if strings.Contains(cmd[2], "--no-coverage") {
		t.Errorf("a composer script must not have phpunit flags appended: %s", cmd[2])
	}
}
