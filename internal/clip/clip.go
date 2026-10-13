// SPDX-License-Identifier: Elastic-2.0

// Package clip shortens text for error messages and records. It is a leaf —
// it imports nothing of corral's — so every package that needs it can import
// it without a cycle: the agent backend, the review package and the CLIs all
// do, and each used to carry its own byte-identical copy.
package clip

// Tail keeps the last n bytes of s, marked with a leading "…" when anything
// was cut. The end of a process's output is where it says why it stopped, so
// a head-truncating clip would keep the wrong half.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
