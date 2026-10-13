// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
func toolchainBoundaries() boundarySet { return toolchainBoundariesFor(runtime.GOOS) }

// boundarySet is a set of boundary directories, compared as the platform's
// filesystem compares paths: case-insensitively on darwin (APFS's default,
// and how the macOS sandbox matches paths), so a binary reached through a
// PATH entry spelled /library/x/bin stops at the /Library boundary instead
// of binding "/library" — which the sandbox would read as /Library,
// keychains included. Exact on Linux.
type boundarySet struct {
	m    map[string]bool
	fold bool
}

func (b boundarySet) key(p string) string {
	if b.fold {
		return strings.ToLower(p)
	}
	return p
}

func (b boundarySet) has(p string) bool { return b.m[b.key(p)] }

func (b boundarySet) add(p string) { b.m[b.key(p)] = true }

// foldsCase reports whether paths on goos compare case-insensitively.
func foldsCase(goos string) bool { return goos == "darwin" }

// samePathOn reports whether a and b name the same path on goos.
func samePathOn(goos, a, b string) bool {
	if foldsCase(goos) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// darwinToolchainBoundaries are macOS's counterparts of the Linux set. A
// binary path is compared with its symlinks resolved, and on macOS /tmp, /var
// and /etc are symlinks into /private, so without these the walk from any
// toolchain under /private/tmp or a temp dir reached the first child of / and
// bound /private whole (master.passwd, every user's temp dir); one under
// /Library bound /Library (the system keychains); one under /Users/Shared
// bound every user's home.
var darwinToolchainBoundaries = []string{
	"/private", "/private/var", "/private/var/folders", "/private/tmp", "/private/etc",
	"/Users", "/Library", "/System", "/Applications", "/Volumes",
}

// toolchainBoundariesFor is toolchainBoundaries for the named GOOS, so the
// darwin set is testable on Linux. $HOME is a boundary in BOTH spellings —
// as given and with its symlinks resolved — because the binary path it is
// compared with is resolved: a symlinked home (/home/op -> /data/op, or any
// macOS temp home under /var) otherwise never matches, and the walk binds
// the directory ABOVE the operator's home.
func toolchainBoundariesFor(goos string) boundarySet {
	b := boundarySet{m: map[string]bool{}, fold: foldsCase(goos)}
	for _, d := range []string{
		"/", "/home", "/usr", "/opt", "/snap", "/var", "/etc", "/srv", "/mnt", "/media",
		"/root", "/tmp", "/local",
	} {
		b.add(d)
	}
	if goos == "darwin" {
		for _, d := range darwinToolchainBoundaries {
			b.add(d)
		}
		// The per-user temp dir, /private/var/folders/<xx>/<hash>/T: a
		// toolchain in it binds its own directory, never the hash bucket or
		// the operator's whole per-user tree. Its siblings are boundaries
		// too — C (caches), 0 (per-user system data), X (code-signing
		// clones) — so a toolchain in the cache binds its own directory, not
		// every cache the operator has. The per-user tree is a boundary only
		// when TMPDIR is really under /private/var/folders, so an odd TMPDIR
		// never makes, say, /Users/op/tmp's parent one.
		tmp := resolvedPath(os.TempDir())
		b.add(tmp)
		if user := filepath.Dir(tmp); strings.HasPrefix(b.key(user), "/private/var/folders/") {
			b.add(user)
			b.add(filepath.Dir(user))
			for _, sib := range []string{"T", "C", "0", "X"} {
				b.add(filepath.Join(user, sib))
			}
		}
	}
	for _, h := range operatorHomes() {
		b.add(h)
	}
	return b
}

// operatorHomes is $HOME as given and with its symlinks resolved (one entry
// when they agree), or none when there is no home.
func operatorHomes() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	h := filepath.Clean(home)
	if r := resolvedPath(h); r != h {
		return []string{h, r}
	}
	return []string{h}
}

// resolvedPath is p with its symlinks resolved, or p cleaned when it cannot
// be (sandbox.resolved, which this package cannot import).
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
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

