// SPDX-License-Identifier: Elastic-2.0

package lang

// ShellQuoteArgs is shellQuoteArgs for the external test package
// (lang_test), which can import adequacy — adequacy imports lang, so a test
// in this package cannot — and so hold the capture script's inline lines
// equal to what adequacy.Overlay composes and the rows build.
var ShellQuoteArgs = shellQuoteArgs
