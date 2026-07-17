// SPDX-License-Identifier: Elastic-2.0
package passwd

import "testing"

func TestGappy_ValidLength(t *testing.T) {
	if !Valid("Abcdefgh1!xy") {
		t.Fatal("a 12-char password was rejected")
	}
}
func TestGappy_TooShort(t *testing.T) {
	if Valid("Ab1!xyz") {
		t.Fatal("a 7-char password was accepted")
	}
}
