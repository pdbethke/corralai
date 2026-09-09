// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"strings"
	"testing"
)

// A mutant must anchor UNIQUELY, which is what lets the recorded hunk stand
// for the edit that was actually made. The guard resumed its scan at
// i+len(SEARCH), so it could not see an occurrence OVERLAPPING the first:
// SEARCH "aa" in "aaa" occurs at 0 and at 1, and Apply silently mutated the
// first instead of refusing. Found by an antigravity seat on internal/adequacy,
// let stand by claude-code, 2026-09-08.
func TestApplyRefusesASelfOverlappingAnchor(t *testing.T) {
	for _, tc := range []struct {
		name, src, search string
		wantErr           bool
	}{
		{"self-overlapping", "xaaay", "aa", true},
		{"plainly repeated", "abcQabc", "abc", true},
		{"genuinely unique", "abcdef", "abc", false},
	} {
		m := Mutant{ID: "s0/m1", Search: tc.search, Replace: "ZZ"}
		got, err := m.Apply(tc.src)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("%s: Apply accepted a SEARCH that occurs more than once and produced %q — the recorded hunk no longer identifies which occurrence was mutated", tc.name, got)
		case tc.wantErr && !strings.Contains(err.Error(), "uniquely"):
			t.Errorf("%s: refused for the wrong reason: %v", tc.name, err)
		case !tc.wantErr && err != nil:
			t.Errorf("%s: a unique anchor was refused: %v", tc.name, err)
		}
	}
}
