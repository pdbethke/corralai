// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBusSignalsOnAction(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bus := NewBus()
	s.SetBus(bus)
	sub, cancel := bus.Subscribe()
	defer cancel()
	if _, err := s.ClaimPaths("A", []string{"x/y"}, 60, true, "work"); err != nil { // audited action -> Publish
		t.Fatal(err)
	}
	select {
	case <-sub:
	case <-time.After(time.Second):
		t.Fatal("expected a bus signal within 1s of an audited action")
	}
}
