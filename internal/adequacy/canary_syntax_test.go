// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// canarySyntaxChecks is, per registered language, a command that exits
// non-zero on a file that is not valid source in that language. The file's
// path is appended. An entry needs only the language's own toolchain; a
// missing toolchain skips that language and says so, the same way the
// jail tests do.
var canarySyntaxChecks = map[string]struct {
	ext  string
	argv []string
}{
	"go":         {".go", []string{"gofmt", "-e"}},
	"python":     {".py", []string{"python3", "-m", "py_compile"}},
	"ruby":       {".rb", []string{"ruby", "-c"}},
	"javascript": {".js", []string{"node", "--check"}},
	"typescript": {".ts", []string{"tsc", "--noEmit", "--skipLibCheck"}},
	"php":        {".php", []string{"php", "-l"}},
	"swift":      {".swift", []string{"swiftc", "-parse"}},
	"rust":       {".rs", []string{"rustc", "--edition", "2021", "--crate-type", "lib", "--emit=metadata", "--out-dir", "."}},
}

// CanaryCode is corral's positive control: dropped in place of a file, it
// must make any check that genuinely reads that file fail. That only holds
// if it is invalid source in EVERY language corral audits. It was not, in
// PHP: text outside `<?php` is literal output, so PHPUnit loaded a canary
// test file, printed it, and passed. Every `certify --repo` on a PHP
// project was then refused as "your test command does not collect the
// authored test" (ramsey/uuid, 2026-11-15), after goal derivation had
// already been paid for.
//
// The language list is derived from the plugin registry, so a new language
// fails here until it says how its syntax is checked.
func TestCanaryCodeIsInvalidSourceInEveryLanguage(t *testing.T) {
	for _, name := range lang.Names() {
		t.Run(name, func(t *testing.T) {
			c, ok := canarySyntaxChecks[name]
			if !ok {
				t.Fatalf("language %q is registered but has no syntax check here: add one, so the canary is proven invalid in it", name)
			}
			if _, err := exec.LookPath(c.argv[0]); err != nil {
				t.Skipf("%s not on PATH: cannot check the canary is invalid %s", c.argv[0], name)
			}
			dir := t.TempDir()
			f := filepath.Join(dir, "canary"+c.ext)
			if err := os.WriteFile(f, []byte(adequacy.CanaryCode), 0o600); err != nil {
				t.Fatal(err)
			}
			// Negative control: the checker must accept valid source, or a
			// checker that fails on everything would pass this test.
			ok2 := filepath.Join(dir, "valid"+c.ext)
			if err := os.WriteFile(ok2, []byte(validSource[name]), 0o600); err != nil {
				t.Fatal(err)
			}
			valid := exec.Command(c.argv[0], append(c.argv[1:], ok2)...) // #nosec G204 -- fixed argv from the table above
			valid.Dir = dir
			if out, err := valid.CombinedOutput(); err != nil {
				t.Fatalf("%v rejected VALID %s source, so it cannot judge the canary: %v\n%s", c.argv, name, err, out)
			}
			cmd := exec.Command(c.argv[0], append(c.argv[1:], f)...) // #nosec G204 -- fixed argv from the table above
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("CanaryCode %q is VALID %s source (%v accepted it), so the positive control cannot fail there:\n%s", adequacy.CanaryCode, name, c.argv, out)
			}
		})
	}
}

var validSource = map[string]string{
	"go":         "package p\n",
	"python":     "x = 1\n",
	"ruby":       "x = 1\n",
	"javascript": "const x = 1;\n",
	"typescript": "export const x: number = 1;\n",
	"php":        "<?php\n$x = 1;\n",
	"rust":       "pub fn f() {}\n",
	"swift":      "let x = 1\n",
}
