// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

func TestPHPPlugin(t *testing.T) {
	p, ok := ByName("php")
	if !ok {
		t.Fatal("php plugin not registered")
	}
	if !p.Detect("app/Invoice.php") || p.Detect("app/Invoice.rb") {
		t.Fatal("Detect must match .php only")
	}
	if got := pairing.Candidates(p.TestRules(), "app/Invoice.php")[0]; got.Path != "app/InvoiceTest.php" || got.Rank != 0 {
		t.Fatalf("Candidates()[0] = %+v, want {app/InvoiceTest.php, 0}", got)
	}
	if got := pairing.Candidates(p.TestRules(), "Invoice.php")[0]; got.Path != "InvoiceTest.php" || got.Rank != 0 {
		t.Fatalf("Candidates()[0] = %+v, want {InvoiceTest.php, 0}", got)
	}
	// A two-command SEQUENCE, not a single argv element: `php -l` only
	// checks one file per invocation, and the workspace substrate execs
	// argv directly with no shell to splice a `&&` for it. argv[0] is the
	// DERIVED interpreter (phpInterpreter's own resolution), never the bare
	// literal "php" — see php_interpreter_test.go: on Debian/Ubuntu that
	// name is commonly a symlink through /etc/alternatives, invisible from
	// inside the sandbox even though it resolves fine on the host.
	cc := p.CompileCheck("Invoice.php", "InvoiceTest.php")
	if len(cc) != 3 {
		t.Fatalf("CompileCheck = %v, want a 3-command sequence (two lints and a load)", cc)
	}
	wantInterp, interpErr := phpInterpreter(nil)
	if interpErr != nil {
		t.Skipf("no php on PATH — cannot derive the expected interpreter on this host: %v", interpErr)
	}
	for i, want := range []string{"Invoice.php", "InvoiceTest.php"} {
		if !reflect.DeepEqual(cc[i], []string{wantInterp, "-l", want}) {
			t.Fatalf("CompileCheck()[%d] = %v, want [%q -l %q]", i, cc[i], wantInterp, want)
		}
	}
	if cmd := p.TestCmd(); len(cmd) == 0 || cmd[0] != "vendor/bin/phpunit" {
		t.Fatalf("TestCmd = %v, want vendor/bin/phpunit", cmd)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "PHPUnit") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("php system prompts must be language-appropriate")
	}
	if p.PromptLang() != "PHP" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}

// TestPHPTestRoots pins the recursive-search roots PHP's rules derive: the
// generic "tests" default plus the singular `test/` spelling named in the
// design doc.
func TestPHPTestRoots(t *testing.T) {
	p, _ := ByName("php")
	got := pairing.Roots(p.TestRules())
	want := []string{"tests", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pairing.Roots(TestRules()) = %v, want %v", got, want)
	}
}

