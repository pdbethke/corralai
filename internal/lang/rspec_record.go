// SPDX-License-Identifier: Elastic-2.0

package lang

// rspecRecordHook is the rspec row's ReportHook: rspec's own JSON account
// of the run, from a second formatter writing to a file
// (`--format json --out <file>`, FileRecordHook). The words go after the
// row's --format progress, and their order is load-bearing: `--out` binds
// to the formatter named before it, and `--out f --format json --format
// progress` wrote the progress text into f (rspec-core 3.13.6). Measured on
// rspec-core 3.13.6 (capture-runner-fixtures.sh rspec-hook-*.txt): what
// rspec prints and its exit code are unchanged, on a failing run and a
// passing one, and the record's ids are the full descriptions the printed
// `Failed examples:` list gives (rspecRecordedFailures).
//
// It never writes a project's file. rspec takes its options from ~/.rspec,
// .rspec, .rspec-local, the command line and SPEC_OPTS, in that order, and
// each source that names a formatter replaces the list before it whole
// (ConfigurationOptions#organize_options), so a configuration file's
// `--format`/`--out` was already replaced by the row's --format progress
// before the hook existed, and a SPEC_OPTS naming one replaces corral's
// formatters, the hook's included, and writes its file with or without
// the hook (corral then reads the printed lines, if SPEC_OPTS leaves any).
// A formatter a spec_helper adds is the project's own code and writes
// either way (each measured, capture-runner-fixtures.sh rspec-wrote-*).
//
// The fallback is the zero RecordFile's: rspec writes its JSON to
// /dev/null and passes a passing run (rspec-hook-fallback-exit).
var rspecRecordHook = FileRecordHook(
	func() []string { return []string{"--format", "json", "--out", recordToken} },
	recordRSpecJSON,
	"corral runs every graded run through `sh -c` and adds `--format json --out <a temporary file>` after `--format progress`, "+
		"and reads which test failed from that JSON record, rspec's own account of the run, by the full descriptions the printed `Failed examples:` list gives; "+
		"what rspec prints and its exit code are unchanged. A SPEC_OPTS that names a formatter replaces corral's, and then only the printed lines are read",
)

// rspecFailedTests is the rspec row's failure reader: the run's JSON record
// first (rspecRecordedFailures), then each example the printed `Failed
// examples:` list names that the record does not, by recordThenPrinted,
// the rule every record-file row follows; the printed list alone where
// there is no record.
func rspecFailedTests(out string) []string {
	return recordThenPrinted(out, rspecRecordedFailures, rspecFailures, sameID)
}

// rspecRecordedFailures is every failing example rspec's JSON record names,
// in its order, by its full description, the id rspecFailures reads and the
// row's -E selection takes. ok is false when jsonRecord reads none, the
// JSON has no examples (it is not rspec's), or a failing example has no
// full description, since a list missing it would be read as every
// failure.
//
// Only status "failed" is a failure: rspec-core 3.13.6 records a pending
// example that fails as "pending" and one that passes as "failed", as its
// printed list does; an example whose before(:context) raised is "failed";
// an error in an after(:context) hook, or a file that failed to load, is
// outside every example (errors_outside_of_examples_count) and named by
// neither source.
func rspecRecordedFailures(out string) ([]string, bool) {
	var rep struct {
		Examples *[]struct {
			FullDescription string `json:"full_description"`
			Status          string `json:"status"`
		} `json:"examples"`
	}
	if !jsonRecord(out, recordRSpecJSON, &rep) || rep.Examples == nil {
		return nil, false
	}
	var ids []string
	for _, e := range *rep.Examples {
		if e.Status != "failed" {
			continue
		}
		if e.FullDescription == "" {
			return nil, false
		}
		ids = append(ids, e.FullDescription)
	}
	return ids, true
}
