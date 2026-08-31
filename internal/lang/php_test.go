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
	// argv directly with no shell to splice a `&&` for it.
	cc := p.CompileCheck("Invoice.php", "InvoiceTest.php")
	if !reflect.DeepEqual(cc, [][]string{{"php", "-l", "Invoice.php"}, {"php", "-l", "InvoiceTest.php"}}) {
		t.Fatalf("CompileCheck = %v", cc)
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
