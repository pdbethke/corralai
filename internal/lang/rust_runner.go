// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// rustGrammar names which runner's output format a parse reads.
type rustGrammar int

const (
	rustGrammarLibtest rustGrammar = iota // `cargo test`
	rustGrammarNextest                    // `cargo nextest run`
)

// The runner decision (libtest or nextest) is made once, by RunnerFor over
// the two cargo rows in runner.go. The output grammar is deliberately NOT
// taken from the row: FirstFailure is handed the output and never the
// command, so it reads the grammar off the output (rustGrammarOf).

// nextestConfig is where cargo-nextest reads a project's profiles, relative
// to the workspace root.
const nextestConfig = ".config/nextest.toml"

// nextestPreflightBin is cargo-nextest, or the operator's own path to it.
func nextestPreflightBin(testCmd []string) string {
	if len(testCmd) > 0 && filepath.Base(testCmd[0]) == "cargo-nextest" {
		return testCmd[0]
	}
	return "cargo-nextest"
}

func libtestPreflightBin(testCmd []string) string { return preflightBin(testCmd, "cargo") }

// nextestDisclose says how nextest's retries are graded.
func nextestDisclose(string, []string) []string {
	return []string{"runner: cargo-nextest — a test it passes only on a retry (FLAKY) is graded as passed, as the project's own retry policy decides; such tests in a file's unmutated baseline are named, but a MUTANT whose run passed only on a retry is counted as survived and is not named"}
}

// libtestDisclose says when a repository keeps nextest profiles that a
// `cargo test` run does not apply.
func libtestDisclose(repoDir string, _ []string) []string {
	if fi, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(nextestConfig))); err != nil || !fi.Mode().IsRegular() { // #nosec G703 -- existence check under the operator's repository
		return nil
	}
	return []string{"runner: cargo test, but this repository keeps " + nextestConfig + " — its nextest profiles (retries, timeouts, test groups) do not apply to this run, and libtest runs every test of a binary in one process where nextest runs each in its own; pass `-- cargo nextest run` to grade under the project's own runner"}
}

// rustFlakyTests names the tests nextest passed only on a retry — the Flaky list
// of the same parse FirstFailure reads (see FlakyReporter). libtest has no
// retries, so its output names none.
func rustFlakyTests(output []byte) []string {
	return parseRustRun(rustGrammarOf(output), output).Flaky
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
