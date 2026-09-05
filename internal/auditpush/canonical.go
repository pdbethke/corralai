// SPDX-License-Identifier: Elastic-2.0

package auditpush

import "encoding/json"

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
