// SPDX-License-Identifier: Elastic-2.0

package lang

import "encoding/json"

// The format words of the JSON records (RecordBlock's format,
// FileRecordHook's format argument). Each runner's JSON has its own shape,
// so each has its own word and its own small reader (jestRecordedFailures,
// rspecRecordedFailures); a block of one is never read as the other.
const (
	recordJestJSON  = "jest-json"
	recordRSpecJSON = "rspec-json"
)

// jsonRecord decodes the run's record (RecordBlock) into v when it is a
// complete block of the given format holding exactly one well-formed JSON
// value: ok is false when the output carries no complete block, the block
// is another format, or its body is not one JSON value (a truncated record,
// or anything after the value), and a row's reader then reads the runner's
// printed lines instead. All or nothing, as junitRecord is for a JUnit
// block: the one door every JSON-record row reads its record through.
func jsonRecord(out, format string, v any) bool {
	f, body, ok := RecordBlock(out)
	if !ok || f != format {
		return false
	}
	return json.Unmarshal([]byte(body), v) == nil
}
