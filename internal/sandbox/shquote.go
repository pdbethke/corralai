// SPDX-License-Identifier: Elastic-2.0

package sandbox

import "strings"

// shQuote quotes s for sh: single quotes, with each embedded ' closed,
// escaped and reopened. Same rule as adequacy's shellQuote; this package
// cannot import adequacy (adequacy imports sandbox).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
