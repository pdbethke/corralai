// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"path/filepath"
	"strings"
)

// rustGrammar names which runner's output format a parse reads.
type rustGrammar int

const (
	rustGrammarLibtest rustGrammar = iota // `cargo test`
	rustGrammarNextest                    // `cargo nextest run`
)

// runnerRow is the ONE runner decision for a Rust audit, made once from the
// operator's test command with cmdHasWord and read by everything that runs a
// test. Nothing else re-tests argv for "nextest": two places answering the
// same question is how a preflight, a single-test selector and a failure
// parser come to disagree about which runner the suite uses.
type runnerRow struct {
	grammar rustGrammar
	// singleTest runs exactly one test (sel is the name the runner lists).
	singleTest func(testPath, sel string) []string
	// listCmd enumerates the tests without running them.
	listCmd []string
	// preflightBin is the binary that must be on PATH.
	preflightBin string
	// profileNote is the scan-line disclosure for this runner, given whether
	// the repository carries `.config/nextest.toml`; "" when nothing to say.
	profileNote func(hasNextestConfig bool) string
}

func rustRunner(testCmd []string) runnerRow {
	if cmdHasWord(testCmd, "nextest") || cmdHasWord(testCmd, "cargo-nextest") {
		bin := "cargo-nextest"
		if len(testCmd) > 0 && filepath.Base(testCmd[0]) == "cargo-nextest" {
			bin = testCmd[0]
		}
		return runnerRow{
			grammar: rustGrammarNextest,
			singleTest: func(_, sel string) []string {
				return []string{"cargo", "nextest", "run", "-E", "test(=" + sel + ")"}
			},
			listCmd:      []string{"cargo", "nextest", "list", "--message-format", "oneline"},
			preflightBin: bin,
			profileNote:  func(bool) string { return "" },
		}
	}
	return runnerRow{
		grammar: rustGrammarLibtest,
		singleTest: func(testPath, sel string) []string {
			cmd := []string{"cargo", "test"}
			// cargo's integration tests are one binary per tests/*.rs file;
			// naming it keeps the run from compiling and filtering every other.
			if dir, file := filepath.Split(filepath.ToSlash(testPath)); strings.HasPrefix(dir, "tests/") {
				cmd = append(cmd, "--test", strings.TrimSuffix(file, ".rs"))
			}
			return append(cmd, "--", "--exact", sel)
		},
		listCmd:      []string{"cargo", "test", "--", "--list", "--format", "terse"},
		preflightBin: preflightBin(testCmd, "cargo"),
		profileNote: func(hasConfig bool) string {
			if !hasConfig {
				return ""
			}
			return "this project has .config/nextest.toml but the test command is `cargo test`: the project's nextest profiles were not applied"
		},
	}
}

// rustRun is what one parse of a runner's output established.
type rustRun struct {
	// Recognised is false when the output carries none of the grammar's
	// summary: a build failure, an empty capture, the other runner's text.
	// Such a run is COULD-NOT-GRADE; it must never be read as a pass or as a
	// measured zero.
	Recognised bool
	// Failures are the failing tests' ids in the order the summary names
	// them, each once. libtest: the test path. nextest: "<binary-id> <name>".
	Failures []string
	// Flaky are tests nextest passed only on a retry. They are graded as
	// passed (the exit code is 0) and are listed so they can be disclosed.
	Flaky []string
}

// rustGrammarOf reads the grammar off an output, for the one caller
// (FailureParser) that is not handed the command.
func rustGrammarOf(output []byte) rustGrammar {
	if bytes.Contains(output, []byte("Nextest run ID")) || bytes.Contains(output, []byte("     Summary [")) {
		return rustGrammarNextest
	}
	return rustGrammarLibtest
}

// parseRustRun is the one parse function; the grammar picks the markers.
//
//	libtest: a `test result: ` line makes the output recognised, and each
//	         `---- <name> stdout ----` block names a failure.
//	nextest: a `Summary [` line makes it recognised; `FAIL [ t ] <binary-id>
//	         <name>` lines name failures (the retry lines, `TRY n FAIL`, do not:
//	         a test that failed once and passed on retry is FLAKY), and
//	         `FLAKY n/m [ t ] <binary-id> <name>` lines name flaky tests.
func parseRustRun(g rustGrammar, output []byte) rustRun {
	var run rustRun
	seen := map[string]bool{}
	add := func(dst *[]string, id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			*dst = append(*dst, id)
		}
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		switch g {
		case rustGrammarLibtest:
			if strings.HasPrefix(line, "test result: ") {
				run.Recognised = true
			}
			if rest, ok := strings.CutPrefix(line, "---- "); ok {
				if name, ok := strings.CutSuffix(rest, " stdout ----"); ok {
					add(&run.Failures, strings.TrimSpace(name))
				}
			}
		case rustGrammarNextest:
			if strings.HasPrefix(line, "Summary [") {
				run.Recognised = true
			}
			if rest, ok := strings.CutPrefix(line, "FAIL "); ok {
				add(&run.Failures, nextestTestID(rest))
			} else if rest, ok := strings.CutPrefix(line, "FLAKY "); ok {
				add(&run.Flaky, nextestTestID(strings.TrimSpace(afterToken(rest))))
			}
		}
	}
	return run
}

// afterToken drops the first whitespace-delimited token ("2/2" in a FLAKY line).
func afterToken(s string) string {
	_, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
	return rest
}

// nextestTestID reads "[   0.006s] ( 7/33) <binary-id> <name>" (the progress
// counter is "(─────)" on a retry line, and absent from none we captured) and
// returns "<binary-id> <name>", or "" when the line is not that shape.
func nextestTestID(s string) string {
	_, rest, ok := strings.Cut(s, "] ")
	if !ok {
		return ""
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "(") {
		_, after, ok := strings.Cut(rest, ") ")
		if !ok {
			return ""
		}
		rest = strings.TrimSpace(after)
	}
	fields := strings.Fields(rest)
	if len(fields) != 2 {
		return ""
	}
	return fields[0] + " " + fields[1]
}
