// SPDX-License-Identifier: Elastic-2.0
package interval

import "testing"

func TestGappy_Inside(t *testing.T) {
	if !Contains(1, 10, 5) {
		t.Fatal("inside excluded")
	}
}
func TestGappy_Outside(t *testing.T) {
	if Contains(1, 10, 100) {
		t.Fatal("far-outside included")
	}
}
