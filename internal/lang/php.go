// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// CompileCheck syntax-checks BOTH files with `php -l` (offline, no
// autoloading needed). `php -l` only ever reports on a SINGLE file per
// invocation, so this returns a TWO-command sequence — codePath then
// testPath, run in order, stopping at the first failure — rather than
// trying to splice `&&` into one argv element: the workspace substrate execs
// argv directly with no shell to interpret it (see ruby.go's identical
// CompileCheck for the bug class this avoids).
func (phpPlugin) CompileCheck(codePath, testPath string) [][]string {
	return [][]string{
		{"php", "-l", codePath},
		{"php", "-l", testPath},
	}
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

// Preflight requires BOTH `php` itself and the test command's own binary —
// or, when the operator named an explicit test command, THAT command's own
// binary (see preflightBin and Plugin.Preflight's doc comment). `php` is
// checked unconditionally because `vendor/bin/phpunit`'s shebang invokes it
// even though the stock command never names "php" in argv itself; a host
// with the phpunit binary present but no `php` interpreter would otherwise
// pass preflight and fail at run time instead of refusing up front.
func (phpPlugin) Preflight(testCmd []string) error {
	if err := toolOnPath("php"); err != nil {
		return err
	}
	return toolOnPath(preflightBin(testCmd, "vendor/bin/phpunit"))
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
