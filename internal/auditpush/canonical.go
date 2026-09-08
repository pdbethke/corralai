// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"encoding/json"
)

// CanonicalSparseJSON is the byte form the warehouse-rows hash is computed
// over from hash version 2 on: v marshalled to JSON, then every empty value
// — null, "", 0, false, an empty array or object — pruned, recursively, and
// the result re-marshalled with sorted keys.
//
// WHY SPARSE. The hash used to be over the full JSON of the Row structs as
// this binary defines them. Every column added to the warehouse since —
// scan_uid, started_at, mutant_budget, the exam's reach, the writer pair's
// counts — added a field to the struct, and a row pushed by an older binary
// reads back with that field at its zero value: nil, "", 0. The full JSON
// of the read-back row therefore differed from the bytes the older binary
// hashed, and `corral verify --db` failed on every statement pushed before
// the column existed, over rows nobody had touched. A zero-valued field is
// exactly what "this row never had this column" looks like, so a form that
// omits zeros hashes the same bytes on both sides. A genuinely zero value
// (Survivors 0) is dropped on both sides too — the form is canonical, not a
// second copy of the semantics.
//
// Deterministic by construction: encoding/json sorts map keys, and nothing
// here depends on struct field order.
func CanonicalSparseJSON(v any) ([]byte, error) {
	full, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(full, &tree); err != nil {
		return nil, err
	}
	pruned, _ := pruneEmpty(tree)
	return json.Marshal(pruned)
}

// CanonicalSparseJSONV2 is the sparse form with two losses closed, used by
// warehouse-rows hash version 4 on. CanonicalSparseJSON stays exactly as it
// was, because every statement signed at rows-hash v2/v3 and every
// corral-ledger-2 entry was hashed under it and must keep verifying.
//
// What moved, both found by a cold review 2026-09-08:
//
//   - Numbers are kept as their literals (json.Number). Decoding without
//     UseNumber sent every number through float64, so two integers past 2^53
//     — a token count, a millisecond clock — hashed identically. That is the
//     exact loss CanonicalFullJSON documents itself as avoiding. (R8)
//   - Empty ELEMENTS are no longer dropped from arrays; only empty KEYS are
//     dropped from objects. Pruning elements meant a Files or Mutants slice
//     carrying an all-zero row hashed the same as one without that row, so
//     the form was not injective over slice length and a signed rows hash
//     did not distinguish two different sets of rows. (R9)
//
// An array's own emptiness still propagates: [] is empty, so a key holding
// it is still dropped. What no longer happens is a non-empty array quietly
// losing members.
func CanonicalSparseJSONV2(v any) ([]byte, error) {
	full, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(full))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	pruned, _ := pruneEmptyV2(tree)
	return json.Marshal(pruned)
}

// pruneEmptyV2 is pruneEmpty with array elements preserved and json.Number
// understood.
func pruneEmptyV2(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		return t, t == ""
	case bool:
		return t, !t
	case json.Number:
		return t, t.String() == "0"
	case float64:
		return t, t == 0
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			// Every element is kept, pruned in place. Dropping an "empty"
			// element changes the array's LENGTH, which is data.
			p, _ := pruneEmptyV2(e)
			out = append(out, p)
		}
		return out, len(out) == 0
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			p, empty := pruneEmptyV2(e)
			if !empty {
				out[k] = p
			}
		}
		return out, len(out) == 0
	default:
		return t, false
	}
}

// CanonicalFullJSON is the byte form a LEDGER ENTRY's hash is computed over
// from corral-ledger-3 on: the entry's own JSON as written, re-marshalled
// with sorted keys and NOTHING pruned. The sparse form is right for
// warehouse rows, which are read back through a schema that grows; it is
// WRONG for an entry, whose bytes are the file itself and never pass
// through a schema — and under it `passed: false` (measured, failed) and
// `passed` absent (never measured) hashed and signed identically, so an
// entry could be edited from one claim to the other and still verify.
// Found by a Claude Code reviewer, let stand by Codex (ed079ca08965#R4).
// Numbers are kept as their literals (json.Number), so a value past 2^53
// is hashed as written, not as the nearest float.
func CanonicalFullJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

// pruneEmpty returns v with every empty value removed, and whether v itself
// is empty after pruning.
func pruneEmpty(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		return t, t == ""
	case bool:
		return t, !t
	case float64:
		return t, t == 0
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			p, empty := pruneEmpty(e)
			if !empty {
				out = append(out, p)
			}
		}
		return out, len(out) == 0
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			p, empty := pruneEmpty(e)
			if !empty {
				out[k] = p
			}
		}
		return out, len(out) == 0
	default:
		return t, false
	}
}
