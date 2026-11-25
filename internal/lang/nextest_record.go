// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// nextestRecordHook is the cargo-nextest row's ReportHook: nextest's own
// JUnit record of the run. No nextest option names a JUnit file; a profile
// in a nextest config does (`[profile.<name>.junit] path`), so the hook
// writes a config of its own beside the record (a Companion) and hands it
// to nextest with `--tool-config-file corral:<file>`, the option nextest
// gives a tool for exactly this: a config below the project's own
// (.config/nextest.toml, or a --config-file) and above nextest's defaults.
//
// What that config says, and why each line, measured on cargo-nextest
// 0.9.148 (capture-runner-fixtures.sh nextest-hook-*.txt and the
// nextest-wrote-* checks):
//
//   - `[profile.default.junit] path`: every profile inherits the default
//     profile's settings, so a profile of the project's own, chosen with
//     -P or NEXTEST_PROFILE, writes the record too.
//   - The path is ABSOLUTE, in the wrapper's own directory. nextest resolves
//     a relative junit path under its store directory for the profile
//     (target/nextest/<profile>/), which is inside the checkout, and on
//     --substrate workspace the checkout is the operator's.
//   - Nothing else. Every key the config sets is inherited by a JUnit
//     report the project's own config names without setting that key, so
//     a second key changes the project's file: with `store-failure-output
//     = false` (tried, to keep the record small) a project's own report
//     lost each failure's captured output (nextest-theirs-same-content).
//     The record therefore carries the captured output of each failing
//     test, as nextest's default does, and one larger than the output
//     corral keeps is not read (the printed lines are).
//
// What it does not change: a JUnit path the project's own config sets for
// the profile in use (or a --config-file's) has the higher priority, so
// that file is written exactly as without the hook, and no record of
// corral's is written (the printed lines are read). And what it does:
// with any JUnit report configured, nextest creates the profile's store
// directory (target/nextest/<profile>, or <store.dir>/<profile> when the
// project's config sets store.dir) when it is missing, so a graded run can
// leave that directory, empty, where the same run without the hook would
// not (measured: no file is written there or anywhere else in the
// checkout). The disclosure says so.
//
// The printed output and the exit code are unchanged, on a failing run and
// a passing one. The hook's two words are dropped whole when the wrapper
// cannot set up (DropWhenUnset, which a Companion requires): pointed at
// /dev/null, nextest would be handed a config file that does not exist.
var nextestRecordHook = func() *ReportHook {
	h := FileRecordHookAt(
		func() []string { return []string{"--tool-config-file", "corral:" + recordToken + ".toml"} },
		recordJUnit,
		"corral runs every graded run through `sh -c` and adds `--tool-config-file corral:<a temporary file>` after its options: "+
			"a nextest config corral writes, which sets the default profile's JUnit report (which every profile inherits) to another temporary file, "+
			"and corral reads which test failed from that record, nextest's own account of the run; what nextest prints and its exit code are unchanged. "+
			"A JUnit path your own nextest config sets for the profile wins and is written as it always is, and corral then reads the printed lines. "+
			"With a JUnit report configured nextest creates the profile's store directory (target/nextest/<profile>, or under your store.dir) when it is missing, "+
			"so a graded run can leave that directory, empty, where the run without corral's hook would not. "+
			"A command that is not `cargo nextest run`, or that has a `--tool-config-file` of its own, is not wrapped",
		RecordFile{
			Suffixes:      []string{".xml"},
			DropWhenUnset: true,
			Companion: Companion{
				Suffix: ".toml",
				Content: "# corral's failure hook: nextest's JUnit record of this run, outside the project.\n" +
					"[profile.default.junit]\n" +
					"path = '" + recordToken + ".xml'\n",
			},
		},
	)
	h.Wraps = nextestRecordWraps
	return h
}()

// nextestRecordWraps declines what the hook cannot be added to as it
// stands: a command whose nextest subcommand is not `run` (or its alias
// `r`), such as `list` or `archive`, which write no record, and one with a
// --tool-config-file of its own, since nextest takes at most one file per
// tool name and orders several by position, which an operator's file could
// depend on. at is the nextest word (nextestIndex); `cargo-nextest nextest
// run`, the binary called directly, has the subcommand one word later.
func nextestRecordWraps(cmd []string, at int) bool {
	i := at + 1
	if i < len(cmd) && cmd[i] == "nextest" {
		i++
	}
	head, _ := splitAtDashDash(cmd)
	if i >= len(head) || head[i] != "run" && head[i] != "r" {
		return false
	}
	return !slices.ContainsFunc(head[i+1:], func(w string) bool {
		return w == "--tool-config-file" || strings.HasPrefix(w, "--tool-config-file=")
	})
}

// nextestFailedTests is the failing tests of a cargo-nextest run: its
// JUnit record first, then the tests its printed FAIL lines name that the
// record does not (recordThenPrinted), each `<binary id> <test>`, the id
// nextest prints and nextestKillerCmd, nextestDeselectCmd and
// nextestSelectCmd take.
//
// The record names more than the printed lines on the shapes captured
// (fail/nextest-edge, nextest-hook-edge.txt): the printed reader takes
// lines that begin `FAIL`, and nextest prints a test that failed every
// retry as `TRY n FAIL`, one that aborted as `SIGABRT` and one it timed out
// as `TIMEOUT`; the record writes each as a <failure>. A test that passed
// on a retry is a <flakyFailure> in the record, no failure, as it is FLAKY
// and not FAIL in the printed lines.
func nextestFailedTests(out string) []string {
	return recordThenPrinted(out, nextestRecordedFailures, rustFailedTests, func(id, name string) bool { return id == name })
}

// nextestRecordedFailures is nextestFailedTests' answer from the record:
// each failing testcase as `<classname> <name>`, in the record's order,
// once. nextest writes the binary id as classname (`fail-cargo`,
// `units-a::it`, `nextest-edge::bin/edge-bin`) and the test's path as name
// (`tests::a_fails`), the two words its printed FAIL line gives (measured
// on 0.9.148, nextest-hook-*.txt). ok is false when junitRecord reads none,
// and when a testcase lacks either attribute or carries a space in one,
// since a list missing it, or an id the killer command cannot split, would
// be read as every failure.
func nextestRecordedFailures(out string) ([]string, bool) {
	cases, ok := junitRecord(out)
	if !ok {
		return nil, false
	}
	ids := []string{}
	for _, c := range cases {
		if c.ClassName == "" || c.Name == "" || strings.ContainsAny(c.ClassName+c.Name, " \t\n") {
			return nil, false
		}
		if id := c.ClassName + " " + c.Name; !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, true
}

// nextestFlakyTests is rustFlakyTests over the printed lines alone, so a
// line inside the record (a test's captured output, when a project's config
// stores it) is never read as nextest's own FLAKY line.
func nextestFlakyTests(output []byte) []string {
	return rustFlakyTests([]byte(withoutRecordBlock(string(output))))
}
