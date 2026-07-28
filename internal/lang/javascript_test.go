package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestJavaScriptPlugin(t *testing.T) {
	p, ok := ByName("javascript")
	if !ok {
		t.Fatal("javascript plugin not registered")
	}
	for _, ok1 := range []string{"a.js", "a.mjs", "a.cjs"} {
		if !p.Detect(ok1) {
			t.Fatalf("Detect(%q) should be true", ok1)
		}
	}
	if p.Detect("a.ts") {
		t.Fatal("must not detect .ts")
	}
	if got := p.TestPaths("pkg/foo.js")[0]; got != "pkg/foo.test.js" {
		t.Fatalf("TestPaths()[0] = %q", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	cc := p.CompileCheck("foo.js", "foo.test.js")
	if !reflect.DeepEqual(cc, []string{"node", "--check", "foo.js", "&&", "node", "--check", "foo.test.js"}) {
		t.Fatalf("CompileCheck = %v", cc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty")
	}
	if !strings.Contains(p.TestWriterSystem(), "node:test") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("js prompts must be language-appropriate")
	}
	if p.PromptLang() != "JavaScript" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestJavaScriptTestPathsOrder pins the ordered-candidate-list contract:
// sibling .test/.spec, then a same-dir __tests__/ folder, then a
// leading-segment-stripped parallel test/ or tests/ tree.
func TestJavaScriptTestPathsOrder(t *testing.T) {
	p, _ := ByName("javascript")
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "top-level file",
			in:   "foo.js",
			want: []string{
				"foo.test.js", "foo.spec.js", "__tests__/foo.test.js",
				"test/foo.test.js", "tests/foo.test.js",
			},
		},
		{
			name: "single-segment dir",
			in:   "pkg/foo.js",
			want: []string{
				"pkg/foo.test.js", "pkg/foo.spec.js", "pkg/__tests__/foo.test.js",
				"test/foo.test.js", "tests/foo.test.js",
			},
		},
		{
			name: "src/ layout",
			in:   "src/pkg/foo.js",
			want: []string{
				"src/pkg/foo.test.js", "src/pkg/foo.spec.js", "src/pkg/__tests__/foo.test.js",
				"test/pkg/foo.test.js", "tests/pkg/foo.test.js",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.TestPaths(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("TestPaths(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("TestPaths(%q)[%d] = %q, want %q\nfull got=%v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}
