// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDarwinBoundariesNeverBindAMacOSSystemTree: on macOS every binary path
// resolves through /private (t.TempDir, /tmp and /etc are symlinks into it),
// and the Linux boundaries know none of macOS's top-level trees. A toolchain
// under /private/tmp or /Library resolved to the first child of / — and the
// jail bound /private (master.passwd, every user's temp dir) or /Library
// (the system keychains) whole. The darwin boundaries stop the walk one level
// below each of them. Platform-neutral: it checks the darwin set by name.
func TestDarwinBoundariesNeverBindAMacOSSystemTree(t *testing.T) {
	bounds := toolchainBoundariesFor("darwin")
	for _, tc := range []struct{ bin, want string }{
		{"/private/tmp/rust/bin/cargo", "/private/tmp/rust"},
		{"/private/var/tools/bin/x", "/private/var/tools"},
		{"/private/var/folders/ab/cdef/T/x/bin/y", ""}, // per-user temp: see below
		{"/Library/Frameworks/Python.framework/Versions/3.12/bin/python3", "/Library/Frameworks"},
		{"/Users/Shared/tools/bin/x", "/Users/Shared"},
		{"/Applications/Foo.app/Contents/MacOS/foo", "/Applications/Foo.app"},
		{"/Volumes/ext/sdk/go/bin/go", "/Volumes/ext"},
		{"/opt/homebrew/bin/node", "/opt/homebrew"},
	} {
		got := toolchainRootIn(bounds, tc.bin)
		if tc.want != "" && got != tc.want {
			t.Errorf("darwin toolchainRoot(%q) = %q, want %q", tc.bin, got, tc.want)
		}
		if bounds.has(got) && got != "" {
			t.Errorf("darwin toolchainRoot(%q) = %q, a boundary the jail must never bind", tc.bin, got)
		}
	}
	for _, b := range []string{"/private", "/private/var", "/private/var/folders", "/private/tmp", "/private/etc",
		"/Users", "/Library", "/System", "/Applications", "/Volumes"} {
		if !bounds.has(b) {
			t.Errorf("%s is not a darwin boundary: a toolchain under it would bind all of it", b)
		}
	}
	// Linux is unchanged: none of the macOS trees is a Linux boundary.
	for _, b := range []string{"/private", "/Users", "/Library"} {
		if toolchainBoundariesFor("linux").has(b) {
			t.Errorf("%s leaked into the linux boundaries", b)
		}
	}
}

// TestDarwinPerUserTempIsABoundary: macOS's per-user temp dir is
// /private/var/folders/<xx>/<hash>/T. A toolchain inside it must bind its own
// directory, never the hash bucket or the operator's whole per-user tree
// (T, C and 0 together: every temp file and cache the operator has).
func TestDarwinPerUserTempIsABoundary(t *testing.T) {
	tmp := "/private/var/folders/ab/cdef/T"
	t.Setenv("TMPDIR", tmp)
	bounds := toolchainBoundariesFor("darwin")
	if got := toolchainRootIn(bounds, tmp+"/rust/bin/cargo"); got != tmp+"/rust" {
		t.Errorf("toolchainRoot under the per-user temp = %q, want %q", got, tmp+"/rust")
	}
	// The per-user tree's other children (C: caches, 0: per-user system
	// data, X: code-signing clones) are boundaries too: a toolchain in
	// one binds its own directory, never the whole cache.
	for _, sib := range []string{"C", "0", "X"} {
		d := "/private/var/folders/ab/cdef/" + sib
		if got := toolchainRootIn(bounds, d+"/x/bin/y"); got != d+"/x" {
			t.Errorf("toolchainRoot under %s = %q, want %q", d, got, d+"/x")
		}
	}
}

