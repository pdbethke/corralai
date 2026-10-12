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
	// preflightBin is the binary that must be on PATH.
	preflightBin string
}

// The nextest.toml disclosure (cargo test ignores the project's nextest
// profiles) lands with the landing task, where a scan line exists to carry it.
func rustRunner(testCmd []string) runnerRow {
	if cmdHasWord(testCmd, "nextest") || cmdHasWord(testCmd, "cargo-nextest") {
		bin := "cargo-nextest"
		if len(testCmd) > 0 && filepath.Base(testCmd[0]) == "cargo-nextest" {
			bin = testCmd[0]
		}
		return runnerRow{grammar: rustGrammarNextest, preflightBin: bin}
	}
	return runnerRow{grammar: rustGrammarLibtest, preflightBin: preflightBin(testCmd, "cargo")}
}

// rustRun is what one parse of a runner's output established.
type rustRun struct {
	// Failures are the failing tests' ids in the order the summary names
	// them, each once. The id differs by runner: libtest gives the test path
	// (`tests::name`), nextest gives `<binary-id> <name>` (the binary id keeps
	// same-named tests in different binaries apart).
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
//	libtest: each `---- <name> stdout ----` block names a failure.
//	nextest: `FAIL [ t ] <binary-id>
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
			if rest, ok := strings.CutPrefix(line, "---- "); ok {
				if name, ok := strings.CutSuffix(rest, " stdout ----"); ok {
					add(&run.Failures, strings.TrimSpace(name))
				}
			}
		case rustGrammarNextest:
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
