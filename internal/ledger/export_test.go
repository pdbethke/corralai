// SPDX-License-Identifier: Elastic-2.0

package ledger

import "encoding/json"

// VerifyKeylessBundle exposes the bundle check, over any artifact, to the
// external test package: the differential and real-bundle tests verify bytes
// that are not ledger entries.
var VerifyKeylessBundle = verifyBundle

// StrictDecodeBundle is the shape gate and decode the keyless verifier runs
// on a bundle, and JSONDecodeBundle is the plain encoding/json decode of the
// same struct, so the strict-JSON fuzz target can assert that whatever the
// gate accepts the standard library also decodes.
func StrictDecodeBundle(b []byte) error {
	var v sigstoreBundle
	return strictDecode(b, bundleShape, &v)
}

func JSONDecodeBundle(b []byte) error {
	var v sigstoreBundle
	return json.Unmarshal(b, &v)
}

// StrictDecodeRekorBody is the same gate for a Rekor hashedrekord body.
func StrictDecodeRekorBody(b []byte) error {
	var v map[string]any
	return strictDecode(b, hashedRekordShape, &v)
}
