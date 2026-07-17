package lang

import (
	"reflect"
	"testing"
)

func TestGoPluginMatchesLegacyBehavior(t *testing.T) {
	p, _ := ByName("go")
	if got := p.Scaffold(); !reflect.DeepEqual(got, map[string]string{"go.mod": "module control\ngo 1.26\n"}) {
		t.Fatalf("Scaffold() = %v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Fatalf("TestCmd() = %v", got)
	}
	if got := p.CompileCheck("a/b.go", "a/b_test.go"); !reflect.DeepEqual(got, []string{"go", "vet", "./..."}) {
		t.Fatalf("CompileCheck() = %v", got)
	}
	for in, want := range map[string]string{
		"login.go":           "login_test.go",
		"internal/auth/x.go": "internal/auth/x_test.go",
	} {
		if got := p.TestPath(in); got != want {
			t.Fatalf("TestPath(%q) = %q, want %q", in, got, want)
		}
	}
	if p.PromptLang() != "Go" {
		t.Fatalf("PromptLang() = %q", p.PromptLang())
	}
}
