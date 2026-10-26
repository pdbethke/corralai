// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// THE JAILED COMPILE CHECK RUNS THE PROJECT'S tsc, BY EXECUTION (ruling R26):
// the repository's node_modules is bound read-only into the jail exactly as
// a scan binds it, and the compile check finds its tsc there. A fake project
// tsc says what it ran with; with CORRALAI_TYPESCRIPT_NODE_MODULES set to a
// real node_modules holding typescript, the real compiler rejects a type
// error inside the jail.
func TestJailedTypeScriptCompileCheckRunsTheProjectsTsc(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap backend: %v", err)
	}
	ts, _ := lang.ByName("typescript")
	cmd := ts.CompileCheck("foo.ts", "foo.test.ts")[0]
	files := map[string]string{
		"foo.ts":      "export function f(n: number): number { return n; }\n",
		"foo.test.ts": "import { f } from './foo.ts';\nexport const g: number = f(1);\n",
	}

	nm := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(filepath.Join(nm, ".bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, ".bin", "tsc"), []byte("#!/bin/sh\necho \"corral-project-tsc $*\"\nexit 0\n"), 0o700); err != nil { // #nosec G306 -- an executable the jail runs
		t.Fatal(err)
	}
	jail := NewJail(backend, 60*time.Second, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}})).(bwrapJail)
	ok, out, err := jail.RunTestVerbose(context.Background(), files, cmd)
	if err != nil || !ok || !strings.Contains(out, "corral-project-tsc -p ") {
		t.Errorf("the jailed compile check did not run the project's tsc: ok=%v err=%v\n%s", ok, err, out)
	}

	realNM := os.Getenv("CORRALAI_TYPESCRIPT_NODE_MODULES")
	if realNM == "" {
		t.Skip("CORRALAI_TYPESCRIPT_NODE_MODULES is not set — the real-compiler half needs a node_modules with typescript in it")
	}
	real := NewJail(backend, 120*time.Second, WithReadOnlyBinds([]DepBind{{Host: realNM, Rel: "node_modules"}})).(bwrapJail)
	if ok, out, err := real.RunTestVerbose(context.Background(), files, cmd); err != nil || !ok {
		t.Errorf("the project's real tsc rejected a correct file in the jail: ok=%v err=%v\n%s", ok, err, out)
	}
	bad := map[string]string{"foo.ts": "export function f(n: number): number { return 'x'; }\n", "foo.test.ts": files["foo.test.ts"]}
	if ok, out, _ := real.RunTestVerbose(context.Background(), bad, cmd); ok || !strings.Contains(out, "TS2322") {
		t.Errorf("the project's real tsc must reject a type error in the jail (TS2322): ok=%v\n%s", ok, out)
	}
}
