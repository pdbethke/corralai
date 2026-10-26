// SPDX-License-Identifier: Elastic-2.0

//go:build extra

package tagged

import "testing"

func TestTwo(t *testing.T) {
	if Two != 2 {
		t.Fatal("two")
	}
}
