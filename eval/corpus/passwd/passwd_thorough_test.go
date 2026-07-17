// SPDX-License-Identifier: Elastic-2.0
package passwd

import "testing"

func TestThorough_Valid(t *testing.T) {
	if !Valid("Abcdefgh1!xy") { // 12 chars, all classes
		t.Fatal("a valid password was rejected")
	}
}
func TestThorough_TooShort(t *testing.T) {
	if Valid("Ab1!xyz") {
		t.Fatal("short accepted")
	}
}
func TestThorough_NoUpper(t *testing.T) {
	if Valid("abcdefgh1!xy") {
		t.Fatal("no-upper accepted")
	}
}
func TestThorough_NoLower(t *testing.T) {
	if Valid("ABCDEFGH1!XY") {
		t.Fatal("no-lower accepted")
	}
}
func TestThorough_NoDigit(t *testing.T) {
	if Valid("Abcdefghij!x") {
		t.Fatal("no-digit accepted")
	}
}
func TestThorough_NoSymbol(t *testing.T) {
	if Valid("Abcdefgh12xy") {
		t.Fatal("no-symbol accepted")
	}
}
