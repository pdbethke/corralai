// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func init() { Register(phpPlugin{}) }

type phpPlugin struct{}

func (phpPlugin) Name() string                { return "php" }
func (phpPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".php" }

// Scaffold is empty: PHPUnit resolves the code under test via Composer's
// PSR-4 autoloader (vendor/autoload.php), not a file corral needs to place
// itself.
func (phpPlugin) Scaffold() map[string]string { return map[string]string{} }

// TestCmd runs the project's own PHPUnit binary. Phase 1 is PHPUnit only
// (see the design doc); vendor/bin/phpunit is a self-executing script (a
// `#!/usr/bin/env php` shebang), so it needs no separate `php` prefix to
// invoke.
func (phpPlugin) TestCmd() []string { return []string{"vendor/bin/phpunit"} }

// CompileCheck syntax-checks BOTH files with `<interpreter> -l` (offline, no
// autoloading needed). `php -l` only ever reports on a SINGLE file per
// invocation, so this returns a TWO-command sequence — codePath then
// testPath, run in order, stopping at the first failure — rather than
// trying to splice `&&` into one argv element: the workspace substrate execs
// argv directly with no shell to interpret it (see ruby.go's identical
// CompileCheck for the bug class this avoids).
//
// The interpreter is phpInterpreter(nil)'s own resolved, symlink-free
// path — NEVER the bare literal "php". CompileCheck's signature carries no
// testCmd (unlike Preflight), so it always takes phpInterpreter's LookPath+
// EvalSymlinks branch; that is sufficient here because a syntax-only `-l`
// check does not need to be the SAME interpreter binary the operator's own
// test command grades with, only a REAL one the sandbox can actually see.
// A bare "php" resolved fine on the HOST in the acceptance run that found
// this bug (webmozart/assert, Debian) but /usr/bin/php was a symlink
// through /etc/alternatives the sandbox's mount table does not carry —
// baseline passed (the operator's own testCmd named an explicit php8.5),
// and every one of 40 mutants was invalidated by THIS hardcoded literal
// with "sh: 1: php: not found". phpInterpreter resolving to a real absolute
// path under /usr fixes it: /usr is always bind-mounted whole.
//
// On the rare host where phpInterpreter itself fails (no php resolvable at
// all), the literal "php" is kept as the argv[0] fallback: CompileCheck has
// no error return, so there is nothing better to do than the exact
// pre-existing behavior — and Preflight (which DOES return an error) is
// what actually refuses the run before this is ever reached.
func (phpPlugin) CompileCheck(codePath, testPath string) [][]string {
	interp, err := phpInterpreter(nil)
	if err != nil {
		interp = "php"
	}
	return [][]string{
		{interp, "-l", codePath},
		{interp, "-l", testPath},
	}
}

// phpVersionedInterpreterRe matches a php interpreter's basename, with or
// without a version suffix (php, php8, php8.3, ...) — deliberately narrow
// so it never matches "phpunit" or any other php-prefixed tool.
var phpVersionedInterpreterRe = regexp.MustCompile(`^php[0-9.]*$`)

// phpInterpreter derives the ACTUAL php binary to invoke for a check corral
// runs on the code's own behalf (CompileCheck's `-l`, and Preflight's own
// jail probe below) — NOT the interpreter that grades the operator's suite,
// which is whatever their own testCmd already names verbatim regardless of
// what this returns.
//
// Debian/Ubuntu's /usr/bin/php is commonly a symlink through
// /etc/alternatives — resolves fine with a bare LookPath on the HOST (the
// alternatives system lives entirely on the host filesystem), but the
// sandbox's mount table carries only /usr ITSELF (see internal/sandbox's
// bwrap Wrap(), which `--ro-bind`s exactly that path and nothing under
// /etc), so that same chain can dangle once inside it.
//
// testCmd's own argv[0] is preferred WHEN it already names a php variant
// (basename matches phpVersionedInterpreterRe): that is the operator's own
// choice, used VERBATIM — no further resolution attempted, mirroring
// preflightBin's identical "the operator's own command is stronger evidence
// than any stock guess" philosophy. Otherwise (testCmd names something else,
// e.g. vendor/bin/phpunit, or is empty), "php" is resolved via LookPath then
// filepath.EvalSymlinks to its FINAL real path — the form the sandbox's
// /usr bind-mount can actually see, unlike the bare, possibly-symlinked
// name.
func phpInterpreter(testCmd []string) (string, error) {
	interp, _, err := phpInterpreterAndProbe(testCmd)
	return interp, err
}

