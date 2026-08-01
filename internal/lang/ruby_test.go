package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestRubyPlugin(t *testing.T) {
	p, ok := ByName("ruby")
	if !ok {
		t.Fatal("ruby plugin not registered")
	}
	if !p.Detect("app/pricing.rb") || p.Detect("app/pricing.py") {
		t.Fatal("Detect must match .rb only")
	}
	if got := p.TestPaths("app/pricing.rb")[0]; got.Path != "app/pricing_test.rb" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {app/pricing_test.rb, 0}", got)
	}
	if got := p.TestPaths("pricing.rb")[0]; got.Path != "pricing_test.rb" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {pricing_test.rb, 0}", got)
	}
	// A two-command SEQUENCE, not a single `&&`-joined argv element: `ruby -c`
	// only checks one file per invocation, and a bare `&&` argv element only
	// means anything to a shell — the workspace substrate execs argv directly
	// with no shell to interpret it.
	cc := p.CompileCheck("pricing.rb", "pricing_test.rb")
	if !reflect.DeepEqual(cc, [][]string{{"ruby", "-c", "pricing.rb"}, {"ruby", "-c", "pricing_test.rb"}}) {
		t.Fatalf("CompileCheck = %v", cc)
	}
	// TestCmd MUST invoke a shell EXPLICITLY. Smuggling the script into argv[0]
	// only worked on the jail substrate, which shell-joins argv; the workspace
	// substrate execs argv directly and tried to run a program literally named
	// `t="$(ls`, which is how the first real rubocop audit died.
	if cmd := p.TestCmd(); len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("TestCmd must be an explicit sh -c invocation, got %v", cmd)
	}
	tc := p.TestCmd()[2]
	if !strings.Contains(tc, "rspec") || !strings.Contains(tc, "ruby ") {
		t.Fatalf("TestCmd must dispatch rspec-or-ruby: %q", tc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "minitest") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("ruby system prompts must be language-appropriate")
	}
	if p.PromptLang() != "Ruby" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestRubyTestPathsOrder pins the ordered-candidate-list contract for the
// lib/ vs test/ (or spec/) layout: sibling first, then the leading directory
// (conventionally "lib") replaced by test/, then the RSpec equivalent.
func TestRubyTestPathsOrder(t *testing.T) {
	p, _ := ByName("ruby")
	cases := []struct {
		name string
		in   string
		want []TestCandidate
	}{
		{
			name: "top-level file",
			in:   "pricing.rb",
			want: []TestCandidate{
				{Path: "pricing_test.rb", Rank: 0},
				{Path: "test/pricing_test.rb", Rank: 1},
				{Path: "spec/pricing_spec.rb", Rank: 1},
			},
		},
		{
			name: "single-segment lib dir",
			in:   "lib/foo.rb",
			want: []TestCandidate{
				{Path: "lib/foo_test.rb", Rank: 0},
				{Path: "test/foo_test.rb", Rank: 1},
				{Path: "spec/foo_spec.rb", Rank: 1},
			},
		},
		{
			name: "lib/<pkg> layout",
			in:   "lib/mypkg/foo.rb",
			want: []TestCandidate{
				{Path: "lib/mypkg/foo_test.rb", Rank: 0},
				{Path: "test/mypkg/foo_test.rb", Rank: 1},
				{Path: "spec/mypkg/foo_spec.rb", Rank: 1},
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
