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
	if got := p.CompileCheck("foo.ts", "foo.test.ts"); !reflect.DeepEqual(got, [][]string{{"tsc", "--noEmit", "-p", "tsconfig.json"}}) {
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