// phpInterpreterAndProbe derives BOTH values phpInterpreter's two callers
// need, which DIFFER for the stock, no-explicit-interpreter case:
//
//   - interp is phpInterpreter's own result — what CompileCheck execs
//     DIRECTLY (no shell, no shebang), so a fully host-resolved absolute
//     path is exactly right there.
//   - probe is what Preflight's jail check must run to predict whether the
//     ACTUAL TEST COMMAND will work. For an explicit interpreter (testCmd's
//     argv0 already names a php variant), the operator's own command execs
//     it directly too — probe equals interp, no indirection.
//
// For the STOCK command (`vendor/bin/phpunit`, or no testCmd at all — the
// dominant real shape, and the exact one the acceptance run's first paid
// attempt burned spend on), the operator's command never names an
// interpreter in argv at all: it runs THROUGH vendor/bin/phpunit's own
// `#!/usr/bin/env php` shebang, and `env` resolves that BARE name "php" via
// its OWN lookup on the JAIL's PATH, at RUN TIME — not via anything this
// plugin pre-resolved on the host. Probing the fully-resolved absolute path
// in that case would only prove CompileCheck's OWN invocation is fine; it
// says nothing about whether env's bare lookup survives entering the
// sandbox, which is precisely the gap that dangled: /usr/bin/php resolves
// through /etc/alternatives on the HOST (where env would also find it), but
// the jail's mount table carries no /etc/alternatives, so the SAME bare
// lookup dangles once inside it even though the host-resolved real path
// (used only by CompileCheck, never by the actual suite run) is perfectly
// reachable. So probe is deliberately the literal "php" in this branch —
// exactly what env will ask the jail's PATH for.
func phpInterpreterAndProbe(testCmd []string) (interp, probe string, err error) {
	if bin, ok := firstExecutableToken(testCmd); ok && phpVersionedInterpreterRe.MatchString(filepath.Base(bin)) {
		return bin, bin, nil
	}
	resolved, err := exec.LookPath("php")
	if err != nil {
		return "", "", fmt.Errorf("lang: php: %w", err)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", "", fmt.Errorf("lang: php: could not resolve %q to its real path: %w", resolved, err)
	}
	return real, "php", nil
}

// TestPaths covers PHPUnit's dominant naming convention — a `Test` SUFFIX on
// the class/file name, PSR-4's own convention — most specific first:
//
//  1. sibling FooTest.php       — same directory as the source.
//  2. tests/<subpath>/FooTest.php — the classic src/-vs-tests/ layout,
//     <subpath> is dir with its leading component (conventionally `src`)
//     replaced by `tests` rather than nested under it.
//  3. test/<subpath>/FooTest.php  — the singular spelling some projects use.
func (phpPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)
	testName := base + "Test.php"

	out := []TestCandidate{
		{Path: joinDir(dir, testName), Rank: 0},
		{Path: filepath.Join("tests", sub, testName), Rank: 1},
		{Path: filepath.Join("test", sub, testName), Rank: 1},
	}
	return dedupeCandidates(out)
}

// TestRoots names PHP's own additional conventional test roots (beyond
// reposcan's generic "tests" default): the singular `test/` spelling some
// projects use alongside the plural.
func (phpPlugin) TestRoots() []string { return []string{"tests", "test"} }

