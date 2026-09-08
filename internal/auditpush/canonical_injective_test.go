// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"encoding/json"
	"testing"
)

// The sparse form is what a warehouse-rows hash is taken over. Two things
// made it non-injective, so two materially different bundles could hash
// identically — a signed number that does not distinguish what it claims to.
// Found by a cold review, 2026-09-08 (R8, R9).
func TestSparseV2IsInjectiveOverArraysAndBigIntegers(t *testing.T) {
	// R9: pruneEmpty removed empty ELEMENTS from arrays, so a slice with an
	// all-zero row hashed the same as one without it — the form was not
	// injective over slice LENGTH.
	withZeroRow := map[string]any{"Files": []any{map[string]any{"Path": "a.go"}, map[string]any{}}}
	withoutIt := map[string]any{"Files": []any{map[string]any{"Path": "a.go"}}}
	a, err := CanonicalSparseJSONV2(withZeroRow)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalSparseJSONV2(withoutIt)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Errorf("a slice with an all-zero row hashes identically to one without it: %s", a)
	}

	// R8: decoding without UseNumber sent every number through float64, so
	// two integers past 2^53 collapsed to the same bytes.
	var x, y any
	if err := json.Unmarshal([]byte(`{"n":9007199254740993}`), &x); err != nil {
		t.Fatal(err)
	}
	_ = x
	p, err := CanonicalSparseJSONV2(json.RawMessage(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	q, err := CanonicalSparseJSONV2(json.RawMessage(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(p) == string(q) {
		t.Errorf("two integers past 2^53 hash identically: %s == %s", p, q)
	}
	_ = y
}

// The old form is still the one format-2 ledger entries and v2/v3 statements
// were signed under, so it must not move.
func TestSparseV1IsUnchanged(t *testing.T) {
	got, err := CanonicalSparseJSON(map[string]any{"a": "x", "b": "", "c": 0, "d": []any{map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"x"}`; string(got) != want {
		t.Fatalf("the legacy sparse form moved: got %s, want %s — every statement and format-2 entry signed under it would stop verifying", got, want)
	}
}
