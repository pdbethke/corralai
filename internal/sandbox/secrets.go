// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This file is the ONE answer to "what inside a toolchain directory must a
// jail never see". Two consumers ask it:
//
//   - adequacy's toolchain binds (toolchainBindsFor), which decide what of a
//     toolchain root to mount: a root with secrets in it is narrowed so none
//     of them is mounted;
//   - the isolators, which mount whole trees (/usr; on macOS also
//     /opt/homebrew) and so expose any toolchain root inside them: they mask
//     the same entries (secretMasks). The container backend mounts the
//     IMAGE's /usr, not the host's, so a host cargo home is never in it and
//     it has nothing to mask.
//
// It lives here, in the leaf package, because the isolators cannot import
// adequacy. Every jail reads it — the networked vendoring step, which runs
// repository-controlled programs, and the graded runs, whose output reaches
// the ledger, which can be public.

// credentialStores are the file names toolchains keep secrets under directly
// inside their own install root: cargo's credentials.toml (and the
// extensionless legacy credentials it still reads), RubyGems' credentials.
var credentialStores = []string{"credentials.toml", "credentials"}

// CredentialStoreIn names the credentials store directly inside dir, or "".
func CredentialStoreIn(dir string) string {
	for _, n := range credentialStores {
		p := filepath.Join(dir, n)
		if _, err := os.Lstat(p); err == nil { // #nosec G703 -- existence check only on a toolchain root the operator's own PATH/env resolved; nothing is opened
			return p
		}
	}
	return ""
}