// Preflight requires the DERIVED php interpreter (phpInterpreter — the
// operator's own testCmd argv[0] when it already names a php variant, else
// "php" resolved via LookPath+EvalSymlinks to its real path) AND the test
// command's own binary — or, when the operator named an explicit test
// command, THAT command's own binary (see preflightBin and
// Plugin.Preflight's doc comment). The interpreter is checked
// unconditionally because `vendor/bin/phpunit`'s shebang invokes it even
// though the stock command never names it in argv itself; a host with the
// phpunit binary present but no working php interpreter would otherwise
// pass preflight and fail at run time instead of refusing up front.
//
// It THEN probes that SAME interpreter through an actual sandbox, before
// any model spend — a host-only LookPath check cannot see the gap this
// closes: /usr/bin/php resolving fine via the OS's /etc/alternatives
// indirection on the HOST tells you nothing about whether the SANDBOX's own
// mount table (which does not carry /etc/alternatives) can still follow it.
// This is the exact bug an acceptance run on webmozart/assert hit: baseline
// passed (an explicit php8.5 in testCmd), but CompileCheck's then-hardcoded
// "php -l" invalidated all 40 mutants with "sh: 1: php: not found" — a cost
// a free, pre-spend probe would have caught. See phpJailPreflight.
func (phpPlugin) Preflight(testCmd []string) error {
	interp, probe, err := phpInterpreterAndProbe(testCmd)
	if err != nil {
		return err
	}
	if err := toolOnPath(interp); err != nil {
		return err
	}
	if err := toolOnPath(preflightBin(testCmd, "vendor/bin/phpunit")); err != nil {
		return err
	}
	iso, isoErr := sandbox.Resolve(sandbox.Config{})
	if isoErr != nil {
		// No sandbox backend at all is a SEPARATE, already-owned gate
		// (certify --local's own "sandbox starts" check, run before any
		// plugin is even resolved) — declining here avoids double-reporting
		// the same absence under a misleading "php" name.
		return nil
	}
	return phpJailPreflight(iso, probe)
}

// phpJailPreflight verifies probe is reachable from INSIDE a sandbox — not
// merely present on the host — by actually running `probe -v` there through
// internal/sandbox's own Run (the same primitive cmd/corral/jail.go's
// newRunJail and its "doctor <-> run parity" checks are built on), with no
// dependency binds at all: a bare version probe needs none, and /usr —
// where a REAL php always lives — is unconditionally bind-mounted by every
// backend regardless of Options.ReadOnlyBinds. probe is whichever of
// phpInterpreterAndProbe's two values actually matters for what will run —
// see that function's doc comment for why they differ for the stock,
// no-explicit-interpreter case.
//
// A missing/unreadable probe workspace is treated as inconclusive (nil, not
// an error): this is a best-effort ADDITIONAL check layered on top of the
// host-side ones above, not the sole gate.
func phpJailPreflight(iso sandbox.Isolator, probe string) error {
	dir, err := os.MkdirTemp("", "corral-php-preflight-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)

	res := sandbox.Run(context.Background(), shellQuote(probe)+" -v", sandbox.Options{
		Workspace: dir,
		Backend:   iso,
		Timeout:   15 * time.Second,
	})
	if res.ExitCode == 0 && !res.TimedOut {
		return nil
	}
	detail := strings.TrimSpace(res.Output)
	if res.Err != "" {
		if detail != "" {
			detail += "\n"
		}
		detail += res.Err
	}
	return fmt.Errorf(
		"lang: php: %q resolves on the HOST but is not reachable INSIDE the sandbox (exit %d): %s\n"+
			"this is commonly Debian/Ubuntu's /usr/bin/php being a symlink through /etc/alternatives, which the sandbox's mount table does not carry — "+
			"name an explicit interpreter in your test command instead (e.g. `-- %s vendor/bin/phpunit tests/`)",
		probe, res.ExitCode, detail, phpSuggestedInterpreter())
}

// phpSuggestedInterpreter names a concrete phpX.Y for the refusal hint's
// example. Best-effort and never load-bearing — a wrong SUGGESTION only
// costs a slightly less on-point example, never a wrong verdict — so this
// stays a one-liner: scan PATH's directories for names matching
// phpVersionedInterpreterRe (excluding the bare "php" itself, which is the
// name that just dangled) and take the lexicographically-greatest. That is
// also the numerically-greatest for the plain single-digit-minor scheme
// every currently-supported PHP release uses (8.0 through 8.5+); it is not
// a general version-comparison (a hypothetical "php8.10" would sort before
// "php8.9"), which is acceptable for an illustrative example and not worth
// a real semver parser here. A host with nothing installed falls back to a
// fixed, currently-current version.
func phpSuggestedInterpreter() string {
	best := ""
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if name == "php" || !phpVersionedInterpreterRe.MatchString(name) {
				continue
			}
			if name > best {
				best = name
			}
		}
	}
	if best != "" {
		return best
	}
	return "php8.5"
}

func (phpPlugin) PromptLang() string { return "PHP" }

