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
	if narrowed, refused := narrowHomeRoot(real, root); refused != "" {
		return sandbox.Bind{}, ErrUnbindableToolchain{Command: cmd0, Path: real, Root: root, Why: refused}
	} else if narrowed != "" {
		root = narrowed
	}
	// Bound at the SAME absolute path, so the operator's own PATH keeps
	// working inside the jail without rewriting the command.
	return sandbox.Bind{Host: root, Target: root}, nil
}

// ErrUnbindableToolchain is returned when the runner resolves to a directory
// the jail must not mount: one that holds the operator's credentials or
// unrelated projects, not a toolchain.
type ErrUnbindableToolchain struct{ Command, Path, Root, Why string }

func (e ErrUnbindableToolchain) Error() string {
	return fmt.Sprintf("%q resolves to %s, and running it in the jail would bind %s read-only into the sandbox — %s. "+
		"Install the runner under a toolchain directory (~/.asdf, ~/.pyenv, ~/.nvm, ~/.cargo, ~/sdk, /opt, /usr/local), "+
		"give it as a repository-relative path (vendor/bin/…, node_modules/.bin/…), or use --substrate workspace",
		e.Command, e.Path, e.Root, e.Why)
}

// homeToolchainDirs are the children of $HOME that hold TOOLCHAINS — version
// managers and language installs — and nothing else. A runner under one of
// these binds that directory, as before.
var homeToolchainDirs = map[string]bool{
	".asdf": true, ".pyenv": true, ".rbenv": true, ".nvm": true, ".nodenv": true, ".phpenv": true,
	".goenv": true, ".gvm": true, ".cargo": true, ".rustup": true, ".volta": true, ".bun": true,
	".deno": true, ".sdkman": true, ".jenv": true, ".pulumi": true, "sdk": true, "go": true, "gopath": true,
	".linuxbrew": true, ".gem": true, ".rvm": true, ".rustup-toolchains": true,
}

// narrowHomeRoot decides what toolchainRoot's answer means when it lies
// directly under $HOME. toolchainRoot walks up to the first child of a
// boundary, so `~/.local/bin/pytest` (pip --user), `~/.local/share/gem/…/bin/
// rspec` and `~/.config/composer/vendor/bin/phpunit` all resolved to
// `~/.local` or `~/.config` — and the jail bound them, read-only, which put
// `~/.local/share/keyrings`, `~/.local/share/claude`, `~/.config/gh` and
// `~/.config/gcloud` inside the sandbox. The design comment said binding
// $HOME "would hand the sandboxed command every credential the operator
// owns"; those two directories are where most of them live.
//
// Returns (narrowed, "") to bind a narrower directory instead — for the
// pip --user layout, `~/.local/bin` and `~/.local/lib` are what the shim
// needs, so `~/.local/bin` is bound and the caller adds `~/.local/lib` — or
// ("", why) to refuse. A root that is a known toolchain directory passes
// unchanged; any other child of $HOME is refused, because the jail cannot
// know what else is in it.
func narrowHomeRoot(real, root string) (narrowed, refused string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || filepath.Dir(root) != filepath.Clean(home) {
		return "", ""
	}
	base := filepath.Base(root)
	if homeToolchainDirs[base] {
		return "", ""
	}
	switch base {
	case ".local":
		// pip --user / pipx: ~/.local/bin + ~/.local/lib is the toolchain;
		// ~/.local/share is where keyrings and every desktop app's data live.
		rel, _ := filepath.Rel(root, real)
		if strings.HasPrefix(rel, "bin/") {
			return filepath.Join(root, "bin"), ""
		}
		if strings.HasPrefix(rel, "share/gem/") || strings.HasPrefix(rel, "share/pipx/") {
			// The one share subtree that is a toolchain: bind it alone.
			parts := strings.SplitN(rel, "/", 3)
			return filepath.Join(root, parts[0], parts[1]), ""
		}
		return "", "~/.local/share holds keyrings and application data"
	case ".config":
		return "", "~/.config holds credentials (gh, gcloud, composer's auth.json)"
	}
	return "", "it is not a toolchain directory, and the jail cannot know what else is in it"
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
