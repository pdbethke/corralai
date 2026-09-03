// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"path/filepath"
	"strings"
)

// THE REDUCED COVERAGE REPORT — one format, several languages.
//
// Go and Python each parse their ecosystem's native report (a coverage
// profile, coverage.py's JSON). The languages added later have no single
// native format worth teaching corral to read — Ruby's stdlib Coverage hands
// back an in-process Hash, and V8 hands back megabytes of range data — so each
// one REDUCES in-process to the same two-line-grammar report and shares this
// parser.
//
// Reducing at the source is not a convenience. reposcan's runner caps the
// stdout it captures (WithWorkspaceMaxOutput), and a raw V8 coverage dump for
// an ordinary project runs to megabytes: shipping it whole would truncate it
// mid-JSON and surface as "unparseable report" on exactly the large repos the
// pre-flight is most useful for. What crosses the boundary is one line per
// file.
//
// The grammar:
//
//	<header>            a language-specific magic line, e.g. corral-ruby-coverage: v1
//	1 /abs/path/to/file executed
//	0 /abs/path/to/file measured, and never executed
//
// A file that was never measured emits NO LINE AT ALL. That absence is the
// third state and it carries real weight — see corralCoverageReport.
const (
	rubyCoverageHeader = "corral-ruby-coverage: v1"
	jsCoverageHeader   = "corral-js-coverage: v1"
	// phpCoverageHeader is declared in php.go, beside the script that writes it.
)

// corralCoverageReport parses the reduced report into CoverageReporter's
// tri-state map.
//
// TRI-STATE, and the third state is the point: present-true is executed,
// present-false is measured-and-never-executed (the only real finding), and
// ABSENT means this run never measured the file. Both runtimes here observe
// only what the suite LOADED, so a source file nothing requires is absent
// rather than false. Recording it as false would accuse every file outside the
// suite's import graph of being untested, when the honest claim is that this
// run never looked at it — the same distinction pyPlugin.ParseCoverage draws
// for files outside coverage.py's configured source scope.
//
// The header check is what makes an unparseable report an ERROR instead of an
// empty map. Without it any unrelated output — a stack trace, a bundler
// warning, a truncated dump — parses as zero entries and reads as "the suite
// covered nothing", which is a repo-wide accusation manufactured out of a
// failure to measure.
func corralCoverageReport(stdout, header, langName, modulePath string) (executed map[string]bool, err error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, fmt.Errorf("lang: %s coverage report is empty — the instrumented run produced nothing, which is a failure to measure, not a measurement of nothing", langName)
	}
	lines := strings.Split(trimmed, "\n")
	hdr := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == header {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		return nil, fmt.Errorf("lang: unparseable %s coverage report: no %q header (got %d line(s), starting %q)",
			langName, header, len(lines), firstLineExcerpt(trimmed))
	}

	root := modulePath
	if root != "" {
		if abs, absErr := filepath.Abs(root); absErr == nil {
			root = abs
		}
	}
	executed = make(map[string]bool)
	parsed := 0
	for _, ln := range lines[hdr+1:] {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		hit, path, found := strings.Cut(ln, " ")
		if !found || (hit != "0" && hit != "1") {
			return nil, fmt.Errorf("lang: unparseable %s coverage report line %q (want `0 <path>` or `1 <path>`)", langName, ln)
		}
		parsed++
		rel := path
		// A RELATIVE PATH IS ALREADY REPO-RELATIVE and is taken as it is —
		// the reducers emit cwd-relative paths precisely so that no
		// substrate needs to know a root. Only an absolute path is aligned.
		// (alignPyPath draws the same line for coverage.py's output.)
		if !filepath.IsAbs(path) {
			clean := filepath.ToSlash(filepath.Clean(path))
			if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
				continue
			}
			if hit == "1" {
				executed[clean] = true
			} else if _, already := executed[clean]; !already {
				executed[clean] = false
			}
			continue
		}
		if root != "" {
			r, relErr := filepath.Rel(root, path)
			// Outside the repo root is a gem, a stdlib file, or something
			// in node_modules. Not a candidate for audit, so it must not
			// appear in the map at all rather than as an entry a caller
			// could report on.
			if relErr != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				continue
			}
			rel = r
		}
		rel = filepath.ToSlash(rel)
		// A file reported more than once (several test processes, several
		// workers) is executed if ANY run executed it. Never downgrade a
		// true to a false.
		if hit == "1" {
			executed[rel] = true
		} else if _, already := executed[rel]; !already {
			executed[rel] = false
		}
	}
	if parsed == 0 {
		return nil, fmt.Errorf("lang: %s coverage report has a header but no file entries — the suite most likely never loaded an application file (a collection or import error), not a genuinely-empty result", langName)
	}
	// KEPT, not merely PARSED. The out-of-root filter above runs AFTER a line
	// is counted, so a report consisting entirely of gems, stdlib or
	// node_modules used to satisfy the "has entries" check and then return an
	// EMPTY map with no error — which the caller reads as Ran=true and reports
	// as a repo-wide "0 files executed". That is a claim about the repository
	// manufactured out of a run that measured nothing in it.
	//
	// It is a real shape, not a hypothetical: it is what every non-Go language
	// produced whenever the caller passed an empty root, and it is what a
	// wrongly-rooted run produces now.
	if len(executed) == 0 {
		return nil, fmt.Errorf("lang: %s coverage report measured %d file(s), but NONE of them are under the repo root %q — every path was outside it (a dependency, the stdlib, or a wrong root). That is a failure to measure this repository, not a measurement that it is uncovered", langName, parsed, modulePath)
	}
	return executed, nil
}

