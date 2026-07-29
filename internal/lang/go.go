// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func init() { Register(goPlugin{}) }

type goPlugin struct{}

func (goPlugin) Name() string                { return "go" }
func (goPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".go" }

func (goPlugin) Scaffold() map[string]string {
	return map[string]string{"go.mod": "module control\ngo 1.26\n"}
}

func (goPlugin) TestCmd() []string { return []string{"go", "test", "./..."} }

// CompileCheck type-checks the authored test. It scopes to the audited file's
// OWN package (./dir/...) rather than the whole module (./...): a single-file
// audit inside a monorepo must not drag in unrelated packages — above all cgo
// deps elsewhere in the tree (e.g. tree-sitter bindings) whose C headers
// `go mod vendor` prunes, which would fail the compile-check for a reason that
// has nothing to do with the authored test. This matches the (package-scoped)
// command the scorer actually runs. A bare filename (single-file mode) has no
// package dir, so it falls back to ./... over the one-package scaffold.
func (goPlugin) CompileCheck(codePath, _ string) []string {
	dir := filepath.ToSlash(filepath.Dir(codePath))
	if dir == "." || dir == "" || dir == "/" {
		return []string{"go", "vet", "./..."}
	}
	return []string{"go", "vet", "./" + dir + "/..."}
}

// TestPaths mirrors the prior advPoolTestPath: same base name, `_test.go`
// suffix, same directory. Go's convention IS the sibling file — there is no
// parallel-tree or flat convention to add, so this is a single-element list.
func (goPlugin) TestPaths(codePath string) []TestCandidate {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return []TestCandidate{{Path: base + "_test.go", Rank: 0}}
	}
	return []TestCandidate{{Path: filepath.Join(dir, filepath.Base(base)+"_test.go"), Rank: 0}}
}

func (goPlugin) Preflight() error { return toolOnPath("go") }

func (goPlugin) PromptLang() string { return "Go" }

// TestWriterSystem is the EXACT string previously named writeTestSystem in
// internal/testgen/testgen.go — moved here unchanged so the Go prompt stays
// byte-identical.
func (goPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`
}

// MutantSystem is the EXACT string previously named genMutantsSystem in
// internal/testgen/testgen.go — moved here unchanged.
func (goPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same signature and package (a drop-in replacement that compiles) and must genuinely fail the goal — vary HOW it fails. No no-ops, no compile errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

func (goPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" {
		return nil, false
	}
	return []string{"go", "test", "-run", "^" + selector + "$", "./..."}, true
}

func (goPlugin) ListTestsCmd(testPath string) ([]string, bool) {
	return []string{"go", "test", "-list", ".*", "./..."}, true
}

// CoverageCmd wraps the project's own test command so a coverage profile
// ends up on stdout after ONE run. `-coverprofile=/dev/stdout` was the
// obvious first attempt but is NOT safe: verified against corral's own
// suite, it interleaves and corrupts the profile whenever more than one
// package's test binary flushes to the shared fd (`go test ./... -p 1
// -coverprofile=/dev/stdout` still corrupted — the race is between the `go
// test` parent's own summary line and a child binary's profile flush, not
// package parallelism). Writing to a real temporary file and `cat`-ing it
// afterward, inside one `sh -c` invocation (so callers still see a single
// command and a single stdout stream), was verified clean. See
// .superpowers/sdd/2026-07-29-coverage-preflight/task-1-report.md for the
// transcript.
func (goPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if len(testCmd) == 0 {
		return nil, false
	}
	quoted := make([]string, len(testCmd))
	for i, arg := range testCmd {
		quoted[i] = shellQuote(arg)
	}
	script := `f=$(mktemp) && trap 'rm -f "$f"' EXIT && ` +
		strings.Join(quoted, " ") + ` -coverprofile="$f" && cat "$f"`
	return []string{"sh", "-c", script}, true
}

// shellQuote wraps s in single quotes for safe use inside a POSIX sh -c
// script, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ParseCoverage reads the output of the command CoverageCmd builds: whatever
// preamble the wrapped test command itself prints (e.g. `go test`'s own
// "ok  pkg  0.01s  coverage: NN%" summary line, which — per the verified
// invocation above — lands on stdout BEFORE the profile), then the profile
// itself: a "mode: <mode>" header line, followed by one line per covered
// block —
//
//	<import-path>/<file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
//
// Lines before the mode header are preamble and are ignored outright — they
// are the wrapped command's own output, not coverage data, so there is
// nothing to validate about their shape. A file is "executed" if ANY of its
// blocks has count > 0. modulePath, when non-empty, is stripped as a
// "<modulePath>/" prefix to yield a repo-relative path.
//
// Once the mode header has been seen, every subsequent non-blank line MUST
// match the block shape above, or this returns an error — never silently
// drops it, because a dropped block line would just as silently shrink the
// executed set, exactly the falsely-empty-coverage outcome this type exists
// to prevent. For the same reason, input with no recognizable "mode:" header
// anywhere is itself an error rather than an empty map: a genuinely-empty
// instrumented run still emits that header, so its absence means the
// command's output was not a coverage profile at all (e.g. it failed before
// ever running `go test`), which is a fact callers must see, not paper over.
func (goPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	executed = make(map[string]bool)
	sawMode := false
	for i, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !sawMode {
			if strings.HasPrefix(trimmed, "mode:") {
				sawMode = true
			}
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 3 {
			return nil, fmt.Errorf("lang: unparseable coverage line %d: %q", i+1, line)
		}
		count, convErr := strconv.Atoi(fields[2])
		if convErr != nil {
			return nil, fmt.Errorf("lang: unparseable coverage count on line %d: %q", i+1, line)
		}
		colon := strings.Index(fields[0], ":")
		if colon <= 0 {
			return nil, fmt.Errorf("lang: unparseable coverage block on line %d: %q", i+1, line)
		}
		if count <= 0 {
			continue
		}
		path := fields[0][:colon]
		if modulePath != "" {
			path = strings.TrimPrefix(path, modulePath+"/")
		}
		executed[path] = true
	}
	if !sawMode {
		return nil, fmt.Errorf("lang: coverage report has no \"mode:\" header (got %d bytes)", len(stdout))
	}
	return executed, nil
}

func (goPlugin) ParseTestList(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// go test -list prints one identifier per line; keep only Test* funcs
		// (drop Example*/Benchmark*/Fuzz* and the "ok  pkg  0.00s" / PASS trailer,
		// which contain whitespace or don't start with "Test").
		if strings.HasPrefix(line, "Test") && !strings.ContainsAny(line, " \t") {
			out = append(out, line)
		}
	}
	return out
}
