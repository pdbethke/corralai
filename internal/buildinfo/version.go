// SPDX-License-Identifier: Elastic-2.0

// Package buildinfo is the one place a binary learns what version it is.
//
// corral and corral-wrangler each carried a private copy of this logic and
// corral-admin, corral-observe and corral-agent carried none, so a
// `go install …@vX` build of those three printed "dev". One home means a fix
// to the rule reaches every binary.
package buildinfo

import "runtime/debug"

// Version prefers an explicitly stamped version (a release build knows more
// than the module graph, and may be building from a checkout rather than a
// tagged module), then the module version Go records in the binary for a
// `go install <module>@<version>`.
//
// "(devel)" — what a local `go build` reports — is NOT a version and must never
// be printed as one; it, an empty string, and unavailable build info all fall
// back to "dev", which is exactly what a developer in-tree has always seen.
func Version(stamped string) string {
	return resolve(stamped, debug.ReadBuildInfo)
}

// resolve is Version with the build-info reader injected, so the three-way
// precedence is testable without building a binary.
func resolve(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	bi, ok := readBuildInfo()
	if !ok || bi == nil {
		return "dev"
	}
	switch v := bi.Main.Version; v {
	case "", "(devel)":
		return "dev"
	default:
		return v
	}
}
