// SPDX-License-Identifier: Elastic-2.0

// internal/admission/admission_test.go
package admission

import "testing"

func TestLocalSemaphoreRefusesAtCap(t *testing.T) {
	c := NewLocal(2, 100, func() float64 { return 0 }) // high loadFactor => load gate never trips
	l1, err := c.Acquire("builder")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if _, err := c.Acquire("builder"); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if _, err := c.Acquire("builder"); err == nil {
		t.Fatal("acquire 3 should be refused at cap 2")
	}
	l1.Release()
	if _, err := c.Acquire("builder"); err != nil {
		t.Fatalf("acquire after release should succeed: %v", err)
	}
}

func TestLocalLoadGateRefuses(t *testing.T) {
	// loadFactor 2.0, NumCPU≥1 => threshold ≥2.0; injected load 999 must refuse.
	c := NewLocal(100, 2.0, func() float64 { return 999 })
	if _, err := c.Acquire("tester"); err == nil {
		t.Fatal("acquire should be refused when load exceeds threshold")
	}
}

func TestLocalReleaseIsIdempotentlySafe(t *testing.T) {
	c := NewLocal(1, 100, func() float64 { return 0 })
	l, err := c.Acquire("x")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release() // double release must not free an extra slot
	if _, err := c.Acquire("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire("x"); err == nil {
		t.Fatal("double-release must not have freed two slots")
	}
}
