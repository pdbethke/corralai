// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
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
		if bounds[got] && got != "" {
			t.Errorf("darwin toolchainRoot(%q) = %q, a boundary the jail must never bind", tc.bin, got)
		}
	}
	for _, b := range []string{"/private", "/private/var", "/private/var/folders", "/private/tmp", "/private/etc",
		"/Users", "/Library", "/System", "/Applications", "/Volumes"} {
		if !bounds[b] {
			t.Errorf("%s is not a darwin boundary: a toolchain under it would bind all of it", b)
		}
	}
	// Linux is unchanged: none of the macOS trees is a Linux boundary.
	for _, b := range []string{"/private", "/Users", "/Library"} {
		if toolchainBoundariesFor("linux")[b] {
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
	if got := toolchainRootIn(bounds, "/private/var/folders/ab/cdef/C/x/bin/y"); got == "/private/var/folders/ab" || got == "/private/var/folders/ab/cdef" {
		t.Errorf("bound %q: the hash bucket or the operator's whole per-user tree", got)
	}
}

// TestSystemBinariesNeedNoBindOnDarwin: on macOS /bin and /sbin are real
// directories the jail already reads (sbpl.go layer 2), so `sh` resolving
// to /bin/sh must add no bind — binding /bin widened the jail for nothing.
// On Linux /bin is usrmerge's symlink into /usr, which already resolves to
// /usr, so the rule there is unchanged.
func TestSystemBinariesNeedNoBindOnDarwin(t *testing.T) {
	for _, tc := range []struct {
		goos, real string
		want       bool
	}{
		{"darwin", "/bin/sh", true},
		{"darwin", "/sbin/ping", true},
		{"darwin", "/usr/bin/git", true},
		{"darwin", "/binx/tool", false},
		{"darwin", "/opt/homebrew/bin/node", false},
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