// firstLineExcerpt returns a short single-line excerpt for error messages.
func firstLineExcerpt(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// coverageRunAndReduce builds the shell command shape both reduce-in-process
// languages use: run the suite with its own output pushed to STDERR so stdout
// carries only the report, then reduce.
//
// THE REDUCTION ALWAYS RUNS, whatever the suite exited with.
//
// `&&` would discard the report whenever tests fail — the single most likely
// state of a repository corral is auditing. An earlier version fixed that with
// `case $rc in 0|1) ;; *) exit "$rc" ;; esac`, which is still wrong: `mocha`
// exits with the NUMBER OF FAILING TESTS and `phpunit` exits 2 on errors, so
// for two allow-listed runners the ordinary failing-suite case fell straight
// into the re-raise and threw the report away.
//
// There is nothing left for the exit code to protect. Coverage data is
// complete whether the suite passed, failed, or died on a bad flag; if the run
// really produced nothing, the reduction emits an absent or header-only report
// and the parser turns THAT into an error. The fail-closed direction is
// preserved by the parser, which is where it belongs — an exit code is a poor
// proxy for "was anything measured", and it was guessing wrong.
func coverageRunAndReduce(setup, env string, testCmd []string, reduce string) []string {
	quoted := make([]string, len(testCmd))
	for i, arg := range testCmd {
		quoted[i] = shellQuote(arg)
	}
	script := `d=$(mktemp -d) && trap 'rm -rf "$d"' EXIT && ` + setup +
		env + " " + strings.Join(quoted, " ") + " >&2" +
		"; " + reduce
	return []string{"sh", "-c", script}
}

// coverageRunnerNamed reports whether testCmd launches one of this language's
// runners, seeing THROUGH a `sh -c` wrapper.
//
// The wrapper is not a corner case: rubyPlugin.TestCmd() is a `sh -c` script
// that inspects the test file and dispatches to rspec or ruby, so a plugin
// that only looked at argv[0] could not instrument its OWN stock command —
// which is exactly what TestPluginStockCommandSatisfiesOwnCoverageCmd caught.
//
// Both instrumentations here are ENVIRONMENT-based (RUBYOPT, NODE_V8_COVERAGE)
// and environment is inherited, so wrapping the suite in a shell changes
// nothing about whether the measurement works. What the allow-list protects is
// something else: certify_repo.go reads CoverageCmd's ok=false to decide which
// language an operator's `--` command belongs to, so a plugin that accepted
// every `sh -c` would claim another language's command and instrument the
// wrong suite. Hence the script is scanned for a runner token rather than
// waved through — `sh -c 'pytest -q'` must still be Python's.
func coverageRunnerNamed(testCmd []string, runners []string) bool {
	if len(testCmd) == 0 {
		return false
	}
	base := strings.TrimSuffix(filepath.Base(testCmd[0]), ".cmd")
	switch base {
	case "sh", "bash", "dash", "zsh":
		script := ""
		for i, a := range testCmd {
			if a == "-c" && i+1 < len(testCmd) {
				script = testCmd[i+1]
				break
			}
		}
		if script == "" {
			return false
		}
		for _, word := range shellCommandWords(script) {
			for _, r := range runners {
				if strings.TrimSuffix(filepath.Base(word), ".cmd") == r {
					return true
				}
			}
		}
		return false
	default:
		for _, r := range runners {
			if base == r {
				return true
			}
		}
		return false
	}
}

// shellCommandWords returns the word in COMMAND POSITION for each command in a
// shell script — the first word after the start, or after any of `; && || | (
// newline` — skipping leading `VAR=value` assignments.
//
// Scanning the whole script for a runner's name, which is what this did
// first, matches it anywhere: inside a path, inside a comment, inside another
// tool's argument. Reproduced, all four accepted by the wrong language:
//
//	sh -c "pytest tests/node/"                 -> javascript claimed it
//	sh -c "pytest --ignore=vendor/php"         -> php claimed it
//	sh -c "cargo test # node is not used here" -> javascript claimed it
//	sh -c "pytest tests/ruby/"                 -> ruby claimed it
//
// That is not cosmetic. certify_repo.go reads CoverageCmd's ok=false to decide
// which language an operator's `--` command belongs to, so a directory named
// `tests/node/` was enough to make a plain pytest command look like two
// languages at once and get the pre-flight skipped as "ambiguous" — defeating
// the exact disambiguation the allow-list exists to perform.
//
// This is a lexer, not a parser: it does not honour quoting, so a command
// substitution or a quoted `;` can still split a word. That direction is safe
// — it can only produce EXTRA candidate words, and every one of them still has
// to equal a runner name to match. It cannot make a runner disappear.
func shellCommandWords(script string) []string {
	var words []string
	seps := func(r byte) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n' || r == '(' || r == ')'
	}
	i := 0
	for i < len(script) {
		for i < len(script) && (script[i] == ' ' || script[i] == '\t' || seps(script[i])) {
			i++
		}
		if i >= len(script) {
			break
		}
		// A comment runs to end of line and contains no command.
		if script[i] == '#' {
			for i < len(script) && script[i] != '\n' {
				i++
			}
			continue
		}
		start := i
		for i < len(script) && script[i] != ' ' && script[i] != '\t' && !seps(script[i]) {
			i++
		}
		word := script[start:i]
		// `FOO=bar cmd` — an assignment is not the command.
		if eq := strings.IndexByte(word, '='); eq > 0 && !strings.ContainsAny(word[:eq], "/.") {
			continue
		}
		// A shell keyword or an exec-style wrapper is TRANSPARENT: the command
		// is the next word, not this one. rubyPlugin.TestCmd() is exactly this
		// shape — `if grep -q RSpec "$t"; then exec rspec "$t"; else exec ruby
		// "$t"; fi` — so without this the plugin cannot instrument its own
		// stock command, which is the failure the repository's
		// TestPluginStockCommandSatisfiesOwnCoverageCmd exists to catch.
		if coverageTransparentWord[word] {
			continue
		}
		// `>&2` splits on `&` and leaves "2" in command position. A bare
		// number is a file descriptor, never a program.
		if strings.Trim(word, "0123456789") == "" {
			continue
		}
		words = append(words, word)
		// Everything up to the next separator is this command's arguments.
		for i < len(script) && !seps(script[i]) {
			i++
		}
	}
	return words
}

