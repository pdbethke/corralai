// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// The report a file's account was read from is named on it, as
// "sha256:<hex>", and survives the JSON text ScanRow.ToolMutants holds: a
// recorded seed can only be replayed against a report the record names.
func TestToolMutantCountsNameTheirReport(t *testing.T) {
	n := 1
	in := []ToolMutantCounts{
		{Path: "a.ts", Tool: "stryker@10.0.0", Report: "sha256:" + strings.Repeat("ab", 32), Taken: &n},
		{Path: "b.ts", Tool: "stryker@10.0.0", Report: "sha256:" + strings.Repeat("ab", 32), Refused: "no entry"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"report":"sha256:abab`) {
		t.Errorf("the JSON text does not carry the report as report: %s", b)
	}
	var out []ToolMutantCounts
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if out[i].Report != in[i].Report {
			t.Errorf("%s: Report round trip = %q, want %q", in[i].Path, out[i].Report, in[i].Report)
		}
	}
}