// OperatorCargoHome is the operator's CARGO_HOME: $CARGO_HOME made absolute,
// else ~/.cargo. "" when neither can be determined. The ONE reading of the
// variable; every consumer goes through it. A CARGO_HOME set to a broad
// directory (/usr/share, /usr/local, ~/sdk) is honored as declared — an
// operator misconfiguration that fails closed (more is hidden, never less).
func OperatorCargoHome() string {
	if ch := os.Getenv("CARGO_HOME"); ch != "" {
		if abs, err := filepath.Abs(ch); err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cargo")
}

// resolved is p with its symlinks resolved, or p cleaned when it cannot be.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// IsCargoHome reports whether dir is a CARGO_HOME. A cargo home keeps far
// more than credentials beside its bin/ — registry tokens in config.toml,
// private git checkouts in git/, binstall.toml — under names a list will
// never finish, so of a cargo home a jail may see bin/ and nothing else.
//
// A cargo home is exactly what cargo itself defines: the operator's
// CARGO_HOME, else ~/.cargo (OperatorCargoHome), compared with symlinks
// resolved on both sides (and, on macOS, case-insensitively: pathKey). Nothing else is ever one — whatever it holds,
// including a stray lib/ in the operator's own. No marker is evidence: cargo
// writes .crates.toml into any `cargo install --root` (Homebrew sets the root
// to a keg), registry/ is also Debian's /usr/share/cargo, and rustup's env
// shares its name with /usr/bin/env.
//
// The cost, accepted: a second cargo home that CARGO_HOME does not name (an
// image's /usr/local/cargo with CARGO_HOME unset, a CARGO_HOME exported only
// in another shell) gets no cargo-home treatment, and what that exposes
// depends on where it sits:
//
//   - inside a tree an isolator mounts whole (/usr; on macOS also
//     /opt/homebrew) NOTHING in it is hidden — not credentials.toml, not
//     registry tokens in config.toml, not git/ checkouts — because
//     toolchainBindsFor binds nothing there and secretMasks masks only
//     OperatorCargoHome;
//   - elsewhere it is bound like any other toolchain root, which hides only
//     its credentials store (CredentialStoreIn): config.toml tokens and git/
//     are still visible.
//
// The remedy is the operator's: set CARGO_HOME to the cargo home the suite
// uses.
func IsCargoHome(dir string) bool {
	op := OperatorCargoHome()
	return op != "" && dir != "" && pathKey(resolved(dir)) == pathKey(resolved(op))
}

// CargoHomeOf is the nearest ancestor of path that IsCargoHome, or "". The
// one walk both consumers use to find the cargo home a binary belongs to.
func CargoHomeOf(path string) string {
	for d := filepath.Dir(filepath.Clean(path)); d != "/" && d != "." && d != filepath.Dir(d); d = filepath.Dir(d) {
		if IsCargoHome(d) {
			return d
		}
	}
	return ""
}

// SecretEntries is what of the toolchain root dir a jail must never see:
// every top-level entry but bin/ of a cargo home, or the credentials store of
// any other root. Nil when dir holds nothing secret by this rule.
func SecretEntries(dir string) []string {
	if IsCargoHome(dir) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range ents {
			if e.Name() != "bin" {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
		return out
	}
	if s := CredentialStoreIn(dir); s != "" {
		return []string{s}
	}
	return nil
}

// mask is one path a jail must cover after mounting the tree that holds it:
// a directory is covered with an empty tmpfs, a file with /dev/null.
type mask struct {
	Path string
	Dir  bool
}

// wholeBoundTrees are the host trees the isolators mount whole. A variable
// so a test can stand a temp dir in for /usr.
var wholeBoundTrees = []string{"/usr", "/opt/homebrew"}

// secretMasks finds the operator's cargo home when it lies inside a tree
// mounted whole (the official rust image sets CARGO_HOME=/usr/local/cargo)
// and returns its SecretEntries as masks. A cargo home elsewhere is not
// reachable through a whole-tree mount, and its toolchain bind narrows it
// instead.
func secretMasks(trees []string) []mask {
	op := OperatorCargoHome()
	if op == "" {
		return nil
	}
	root := resolved(op)
	if !insideAny(root, trees) {
		return nil
	}
	seen := map[string]bool{}
	var out []mask
	for _, s := range SecretEntries(root) {
		if m, ok := maskFor(s, trees); ok && !seen[m.Path] {
			seen[m.Path] = true
			out = append(out, m)
		}
	}
	return out
}

// maskFor is the mask covering entry, classified by what it RESOLVES to: an
// isolator mounts a file over a file and a tmpfs over a directory, and the
// wrong one makes the jail unstartable. A symlinked entry is masked at its
// target when that target lies inside a whole-bound tree; a target outside
// every such tree, or a dangling link, is skipped — it is not reachable
// through the tree, and bwrap cannot create a mountpoint for it on a
// read-only mount.
func maskFor(entry string, trees []string) (mask, bool) {
	target, err := filepath.EvalSymlinks(entry)
	if err != nil || !insideAny(target, trees) {
		return mask{}, false
	}
	fi, err := os.Stat(target) // #nosec G703 -- type check only, see CredentialStoreIn
	if err != nil {
		return mask{}, false
	}
	return mask{Path: target, Dir: fi.IsDir()}, true
}

// insideAny reports whether p lies strictly inside one of trees, comparing
// as this host's filesystem does (pathKey).
func insideAny(p string, trees []string) bool { return insideAnyOn(runtime.GOOS, p, trees) }

// pathKeyOn is the form of p two paths are compared in on goos. macOS's
// default APFS volume and its sandbox (SBPL) both match paths
// case-insensitively, so there /USR/local/cargo IS /usr/local/cargo, and a
// case-sensitive comparison would fail to recognize a cargo home — leaving
// its secrets unmasked under a sandbox that still reads them. Folding is
// strings.ToLower, as the sandbox profile's own checks do (sbplCheckReopen).
// Linux is case-sensitive and compares exactly.
func pathKeyOn(goos, p string) string {
	if goos == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// pathKey is pathKeyOn for this host.
func pathKey(p string) string { return pathKeyOn(runtime.GOOS, p) }

// insideAnyOn reports whether p lies strictly inside one of trees, comparing
// as goos does (pathKeyOn).
func insideAnyOn(goos, p string, trees []string) bool {
	k := pathKeyOn(goos, p)
	for _, t := range trees {
		if strings.HasPrefix(k, pathKeyOn(goos, filepath.Clean(t))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
