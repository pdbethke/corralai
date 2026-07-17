package lang

import (
	"reflect"
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
	if got := p.CompileCheck("pricing.py", "test_pricing.py"); !reflect.DeepEqual(got,
		[]string{"python", "-m", "py_compile", "pricing.py", "test_pricing.py"}) {
		t.Fatalf("CompileCheck = %v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"python", "-m", "pytest", "-q"}) {
		t.Fatalf("TestCmd = %v", got)
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
