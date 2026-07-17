// SPDX-License-Identifier: Elastic-2.0
package passwd

import "unicode"

// Valid reports whether p is a valid password: length >= 12 AND it contains an
// uppercase letter, a lowercase letter, a digit, and a symbol.
func Valid(p string) bool {
	if len(p) < 12 {
		return false
	}
	var up, lo, di, sy bool
	for _, r := range p {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			lo = true
		case unicode.IsDigit(r):
			di = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			sy = true
		}
	}
	return up && lo && di && sy
}
