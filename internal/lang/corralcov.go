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
	sawAny := false
	for _, ln := range lines[hdr+1:] {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		hit, path, found := strings.Cut(ln, " ")
		if !found || (hit != "0" && hit != "1") {
			return nil, fmt.Errorf("lang: unparseable %s coverage report line %q (want `0 <path>` or `1 <path>`)", langName, ln)
		}
		sawAny = true
		rel := path
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
	if !sawAny {
		return nil, fmt.Errorf("lang: %s coverage report has a header but no file entries — the suite most likely never loaded an application file (a collection or import error), not a genuinely-empty result", langName)
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
// The `;` and explicit rc check between the run and the reduction are
// deliberate, and they are the same treatment goPlugin.CoverageCmd carries: a
// suite with FAILING TESTS is the single most likely state of a repository
// corral is auditing, and `&&` would discard the report precisely there.
// Coverage data is complete whether the suite passed or failed. Only 0 and 1
// fall through — anything else (a bad flag, a signal) re-raises that exit code
// and skips the reduction, leaving stdout non-conforming, which the parser
// turns into an error rather than a silent empty map.
func coverageRunAndReduce(setup, env string, testCmd []string, reduce string) []string {
	quoted := make([]string, len(testCmd))
	for i, arg := range testCmd {
		quoted[i] = shellQuote(arg)
	}
	script := `d=$(mktemp -d) && trap 'rm -rf "$d"' EXIT && ` + setup +
		env + " " + strings.Join(quoted, " ") + " >&2" +
		`; rc=$?; case $rc in 0|1) ;; *) exit "$rc" ;; esac; ` + reduce
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
		// Scan the script body, not the whole argv: `sh -c` puts it in the
		// argument after -c.
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
		for _, r := range runners {
			// Word-ish boundaries: "ruby" must not match "rubygems", and
			// a bare mention inside a longer identifier is not a launch.
			if containsRunnerToken(script, r) {
				return true
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

// containsRunnerToken reports whether script invokes runner as a word, so
// "ruby" does not match "rubygems" and "node" does not match "nodemon".
func containsRunnerToken(script, runner string) bool {
	for i := 0; i+len(runner) <= len(script); i++ {
		if script[i:i+len(runner)] != runner {
			continue
		}
		if i > 0 && isRunnerWordByte(script[i-1]) {
			continue
		}
		if j := i + len(runner); j < len(script) && isRunnerWordByte(script[j]) {
			continue
		}
		return true
	}
	return false
}

func isRunnerWordByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' ||
		(b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
