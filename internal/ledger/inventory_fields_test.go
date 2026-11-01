// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fields that record the walk's inventory, the shadow draw, the cap's
// outcome and the per-file mutant counts are additive: a row without them
// marshals to the bytes it always did (so an older entry still verifies), and
// an older entry's JSON reads back with them absent.
func TestInventoryFieldsAreAdditive(t *testing.T) {
	scan, _ := json.Marshal(ScanRow{Repo: "o/r"})
	row, _ := json.Marshal(Row{Repo: "o/r"})
	for _, k := range []string{"Inventory", "ShadowDraw", "MaxTokensReached"} {
		if strings.Contains(string(scan), k) {
			t.Errorf("an empty ScanRow marshals %s: %s", k, scan)
		}
	}
	for _, k := range []string{"MutantsUnplaced", "MutantsUnmeasured", "MutantsDuplicate", "WriterAttempts"} {
		if strings.Contains(string(row), k) {
			t.Errorf("an empty Row marshals %s: %s", k, row)
		}
	}
	var s ScanRow
	var r Row
	if err := json.Unmarshal(scan, &s); err != nil || s.Inventory != "" || s.MaxTokensReached != nil {
		t.Errorf("old ScanRow: %+v %v", s, err)
	}
	if err := json.Unmarshal(row, &r); err != nil || r.MutantsUnplaced != nil || r.MutantsUnmeasured != nil || r.MutantsDuplicate != nil {
		t.Errorf("old Row: %+v %v", r, err)
	}
	// A measured zero is kept, not omitted.
	zero := 0
	b, _ := json.Marshal(Row{MutantsUnmeasured: &zero})
	if !strings.Contains(string(b), `"MutantsUnmeasured":0`) {
		t.Errorf("a measured zero was dropped: %s", b)
	}
}
