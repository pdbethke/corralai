// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestTypeScriptPlugin(t *testing.T) {
	p, ok := ByName("typescript")
	if !ok {
		t.Fatal("typescript plugin not registered")
	}
	if !p.Detect("app/foo.ts") || p.Detect("app/foo.js") || p.Detect("app/foo.tsx") {
		t.Fatal("Detect must match .ts only (not .tsx in v1)")
	}
	if got := p.TestPaths("pkg/foo.ts")[0]; got.Path != "pkg/foo.test.ts" || got.Rank != 0 {
		t.Fatalf("TestPaths()[0] = %+v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--experimental-strip-types", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	// The compile check is SCOPED to the audited file and its test. It used to
	// be project-mode (`-p tsconfig.json`), which is correct only in
	// single-file mode where our scaffold is the only tsconfig — against a real
	// repo the project's own config governs and any pre-existing error anywhere
	// fails every authored test. See TestTSCompileCheckIsScopedNotProjectWide.
	if got := p.CompileCheck("foo.ts", "foo.test.ts"); len(got) != 1 ||
		!reflect.DeepEqual(got[0][:2], []string{"sh", "-c"}) ||
		!strings.Contains(got[0][2], "tsc -p") || !strings.Contains(got[0][2], "$PWD/foo.ts") || !strings.Contains(got[0][2], "$PWD/foo.test.ts") {
		t.Fatalf("CompileCheck = %v", got)
	}
	sc := p.Scaffold()
	if _, ok := sc["tsconfig.json"]; !ok {
		t.Fatalf("Scaffold must include tsconfig.json, got %v", sc)
	}
	if !strings.Contains(sc["tsconfig.json"], "allowImportingTsExtensions") {
		t.Fatal("tsconfig must allow importing .ts extensions")
	}
	// The type-check must be self-contained (no @types/node dependency, which
	// isn't in the jail workspace): the scaffold ships an ambient shim for the
	// node builtins an audit test uses, and the tsconfig must NOT force
	// types:["node"] (which would demand @types/node and fail with TS2688).
	if strings.Contains(sc["tsconfig.json"], `"types"`) {
		t.Fatal("tsconfig must not pin types:[\"node\"] — @types/node is absent in the jail")
	}
	shim, ok := sc["corral-env.d.ts"]
	if !ok || !strings.Contains(shim, `declare module "node:test"`) || !strings.Contains(shim, `declare module "node:assert"`) {
		t.Fatalf("Scaffold must include an ambient shim declaring node:test + node:assert, got %v", sc)
	}
	if !strings.Contains(p.TestWriterSystem(), "node:test") || !strings.Contains(p.TestWriterSystem(), ".ts") {
		t.Fatal("ts writer prompt must instruct node:test + explicit .ts import")
	}
	if p.PromptLang() != "TypeScript" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestTypeScriptTestPathsOrder mirrors TestJavaScriptTestPathsOrder with the
// .ts suffix — see there for the ordering rationale.
func TestTypeScriptTestPathsOrder(t *testing.T) {
	p, _ := ByName("typescript")
	cases := []struct {
		name string
		in   string
		want []TestCandidate
	}{
		{
			name: "top-level file",
			in:   "foo.ts",
			want: []TestCandidate{
				{Path: "foo.test.ts", Rank: 0}, {Path: "foo.spec.ts", Rank: 0}, {Path: "__tests__/foo.test.ts", Rank: 1},
				{Path: "test/foo.test.ts", Rank: 2}, {Path: "tests/foo.test.ts", Rank: 2},
			},
		},
		{
			name: "src/ layout",
			in:   "src/pkg/foo.ts",
			want: []TestCandidate{
				{Path: "src/pkg/foo.test.ts", Rank: 0}, {Path: "src/pkg/foo.spec.ts", Rank: 0}, {Path: "src/pkg/__tests__/foo.test.ts", Rank: 1},
				{Path: "test/pkg/foo.test.ts", Rank: 2}, {Path: "tests/pkg/foo.test.ts", Rank: 2},
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

// TestTSCompileCheckIsScopedNotProjectWide pins the fix for a compile gate that
// could never pass on a real repository. Project mode (`-p tsconfig.json`) uses
// the PROJECT's tsconfig under --repo-dir, so any pre-existing type error
// anywhere in the repo fails the check — and the test-writer's authored test is
// reported as non-compiling no matter how correct it is, leaving every survivor
// unproven while the run still grades and looks healthy.
//
// Same defect, and same fix, as scoping `go vet` to the audited package.
func TestTSCompileCheckIsScopedNotProjectWide(t *testing.T) {
	cmds := tsPlugin{}.CompileCheck("src/client/ApiError.ts", "src/client/__tests__/ApiError.test.ts")
	if len(cmds) != 1 {
		t.Fatalf("expected a single command, got %v", cmds)
	}
	got := strings.Join(cmds[0], " ")
	// A project file of OUR OWN in a temp dir (-p corral-tsc.json), never the
	// repository's tsconfig.json: that is what keeps the check scoped on
	// tsc 5 and alive at all on tsc 6 (TS5112 refuses files on the command
	// line beside a tsconfig).
	if strings.Contains(got, "tsconfig.json") || !strings.Contains(got, "corral-tsc.json") {
		t.Fatalf("compile check must run against corral's own project file, never the repository's tsconfig: %q", got)
	}
	for _, want := range []string{"src/client/ApiError.ts", "src/client/__tests__/ApiError.test.ts", `\"noEmit\":true`, `\"strict\":true`, "corral-env.d.ts"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in the scoped check, got %q", want, got)
		}
	}
}

// TestTSCompileCheckNoDuplicateWhenTestIsTheCode guards the single-file shape,
// where codePath and testPath can be the same file: naming it twice makes tsc
// error on a duplicate input rather than type-check it.
func TestTSCompileCheckNoDuplicateWhenTestIsTheCode(t *testing.T) {
	got := strings.Join(tsPlugin{}.CompileCheck("a.ts", "a.ts")[0], " ")
	if strings.Count(got, `"$PWD/a.ts"`) != 1 && strings.Count(got, `$PWD/a.ts`) != 1 {
		t.Fatalf("the same path must be named once, got %q", got)
	}
}