// toolchainBindsFor resolves cmd0 on the HOST and returns the read-only binds
// the jail needs in order to run it, or none when the binary already lives
// under a path the jail mounts anyway (/usr, which covers apt and /usr/local).
//
// A command that cannot be resolved at all returns no binds and no error:
// the jail will fail on its own with the runner's real message, which is a
// better diagnosis than a guess from here. That is deliberate — this function
// exists to REMOVE a misleading failure, not to add one.
//
// No bind ever covers a toolchain root's secrets, as sandbox.SecretEntries
// defines them (the same rule the isolators mask whole-mounted trees with).
// Every jail reads these binds — the networked vendoring step, which runs
// repository-controlled programs, and the graded runs, whose output reaches
// the ledger, which can be public. So:
//
//   - a cargo home (sandbox.IsCargoHome: exactly the operator's CARGO_HOME,
//     else ~/.cargo, symlinks resolved) is ALWAYS bound as its bin/ alone,
//     plus the rustup home when bin/ holds a rustup proxy, whichever of its
//     binaries was named (a symlinked or hard-linked proxy, cargo-nextest,
//     anything `cargo install`ed);
//   - any other root holding a credentials store (RubyGems' ~/.gem) is
//     narrowed to the one child directory the binary lives in;
//   - a root that cannot be narrowed safely is refused, with the reason.
func toolchainBindsFor(cmd0 string) ([]sandbox.Bind, error) {
	cmd0 = strings.TrimSpace(cmd0)
	if cmd0 == "" {
		return nil, nil
	}
	p, err := exec.LookPath(cmd0)
	if err != nil {
		return nil, nil
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
		return nil, ErrSnapToolchain{Command: cmd0, Path: p}
	}

	// Already inside what the jail mounts.
	if jailReadsSystemPath(runtime.GOOS, real) {
		return nil, nil
	}

	root := toolchainRoot(real)
	if root == "" {
		return nil, nil
	}
	if narrowed, refused := narrowHomeRoot(real, root); refused != "" {
		return nil, ErrUnbindableToolchain{Command: cmd0, Path: real, Root: root, Why: refused}
	} else if narrowed != "" {
		root = narrowed
	}
	ch := sandbox.CargoHomeOf(real)
	if ch != "" && ch != root && !strings.HasPrefix(ch, root+string(filepath.Separator)) {
		ch = "" // a cargo home ABOVE the toolchain root is not what this bind would expose
	}
	switch {
	case ch != "":
		// A cargo home is narrowed by what it IS, whatever binary was named
		// and whether or not a credentials file exists yet: beside bin/ it
		// keeps registry tokens in config.toml, private checkouts in git/,
		// binstall.toml — names no list finishes (sandbox.IsCargoHome).
		root = ch
		bin := filepath.Join(root, "bin")
		if got := childOnPath(root, real); !samePathOn(runtime.GOOS, got, bin) {
			return nil, ErrUnbindableToolchain{Command: cmd0, Path: real, Root: root,
				Why: "it is a cargo home, and only its bin/ may be bound; the runner is not in bin/"}
		}
		bin = childOnPath(root, real) // the spelling the binary was reached by
		binds := []sandbox.Bind{{Host: bin, Target: bin}}
		if _, err := os.Lstat(filepath.Join(bin, "rustup")); err == nil { // #nosec G703 -- existence check only
			rh, rerr := rustupHome(cmd0)
			if rerr != nil {
				return nil, rerr
			}
			if rh != "" {
				binds = append(binds, sandbox.Bind{Host: rh, Target: rh})
			}
		}
		return binds, nil
	case sandbox.CredentialStoreIn(root) != "":
		store := sandbox.CredentialStoreIn(root)
		child := childOnPath(root, real)
		if child == "" || sandbox.CredentialStoreIn(child) != "" {
			return nil, ErrUnbindableToolchain{Command: cmd0, Path: real, Root: root,
				Why: "it holds a credentials store (" + store + ") and the toolchain cannot be bound without it"}
		}
		root = child
	}
	// Bound at the SAME absolute path, so the operator's own PATH keeps
	// working inside the jail without rewriting the command.
	return []sandbox.Bind{{Host: root, Target: root}}, nil
}

// jailReadsSystemPath reports whether the resolved binary real lies under a
// system tree every jail on goos already reads, so binding it would widen the
// sandbox for nothing: /usr everywhere (bwrap binds it; the macOS profile
// reads it), and on macOS also /bin and /sbin, which are real directories
// there (on Linux they are usrmerge symlinks that already resolved to /usr)
// and which the macOS profile reads in its system layer (sandbox/sbpl.go).
//
// On macOS that layer also reads /opt/homebrew whole and then denies its
// var/ and etc/ (service data and config). A Homebrew binary's toolchain
// root is /opt/homebrew itself, and binding it would be an allow AFTER those
// denies, re-opening them — the profile builder refuses exactly that
// (sbplCheckReopen). So it is read in place, never bound. Folded as the
// volume folds: /OPT/homebrew is the same tree there.
func jailReadsSystemPath(goos, real string) bool {
	if strings.HasPrefix(real, "/usr/") {
		return true
	}
	if goos != "darwin" {
		return false
	}
	k := strings.ToLower(real)
	return strings.HasPrefix(k, "/bin/") || strings.HasPrefix(k, "/sbin/") || strings.HasPrefix(k, "/opt/homebrew/")
}

