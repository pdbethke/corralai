// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
	{`(deny file-read* (literal "` + sbplMasterPasswd + `"))`, "the password hashes: bwrap never binds /etc/shadow either"},
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

// The fixed secret stores the profile denies: master.passwd in layer 2, the
// other two in layer 4. Named once, because sbplCheckReopen must refuse any
// later allow that would re-expose them.
const (
	sbplMasterPasswd = "/private/etc/master.passwd"
	sbplKeychains    = "/Library/Keychains"
	sbplDSLocal      = "/private/var/db/dslocal"
)

// Layer 4 (fixed part): operator secret stores outside $HOME.
var sbplOperatorSecrets = []sbplRule{
	{`(deny file-read* (subpath "` + sbplKeychains + `"))`, "system keychains"},
	{`(deny file-read* (subpath "` + sbplDSLocal + `"))`, "the local directory-service database, which holds password hashes"},
}

// Layer 8: granted only with Options.Network (the fetch step).
var sbplNetwork = []sbplRule{
	{"(allow network*)", "the dependency-fetch step only (Options.Network); graded runs stay offline"},
	{`(allow mach-lookup (global-name "com.apple.trustd.agent"))`, "TLS certificate verification; without it x509 fails with OSStatus -26276 (measured 2026-10-13)"},
}

// sbplQuoter escapes the only two characters that are special inside an SBPL
// string literal. Control characters never reach it: sbplCheckPath rejects
// them first, so this is the whole of SBPL string escaping, not a subset.
var sbplQuoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// sbplQuote renders s as one SBPL string literal.
func sbplQuote(s string) string {
	return `"` + sbplQuoter.Replace(s) + `"`
}

// sbplCheckPath refuses a path the sandbox could not match. A deny on a path
// that matches nothing reads as protection and protects nothing, so every
// path is validated before any rule is emitted: absolute, valid UTF-8, and
// free of control characters.
func sbplCheckPath(field, p string) error {
	if p == "" {
		return fmt.Errorf("sbpl: %s is empty", field)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("sbpl: %s path %q is not absolute", field, p)
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("sbpl: %s path %q is not valid UTF-8", field, p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("sbpl: %s path %q contains control character U+%04X", field, p, r)
		}
	}
	return nil
}

// sbplValidate checks every path in in. OperatorHome may be empty (unknown);
// every other path is required.
func sbplValidate(in sbplInput) error {
	if err := sbplCheckPath("Workspace", in.Workspace); err != nil {
		return err
	}
	if in.OperatorHome != "" {
		if err := sbplCheckPath("OperatorHome", in.OperatorHome); err != nil {
			return err
		}
	}
	for _, p := range in.ReadOnly {
		if err := sbplCheckPath("ReadOnly", p); err != nil {
			return err
		}
	}
	for _, m := range in.Masks {
		if err := sbplCheckPath("Mask", m.Path); err != nil {
			return err
		}
	}
	if err := sbplCheckReopen("Workspace", in.Workspace, in.OperatorHome); err != nil {
		return err
	}
	for _, p := range in.ReadOnly {
		if err := sbplCheckReopen("ReadOnly", p, in.OperatorHome); err != nil {
			return err
		}
	}
	return nil
}

// sbplCheckReopen refuses an allow (a ReadOnly path, layer 5, or the
// workspace, layer 6) that would undo a deny above it. SBPL applies the LAST
// matching rule, so an allow on a path that EQUALS or CONTAINS a denied one
// re-allows it: ReadOnly "/private" would hand the jail master.passwd,
// "/Library" the system keychains, "/Users" the operator's home. A toolchain
// resolver can produce such a path (it once bound "/private" for any
// toolchain under a macOS temp dir); this makes that a refusal, never a
// silently open jail.
//
// An allow INSIDE the operator's home is the reason layer 5 follows layer 4
// (~/.cargo/bin, ~/sdk/go) and is accepted. One inside a fixed secret store
// (a single keychain file) is refused: nothing a jail runs lives there.
func sbplCheckReopen(field, p, operatorHome string) error {
	type denied struct {
		path, what string
		inside     bool // refuse descendants too
	}
	ds := []denied{
		{sbplMasterPasswd, "the password hashes", true},
		{sbplKeychains, "the system keychains", true},
		{sbplDSLocal, "the directory-service password database", true},
	}
	if operatorHome != "" {
		ds = append(ds, denied{filepath.Clean(operatorHome), "the operator's home", false})
	}
	c := filepath.Clean(p)
	var reopened []string
	for _, d := range ds {
		if c == "/" || c == d.path || strings.HasPrefix(d.path, c+"/") || (d.inside && strings.HasPrefix(c, d.path+"/")) {
			reopened = append(reopened, d.path+" ("+d.what+")")
		}
	}
	if len(reopened) > 0 {
		return fmt.Errorf("sbpl: %s path %q would re-allow %s: its allow comes after those denies, and SBPL applies the last matching rule",
			field, p, strings.Join(reopened, ", "))
	}
	return nil
}

// sbplProfile assembles the profile in its eight layers. ORDER IS THE
// SECURITY PROPERTY: SBPL applies the LAST matching rule, so
//   - the home deny (4) precedes the toolchain allows (5), or every toolchain
//     under $HOME (~/sdk/go, ~/.cargo, ~/.pyenv) is invisible;
//   - the workspace allow (6) follows the home deny, because a workspace can
//     live under $HOME;
//   - the credential masks (7) follow every file allow, so nothing above can
//     re-expose them.
//
// It refuses (returns an error and no profile) any input with a path SBPL
// could not match, so a mask can never silently fail to apply, and any allow
// that would re-open a deny above it (sbplCheckReopen).
func sbplProfile(in sbplInput) (string, error) {
	if err := sbplValidate(in); err != nil {
		return "", err
	}
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
		put(sbplRule{fmt.Sprintf("(deny file-read* (subpath %s))", sbplQuote(in.OperatorHome)), "the operator's home: credentials, keys, other projects"})
	}
	put(sbplOperatorSecrets...)
	for _, p := range in.ReadOnly { // 5
		put(sbplRule{fmt.Sprintf("(allow file-read* (subpath %s))", sbplQuote(p)), "a toolchain or dependency dir (Options.ReadOnlyBinds), after the home deny so one under $HOME stays visible"})
	}
	put(sbplRule{fmt.Sprintf("(allow file-read* file-write* (subpath %s))", sbplQuote(in.Workspace)), "the workspace: the only writable tree, holding the per-run HOME and TMPDIR"}) // 6
	for _, m := range in.Masks {                                                                                                                                                     // 7
		kind := "literal"
		if m.Dir {
			kind = "subpath"
		}
		put(sbplRule{fmt.Sprintf("(deny file-read* (%s %s))", kind, sbplQuote(m.Path)), "a credential mask from secrets.go, last so no allow above re-exposes it"})
	}
	if in.Network { // 8
		put(sbplNetwork...)
	}
	return b.String(), nil
}