// TestPHPCandidatesOrder pins the ordered-candidate-list contract: sibling
// FooTest.php first, then the tests/ and test/ mirrors (leading directory
// replaced, matching the shipped plugins' parallel-tree convention).
func TestPHPCandidatesOrder(t *testing.T) {
	p, _ := ByName("php")
	cases := []struct {
		name string
		in   string
		want []pairing.Candidate
	}{
		{
			name: "top-level file",
			in:   "Invoice.php",
			want: []pairing.Candidate{
				{Path: "InvoiceTest.php", Rank: 0},
				{Path: "tests/InvoiceTest.php", Rank: 1},
				{Path: "test/InvoiceTest.php", Rank: 1},
			},
		},
		{
			name: "single-segment src dir",
			in:   "src/Invoice.php",
			want: []pairing.Candidate{
				{Path: "src/InvoiceTest.php", Rank: 0},
				{Path: "tests/InvoiceTest.php", Rank: 1},
				{Path: "test/InvoiceTest.php", Rank: 1},
			},
		},
		{
			name: "nested src dir",
			in:   "src/Billing/Invoice.php",
			want: []pairing.Candidate{
				{Path: "src/Billing/InvoiceTest.php", Rank: 0},
				{Path: "tests/Billing/InvoiceTest.php", Rank: 1},
				{Path: "test/Billing/InvoiceTest.php", Rank: 1},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pairing.Candidates(p.TestRules(), c.in)
			if len(got) != len(c.want) {
				t.Fatalf("Candidates(%q) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i].Path != c.want[i].Path || got[i].Rank != c.want[i].Rank {
					t.Errorf("Candidates(%q)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// TestPHPPreflight exercises the "php + test-command argv[0] runnable"
// requirement from the design doc: Preflight must fail closed when php
// itself is unavailable, independent of whatever the test command names.
func TestPHPPreflight(t *testing.T) {
	p, _ := ByName("php")
	// A nonsense binary name can never resolve on any host: Preflight must
	// report an error, not a silent pass.
	if err := p.Preflight("", []string{"totally-not-a-real-binary-xyz"}); err == nil {
		t.Fatal("Preflight with an unresolvable test command binary must fail closed")
	}
}

// TestPHPCoverageCmdDisablesTheProjectsOwnCoverage pins the sixth review's
// M3: a phpunit.xml that requests its own coverage report makes
// php-code-coverage's PCOV driver \pcov\clear() before every test, so
// corral's shutdown snapshot held nothing — a header-only report and a
// diagnosis blaming the suite. PHPUnit's own --no-coverage is appended when
// the runner is phpunit; a composer script is left alone.
func TestPHPCoverageCmdDisablesTheProjectsOwnCoverage(t *testing.T) {
	cmd, ok := phpPlugin{}.CoverageCmd([]string{"vendor/bin/phpunit", "-c", "phpunit.xml"})
	if !ok {
		t.Fatal("no coverage command")
	}
	if got := cmd[len(cmd)-2:]; got[0] != "phpunit.xml" || got[1] != "--no-coverage" {
		t.Errorf("phpunit run must carry --no-coverage: %q", cmd)
	}
	cmd, ok = phpPlugin{}.CoverageCmd([]string{"composer", "test"})
	if !ok {
		t.Fatal("no coverage command for composer")
	}
	if strings.Contains(strings.Join(cmd, " "), "--no-coverage") {
		t.Errorf("a composer script must not have phpunit flags appended: %q", cmd)
	}
}

// TestPHPLoadCheckUsesTheProjectsAutoloader is the webmozart/assert case
// (2026-10-15): src/Assert.php uses a trait declared in src/Mixin.php, which
// only Composer's autoloader can find. A load check that required the file
// bare rejected the UNMODIFIED source with `Trait ... not found`, so every
// test the writer authored for a PHP class with a dependency was refused as
// "does not compile". The check runs from the tree root, where
// vendor/autoload.php is; with it, the file loads, and a genuinely missing
// parent is still refused.
func TestPHPLoadCheckUsesTheProjectsAutoloader(t *testing.T) {
	p, _ := ByName("php")
	if _, err := phpInterpreter(nil); err != nil {
		t.Skipf("no php on PATH: %v", err)
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("vendor/autoload.php", `<?php
spl_autoload_register(function ($c) {
    $f = __DIR__ . '/../src/' . substr($c, strlen('App\\')) . '.php';
    if (is_file($f)) { require $f; }
});
`)
	write("src/Mixin.php", "<?php\nnamespace App;\ntrait Mixin { public static function hi() { return 1; } }\n")
	write("src/Assert.php", "<?php\nnamespace App;\nclass Assert { use Mixin; }\n")
	write("src/Broken.php", "<?php\nnamespace App;\nclass Broken extends DoesNotExist {}\n")

	load := func(code string) (string, error) {
		cc := p.CompileCheck(code, "tests/AssertTest.php")
		cmd := exec.Command(cc[len(cc)-1][0], cc[len(cc)-1][1:]...) // #nosec G204 -- the plugin's own check, in a temp dir
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := load("src/Assert.php"); err != nil {
		t.Fatalf("the load check rejected a class whose trait the project's autoloader provides: %v\n%s", err, out)
	}
	if out, err := load("src/Broken.php"); err == nil {
		t.Fatalf("the load check accepted a class extending a class that exists nowhere:\n%s", out)
	}
}

// PHP COUNTS COME FROM A REAL pcov RUN of the fixture suite (the captured
// report a live run is held to). Shape.php is an interface: pcov reports a hit
// for it only on the file's implicit include marker, one line past its end,
// which is not a line of the file and must not make it read as executed.
func TestPHPCoverageCountsFromRealRun(t *testing.T) {
	assertFixtureCounts(t, capturedCounts(t, "php"), "lib/Calc.php", "lib/Dead.php", "lib/Unloaded.php", "lib/Shape.php")
}

// The coverage run loads pcov ONCE. The injected ini used to name
// `extension=pcov` whether or not the machine already loaded it, and a
// second load makes PHP print `PHP Warning:  Module "pcov" is already loaded`
// on stderr at every start-up. PHPUnit fails a test it runs in a separate
// process when that process writes to stderr, so on ramsey/uuid (2026-11-18,
// PHPUnit 9.6.39, PHP 8.5.4) eight test files failed under coverage and
// passed without it, and gave no unit evidence. The run still measures: the
// extension is named when, under the same ini scan path, PHP does not have
// it.
func TestPHPCoverageLoadsPcovOnce(t *testing.T) {
	if _, err := phpInterpreter(nil); err != nil {
		t.Skipf("SKIP php coverage: no php on PATH: %v", err)
	}
	if out, err := exec.Command("php", "-r", `exit(extension_loaded("pcov") ? 0 : 1);`).CombinedOutput(); err != nil {
		t.Skipf("SKIP php coverage: this php does not load pcov: %v %s", err, out)
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"lib/Calc.php": "<?php\nfunction add(int $a, int $b): int {\n    return $a + $b;\n}\nfunction unused(): int {\n    return 0;\n}\n",
		"main.php":     "<?php\nrequire __DIR__ . '/lib/Calc.php';\nexit(add(1, 2) === 3 ? 0 : 1);\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd, ok := phpPlugin{}.CoverageCmd([]string{"php", "main.php"})
	if !ok {
		t.Fatal("no coverage command")
	}
	var stdout, stderr bytes.Buffer
	x := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the plugin's own coverage command, in a temp dir
	x.Dir = dir
	x.Stdout, x.Stderr = &stdout, &stderr
	if err := x.Run(); err != nil {
		t.Fatalf("coverage run: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "already loaded") {
		t.Errorf("the coverage run loaded pcov a second time:\n%s", stderr.String())
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := phpPlugin{}.CoverageCounts(stdout.String(), root)
	if err != nil {
		t.Fatalf("CoverageCounts: %v\n%s", err, stdout.String())
	}
	if c := counts["lib/Calc.php"]; c.Executed == 0 || c.Executed >= c.Executable {
		t.Errorf("lib/Calc.php: one function ran and one did not, got %+v (all: %v)", c, counts)
	}
}

// The other half of TestPHPCoverageLoadsPcovOnce: when the probe says PHP
// has no pcov under the injected scan path, the extension IS named. The
// probe is answered "no" here by a `php` shim that passes every other call
// to the real php; this machine does load pcov, so the line the wrapper
// then writes shows as PHP's "already loaded" warning, which is the proof
// it was written.
func TestPHPCoverageNamesPcovWhenTheProbeFindsNone(t *testing.T) {
	real, err := exec.LookPath("php")
	if err != nil {
		t.Skipf("SKIP php coverage: no php on PATH: %v", err)
	}
	if out, err := exec.Command(real, "-r", `exit(extension_loaded("pcov") ? 0 : 1);`).CombinedOutput(); err != nil {
		t.Skipf("SKIP php coverage: this php does not load pcov: %v %s", err, out)
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "bin")
	if err := os.MkdirAll(shim, 0o750); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in *extension_loaded*) exit 1;; esac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "php"), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable shim in a temp dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.php"), []byte("<?php\nexit(0);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, ok := phpPlugin{}.CoverageCmd([]string{"php", "main.php"})
	if !ok {
		t.Fatal("no coverage command")
	}
	var stderr bytes.Buffer
	x := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the plugin's own coverage command, in a temp dir
	x.Dir = dir
	x.Env = append(os.Environ(), "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	x.Stderr = &stderr
	_ = x.Run()
	if !strings.Contains(stderr.String(), `Module "pcov" is already loaded`) {
		t.Errorf("a probe that finds no pcov must name the extension; stderr:\n%s", stderr.String())
	}
}