// coverageTransparentWord is the set of shell keywords and command wrappers
// that precede a command without being one. Being generous here is safe in the
// same direction the lexer is: it can only surface MORE candidate words, and
// each still has to equal a runner name to match.
var coverageTransparentWord = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"while": true, "until": true, "do": true, "done": true,
	"for": true, "in": true, "case": true, "esac": true,
	"{": true, "}": true, "!": true, "time": true,
	"exec": true, "command": true, "builtin": true, "env": true,
	"nohup": true, "sudo": true, "xargs": true, "eval": true,
	// `bundle exec <runner>` is a ruby process; `bundle install` is not. Being
	// transparent gets both right without a special case: the word AFTER it is
	// tested against the runner list, so `exec rspec` matches and `install`
	// matches nothing.
	"bundle": true, "bundler": true,
}

// coverageMergeDir is the reduction for the reporters that write ONE FILE PER
// PROCESS into "$d/cov": emit the header once, then every process's lines.
//
// The header is emitted HERE rather than by each process for a reason — a
// second header line inside the stream would be parsed as a malformed entry
// and fail the whole report. One run, one header, however many processes
// contributed. A file reported by several of them is resolved by the parser,
// where a hit from any process wins.
//
// `2>/dev/null` and the `|| true` keep an empty directory from turning into a
// shell error: a run that measured nothing must reach the parser as a
// header-with-no-entries, which the parser already rejects with a message
// naming the real problem, rather than as a broken pipeline.
func coverageMergeDir(header string) string {
	return `printf '%s\n' ` + shellQuote(header) + `; cat "$d"/cov/* 2>/dev/null || true`
}

// InterpretersIn returns every program a command will actually execute in
// command position: argv[0] for a plain command, and each command-position
// word of the script for a `sh -c` wrapper.
//
// It exists because the jail resolves the operator's toolchain from argv[0]
// ONLY. Every coverage command this package builds is a `sh -c` wrapper — the
// instrumentation has to set up a temp dir, run the suite, then reduce — so
// argv[0] is "sh", and a Go under ~/sdk, a pyenv python, a venv, nvm's node,
// all of them reachable for the ordinary scoring runs in the same scan, were
// invisible to the pre-flight and to test selection. The failure surfaced as
// "coverage report unparseable: invalid character 's'" — the 's' of
// `sh: 1: /home/…/venv/bin/python: not found`, discarded before anyone could
// read it.
func InterpretersIn(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	switch strings.TrimSuffix(filepath.Base(argv[0]), ".cmd") {
	case "sh", "bash", "dash", "zsh":
		for i, a := range argv {
			if a == "-c" && i+1 < len(argv) {
				return shellCommandWords(argv[i+1])
			}
		}
		return []string{argv[0]}
	default:
		return []string{argv[0]}
	}
}
