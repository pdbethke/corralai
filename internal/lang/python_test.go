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
	if got := p.TestPaths("app/pricing.py")[0]; got != "app/test_pricing.py" {
		t.Fatalf("TestPaths()[0] = %q, want app/test_pricing.py", got)
	}
	if got := p.TestPaths("pricing.py")[0]; got != "test_pricing.py" {
		t.Fatalf("TestPaths()[0] = %q, want test_pricing.py", got)
	}
	tc := p.TestCmd()
	if len(tc) != 4 || (tc[0] != "python3" && tc[0] != "python") || tc[1] != "-m" || tc[2] != "pytest" || tc[3] != "-q" {
		t.Fatalf("TestCmd = %v", tc)
	}
	// The leading token MUST be the PYTHONPYCACHEPREFIX assignment: without it,
	// py_compile writes bytecode into the jail-read-only workspace and a valid
	// test is falsely rejected as "does not compile" on the container backend.
	cc := p.CompileCheck("pricing.py", "test_pricing.py")
	if len(cc) != 6 || cc[0] != "PYTHONPYCACHEPREFIX=/tmp/corral-pyc" ||
		(cc[1] != "python3" && cc[1] != "python") || cc[2] != "-m" || cc[3] != "py_compile" ||
		cc[4] != "pricing.py" || cc[5] != "test_pricing.py" {
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

// TestPythonTestPathsOrder pins the ordered-candidate-list contract: most
// specific (least likely to collide with a different source file) first.
// The aisuite/agents/artifact_store.py case is the real shape measured
// against github.com/andrewyng/aisuite — the whole reason this seam exists.
func TestPythonTestPathsOrder(t *testing.T) {
	p, _ := ByName("python")
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "top-level file",
			in:   "pricing.py",
			want: []string{"test_pricing.py", "pricing_test.py", "tests/test_pricing.py"},
		},
		{
			name: "sibling dir, single segment",
			in:   "app/pricing.py",
			want: []string{
				"app/test_pricing.py",
				"app/pricing_test.py",
				"tests/app/test_pricing.py", // full mirror
				"tests/test_pricing.py",     // leading-segment-stripped ("app" stripped to "") and flat coincide
			},
		},
		{
			name: "aisuite shape: package/subdir",
			in:   "aisuite/agents/artifact_store.py",
			want: []string{
				"aisuite/agents/test_artifact_store.py",
				"aisuite/agents/artifact_store_test.py",
				"tests/aisuite/agents/test_artifact_store.py", // full mirror
				"tests/agents/test_artifact_store.py",         // leading segment stripped — the real aisuite layout
				"tests/test_artifact_store.py",                // flat, tried last
			},
		},
		{
			name: "src/ layout",
			in:   "src/pkg/foo.py",
			want: []string{
				"src/pkg/test_foo.py",
				"src/pkg/foo_test.py",
				"tests/src/pkg/test_foo.py", // full mirror
				"tests/pkg/test_foo.py",     // leading segment ("src") stripped
				"tests/test_foo.py",         // flat, tried last
			},
		},
		{
			// A source more than 2 directory segments deep must NOT generate
			// the flat tests/test_foo.py candidate at all: on a real repo
			// (flask) a 3-segment-deep example app
			// (examples/javascript/js_example/views.py) generated the exact
			// same flat candidate as the genuine top-level src/flask/views.py
			// and both silently "paired" with the same test file. No flat
			// entry here is what removes that collision at the source.
			name: "deep dir (>2 segments) excludes the flat fallback",
			in:   "examples/celery/src/task_app/views.py",
			want: []string{
				"examples/celery/src/task_app/test_views.py",
				"examples/celery/src/task_app/views_test.py",
				"tests/examples/celery/src/task_app/test_views.py",
				"tests/celery/src/task_app/test_views.py",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.TestPaths(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("TestPaths(%q) = %v (len %d), want %v (len %d)", c.in, got, len(got), c.want, len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("TestPaths(%q)[%d] = %q, want %q\nfull got=%v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}