// TestDarwinBoundariesFoldCase: APFS is case-insensitive by default, so a
// PATH entry spelled /library/x/bin resolves to a real binary whose path is
// spelled that way — and SBPL matches it case-insensitively too. The darwin
// boundaries must catch a mis-cased spelling exactly as the real one: the
// root is /library/x, never /library (which the sandbox would read as
// /Library, keychains included). Linux stays case-sensitive.
func TestDarwinBoundariesFoldCase(t *testing.T) {
	t.Setenv("HOME", "/Users/op")
	t.Setenv("TMPDIR", "/private/var/folders/ab/cdef/T")
	darwin := toolchainBoundariesFor("darwin")
	for _, tc := range []struct{ bin, want string }{
		{"/library/x/bin/tool", "/library/x"},
		{"/LIBRARY/Frameworks/P/bin/python3", "/LIBRARY/Frameworks"},
		{"/users/shared/t/bin/x", "/users/shared"},
		{"/PRIVATE/tmp/rust/bin/cargo", "/PRIVATE/tmp/rust"},
		{"/users/OP/.asdf/installs/go/bin/go", "/users/OP/.asdf"},
		{"/Private/Var/Folders/ab/CDEF/c/x/bin/y", "/Private/Var/Folders/ab/CDEF/c/x"},
	} {
		if got := toolchainRootIn(darwin, tc.bin); got != tc.want {
			t.Errorf("darwin toolchainRoot(%q) = %q, want %q", tc.bin, got, tc.want)
		}
	}
	for _, b := range []string{"/library", "/USERS", "/users/op", "/Private/Var"} {
		if !darwin.has(b) {
			t.Errorf("darwin: %q is not recognized as a boundary", b)
		}
	}
	linux := toolchainBoundariesFor("linux")
	if linux.has("/HOME") || linux.has("/Usr") {
		t.Error("linux boundaries must stay case-sensitive")
	}
	if got := toolchainRootIn(linux, "/Home/op/x/bin/y"); got != "/Home" {
		t.Errorf("linux toolchainRoot(/Home/op/x/bin/y) = %q, want /Home (case-sensitive, unchanged)", got)
	}
}

// TestDarwinHomeNarrowingFoldsCase: on darwin a mis-cased spelling of $HOME
// (/users/op for /Users/op) is still the home, so ~/.config is refused and
// ~/.local narrows to bin/ whatever case the home was spelled in.
func TestDarwinHomeNarrowingFoldsCase(t *testing.T) {
	t.Setenv("HOME", "/Users/op")
	if _, refused := narrowHomeRootOn("darwin", "/users/op/.config/gh/bin/x", "/users/op/.config"); refused == "" {
		t.Error("darwin: /users/op/.config was not refused — a mis-cased home escaped narrowing")
	}
	if narrowed, _ := narrowHomeRootOn("darwin", "/users/op/.local/bin/pytest", "/users/op/.local"); narrowed != "/users/op/.local/bin" {
		t.Errorf("darwin: /users/op/.local narrowed to %q, want /users/op/.local/bin", narrowed)
	}
	if _, refused := narrowHomeRootOn("linux", "/users/op/.config/gh/bin/x", "/users/op/.config"); refused != "" {
		t.Error("linux: a differently-cased path is a different directory and is not the home")
	}
}

// TestMisCasedPathOnTheRealFilesystem: end to end on a case-insensitive
// volume (macOS's default APFS): a PATH entry that spells the cargo home's
// bin/ in another case must still bind bin/ alone, never the cargo home with
// its registry token, and never the home.
func TestMisCasedPathOnTheRealFilesystem(t *testing.T) {
	home := resolvedTempDir(t)
	layout(t, home, map[string]string{
		".cargo/bin/cargo-nextest": "#!/bin/sh\n",
		".cargo/config.toml":       "[registries.priv]\ntoken = \"secret\"\n",
		".config/gh/hosts.yml":     "token\n",
	})
	upper := strings.ToUpper(home)
	if _, err := os.Stat(filepath.Join(upper, ".CARGO", "BIN", "CARGO-NEXTEST")); err != nil {
		t.Skipf("the filesystem under %s is case-sensitive; this test needs a case-insensitive volume (macOS APFS default)", home)
	}
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(upper, ".CARGO", "BIN")+":/usr/bin:/bin")
	binds, err := toolchainBindsFor("cargo-nextest")
	if err != nil {
		t.Fatalf("cargo-nextest: %v", err)
	}
	for _, b := range binds {
		lb := strings.ToLower(b.Host)
		if lb != strings.ToLower(filepath.Join(home, ".cargo", "bin")) {
			t.Errorf("bind %s: a mis-cased PATH widened the bind beyond the cargo home's bin/ (binds %+v)", b.Host, binds)
		}
	}
	if len(binds) != 1 {
		t.Errorf("binds = %+v, want exactly the cargo home's bin/", binds)
	}
}

