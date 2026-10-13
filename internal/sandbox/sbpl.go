// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"fmt"
	"strings"
)

// sbplInput is everything the macOS jail's profile depends on. Every path is
// ALREADY RESOLVED (see resolved): SBPL matches canonical paths, so a
// workspace from os.MkdirTemp("") must arrive as /private/var/folders/...,
// never /var/folders/... — the unresolved form matches nothing and the jail
// cannot touch its own workspace (measured on macOS 27.0.1, 2026-10-13).
//
// The builder is platform-neutral on purpose: its ordering is the security
// property, and these tests run on Linux CI as well as on a Mac.
type sbplInput struct {
	Workspace    string   // the command's workspace; holds the per-run HOME and TMPDIR
	OperatorHome string   // the operator's real home, denied; "" when unknown
	ReadOnly     []string // Options.ReadOnlyBinds hosts: toolchains and dependency dirs
	Masks        []mask   // secretMasks(wholeBoundTrees), denied last
	Network      bool     // Options.Network: the dependency-fetch step only
}

// sbplRule is one profile line and the reason it exists.
type sbplRule struct{ Rule, Why string }

// Layer 1: the base. Deny everything, then grant what every process needs.
var sbplBase = []sbplRule{
	{"(deny default)", "deny by default, like bwrap: nothing below is granted implicitly"},
	{"(allow process*)", "fork and exec: a test runner is a process tree"},
	{"(allow sysctl-read)", "toolchains read CPU count, memory size, OS version; sysctl writes stay denied"},
	{"(allow signal (target same-sandbox))", "a runner may stop its own children, never a host process"},
	{"(allow file-read-metadata)", "getcwd and symlink traversal stat every ancestor; contents stay denied (a named gap: existence and size leak)"},
}

// Layer 2: the system paths a toolchain reads. Each is the macOS counterpart
// of something bwrap binds read-only.
var sbplSystemReads = []sbplRule{
	{`(allow file-read* (literal "/"))`, "the root itself: getcwd and path resolution open it"},
	{`(allow file-read* (subpath "/usr"))`, "the OS toolchain and libraries, as bwrap binds /usr"},
	{`(allow file-read* (subpath "/bin"))`, "sh, and the base utilities"},
	{`(allow file-read* (subpath "/sbin"))`, "base system utilities some build scripts call"},
	{`(allow file-read* (subpath "/System"))`, "frameworks and the dyld shared cache every binary maps at exec"},
	{`(allow file-read* (subpath "/private/etc"))`, "hosts, passwd, group, ssl: the system databases bwrap binds from /etc"},
	{`(deny file-read* (literal "/private/etc/master.passwd"))`, "the password hashes: bwrap never binds /etc/shadow either"},
	{`(allow file-read* (subpath "/private/var/select"))`, "/bin/sh resolves which shell to run through /private/var/select/sh"},
	{`(allow file-read* (subpath "/private/var/db/timezone"))`, "the timezone database libc reads at startup"},
	{`(allow file-read* (subpath "/dev"))`, "device nodes: /dev/null, /dev/urandom, /dev/tty"},
	{`(allow file-read* (subpath "/Library/Developer"))`, "the Xcode command line tools: clang, git, the SDK headers cgo needs"},
	{`(allow file-read* (subpath "/opt/homebrew"))`, "Apple-silicon Homebrew toolchains, masked below like /usr"},
	{`(allow file-write* (literal "/dev/null") (literal "/dev/tty"))`, "discarding output and writing to the terminal"},
	{`(allow file-ioctl (literal "/dev/null") (literal "/dev/tty"))`, "isatty checks the shell and test runners make"},
}

// Layer 3: the only system services libSystem needs at startup.
var sbplServices = []sbplRule{
	{`(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo"))`, "user and group lookups (getpwuid), the macOS counterpart of binding /etc/passwd"},
	{`(allow mach-lookup (global-name "com.apple.system.notification_center"))`, "libSystem initialization registers with notifyd"},
	{`(allow mach-lookup (global-name "com.apple.logd"))`, "os_log, which libSystem calls whether or not the program logs"},
	{`(allow ipc-posix-shm-read* (ipc-posix-name "apple.shm.notification_center"))`, "notifyd's shared page, read only"},
}

// Layer 4 (fixed part): operator secret stores outside $HOME.
var sbplOperatorSecrets = []sbplRule{
	{`(deny file-read* (subpath "/Library/Keychains"))`, "system keychains"},
	{`(deny file-read* (subpath "/private/var/db/dslocal"))`, "the local directory-service database, which holds password hashes"},
}

// Layer 8: granted only with Options.Network (the fetch step).
var sbplNetwork = []sbplRule{
	{"(allow network*)", "the dependency-fetch step only (Options.Network); graded runs stay offline"},
	{`(allow mach-lookup (global-name "com.apple.trustd.agent"))`, "TLS certificate verification; without it x509 fails with OSStatus -26276 (measured 2026-10-13)"},
}

// sbplProfile assembles the profile in its eight layers. ORDER IS THE
// SECURITY PROPERTY: SBPL applies the LAST matching rule, so
//   - the home deny (4) precedes the toolchain allows (5), or every toolchain
//     under $HOME (~/sdk/go, ~/.cargo, ~/.pyenv) is invisible;
//   - the workspace allow (6) follows the home deny, because a workspace can
//     live under $HOME;
//   - the credential masks (7) follow every file allow, so nothing above can
//     re-expose them.
func sbplProfile(in sbplInput) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	put := func(rules ...sbplRule) {
		for _, r := range rules {
			fmt.Fprintf(&b, "%s ; %s\n", r.Rule, r.Why)
		}
	}
	put(sbplBase...)           // 1
	put(sbplSystemReads...)    // 2
	put(sbplServices...)       // 3
	if in.OperatorHome != "" { // 4
		put(sbplRule{fmt.Sprintf("(deny file-read* (subpath %q))", in.OperatorHome), "the operator's home: credentials, keys, other projects"})
	}
	put(sbplOperatorSecrets...)
	for _, p := range in.ReadOnly { // 5
		put(sbplRule{fmt.Sprintf("(allow file-read* (subpath %q))", p), "a toolchain or dependency dir (Options.ReadOnlyBinds), after the home deny so one under $HOME stays visible"})
	}
	put(sbplRule{fmt.Sprintf("(allow file-read* file-write* (subpath %q))", in.Workspace), "the workspace: the only writable tree, holding the per-run HOME and TMPDIR"}) // 6
	for _, m := range in.Masks {                                                                                                                                          // 7
		kind := "literal"
		if m.Dir {
			kind = "subpath"
		}
		put(sbplRule{fmt.Sprintf("(deny file-read* (%s %q))", kind, m.Path), "a credential mask from secrets.go, last so no allow above re-exposes it"})
	}
	if in.Network { // 8
		put(sbplNetwork...)
	}
	return b.String()
}
