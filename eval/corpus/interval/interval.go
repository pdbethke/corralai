// SPDX-License-Identifier: Elastic-2.0
package interval

// Contains reports whether x is within the inclusive range [lo, hi].
func Contains(lo, hi, x int) bool { return x >= lo && x <= hi }
