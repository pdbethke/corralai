// SPDX-License-Identifier: Elastic-2.0

package b_test

import (
	"testing"

	"example.com/units/b"
)

func TestNeg(t *testing.T) {
	if b.Neg(1) != -1 {
		t.Fatal("neg")
	}
}