func (phpPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable PHPUnit test that verifies the code SATISFIES the goal.
- Start with ` + "`<?php`" + ` and ` + "`require_once __DIR__ . '/vendor/autoload.php';`" + ` (or the project's own autoload path convention), then define a class extending ` + "`PHPUnit\\Framework\\TestCase`" + ` with one or more ` + "`public function test...()`" + ` methods.
- It MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- PHPUnit assertions only (no other packages), deterministic, no network.
Return ONLY the raw PHP test file content — no prose, no markdown fences.`
}

func (phpPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same public method signatures (drop-in PHP that loads) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

// ImportPath is always ok=false: Composer's PSR-4 autoloader resolves a
// class by NAMESPACE, not by the requiring file's own location, and roles.go
// always places the authored test in the SAME directory as the code under
// test — so the class the test extends (from the file's own `namespace`
// declaration) is already resolvable with no per-task correction, the same
// reasoning ruby.go and javascript.go give for their own ImportPath (see
// lang.Plugin.ImportPath's doc comment for the general rule).
func (phpPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — PHP's namespace-based
// autoloading needs no per-task correction.
func (phpPlugin) ImportNote(string, bool) string { return "" }

func (phpPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (phpPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (phpPlugin) ParseTestList(string) []string { return nil }

// WorkspaceRunEnv is a no-op: PHP's opcache is not, by default, a
// persistent bytecode cache keyed off a source file's (mtime, size) sitting
// next to that source the way CPython's __pycache__ is — the class of hole
// this method exists to close (see lang.Plugin.WorkspaceRunEnv's doc
// comment and python.go's own implementation). A CLI invocation of PHPUnit
// typically runs with opcache either disabled or file-cache-only for the
// process's own lifetime, so there is no cross-run staleness hole in THIS
// plugin's stock scoring path today.
func (phpPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// phpFailureID matches PHPUnit's numbered failure/error entry — "N) id" —
// shared by both its "There was N failure(s):" and "There were N error(s):"
// sections, which use the identical "N) Class::method" shape. The id is
// everything after the first "N) " on the line, taken verbatim.
func phpFailureID(line string) (string, bool) {
	line = strings.TrimSpace(line)
	idx := strings.Index(line, ") ")
	if idx <= 0 {
		return "", false
	}
	if _, err := strconv.Atoi(line[:idx]); err != nil {
		return "", false
	}
	id := strings.TrimSpace(line[idx+2:])
	if id == "" {
		return "", false
	}
	return id, true
}

// FirstFailure names the first test PHPUnit's numbered failure/error section
// reports, read verbatim from a "N) Class::method" line. PHPUnit prints one
// such line per failing OR erroring test — both sections share the shape, so
// no prefix distinguishes them the way pytest's FAILED/ERROR split does:
// unlike pytest's ERROR (a test that never ran, a collection failure),
// PHPUnit's "error" is a test that DID run and threw, which is exactly the
// same kind of finding as a failure.
//
// The FIRST such line in the stream is the answer — whichever of the
// failure or error sections PHPUnit happened to print first — mirroring
// go.go's and python.go's identical "first line in the stream wins" rule.
//
// A clean "OK (...)" run, or output PHPUnit produced no numbered section
// for at all (e.g. a fatal error that crashed before any test summary),
// names nothing: no test can be blamed for a summary that never named one.
func (phpPlugin) FirstFailure(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		if id, ok := phpFailureID(line); ok {
			return id
		}
	}
	return ""
}

// phpNamespaceRe matches a top-level `namespace X\Y;` declaration — PHP's
// SINGLETON header: exactly one may govern a file, unlike an `import`/`use`
// line, which may repeat harmlessly. Only the single-statement form is
// matched (no `namespace X { ... }` braced form): the writer prompt never
// asks for it, and none of the shipped languages' concatenators handle a
// braced/block variant of their own singleton header either (Go's
// `goPackageRe` is equally single-form).
var phpNamespaceRe = regexp.MustCompile(`^namespace\s+([\w\\]+)\s*;`)

// phpUseRe matches a top-level `use X\Y;` IMPORT statement. It is checked
// only when a line is top-level (mergeParts never applies isHeader to an
// indented line), which is what keeps this from also matching a trait
// inclusion (`use Loggable;`) INSIDE a class body — that always sits
// indented one level in.
var phpUseRe = regexp.MustCompile(`^use\s+[\w\\]`)

var (
	phpRequireRe = regexp.MustCompile(`^require(?:_once)?\s`)
	phpDeclareRe = regexp.MustCompile(`^declare\s*\(`)
	// phpClassRe accepts the `final`/`abstract` modifiers PHPUnit test
	// classes sometimes carry ahead of `class`.
	phpClassRe = regexp.MustCompile(`^(?:final\s+|abstract\s+)*class\s+([A-Za-z_]\w*)`)
	phpFuncRe  = regexp.MustCompile(`^function\s+([A-Za-z_]\w*)\s*\(`)
)

// ConcatTests folds several proven PHPUnit files into one: the opening
// `<?php` tag and a `namespace` declaration are stripped out and re-emitted
// exactly once (both are SINGLETONS PHP fails to parse repeated — the same
// reasoning goPlugin.ConcatTests gives its own `package` clause), disagreeing
// namespace declarations REFUSED rather than guessed at (mirrors Go's
// package-clause mismatch), `use`/`require(_once)`/`declare` lines hoisted
// and de-duplicated by exact text, and a colliding class (or, defensively, a
// colliding top-level function) name SUFFIXED with its mutant id.
//
// Every top-level declaration here is renameable, unlike Go/Python/Ruby's
// "only the test name" carve-out: an authored PHP test's class is not
// referenced from ANYWHERE outside its own file — PHPUnit's directory-based
// discovery `require()`s a matching file and reflects on whichever TestCase
// subclasses it finds newly declared, it does not look the class up by name
// — so suffixing every occurrence of a colliding name within that SAME
// part's body (mergeParts' word-boundary-bounded substitution, the identical
// mechanism JS's full permissiveness already relies on) safely rewrites its
// own internal self-references (`new InvoiceTest()`, `InvoiceTest::class`)
// along with the declaration itself. That is also why two parts declaring
// the SAME class name is the ordinary case here, not a rare collision: the
// writer prompt gives the model no per-mutant class name to differentiate
// on, so the common shape is exactly the one this merge exists to handle —
// see the design doc's "decide by what phpunit COLLECTS" note and the
// fake-jail proof in concat_php_jail_test.go: a file with several
// distinctly (or suffix-disambiguated) named TestCase subclasses is
// discovered and runs every one of them.
func (phpPlugin) ConcatTests(parts []AuthoredPart) (string, error) {
	ns := ""
	nsSet := false
	flat := make([]AuthoredPart, 0, len(parts))
	for _, part := range parts {
		var kept []string
		for _, line := range strings.Split(part.Source, "\n") {
			trimmed := strings.TrimRight(line, " \t")
			probe := strings.TrimSpace(trimmed)
			switch {
			case probe == "<?php":
				// The opening tag is a SINGLETON re-emitted once, verbatim,
				// as ConcatTests' own literal prefix below — not a
				// de-duplicated header collected from the parts.
				continue
			case phpNamespaceRe.MatchString(probe):
				name := phpNamespaceRe.FindStringSubmatch(probe)[1]
				if nsSet && ns != name {
					return "", fmt.Errorf("lang: authored parts disagree about the namespace declaration (%q vs %q)", ns, name)
				}
				ns, nsSet = name, true
				continue
			default:
				kept = append(kept, trimmed)
			}
		}
		flat = append(flat, AuthoredPart{MutantID: part.MutantID, Source: strings.Join(kept, "\n")})
	}

	prefix := "<?php"
	if ns != "" {
		prefix += "\n\nnamespace " + ns + ";"
	}

	return mergeParts(flat, concatSpec{
		isHeader: func(line string) bool {
			return phpUseRe.MatchString(line) || phpRequireRe.MatchString(line) || phpDeclareRe.MatchString(line)
		},
		declRes:           []*regexp.Regexp{phpClassRe, phpFuncRe},
		renameOnCollision: func(string) bool { return true },
	}, prefix, func(headers []string) string { return strings.Join(headers, "\n") })
}

// FailFastArgs is phpunit's `--stop-on-failure`. See lang.FailFaster.
func (phpPlugin) FailFastArgs(testCmd []string) ([]string, bool) {
	if len(testCmd) == 0 || cmdIsShellWrapped(testCmd) {
		return nil, false
	}
	if cmdHasWord(testCmd, "phpunit") {
		return []string{"--stop-on-failure"}, true
	}
	return nil, false
}
