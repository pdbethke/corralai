// SPDX-License-Identifier: Elastic-2.0
package interval

import "testing"

func TestThorough_LowerBoundary(t *testing.T) {
	if !Contains(1, 10, 1) {
		t.Fatal("lo excluded")
	}
}
func TestThorough_UpperBoundary(t *testing.T) {
	if !Contains(1, 10, 10) {
		t.Fatal("hi excluded")
	}
}
func TestThorough_Inside(t *testing.T) {
	if !Contains(1, 10, 5) {
		t.Fatal("inside excluded")
	}
}
func TestThorough_Below(t *testing.T) {
	if Contains(1, 10, 0) {
		t.Fatal("below included")
	}
}
func TestThorough_Above(t *testing.T) {
	if Contains(1, 10, 11) {
		t.Fatal("above included")
	}
}
