// SPDX-License-Identifier: Elastic-2.0

package advpool

// VerdictFromSpec is verdictFromSpec for the external test package
// (advpool_test), which can import reposcan — reposcan imports advpool, so
// a test in this package cannot — and so drive the unit pass's real
// producer through to the verdict and its wire form.
var VerdictFromSpec = verdictFromSpec
