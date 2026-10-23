// SPDX-License-Identifier: Elastic-2.0

package auditpush

import "github.com/pdbethke/corralai/internal/ledger"

// The canonical byte forms live in internal/ledger (which builds for the
// browser, where a page recomputes an entry's hash); these are the names
// this package has always exported.

// CanonicalSparseJSON is the warehouse-rows hash form through hash version
// 3; see ledger.CanonicalSparseJSON.
func CanonicalSparseJSON(v any) ([]byte, error) { return ledger.CanonicalSparseJSON(v) }

// CanonicalSparseJSONV2 is the sparse form used from warehouse-rows hash
// version 4; see ledger.CanonicalSparseJSONV2.
func CanonicalSparseJSONV2(v any) ([]byte, error) { return ledger.CanonicalSparseJSONV2(v) }

// CanonicalFullJSON is the form a ledger entry's hash is computed over from
// corral-ledger-3; see ledger.CanonicalFullJSON.
func CanonicalFullJSON(raw []byte) ([]byte, error) { return ledger.CanonicalFullJSON(raw) }
