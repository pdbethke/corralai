// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// nodeRecordHook is the `node --test` row's ReportHook: node's own JUnit
// record of the run, from its built-in junit reporter writing to a file
// (FileRecordHookAt). Naming any --test-reporter replaces node's default
// reporter, so the hook names two, each with its destination, as node
// pairs them by position: tap on stdout, which is what node 22 prints off a
// TTY without the flag, and junit to the record. Measured on node 22.23.5
// (capture-runner-fixtures.sh node-hook-*.txt): the printed output is
// unchanged and the exit code too, on a failing run and a passing one.
// node 23 and later print the spec reporter off a TTY instead (measured on
// node 24.22.1, node24-hook-*.txt), so there the hook changes what is
// printed from spec to TAP; corral reads both, and the disclosure says so.
//
// The four words are dropped whole when the wrapper cannot make its
// directory (DropWhenUnset), not pointed at /dev/null: node fsyncs a
// reporter's destination, /dev/null refuses that, and node then exits 1 on
// a run that passed (node 22.23.5, "EINVAL: invalid argument, fsync").
var nodeRecordHook = func() *ReportHook {
	h := FileRecordHookAt(
		func() []string {
			return []string{
				"--test-reporter=tap", "--test-reporter-destination=stdout",
				"--test-reporter=junit", "--test-reporter-destination=" + recordToken,
			}
		},
		recordJUnit,
		"corral runs every graded run through `sh -c` and adds `--test-reporter=tap --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination=<a temporary file>` after `--test`, "+
			"and reads which test failed from that JUnit record, node's own account of the run; the exit code is unchanged, and so is what node 22 prints, but node 23 and later print TAP in place of their spec report. "+
			"A command naming a reporter or a config file of its own, or run with one in NODE_OPTIONS, is not wrapped",
		RecordFile{DropWhenUnset: true},
	)
	h.Wraps = nodeRecordWraps
	return h
}()

// nodeReporterOptions are the words that give node a reporter or a config
// that may name one. node refuses a run whose reporters and destinations
// differ in number (ERR_INVALID_ARG_VALUE, node 22.23.5), so the hook's two
// pairs beside an operator's lone `--test-reporter=spec`, or one from
// NODE_OPTIONS or a config file (`"test": {"test-reporter": "dot"}`, node
// 24.22.1 with --experimental-config-file), would fail a run that passed.
var nodeReporterOptions = []string{"--test-reporter", "--test-reporter-destination", "--experimental-config-file", "--experimental-default-config-file"}

// nodeRecordWraps declines a command the hook cannot be added to without
// changing the run: one naming a reporter or a config file (or carrying one
// in NODE_OPTIONS, which on --substrate workspace the run inherits from
// corral's own environment; the jail passes no NODE_OPTIONS, so there the
// check only ever declines more than it must), and one where a word before
// `--test` is neither an option nor the value of one node lists as taking
// the next word (nodeOptionTakesValue): it could be the script node runs,
// which would receive `--test` and the hook's words as its own arguments.
// An option node does not list (a V8 option, `--max-old-space-size=4096`)
// is passed over; node reads such an option's value only after "=", so a
// bare word after it is that script, and declines.
func nodeRecordWraps(cmd []string, at int) bool {
	if at < 1 || hasFlag(cmd, nodeReporterOptions...) {
		return false
	}
	if hasFlag(strings.Fields(os.Getenv("NODE_OPTIONS")), nodeReporterOptions...) {
		return false
	}
	for i := 1; i < at; i++ {
		if !strings.HasPrefix(cmd[i], "-") {
			return false
		}
		flag, _, inline := strings.Cut(cmd[i], "=")
		if value, _ := nodeOptionTakesValue(flag); value && !inline {
			i++ // its value, which node reads as one word
		}
	}
	return true
}

// nodeTestAt is the node --test row's At: the index of `--test` in a
// command whose program is node, -1 otherwise.
func nodeTestAt(argv []string) int {
	if len(argv) == 0 || filepath.Base(argv[0]) != "node" {
		return -1
	}
	return slices.Index(argv, "--test")
}

// nodeFailedTests is the node --test row's failure reader: the run's JUnit
// record first (nodeRecordedFailures), then each test the printed report
// names that the record does not, by recordThenPrinted, the rule every
// record-file row follows; the printed report alone where there is no
// record.
func nodeFailedTests(out string) []string {
	return recordThenPrinted(out, nodeRecordedFailures, nodeFailures, sameID)
}

// nodeRecordedFailures is every failing test node's JUnit record names, in
// its order, by the id nodeFailures reads from the printed report: the
// test's suites and its own name, joined by "::". node writes no file in
// the record before node 24 (and only an absolute one from 24), so the id
// is its path of titles, as the printed report's is. A suite, or a test
// with subtests, is written as a <testsuite> and is never itself a
// <failure>, so the record names the tests with no subtests that failed,
// as the printed reader does (node 22.23.5 and 24.22.1, measured). ok is
// false when junitRecord reads none, and when a testcase has no name.
//
// A failing todo is left out: node writes it with a <skipped type="todo">
// beside its <failure> and does not fail the run for it, so it is never
// what failed (nodeFailures leaves it out too).
//
// What the record writes that is not a test is left out too: a file that
// failed to load is a top-level testcase named after the file (22.23.5:
// the path as given; 24.22.1: the path as given, with its absolute path in
// `file`; node 18.20.8 and 20.20.2 wrote its absolute path, run once each in
// a container and not captured). It is left out when its name is
// one the printed report marks as a file (an `exitCode` in its TAP entry),
// an absolute path, or the tail of its `file`. A real top-level test whose
// name looks like that is left out of the record's part too, and is named
// again by the printed report, which recordThenPrinted adds.
func nodeRecordedFailures(out string) ([]string, bool) {
	cases, ok := junitRecord(out)
	if !ok {
		return nil, false
	}
	entries, _ := nodeEntries(PrintedOutput(out))
	var ids []string
	for _, c := range cases {
		if c.Name == "" {
			return nil, false
		}
		if c.Skipped || (len(c.Suites) == 0 && nodeRecordedFile(c, entries)) {
			continue
		}
		ids = append(ids, strings.Join(append(slices.Clone(c.Suites), c.Name), "::"))
	}
	return ids, true
}

// nodeRecordedFile reports whether a top-level testcase of node's record is
// a file that failed to load rather than a test (nodeRecordedFailures).
func nodeRecordedFile(c junitCase, printed []*nodeEntry) bool {
	if strings.HasPrefix(c.Name, "/") || (c.File != "" && strings.HasSuffix(c.File, "/"+c.Name)) {
		return true
	}
	return slices.ContainsFunc(printed, func(e *nodeEntry) bool {
		return e.exit && len(e.path) == 1 && e.path[0] == c.Name
	})
}
