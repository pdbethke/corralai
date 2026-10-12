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

// IsCargoHome reports whether dir is a CARGO_HOME: it holds cargo's bin/ with
// a rustup proxy or cargo in it. A cargo home keeps far more than credentials
// beside its bin/ — registry tokens in config.toml, private git checkouts in
// git/, cargo-binstall's binstall.toml — under names a list will never
// finish, so it is judged by its layout, not by its file names: of a cargo
// home, a jail may see bin/ and nothing else.
func IsCargoHome(dir string) bool {
	for _, n := range []string{"rustup", "cargo"} {
		if fi, err := os.Lstat(filepath.Join(dir, "bin", n)); err == nil && !fi.IsDir() { // #nosec G703 -- existence check only, see CredentialStoreIn
			return true
		}
	}
	return false
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
	if ch := os.Getenv("CARGO_HOME"); ch != "" {
		roots = append(roots, filepath.Clean(ch))
	}
	if p, err := exec.LookPath("cargo"); err == nil {
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
			p = real
		}
		for d := filepath.Dir(p); d != "/" && d != "."; d = filepath.Dir(d) {
			if IsCargoHome(d) {
				roots = append(roots, d)
				break
			}
		}
	}
	seen := map[string]bool{}
	var out []mask
	for _, root := range roots {
		if seen[root] || !insideAny(root, trees) {
			continue
		}
		seen[root] = true
		for _, s := range SecretEntries(root) {
			fi, err := os.Lstat(s) // #nosec G703 -- type check only, see CredentialStoreIn
			if err != nil {
				continue
			}
			out = append(out, mask{Path: s, Dir: fi.IsDir()})
		}
	}
	return out
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
