// SPDX-License-Identifier: Elastic-2.0

package lang

// phpunitRecordHook is the PHPUnit row's ReportHook: PHPUnit's own JUnit
// record of the run, which it writes to the file `--log-junit` names
// (FileRecordHook). Measured on PHPUnit 11.5.57 (capture-runner-fixtures.sh,
// phpunit-hook-fail.txt and report-phpunit-testdox-hooked.txt): the run
// prints what it printed without the flag and exits with the same code, the
// record names every failing and erroring test, and a phpunit.xml with
// testdox="true", which replaces the printed `N) Class::method` lines, does
// not change the record.
//
// What it does change, and the disclosure says: PHPUnit writes one JUnit
// file, for the last --log-junit it is given, ahead of a <junit> entry in
// the configuration's <logging>. So on a graded run a JUnit file the
// project's configuration asks for is not written (measured on 11.5.57,
// phpunit-config-junit-*). A command that names a --log-junit of its own is
// not wrapped (phpunitRecordWraps), as the other rows decline an operator's
// own record option: wrapped, the operator's file was not written either
// (phpunit-operator-junit-*), and nothing measured sets PHPUnit apart from
// those rows. The configuration's <junit> cannot be declined the same way,
// since corral does not read phpunit.xml to decide whether to wrap.
//
// The fallback is the zero RecordFile's: PHPUnit writes its record to
// /dev/null and passes a passing run (phpunit-hook-fallback-exit), and a
// passing run exits 0 with the hook as without it (phpunit-hook-exit-pass).
var phpunitRecordHook = func() *ReportHook {
	h := FileRecordHook(
		func() []string { return []string{"--log-junit", recordToken} },
		recordJUnit,
		"corral runs every graded run through `sh -c` and adds `--log-junit <a temporary file>` after PHPUnit's options, "+
			"and reads which test failed from that JUnit record, PHPUnit's own account of the run, whatever the project's output settings print (testdox included); "+
			"what PHPUnit prints and its exit code are unchanged. PHPUnit writes one JUnit file, so a `<junit>` entry in your phpunit.xml's `<logging>` is not written on those runs; "+
			"a command with a `--log-junit` of its own is not wrapped",
	)
	h.Wraps = phpunitRecordWraps
	return h
}()

// phpunitRecordWraps declines a command that names a --log-junit of its
// own: PHPUnit writes the last one given, so the hook's would replace it.
func phpunitRecordWraps(cmd []string, at int) bool {
	return !hasFlag(cmd[at+1:], "--log-junit")
}

// phpunitRecordedFailures is phpFailures' answer from the run's JUnit
// record: ok is false when junitRecord reads none, and the printed lines are
// then read instead. With a record, phpFailures adds
// the printed names the record leaves out (errors outside a test method);
// its doc comment says which and why that is safe.
//
// The ids are what phpFailures reads from the printed lines of the same run:
// the testcase's `class` attribute (the backslashed name PHPUnit prints and
// filters by; `classname` has dots for a namespace), `::`, and its name,
// which for a data set is `method with data set "x"` or `method with data
// set #k`, the form phpFailureID gives the printed line. A testcase with no
// class or no name refuses the whole record, since a list missing it would
// be read as every failure.
//
// Errors come first, then failures, each in the record's order, because
// that is the order PHPUnit prints its two sections in, so phpFirstFailure
// names the same test with the hook and without it. Under --reverse-list
// PHPUnit prints each section in reverse (measured on 11.5.57) and the
// record does not change, so there the first named error differs between
// the two sources; both name a test that failed, and the kill confirmation
// runs whichever is named alone.
func phpunitRecordedFailures(out string) ([]string, bool) {
	cases, ok := junitRecord(out)
	if !ok {
		return nil, false
	}
	var errs, fails []string
	for _, c := range cases {
		if c.Class == "" || c.Name == "" {
			return nil, false
		}
		id := c.Class + "::" + c.Name
		if c.Kind == junitError {
			errs = append(errs, id)
		} else {
			fails = append(fails, id)
		}
	}
	return append(errs, fails...), true
}
