// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"io"
	"os"
)

// runnerWarning raises a GitHub Actions workflow annotation when running on
// a runner, and does nothing anywhere else. It is for a side effect of an
// audit that failed while the audit itself did not — a statement write, a
// push, a transparency upload: the exit code stays the verdict's, but on a
// runner a stderr line is scrolled past and the job is green, so the
// failure is raised where it will be read. The caller prints its own stderr
// line first; this is the annotation only.
//
// Three call sites wrote this inline and one forgot to (review
// e1608f971235#R2); a rule at two doors and not the third is why it is one
// function now.
func runnerWarning(stderr io.Writer, title, format string, args ...any) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	fmt.Fprintf(stderr, "::warning title=%s::%s\n", title, fmt.Sprintf(format, args...))
}
