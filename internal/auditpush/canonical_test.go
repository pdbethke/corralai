// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"testing"
)

// A row pushed before a column existed reads back with the new field at its
// zero value. The sparse form hashes it to the same bytes the older binary
// saw — which is the whole reason the form exists.
func TestCanonicalSparseJSONIgnoresFieldsARowNeverHad(t *testing.T) {
	type older struct {
		Path      string
		Survivors int
		KillRate  *float64
	}
	type newer struct {
		Path      string
		Survivors int
		KillRate  *float64
		// Added later: nil / "" / 0 on every row the older binary wrote.
		MutantBudget     *int
		MutantBudgetRule string
		Complexity       int
		Decisions        []int
	}
	kr := 0.5
	a, err := CanonicalSparseJSON(older{Path: "a.py", Survivors: 3, KillRate: &kr})
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalSparseJSON(newer{Path: "a.py", Survivors: 3, KillRate: &kr})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("the same row through two struct generations must hash alike:\n%s\n%s", a, b)
	}
	// And a field that IS set changes the bytes — the form is sparse, not blind.
	mb := 8
	c, _ := CanonicalSparseJSON(newer{Path: "a.py", Survivors: 3, KillRate: &kr, MutantBudget: &mb})
	if bytes.Equal(a, c) {
		t.Fatal("a populated new field must change the canonical bytes")
	}
	// Nested empties prune all the way down; key order is irrelevant.
	d, _ := CanonicalSparseJSON(map[string]any{"z": map[string]any{"x": ""}, "a": []any{0, ""}, "k": 1})
	if string(d) != `{"k":1}` {
		t.Fatalf("nested empties must prune: %s", d)
	}
}
