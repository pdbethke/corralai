// SPDX-License-Identifier: Elastic-2.0

package sandbox

// jailDirs names the jail's own writable HOME and TMPDIR. Each backend
// supplies its own: bwrap and the container mount tmpfs at fixed paths
// (linuxJailDirs); sandbox-exec has no tmpfs, so it puts both inside the
// workspace (sandboxExecIsolator.jailDirs).
type jailDirs struct{ Home, Tmp string }

// linuxJailDirs are the tmpfs mounts bwrap (--tmpfs) and the container
// (--tmpfs) give every command.
var linuxJailDirs = jailDirs{Home: "/home/agent", Tmp: "/tmp"}

// envPin is one variable the jail SETS rather than inherits.
type envPin struct{ Key, Value string }

// pinnedEnv is every environment variable the jail SETS rather than inherits:
// each backend strips these keys from the host env it forwards and sets the
// pinned value instead. One table, read by all three backends, so a pin added
// for one toolchain reaches every jail.
//
// The pins live here, not on the language plugins, because internal/lang
// imports this package and because an isolation property must not depend on
// which packages a binary happens to link — corral-agent links sandbox
// without lang.
func pinnedEnv(d jailDirs) []envPin {
	return []envPin{
		// A writable HOME the jail owns, so toolchains that cache under $HOME
		// (go build's GOCACHE, npm, pip) work. Ephemeral per command; the
		// workspace is for artifacts.
		{"HOME", d.Home},
		// The host's TMPDIR (MinimalEnv forwards it) names a host path: on
		// macOS a /var/folders dir the jail cannot write, on Linux a path that
		// does not exist inside the mount namespace.
		{"TMPDIR", d.Tmp},
		// The jail is offline (no network unless Options.Network). Pin
		// GOTOOLCHAIN=local so `go` NEVER tries to DOWNLOAD a go.mod-pinned
		// toolchain (which, with no network, either hangs or dies with a cryptic
		// "toolchain not available"). A repo requiring a newer Go than the jail's
		// then fails with a clear "go.mod requires go >= X" error instead — an
		// honest, actionable signal.
		{"GOTOOLCHAIN", "local"},
		// The system gitconfig is operator configuration the jail must not
		// depend on or read. On macOS it lives under the denied <brew>/etc,
		// where git dies on the unreadable file (EPERM, not ENOENT); bwrap
		// never binds /etc/gitconfig either. So this is parity, and changes
		// nothing on Linux.
		{"GIT_CONFIG_NOSYSTEM", "1"},
	}
}

// isPinned reports whether key is set by the jail rather than inherited.
func isPinned(key string) bool {
	for _, p := range pinnedEnv(jailDirs{}) {
		if p.Key == key {
			return true
		}
	}
	return false
}
