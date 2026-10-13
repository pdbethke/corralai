// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
)

func sampleSBPL(network bool) sbplInput {
	return sbplInput{
		Workspace:    "/private/var/folders/x/T/corral-adequacy-1",
		OperatorHome: "/Users/op",
		ReadOnly:     []string{"/Users/op/sdk/go", "/Users/op/.cargo/bin"},
		Masks:        []mask{{Path: "/opt/homebrew/cargo/credentials.toml"}, {Path: "/opt/homebrew/cargo/registry", Dir: true}},
		Network:      network,
	}
}

// mustProfile builds a profile from an input that must be valid.
func mustProfile(t *testing.T, in sbplInput) string {
	t.Helper()
	p, err := sbplProfile(in)
	if err != nil {
		t.Fatalf("sbplProfile: unexpected error: %v", err)
	}
	return p
}

// lineIndex returns the index of the first profile line containing needle, or -1.
func lineIndex(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i
		}
	}
	return -1
}

// TestSBPLLayersAreOrdered: in SBPL the LAST matching rule wins, so order is
// the security property. The home deny must precede every toolchain allow
// (or ~/sdk/go is invisible), the workspace allow must follow the home deny
// (workspaces can live under $HOME), and the credential masks come last of
// all file rules so nothing re-exposes them.
func TestSBPLLayersAreOrdered(t *testing.T) {
	lines := strings.Split(mustProfile(t, sampleSBPL(true)), "\n")
	homeDeny := lineIndex(lines, `(deny file-read* (subpath "/Users/op"))`)
	goAllow := lineIndex(lines, `(allow file-read* (subpath "/Users/op/sdk/go"))`)
	cargoAllow := lineIndex(lines, `(allow file-read* (subpath "/Users/op/.cargo/bin"))`)
	ws := lineIndex(lines, `(allow file-read* file-write* (subpath "/private/var/folders/x/T/corral-adequacy-1"))`)
	maskFile := lineIndex(lines, `(deny file-read* (literal "/opt/homebrew/cargo/credentials.toml"))`)
	maskDir := lineIndex(lines, `(deny file-read* (subpath "/opt/homebrew/cargo/registry"))`)
	etc := lineIndex(lines, `(allow file-read* (subpath "/private/etc"))`)
	passwd := lineIndex(lines, `(deny file-read* (literal "/private/etc/master.passwd"))`)
	for name, i := range map[string]int{"homeDeny": homeDeny, "goAllow": goAllow, "cargoAllow": cargoAllow, "ws": ws, "maskFile": maskFile, "maskDir": maskDir, "etc": etc, "passwd": passwd} {
		if i < 0 {
			t.Fatalf("%s rule missing from profile:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	if !(homeDeny < goAllow && homeDeny < cargoAllow) {
		t.Errorf("home deny (%d) must precede toolchain allows (%d, %d)", homeDeny, goAllow, cargoAllow)
	}
	if !(homeDeny < ws) {
		t.Errorf("home deny (%d) must precede the workspace allow (%d)", homeDeny, ws)
	}
	if !(ws < maskFile && ws < maskDir && goAllow < maskFile && cargoAllow < maskDir) {
		t.Errorf("masks (%d, %d) must follow every file allow", maskFile, maskDir)
	}
	if !(etc < passwd) {
		t.Errorf("master.passwd deny (%d) must follow the /private/etc allow (%d)", passwd, etc)
	}
}

// TestSBPLSecretStoresPrecedeAllows: the keychain and directory-service denies
// must come before every ReadOnly allow and the workspace allow, and the
// master.passwd deny must come before both as well. A later allow that matched
// one of these paths would re-expose the secret.
func TestSBPLSecretStoresPrecedeAllows(t *testing.T) {
	lines := strings.Split(mustProfile(t, sampleSBPL(false)), "\n")
	keychain := lineIndex(lines, `(deny file-read* (subpath "/Library/Keychains"))`)
	dslocal := lineIndex(lines, `(deny file-read* (subpath "/private/var/db/dslocal"))`)
	passwd := lineIndex(lines, `(deny file-read* (literal "/private/etc/master.passwd"))`)
	ws := lineIndex(lines, `(allow file-read* file-write* (subpath "/private/var/folders/x/T/corral-adequacy-1"))`)
	for name, i := range map[string]int{"keychain": keychain, "dslocal": dslocal, "passwd": passwd, "ws": ws} {
		if i < 0 {
			t.Fatalf("%s rule missing from profile:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	for _, ro := range []string{`(allow file-read* (subpath "/Users/op/sdk/go"))`, `(allow file-read* (subpath "/Users/op/.cargo/bin"))`} {
		r := lineIndex(lines, ro)
		if r < 0 {
			t.Fatalf("ReadOnly allow %s missing", ro)
		}
		if !(keychain < r && dslocal < r && passwd < r) {
			t.Errorf("secret-store denies (keychain %d, dslocal %d, passwd %d) must precede %s (%d)", keychain, dslocal, passwd, ro, r)
		}
	}
	if !(keychain < ws && dslocal < ws && passwd < ws) {
		t.Errorf("secret-store denies (keychain %d, dslocal %d, passwd %d) must precede the workspace allow (%d)", keychain, dslocal, passwd, ws)
	}
}

// TestSBPLRefusesKnownBadRules: the rules that broke or would widen the jail.
// Substring checks cover the named rules; the per-line check covers a blanket
// file-read grant, whose rule text is the part of each line before " ; ".
func TestSBPLRefusesKnownBadRules(t *testing.T) {
	p := mustProfile(t, sampleSBPL(true))
	for _, bad := range []string{"signal*", "(allow mach-lookup)", "(allow default)", "(allow sysctl*)", "xpc-service-name-prefix"} {
		if strings.Contains(p, bad) {
			t.Errorf("profile contains %q:\n%s", bad, p)
		}
	}
	for _, l := range strings.Split(p, "\n") {
		rule, _, _ := strings.Cut(l, " ; ")
		if rule == "(allow file-read*)" {
			t.Errorf("profile grants a blanket file read: %q", l)
		}
	}
	if !strings.HasPrefix(p, "(version 1)\n(deny default)") {
		t.Errorf("profile must open with (version 1) then (deny default):\n%s", p)
	}
}

// TestSBPLNetworkOnlyWhenAsked: the graded runs stay offline; only the
// fetch step (Options.Network) gets the network and the trust service TLS
// needs (measured 2026-10-13: without com.apple.trustd.agent, x509 fails).
func TestSBPLNetworkOnlyWhenAsked(t *testing.T) {
	off := mustProfile(t, sampleSBPL(false))
	if strings.Contains(off, "network") || strings.Contains(off, "trustd") {
		t.Errorf("offline profile grants network:\n%s", off)
	}
	on := mustProfile(t, sampleSBPL(true))
	if !strings.Contains(on, "(allow network*)") || !strings.Contains(on, `(global-name "com.apple.trustd.agent")`) {
		t.Errorf("networked profile lacks network* or trustd.agent:\n%s", on)
	}
}

// TestSBPLEveryRuleSaysWhy: the profile is the jail's security policy, and it
// is printed in debug output; every rule carries its reason, as every bwrap
// mount carries a comment.
func TestSBPLEveryRuleSaysWhy(t *testing.T) {
	for _, l := range strings.Split(strings.TrimSpace(mustProfile(t, sampleSBPL(true))), "\n") {
		if l == "(version 1)" {
			continue
		}
		if !strings.Contains(l, " ; ") {
			t.Errorf("rule without a reason: %s", l)
		}
	}
}

// TestSBPLQuotesHostilePaths: a home with a space or a double quote must
// stay one valid string literal, never break out of it.
func TestSBPLQuotesHostilePaths(t *testing.T) {
	in := sampleSBPL(false)
	in.OperatorHome = `/Users/John "JJ" Smith`
	p := mustProfile(t, in)
	if !strings.Contains(p, `(deny file-read* (subpath "/Users/John \"JJ\" Smith"))`) {
		t.Errorf("hostile home not escaped:\n%s", p)
	}
}

// TestSBPLNoHomeNoDeny: with no resolvable operator home there is no empty
// (subpath "") rule, which would match nothing and read as protection.
func TestSBPLNoHomeNoDeny(t *testing.T) {
	in := sampleSBPL(false)
	in.OperatorHome = ""
	if strings.Contains(mustProfile(t, in), `(subpath "")`) {
		t.Error(`profile contains (subpath "")`)
	}
}

// TestSBPLRejectsUnmatchablePaths: a path the sandbox cannot match turns a
// mask into a deny that matches nothing, so the credential stays readable.
// Every such input must fail with an error naming the field.
func TestSBPLRejectsUnmatchablePaths(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sbplInput)
		field  string
	}{
		{"empty workspace", func(in *sbplInput) { in.Workspace = "" }, "Workspace"},
		{"relative workspace", func(in *sbplInput) { in.Workspace = "corral-adequacy-1" }, "Workspace"},
		{"relative ReadOnly", func(in *sbplInput) { in.ReadOnly = []string{"rel/dir"} }, "ReadOnly"},
		{"empty ReadOnly entry", func(in *sbplInput) { in.ReadOnly = []string{""} }, "ReadOnly"},
		{"relative mask", func(in *sbplInput) { in.Masks = []mask{{Path: "cargo/credentials.toml"}} }, "Mask"},
		{"mask with newline", func(in *sbplInput) { in.Masks = []mask{{Path: "/opt/homebrew/cargo/cred\nentials.toml"}} }, "Mask"},
		{"mask with NUL", func(in *sbplInput) { in.Masks = []mask{{Path: "/opt/x\x00y", Dir: true}} }, "Mask"},
		{"home with control char", func(in *sbplInput) { in.OperatorHome = "/Users/op\x01" }, "OperatorHome"},
		{"home with DEL", func(in *sbplInput) { in.OperatorHome = "/Users/op\x7f" }, "OperatorHome"},
		{"relative home", func(in *sbplInput) { in.OperatorHome = "Users/op" }, "OperatorHome"},
		{"invalid UTF-8 ReadOnly", func(in *sbplInput) { in.ReadOnly = []string{"/Users/\xffop"} }, "ReadOnly"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := sampleSBPL(false)
			c.mutate(&in)
			p, err := sbplProfile(in)
			if err == nil {
				t.Fatalf("expected an error, got a profile:\n%s", p)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("error %q does not name field %q", err, c.field)
			}
			if p != "" {
				t.Errorf("error case returned a non-empty profile")
			}
		})
	}
}

// TestSBPLQuoteEscapesOnlyBackslashAndQuote: sbplQuote is the whole of SBPL
// string escaping once control characters are rejected.
func TestSBPLQuoteEscapesOnlyBackslashAndQuote(t *testing.T) {
	cases := map[string]string{
		`/plain/path`: `"/plain/path"`,
		`/a "b" c`:    `"/a \"b\" c"`,
		`/back\slash`: `"/back\\slash"`,
		`/both\"here`: `"/both\\\"here"`,
	}
	for in, want := range cases {
		if got := sbplQuote(in); got != want {
			t.Errorf("sbplQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
