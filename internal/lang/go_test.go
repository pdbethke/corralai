// SPDX-License-Identifier: Elastic-2.0

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
	// A file in a package dir scopes the build to that package (not the whole
	// module) so a monorepo audit doesn't compile unrelated cgo deps. It is a
	// BUILD (`go test -run ^$`), not `go vet`: vet's analyzers rejected
	// mutants that compile and run, taking them out of the denominator.
	if got := p.CompileCheck("a/b.go", "a/b_test.go"); !reflect.DeepEqual(got, [][]string{{"go", "test", "-count=1", "-run", "^$", "./a/..."}}) {
		t.Fatalf("CompileCheck(package path) = %v", got)
	}
	// A bare filename (single-file mode) has no package dir → whole scaffold.
	if got := p.CompileCheck("b.go", "b_test.go"); !reflect.DeepEqual(got, [][]string{{"go", "test", "-count=1", "-run", "^$", "./..."}}) {
		t.Fatalf("CompileCheck(bare file) = %v", got)
	}
	for in, want := range map[string]string{
		"login.go":           "login_test.go",
		"internal/auth/x.go": "internal/auth/x_test.go",
	} {
		if got := p.TestPaths(in); len(got) != 1 || got[0].Path != want || got[0].Rank != 0 {
			t.Fatalf("TestPaths(%q) = %v, want [{%q, 0}]", in, got, want)
		}
	}
	if p.PromptLang() != "Go" {
		t.Fatalf("PromptLang() = %q", p.PromptLang())
	}
}
