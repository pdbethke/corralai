// SPDX-License-Identifier: Elastic-2.0

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
	// The command MUST set PYTHONPYCACHEPREFIX on the child process via the
	// `env` utility, not as a bare argv[0] env-assignment token: without it,
	// py_compile writes bytecode into the jail-read-only workspace and a valid
	// test is falsely rejected as "does not compile" on the container
	// backend; and a bare "VAR=value" argv[0] only works when something
	// shell-joins the command (the jail substrate) — the workspace substrate
	// execs argv directly and would try to run a file literally named
	// "PYTHONPYCACHEPREFIX=/tmp/corral-pyc".
	// py_compile takes multiple files in one invocation, so CompileCheck's
	// sequence is a single command.
	cc := p.CompileCheck("pricing.py", "test_pricing.py")
	if len(cc) != 1 {
		t.Fatalf("CompileCheck sequence = %v, want exactly 1 command", cc)
	}
	c0 := cc[0]
	if len(c0) != 7 || c0[0] != "env" || c0[1] != "PYTHONPYCACHEPREFIX=/tmp/corral-pyc" ||
		(c0[2] != "python3" && c0[2] != "python") || c0[3] != "-m" || c0[4] != "py_compile" ||
		c0[5] != "pricing.py" || c0[6] != "test_pricing.py" {
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

// fakeFS backs an ImportPath exists() function with a plain set of paths —
// pure, no real filesystem, so these cases prove the derivation itself
// without a jail or a live checkout.
type fakeFS map[string]bool

func (f fakeFS) exists(path string) bool { return f[path] }

// TestPythonImportPath pins the bug this fix exists to stop repeating: the
// test-writer used to be told, UNCONDITIONALLY, to "assume it is importable
// by its file's base name" — correct for a flat single-file workspace, but
// actively wrong for a real repo file inside a package (src/flask/cli.py
// imports as flask.cli; `import cli` cannot resolve). ImportPath is the
// pure derivation that replaces that guess with a real answer, or an
// honest "unknown" — see lang.Plugin.ImportPath's doc comment.
func TestPythonImportPath(t *testing.T) {
	p, _ := ByName("python")

	t.Run("real package tree (flask shape)", func(t *testing.T) {
		// The exact case from the bug report: src/flask/cli.py, with
		// src/flask/__init__.py present but src/__init__.py absent (src/ is
		// a layout directory, not a package) — must resolve to "flask.cli",
		// not "cli" and not "src.flask.cli".
		fs := fakeFS{"src/flask/__init__.py": true}
		got, ok := p.ImportPath("src/flask/cli.py", fs.exists)
		if !ok || got != "flask.cli" {
			t.Fatalf("ImportPath(src/flask/cli.py) = (%q, %v), want (flask.cli, true)", got, ok)
		}
	})

	t.Run("flat top-level file", func(t *testing.T) {
		// No directory at all: the base-name assumption IS correct here —
		// this is the single-file `--local` shape this fix must not regress.
		fs := fakeFS{}
		got, ok := p.ImportPath("pricing.py", fs.exists)
		if !ok || got != "pricing" {
			t.Fatalf("ImportPath(pricing.py) = (%q, %v), want (pricing, true)", got, ok)
		}
	})

	t.Run("no __init__.py anywhere", func(t *testing.T) {
		// A real, common case: a script-style file with no package markers
		// above it at all. Zero climbs is the CORRECT determination (Python
		// really does import a rootless module by its bare name), not a
		// "could not determine" case.
		fs := fakeFS{}
		got, ok := p.ImportPath("utils/helpers.py", fs.exists)
		if !ok || got != "helpers" {
			t.Fatalf("ImportPath(utils/helpers.py) = (%q, %v), want (helpers, true)", got, ok)
		}
	})

	t.Run("nested package", func(t *testing.T) {
		fs := fakeFS{
			"src/pkg/sub/__init__.py": true,
			"src/pkg/__init__.py":     true,
			// src/__init__.py deliberately absent: src/ is a layout dir.
		}
		got, ok := p.ImportPath("src/pkg/sub/mod.py", fs.exists)
		if !ok || got != "pkg.sub.mod" {
			t.Fatalf("ImportPath(src/pkg/sub/mod.py) = (%q, %v), want (pkg.sub.mod, true)", got, ok)
		}
	})

	t.Run("cannot be derived — no filesystem context", func(t *testing.T) {
		// exists == nil models the hosted/MCP run: no checkout on disk to
		// consult at all. Guessing "no packages here" would silently
		// reinstate the exact bug this fix removes; ok=false is the honest
		// answer.
		got, ok := p.ImportPath("src/flask/cli.py", nil)
		if ok || got != "" {
			t.Fatalf("ImportPath with nil exists = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("__init__.py itself canonicalizes to the package, not pkg.__init__", func(t *testing.T) {
		// src/flask/__init__.py resolves segments to ["flask","__init__"]
		// before canonicalization: import flask.__init__ WOULD technically
		// work (Python accepts it), but as a second, distinct module object
		// from the canonical "flask" — not what a human reviewing the
		// authored test would recognize. Canonical is "flask".
		fs := fakeFS{"src/flask/__init__.py": true}
		got, ok := p.ImportPath("src/flask/__init__.py", fs.exists)
		if !ok || got != "flask" {
			t.Fatalf("ImportPath(src/flask/__init__.py) = (%q, %v), want (flask, true)", got, ok)
		}
	})

	t.Run("top-level __init__.py has nothing to canonicalize down to", func(t *testing.T) {
		// A degenerate edge of the same rule: a top-level __init__.py with no
		// package directory to climb into at all. There is no canonical
		// package name to fall back to (stripping the sole segment would
		// leave an empty import), so this must stay "__init__", not "".
		fs := fakeFS{}
		got, ok := p.ImportPath("__init__.py", fs.exists)
		if !ok || got != "__init__" {
			t.Fatalf("ImportPath(__init__.py) = (%q, %v), want (__init__, true)", got, ok)
		}
	})

	// Package-directory-is-not-a-legal-identifier shapes: the bug re-entering
	// through a directory name instead of a missing fact. Each of these MUST
	// be ok=false — joining the segment anyway would hand the test-writer a
	// dotted string that reads like a real import but is a SyntaxError the
	// instant it is written (e.g. `import 2fa.totp`).
	notIdentifierCases := []struct {
		name string
		dir  string
	}{
		{"leading digit", "2fa"},
		{"dashed", "my-pkg"},
		{"dotted", "my.pkg"},
		{"spaced", "my pkg"},
		{"python keyword", "class"},
	}
	for _, c := range notIdentifierCases {
		t.Run("non-identifier package dir: "+c.name, func(t *testing.T) {
			fs := fakeFS{c.dir + "/__init__.py": true}
			codePath := c.dir + "/totp.py"
			got, ok := p.ImportPath(codePath, fs.exists)
			if ok || got != "" {
				t.Fatalf("ImportPath(%q) = (%q, %v), want (\"\", false) — %q is not a legal Python identifier", codePath, got, ok, c.dir)
			}
		})
	}

	t.Run("2fa/totp.py returns ok=false while src/flask/cli.py still resolves", func(t *testing.T) {
		// The exact pairing named in review: confirms the identifier guard
		// does not collaterally break the flask case it sits right next to.
		fs := fakeFS{"2fa/__init__.py": true, "src/flask/__init__.py": true}
		if got, ok := p.ImportPath("2fa/totp.py", fs.exists); ok || got != "" {
			t.Fatalf("ImportPath(2fa/totp.py) = (%q, %v), want (\"\", false)", got, ok)
		}
		if got, ok := p.ImportPath("src/flask/cli.py", fs.exists); !ok || got != "flask.cli" {
			t.Fatalf("ImportPath(src/flask/cli.py) = (%q, %v), want (flask.cli, true)", got, ok)
		}
	})
}

// TestIsPythonIdentifier pins the identifier/keyword guard ImportPath relies
// on directly, independent of the directory-climbing logic above.
func TestIsPythonIdentifier(t *testing.T) {
	valid := []string{"flask", "_private", "cli2", "a", "_", "totp"}
	for _, s := range valid {
		if !isPythonIdentifier(s) {
			t.Errorf("isPythonIdentifier(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "2fa", "my-pkg", "my.pkg", "my pkg", "class", "def", "None", "import"}
	for _, s := range invalid {
		if isPythonIdentifier(s) {
			t.Errorf("isPythonIdentifier(%q) = true, want false", s)
		}
	}
}

// TestPythonImportNote pins the two readouts ImportNote must produce: a
// concrete, confident fact when ImportPath succeeded, and an honest "could
// not determine — do not guess the base name" when it did not. Silently
// falling back to the base-name claim on ok=false is exactly the bug.
func TestPythonImportNote(t *testing.T) {
	p, _ := ByName("python")

	known := p.ImportNote("flask.cli", true)
	if !strings.Contains(known, "flask.cli") {
		t.Fatalf("ImportNote(known) = %q, want it to state the derived import", known)
	}
	if strings.Contains(known, "could not be determined") {
		t.Fatalf("ImportNote(known) = %q, must not also hedge", known)
	}

	unknown := p.ImportNote("", false)
	if !strings.Contains(unknown, "could not be determined") {
		t.Fatalf("ImportNote(unknown) = %q, want an honest could-not-determine note", unknown)
	}
	if strings.Contains(unknown, "base file name") {
		t.Fatalf("ImportNote(unknown) = %q, must not assert the base-name convention", unknown)
	}
}

// TestOtherPluginsDeclineImportPath pins that go/js/ts/ruby all say "not
// applicable" rather than guessing: their own test-authoring convention
// (same-package for Go, same-directory relative import/require for the
// rest) already resolves correctly regardless of nesting, so there is
// nothing for ImportPath to correct — see each plugin's own doc comment.
func TestOtherPluginsDeclineImportPath(t *testing.T) {
	for _, name := range []string{"go", "javascript", "typescript", "ruby"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("plugin %q not registered", name)
		}
		if got, ok := p.ImportPath("src/pkg/foo.ext", func(string) bool { return true }); ok || got != "" {
			t.Errorf("%s.ImportPath = (%q, %v), want (\"\", false)", name, got, ok)
		}
		if got := p.ImportNote("pkg.foo", true); got != "" {
			t.Errorf("%s.ImportNote = %q, want \"\"", name, got)
		}
	}
}