// TestSystemBinariesNeedNoBindOnDarwin: on macOS /bin and /sbin are real
// directories the jail already reads (sbpl.go layer 2), so `sh` resolving
// to /bin/sh must add no bind — binding /bin widened the jail for nothing.
// On Linux /bin is usrmerge's symlink into /usr, which already resolves to
// /usr, so the rule there is unchanged.
//
// /opt/homebrew is read whole by the same layer, which then DENIES its var/
// (service databases) and etc/ (service config). A Homebrew binary must
// therefore add no bind either: the bind would be /opt/homebrew itself, an
// allow after those denies that re-opens them, which the profile builder
// refuses (sbplCheckReopen) — so binding it would make every Homebrew
// toolchain unrunnable, not merely widen the jail.
func TestSystemBinariesNeedNoBindOnDarwin(t *testing.T) {
	for _, tc := range []struct {
		goos, real string
		want       bool
	}{
		{"darwin", "/bin/sh", true},
		{"darwin", "/sbin/ping", true},
		{"darwin", "/usr/bin/git", true},
		{"darwin", "/binx/tool", false},
		{"darwin", "/opt/homebrew/bin/node", true},
		{"darwin", "/opt/homebrew/Cellar/node/22.1.0/bin/node", true},
		{"darwin", "/OPT/Homebrew/bin/node", true},
		{"darwin", "/opt/homebrewx/bin/node", false},
		{"darwin", "/opt/local/bin/port", false},
		{"linux", "/opt/homebrew/bin/node", false},
		{"linux", "/usr/bin/go", true},
		{"linux", "/bin/sh", false},
		{"linux", "/sbin/x", false},
	} {
		if got := jailReadsSystemPath(tc.goos, tc.real); got != tc.want {
			t.Errorf("jailReadsSystemPath(%s, %s) = %v, want %v", tc.goos, tc.real, got, tc.want)
		}
	}
}

// TestSymlinkedHomeIsStillABoundary: a binary path is compared with its
// symlinks resolved, so $HOME must be too. With HOME=/home/op a symlink to
// /data/op, a pip --user pytest resolves to /data/op/.local/bin/pytest; the
// unresolved home never matched, the walk ran past it, and the jail bound
// /data — the operator's whole home, ~/.ssh included. This is the macOS
// default shape for any temp HOME (/var -> /private/var) and a real Linux
// one for a home moved to another disk.
func TestSymlinkedHomeIsStillABoundary(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "data", "op")
	layout(t, data, map[string]string{
		".local/bin/pytest":               "#!/bin/sh\n",
		"src/repo/node_modules/.bin/jest": "#!/bin/sh\n",
		".ssh/id_ed25519":                 "secret\n",
	})
	link := filepath.Join(base, "home-op")
	if err := os.Symlink(data, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	t.Setenv("HOME", link)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(link, ".local", "bin")+":"+filepath.Join(link, "src", "repo", "node_modules", ".bin")+":/usr/bin:/bin")

	binds, err := toolchainBindsFor("pytest")
	if err != nil {
		t.Fatalf("pytest: %v", err)
	}
	if b, ok := coveringBind(binds, filepath.Join(data, ".ssh", "id_ed25519")); ok {
		t.Fatalf("pytest: bind %s exposes the operator's ~/.ssh", b.Host)
	}
	if len(binds) != 1 || binds[0].Host != filepath.Join(data, ".local", "bin") {
		t.Errorf("pytest binds = %+v, want exactly ~/.local/bin (resolved)", binds)
	}

	binds, err = toolchainBindsFor("jest")
	if err == nil {
		t.Errorf("jest under ~/src must be refused, got binds %+v", binds)
	}
	if b, ok := coveringBind(binds, filepath.Join(data, ".ssh", "id_ed25519")); ok {
		t.Errorf("jest: bind %s exposes the operator's ~/.ssh", b.Host)
	}
}
