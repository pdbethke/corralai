// SPDX-License-Identifier: Elastic-2.0

//go:build !unix

package statedir

// lockedByAnother cannot probe fcntl locks off unix. Windows refuses to
// rename a file another process holds open, so a running daemon makes the
// move itself fail safely instead of being detected up front.
func lockedByAnother(string) (bool, error) { return false, nil }
