// SPDX-License-Identifier: Elastic-2.0

package ledger

// VerifyKeylessBundle exposes the bundle check, over any artifact, to the
// external test package: the differential and real-bundle tests verify bytes
// that are not ledger entries.
var VerifyKeylessBundle = verifyBundle
