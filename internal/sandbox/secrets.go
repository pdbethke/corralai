// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
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

// cargoMarkers are files and directories only cargo (or rustup's installer)
// writes into a CARGO_HOME: `cargo install`'s ledgers, the package-cache
// lock, the registry cache, and rustup's own proxy. bin/cargo alone is NOT
// one — every install prefix has a bin/, and a standalone Rust install,
// /usr/local or Homebrew's prefix can hold bin/cargo. A generic name is never
// one either: rustup's `env` script shares its name with /usr/bin/env and
// with the env file uv's installer writes into ~/.local/bin, and a rustup
// home already carries bin/rustup.
var cargoMarkers = []string{".crates.toml", ".crates2.json", ".package-cache", "registry", "bin/rustup"}

// OperatorCargoHome is the operator's CARGO_HOME: $CARGO_HOME made absolute,
// else ~/.cargo. "" when neither can be determined. The ONE reading of the
// variable; every consumer goes through it. A CARGO_HOME set to a broad
// directory (/usr/share, ~/sdk) is honored as declared — an operator
// misconfiguration that fails closed (more is hidden, never less).
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

// IsCargoHome reports whether dir is a CARGO_HOME. A cargo home keeps far
// more than credentials beside its bin/ — registry tokens in config.toml,
// private git checkouts in git/, binstall.toml — under names a list will
// never finish, so it is judged by what it IS, not by its file names: of a
// cargo home, a jail may see bin/ and nothing else.
//
//   - The operator's own CARGO_HOME (OperatorCargoHome) is one by
//     definition, whatever else it holds — including the distribution-cargo
//     layout where ~/.cargo holds only `cargo install`ed tools, and a stray
//     lib/. This comes first: the operator's declaration can only narrow
//     what is bound, never widen it.
//   - Any other directory with lib/ is an install PREFIX (/usr, /usr/local,
//     Homebrew's prefix and Cellar kegs, a standalone /opt/rust holding
//     rustc's libraries) and is NEVER a cargo home, whatever its bin/ holds.
//   - Any other directory is one only when it is shaped like one — it has a
//     bin/ directory — AND carries a cargo marker (cargoMarkers). A directory
//     of programs (/usr/bin, ~/.local/bin) has no bin/ child, so nothing in
//     it can make it a cargo home.
func IsCargoHome(dir string) bool {
	dir = filepath.Clean(dir)
	if dir == OperatorCargoHome() {
		return true
	}
	isDir := func(rel string) bool {
		fi, err := os.Stat(filepath.Join(dir, rel)) // #nosec G703 -- existence check only on a toolchain path the operator's own PATH/env resolved
		return err == nil && fi.IsDir()
	}
	if isDir("lib") || !isDir("bin") {
		return false
	}
	for _, m := range cargoMarkers {
		if _, err := os.Lstat(filepath.Join(dir, m)); err == nil { // #nosec G703 -- existence check only, see above
			return true
		}
	}
	return false
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

// secretMasks finds the toolchain roots inside trees that are mounted whole
// and returns their SecretEntries as masks. The roots are found the way the
// toolchain is: the operator's CARGO_HOME, and the cargo home the cargo on
// PATH lives in (the official rust image puts both at /usr/local/cargo).
// Only a root strictly inside one of trees is reported — a root elsewhere is
// not reachable through a whole-tree mount, and its toolchain bind narrows
// it instead.
func secretMasks(trees []string) []mask {
	var roots []string
	if os.Getenv("CARGO_HOME") != "" {
		roots = append(roots, OperatorCargoHome())
	}
	if p, err := exec.LookPath("cargo"); err == nil {
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			p = real
		}
		if ch := CargoHomeOf(p); ch != "" {
			roots = append(roots, ch)
		}
	}
	seen := map[string]bool{}
	var out []mask
	for _, root := range roots {
		if seen[root] || !insideAny(root, trees) || len(SecretEntries(root)) == 0 {
			continue
		}
		seen[root] = true
		for _, s := range SecretEntries(root) {
			if m, ok := maskFor(s, trees); ok && !seen[m.Path] {
				seen[m.Path] = true
				out = append(out, m)
			}
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

// insideAny reports whether p lies strictly inside one of trees.
func insideAny(p string, trees []string) bool {
	for _, t := range trees {
		if strings.HasPrefix(p, filepath.Clean(t)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