// childOnPath is the child of root that real lives under (root/bin for
// root/bin/cargo), or "" when real sits directly in root.
func childOnPath(root, real string) string {
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	first, rest, _ := strings.Cut(rel, string(filepath.Separator))
	if rest == "" {
		return ""
	}
	return filepath.Join(root, first)
}

// rustupHome is the rustup home a rustup proxy needs beside its bin/ (the
// toolchains it dispatches to live in RUSTUP_HOME, default ~/.rustup), or ""
// when there is none to bind: no such directory, or one under /usr, which the
// jail mounts anyway. It is a bind root like any other, so it goes through
// the same home-directory rule (narrowHomeRoot): RUSTUP_HOME=~/.config is
// refused, never bound, because ~/.config holds gh, gcloud and composer auth.
// A RUSTUP_HOME that is a cargo home (sandbox.IsCargoHome) is refused too:
// binding it whole would expose everything the cargo-home rule hides.
// Inside the jail the proxy finds it only through RUSTUP_HOME, which the
// jail does not set yet; the bind is what setting RUSTUP_HOME in the jail
// will point at.
func rustupHome(cmd0 string) (string, error) {
	rh := os.Getenv("RUSTUP_HOME")
	if rh == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", nil
		}
		rh = filepath.Join(home, ".rustup")
	}
	rh = filepath.Clean(rh)
	fi, err := os.Stat(rh) // #nosec G703 -- stat only; rh is the operator's RUSTUP_HOME or ~/.rustup
	if err != nil || !fi.IsDir() || strings.HasPrefix(rh, "/usr/") {
		return "", nil
	}
	refuse := func(why string) (string, error) {
		return "", ErrUnbindableToolchain{Command: cmd0, Path: rh, Root: rh, Why: "it is the rustup home, and " + why}
	}
	probe := filepath.Join(rh, "toolchains")
	root := toolchainRoot(probe)
	if root == "" {
		return refuse("it is, or sits directly in, a directory the jail must never bind")
	}
	if _, refused := narrowHomeRoot(probe, root); refused != "" {
		return refuse(refused)
	}
	if sandbox.IsCargoHome(rh) {
		return refuse("it is a cargo home, of which only bin/ may be bound")
	}
	if len(sandbox.SecretEntries(rh)) > 0 {
		return refuse("it holds a credentials store")
	}
	return rh, nil
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
	return narrowHomeRootOn(runtime.GOOS, real, root)
}

func narrowHomeRootOn(goos, real, root string) (narrowed, refused string) {
	if !slices.ContainsFunc(operatorHomes(), func(h string) bool { return samePathOn(goos, h, filepath.Dir(root)) }) {
		return "", ""
	}
	base := filepath.Base(root)
	if foldsCase(goos) {
		base = strings.ToLower(base) // ~/.Local is ~/.local on a case-insensitive volume
	}
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
		if strings.HasPrefix(rel, "share/gem/") || strings.HasPrefix(rel, "share/pipx/") || strings.HasPrefix(rel, "share/rustup/") {
			// The one share subtree that is a toolchain: bind it alone.
			parts := strings.SplitN(rel, "/", 3)
			return filepath.Join(root, parts[0], parts[1]), ""
		}
		return "", "~/.local/share holds keyrings and application data"
	case ".config":
		return "", "~/.config holds credentials (gh, gcloud, composer's auth.json)"
	case "hostedtoolcache":
		// GitHub Actions' setup-* tool cache, which macOS runners keep in
		// $HOME (Linux runners: /opt): toolchain versions only. Bound as the
		// one tool's directory (~/hostedtoolcache/go), never the whole cache.
		rel, _ := filepath.Rel(root, real)
		if tool, rest, ok := strings.Cut(rel, "/"); ok && rest != "" && tool != ".." {
			return filepath.Join(root, tool), ""
		}
		return "", "only a tool's own directory under ~/hostedtoolcache is a toolchain"
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
func toolchainRoot(real string) string { return toolchainRootIn(toolchainBoundaries(), real) }

func toolchainRootIn(bounds boundarySet, real string) string {
	dir := filepath.Dir(filepath.Clean(real))
	for dir != "" && dir != "/" {
		parent := filepath.Dir(dir)
		if bounds.has(parent) {
			if bounds.has(dir) {
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
