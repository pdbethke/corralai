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
	if got := p.TestPaths("app/pricing.py")[0]; got.Path != "app/test_pricing.py" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {app/test_pricing.py, 0}", got)
	}
	if got := p.TestPaths("pricing.py")[0]; got.Path != "test_pricing.py" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v, want {test_pricing.py, 0}", got)
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
// specific (least likely to collide with a different source file) first,
// AND pins each candidate's Rank — the evidentiary specificity a
// cross-source collision check actually compares (see lang.TestCandidate).
// Rank is NOT always equal to list position: when several forms collapse
// onto the same string, dedupeCandidates attributes the surviving entry the
// LEAST specific (highest) rank among the colliding forms, which several
// cases below exercise explicitly (a naive "position in the deduped slice"
// rank would instead vary with how many forms happened to collide, which is
// exactly the bug a real flask/docs collision exposed — see
// internal/reposcan/candidate_pairing_test.go).
//
// The aisuite/agents/artifact_store.py case is the real shape measured
// against github.com/andrewyng/aisuite — the whole reason this seam exists.
func TestPythonTestPathsOrder(t *testing.T) {
	p, _ := ByName("python")
	cases := []struct {
		name string
		in   string
		want []TestCandidate
	}{
		{
			name: "top-level file",
			in:   "pricing.py",
			want: []TestCandidate{
				{Path: "test_pricing.py", Rank: 0},
				{Path: "pricing_test.py", Rank: 0},
				// mirror, stripped, AND flat all degenerate to this same
				// string at depth 0 — attributed flat's rank (3), the least
				// specific of the three, not mirror's rank (1) merely
				// because mirror happened to be generated first.
				{Path: "tests/test_pricing.py", Rank: 3},
			},
		},
		{
			name: "sibling dir, single segment",
			in:   "app/pricing.py",
			want: []TestCandidate{
				{Path: "app/test_pricing.py", Rank: 0},
				{Path: "app/pricing_test.py", Rank: 0},
				{Path: "tests/app/test_pricing.py", Rank: 1}, // full mirror — distinct, not collapsed
				// stripped ("app" stripped to "") and flat coincide; the
				// surviving entry is attributed flat's rank (3), not
				// stripped's (2).
				{Path: "tests/test_pricing.py", Rank: 3},
			},
		},
		{
			name: "aisuite shape: package/subdir",
			in:   "aisuite/agents/artifact_store.py",
			want: []TestCandidate{
				{Path: "aisuite/agents/test_artifact_store.py", Rank: 0},
				{Path: "aisuite/agents/artifact_store_test.py", Rank: 0},
				{Path: "tests/aisuite/agents/test_artifact_store.py", Rank: 1}, // full mirror
				{Path: "tests/agents/test_artifact_store.py", Rank: 2},         // leading segment stripped — the real aisuite layout
				{Path: "tests/test_artifact_store.py", Rank: 3},                // flat, tried last
			},
		},
		{
			name: "src/ layout",
			in:   "src/pkg/foo.py",
			want: []TestCandidate{
				{Path: "src/pkg/test_foo.py", Rank: 0},
				{Path: "src/pkg/foo_test.py", Rank: 0},
				{Path: "tests/src/pkg/test_foo.py", Rank: 1}, // full mirror
				{Path: "tests/pkg/test_foo.py", Rank: 2},     // leading segment ("src") stripped
				{Path: "tests/test_foo.py", Rank: 3},         // flat, tried last
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
			want: []TestCandidate{
				{Path: "examples/celery/src/task_app/test_views.py", Rank: 0},
				{Path: "examples/celery/src/task_app/views_test.py", Rank: 0},
				{Path: "tests/examples/celery/src/task_app/test_views.py", Rank: 1},
				{Path: "tests/celery/src/task_app/test_views.py", Rank: 2},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.TestPaths(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("TestPaths(%q) = %+v (len %d), want %+v (len %d)", c.in, got, len(got), c.want, len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("TestPaths(%q)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}
