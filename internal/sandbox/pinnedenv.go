// SPDX-License-Identifier: Elastic-2.0

package sandbox

// SystemRustupHome is where the jail looks for Rust toolchains: the
// system-wide rustup layout (RUSTUP_HOME=/usr/local/rustup, CARGO_HOME=
// /usr/local/cargo at install time) that scripts/ci-provision-test-toolchains.sh
// installs and the Rust preflight tells the operator to install.
const SystemRustupHome = "/usr/local/rustup"

// pinnedEnv is every environment variable the jail SETS rather than inherits:
// the bwrap and container backends strip these keys from the host env they
// forward and set the pinned value instead. One table, read by both of those
// backends, so a pin added for one toolchain (Rust's three are in it now)
// cannot reach the bwrap jail and miss the container one.
//
// The darwin sandbox-exec backend (isolator_darwin.go) neither strips nor
// pins these keys: it passes through the environment it is handed —
// MinimalEnv by default, which does not carry GOTOOLCHAIN, so `go` there
// falls back to its default of `auto`. That is a known gap, not fixed here. The pins live here, not on the language plugins,
// because internal/lang imports this package and because an isolation
// property must not depend on which packages a binary happens to link —
// corral-agent links sandbox without lang.
//
// A row marked OfflineOnly is set only on a jail that has
// no network. The one networked jail, the dependency fetch (`cargo vendor`),
// has to reach the network, so it is not told the flag that forbids it; the
// host's own value of the key is still stripped from it, so neither the host
// nor the table can make a fetch offline. Read the table through pinsFor.
var pinnedEnv = []struct {
	Key, Value  string
	OfflineOnly bool
}{
	// A writable HOME on tmpfs so toolchains that cache under $HOME (go
	// build's GOCACHE, npm, pip) work. Ephemeral per command; the workspace is
	// for artifacts.
	{Key: "HOME", Value: "/home/agent"},
	// The jail is offline (no network unless Options.Network). Pin
	// GOTOOLCHAIN=local so `go` NEVER tries to DOWNLOAD a go.mod-pinned
	// toolchain (which, with no network, either hangs or dies with a cryptic
	// "toolchain not available"). A repo requiring a newer Go than the jail's
	// then fails with a clear "go.mod requires go >= X" error instead — an
	// honest, actionable signal.
	{Key: "GOTOOLCHAIN", Value: "local"},
	// The Rust toolchain lives system-wide under SystemRustupHome, inside the
	// /usr the jail mounts read-only. With HOME pinned to the tmpfs above, a
	// rustup proxy that resolved on the host (~/.cargo/bin/cargo, bound by
	// adequacy's toolchain binds) looks for its toolchains in the EMPTY
	// /home/agent/.rustup and dies with "no default is configured" (measured).
	// Pinning the rustup home makes the proxy dispatch to the system toolchain
	// whichever cargo the operator's PATH found, and makes a direct system
	// cargo behave identically. Not inherited: a host RUSTUP_HOME names a
	// directory the jail cannot see.
	{Key: "RUSTUP_HOME", Value: SystemRustupHome},
	// Cargo takes locks and writes its registry cache under CARGO_HOME even
	// when offline, and the toolchain's own cargo home under /usr is
	// read-only. The writable tmpfs HOME is the one place that works; it is
	// ephemeral, like HOME. It is also the vendoring step's download cache.
	// A host CARGO_HOME would name a directory the jail cannot see, and
	// pointing a jail at the operator's real one would put their registry
	// credentials in reach.
	{Key: "CARGO_HOME", Value: "/home/agent/.cargo"},
	// The jail is offline, so cargo would otherwise try the network for the
	// registry index and hang until a timeout on any resolution it can't do
	// locally. With this it fails at once, naming the missing crate. Graded
	// runs ONLY: the vendoring step runs `cargo vendor` with network on
	// purpose, and this flag would make it unable to download anything.
	{Key: "CARGO_NET_OFFLINE", Value: "true", OfflineOnly: true},
}

// pinsFor is the pins a jail with (network) or without network is given:
// every row, except the offline-only rows on a networked jail. Both backends
// read the table through here so the two cannot disagree.
func pinsFor(network bool) []struct {
	Key, Value  string
	OfflineOnly bool
} {
	var out []struct {
		Key, Value  string
		OfflineOnly bool
	}
	for _, p := range pinnedEnv {
		if p.OfflineOnly && network {
			continue
		}
		out = append(out, p)
	}
	return out
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
