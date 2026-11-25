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
// project's configuration or the operator's command asks for is not
// written (both measured on 11.5.57).
var phpunitRecordHook = FileRecordHook(
	func() []string { return []string{"--log-junit", recordToken} },
	recordJUnit,
	"corral runs every graded run through `sh -c` and adds `--log-junit <a temporary file>` after PHPUnit's options, "+
		"and reads which test failed from that JUnit record, PHPUnit's own account of the run, whatever the project's output settings print (testdox included); "+
		"what PHPUnit prints and its exit code are unchanged. PHPUnit writes one JUnit file, so a `--log-junit` in your command, or a `<junit>` entry in your phpunit.xml's `<logging>`, is not written on those runs",
)

// phpunitRecordedFailures is phpFailures' answer from the run's JUnit
// record: ok is false when the output carries no complete record block, the
// block is not JUnit, or the JUnit is not well-formed (junitFailures), and
// the printed lines are then read instead. With a record, phpFailures adds
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
	format, body, ok := RecordBlock(out)
	if !ok || format != recordJUnit {
		return nil, false
	}
	cases, err := junitFailures(body)
	if err != nil {
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
