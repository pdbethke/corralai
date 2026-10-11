// SPDX-License-Identifier: Elastic-2.0

package sandbox

// pinnedEnv is every environment variable the jail SETS rather than inherits:
// the bwrap and container backends strip these keys from the host env they
// forward and set the pinned value instead. One table, read by both of those
// backends, so a pin added for one toolchain (Rust's offline flag is next)
// cannot reach the bwrap jail and miss the container one.
//
// The darwin sandbox-exec backend (isolator_darwin.go) neither strips nor
// pins these keys: it passes through the environment it is handed —
// MinimalEnv by default, which does not carry GOTOOLCHAIN, so `go` there
// falls back to its default of `auto`. That is a known gap, not fixed here. The pins live here, not on the language plugins,
// because internal/lang imports this package and because an isolation
// property must not depend on which packages a binary happens to link —
// corral-agent links sandbox without lang.
var pinnedEnv = []struct{ Key, Value string }{
	// A writable HOME on tmpfs so toolchains that cache under $HOME (go
	// build's GOCACHE, npm, pip) work. Ephemeral per command; the workspace is
	// for artifacts.
	{"HOME", "/home/agent"},
	// The jail is offline (no network unless Options.Network). Pin
	// GOTOOLCHAIN=local so `go` NEVER tries to DOWNLOAD a go.mod-pinned
	// toolchain (which, with no network, either hangs or dies with a cryptic
	// "toolchain not available"). A repo requiring a newer Go than the jail's
	// then fails with a clear "go.mod requires go >= X" error instead — an
	// honest, actionable signal.
	{"GOTOOLCHAIN", "local"},
}

// isPinned reports whether key is set by the jail rather than inherited.
func isPinned(key string) bool {
	for _, p := range pinnedEnv {
		if p.Key == key {
			return true
		}
	}
	return false
}
