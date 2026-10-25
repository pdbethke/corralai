// SPDX-License-Identifier: Elastic-2.0

package a

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 1) != 2 {
		t.Fatal("add")
	}
}

func TestAddMore(t *testing.T) {
	if Add(2, 2) != 4 {
		t.Fatal("add")
	}
}
