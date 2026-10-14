// SPDX-License-Identifier: Elastic-2.0

package clip

import "testing"

func TestTailKeepsTheEndAndMarksTheCut(t *testing.T) {
	if got := Tail("short", 10); got != "short" {
		t.Errorf("Tail(short, 10) = %q, want it unchanged", got)
	}
	if got := Tail("abcdef", 6); got != "abcdef" {
		t.Errorf("Tail at exactly n must not cut: %q", got)
	}
	if got := Tail("abcdefgh", 3); got != "…fgh" {
		t.Errorf("Tail(abcdefgh, 3) = %q, want …fgh", got)
	}
}
