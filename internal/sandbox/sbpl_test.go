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
	lines := strings.Split(sbplProfile(sampleSBPL(true)), "\n")
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

// TestSBPLRefusesKnownBadRules: the rules that broke or would widen the jail.
func TestSBPLRefusesKnownBadRules(t *testing.T) {
	p := sbplProfile(sampleSBPL(true))
	for _, bad := range []string{"signal*", "(allow mach-lookup)", "(allow default)", "(allow sysctl*)", "(allow file-read*)\n", "xpc-service-name-prefix"} {
		if strings.Contains(p, bad) {
			t.Errorf("profile contains %q:\n%s", bad, p)
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
	off := sbplProfile(sampleSBPL(false))
	if strings.Contains(off, "network") || strings.Contains(off, "trustd") {
		t.Errorf("offline profile grants network:\n%s", off)
	}
	on := sbplProfile(sampleSBPL(true))
	if !strings.Contains(on, "(allow network*)") || !strings.Contains(on, `(global-name "com.apple.trustd.agent")`) {
		t.Errorf("networked profile lacks network* or trustd.agent:\n%s", on)
	}
}

// TestSBPLEveryRuleSaysWhy: the profile is the jail's security policy, and it
// is printed in debug output; every rule carries its reason, as every bwrap
// mount carries a comment.
func TestSBPLEveryRuleSaysWhy(t *testing.T) {
	for _, l := range strings.Split(strings.TrimSpace(sbplProfile(sampleSBPL(true))), "\n") {
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
	p := sbplProfile(in)
	if !strings.Contains(p, `(deny file-read* (subpath "/Users/John \"JJ\" Smith"))`) {
		t.Errorf("hostile home not escaped:\n%s", p)
	}
}

// TestSBPLNoHomeNoDeny: with no resolvable operator home there is no empty
// (subpath "") rule, which would match nothing and read as protection.
func TestSBPLNoHomeNoDeny(t *testing.T) {
	in := sampleSBPL(false)
	in.OperatorHome = ""
	if strings.Contains(sbplProfile(in), `(subpath "")`) {
		t.Error(`profile contains (subpath "")`)
	}
}
