// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// An entry signed before ScanRow.UnitPass existed (the committed two-entries
// fixture, written 2026-10-22) still verifies and reads back with the field
// empty: a new optional field must not invalidate the record that predates it.
func TestAnEntryFromBeforeTheUnitPassFieldReadsBackWithoutIt(t *testing.T) {
	pub := fixtureKey().Public().(ed25519.PublicKey)
	d, err := ledger.Derive(rawDir(t, filepath.Join("testdata", "two-entries")), ledger.Trust{RootPub: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) == 0 || len(d.Scans) != len(d.Entries) {
		t.Fatalf("fixture derived to %d entries and %d scans", len(d.Entries), len(d.Scans))
	}
	for _, st := range d.Entries {
		if st.Status != "verified" {
			t.Errorf("a pre-UnitPass entry must still verify: %+v", st)
		}
	}
	for _, s := range d.Scans {
		if s.UnitPass != "" {
			t.Errorf("UnitPass = %q on an entry written before the field", s.UnitPass)
		}
	}
}
