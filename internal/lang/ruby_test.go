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
	if got := p.TestPath("app/pricing.rb"); got != "app/pricing_test.rb" {
		t.Fatalf("TestPath = %q, want app/pricing_test.rb", got)
	}
	if got := p.TestPath("pricing.rb"); got != "pricing_test.rb" {
		t.Fatalf("TestPath = %q, want pricing_test.rb", got)
	}
	cc := p.CompileCheck("pricing.rb", "pricing_test.rb")
	if !reflect.DeepEqual(cc, []string{"ruby", "-c", "pricing.rb", "&&", "ruby", "-c", "pricing_test.rb"}) {
		t.Fatalf("CompileCheck = %v", cc)
	}
	// TestCmd MUST be a single shell string: the jail space-joins the argv and
	// runs it under `sh -c`, so a multi-token slice with an embedded snippet
	// would lose its argument boundaries. One element keeps the snippet intact.
	if len(p.TestCmd()) != 1 {
		t.Fatalf("TestCmd must be a single shell string, got %v", p.TestCmd())
	}
	tc := p.TestCmd()[0]
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
