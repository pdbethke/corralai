// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// The jail binds /usr and nothing else, on the reasoning that a toolchain is
// part of the operating system. That is only true of a toolchain installed by
// the operating system's package manager. Every popular version manager
// deliberately installs somewhere else — asdf under ~/.asdf, nvm under ~/.nvm,
// rustup under ~/.cargo, pyenv under ~/.pyenv, Homebrew under /opt/homebrew or
// /home/linuxbrew, mise and volta under ~/.local — and so does snap, under
// /snap.
//
// The result was a jail that could not see the compiler for the language it was
// auditing, reporting "go: not found" as though the operator's PROJECT were
// broken. Naming each installer in an allowlist loses that race permanently, so
// this resolves the operator's OWN command instead and binds what that binary
// actually needs: nothing is guessed, and nothing is mounted that the command
// did not already point at.

// toolchainBoundaries are directories whose CHILDREN are plausible toolchain
// roots, and which must never themselves be bound. Binding $HOME to run a test
// would hand the sandboxed command every credential the operator owns, which is
// the opposite of this package's purpose.
func toolchainBoundaries() map[string]bool {
	b := map[string]bool{
		"/": true, "/home": true, "/usr": true, "/opt": true, "/snap": true,
		"/var": true, "/etc": true, "/srv": true, "/mnt": true, "/media": true,
		"/root": true, "/tmp": true, "/local": true,
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		b[filepath.Clean(home)] = true
	}
	return b
}

// ErrSnapToolchain is returned when the command resolves to a snap. It is a
// sentinel so a caller can recognize the case and say something useful.
type ErrSnapToolchain struct{ Command, Path string }

func (e ErrSnapToolchain) Error() string {
	return fmt.Sprintf("%q resolves to a snap (%s), and snaps cannot run inside the audit sandbox: "+
		"a snap re-execs through snapd over /run/snapd.socket, which a network-isolated jail has no access to — "+
		"mounting more paths cannot fix it. Install %s from your distribution's packages or upstream "+
		"(e.g. apt, or the official tarball) so it lives on a real filesystem path",
		e.Command, e.Path, e.Command)
}

// toolchainBindFor resolves cmd0 on the HOST and returns the read-only bind the
// jail needs in order to run it, or a zero Bind when the binary already lives
// under a path the jail mounts anyway (/usr, which covers apt and /usr/local).
//
// A command that cannot be resolved at all returns a zero Bind and no error:
// the jail will fail on its own with the runner's real message, which is a
// better diagnosis than a guess from here. That is deliberate — this function
// exists to REMOVE a misleading failure, not to add one.
func toolchainBindFor(cmd0 string) (sandbox.Bind, error) {
	cmd0 = strings.TrimSpace(cmd0)
	if cmd0 == "" {
		return sandbox.Bind{}, nil
	}
	p, err := exec.LookPath(cmd0)
	if err != nil {
		return sandbox.Bind{}, nil
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		real = p
	}

	// A snap shim is a symlink to /usr/bin/snap: the real toolchain is not on
	// the path at all, and the shim only works by talking to snapd. Refuse
	// with an explanation rather than binding /snap and failing later for a
	// reason the operator cannot act on.
	if strings.HasPrefix(p, "/snap/") || real == "/usr/bin/snap" || strings.HasPrefix(real, "/snap/") {
		return sandbox.Bind{}, ErrSnapToolchain{Command: cmd0, Path: p}
	}

	// Already inside what the jail mounts.
	if strings.HasPrefix(real, "/usr/") {
		return sandbox.Bind{}, nil
	}

	root := toolchainRoot(real)
	if root == "" {
		return sandbox.Bind{}, nil
	}
	// Bound at the SAME absolute path, so the operator's own PATH keeps
	// working inside the jail without rewriting the command.
	return sandbox.Bind{Host: root, Target: root}, nil
}

// toolchainRoot walks up from a resolved binary to the directory that installs
// it — the first ancestor whose own parent is a boundary. For
// ~/.asdf/installs/golang/1.22/bin/go that is ~/.asdf; for /opt/homebrew/bin/node
// it is /opt/homebrew.
//
// Returns "" when the walk reaches a boundary immediately (a binary sitting
// directly in a boundary directory has no root to bind that would not be the
// boundary itself).
func toolchainRoot(real string) string {
	bounds := toolchainBoundaries()
	dir := filepath.Dir(filepath.Clean(real))
	for dir != "" && dir != "/" {
		parent := filepath.Dir(dir)
		if bounds[parent] {
			if bounds[dir] {
				return "" // the binary lives directly in a boundary
			}
			return dir
		}
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}
