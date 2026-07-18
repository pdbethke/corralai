package lang

import (
	"strings"
	"testing"
)

func TestPythonPlugin(t *testing.T) {
	p, ok := ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	if !p.Detect("app/pricing.py") || p.Detect("app/pricing.go") {
		t.Fatal("Detect must match .py only")
	}
	if got := p.TestPath("app/pricing.py"); got != "app/test_pricing.py" {
		t.Fatalf("TestPath = %q, want app/test_pricing.py", got)
	}
	if got := p.TestPath("pricing.py"); got != "test_pricing.py" {
		t.Fatalf("TestPath = %q, want test_pricing.py", got)
	}
	tc := p.TestCmd()
	if len(tc) != 4 || (tc[0] != "python3" && tc[0] != "python") || tc[1] != "-m" || tc[2] != "pytest" || tc[3] != "-q" {
		t.Fatalf("TestCmd = %v", tc)
	}
	cc := p.CompileCheck("pricing.py", "test_pricing.py")
	if len(cc) != 5 || (cc[0] != "python3" && cc[0] != "python") || cc[1] != "-m" || cc[2] != "py_compile" || cc[3] != "pricing.py" || cc[4] != "test_pricing.py" {
		t.Fatalf("CompileCheck = %v", cc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty for python, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "pytest") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("python system prompts must be language-appropriate")
	}
	if p.PromptLang() != "Python" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}
