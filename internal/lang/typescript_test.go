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
	if got := p.TestPath("pkg/foo.ts"); got != "pkg/foo.test.ts" {
		t.Fatalf("TestPath = %q", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--experimental-strip-types", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	if got := p.CompileCheck("foo.ts", "foo.test.ts"); !reflect.DeepEqual(got, []string{"tsc", "--noEmit", "-p", "tsconfig.json"}) {
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
