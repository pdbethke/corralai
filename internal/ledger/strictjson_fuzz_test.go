// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// FuzzStrictJSON drives the token walk that gates the bundle and the Rekor
// body. It must not panic, and anything the strict gate accepts must also
// decode with plain encoding/json: the gate only narrows what is accepted.
func FuzzStrictJSON(f *testing.F) {
	_, bundle, _ := keylessFuzzFixture(f)
	f.Add(bundle)
	f.Add(bytes.Replace(bundle, []byte(`"mediaType"`), []byte(`"MEDIATYPE"`), 1))
	f.Add(bytes.Replace(bundle, []byte(`{`), []byte(`{"mediaType":"x","mediaType":"y",`), 1))
	f.Add(append(append([]byte(nil), bundle...), []byte(` {}`)...))
	f.Add(bundle[:len(bundle)-1])
	f.Add([]byte(`{"apiVersion":"0.0.1","kind":"hashedrekord","spec":{"data":{"hash":{"algorithm":"sha256","value":"00"}},"signature":{"content":"x","publicKey":{"content":"y"}}}}`))
	f.Add([]byte(`{"spec":[]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if ledger.StrictDecodeBundle(b) == nil && ledger.JSONDecodeBundle(b) != nil {
			t.Fatalf("strict gate accepted a bundle encoding/json cannot decode:\n%s", b)
		}
		_ = ledger.StrictDecodeRekorBody(b)
	})
}
