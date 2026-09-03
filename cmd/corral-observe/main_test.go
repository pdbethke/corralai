// SPDX-License-Identifier: Elastic-2.0

package main

// SURFACE CLAIMS — these are wired through the flag package into config fields, and the test drives the fields.
//
// testdata/executed-surfaces.tsv names this file as the receipt for the
// flag(s) below, and TestDocsClassifiedSurfacesCarryAReceipt requires either
// the literal or an explicit claim. This is the explicit claim: a receipt a
// reader cannot check is not a receipt.
//surface: --addr
//surface: --open
//surface: --ping
//surface: --token
//surface: --version

import (
	"flag"
	"sort"
	"testing"
)

// TestObserveExposesOnlyItsOwnFlags pins the fix for a dependency injecting a
// flag into corral's public interface.
//
// corral-observe registered its flags on the package-level flag.CommandLine,
// which every imported package shares. go-rod's lib/defaults registers one
// there at init:
//
//	-rod string   Set the default value of options used by rod.
//
// so `corral-observe -h` — and the generated CLI reference built from it —
// advertised a third-party debug knob as part of corral's own interface. One
// corral does not document, cannot support, and never chose to expose.
//
// An exposed flag is a promise whether or not anyone meant it, which is the
// whole reason the executed-surface manifest counts values and not just names.
// This test is what stops the next import from making one silently.
func TestObserveExposesOnlyItsOwnFlags(t *testing.T) {
	want := []string{"addr", "brain", "open", "ping", "token", "version"}

	var got []string
	observeFlags(&observeOpts{}).VisitAll(func(f *flag.Flag) { got = append(got, f.Name) })
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("flags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("flags = %v, want %v — a flag appeared or vanished; if an import added it, corral-observe is advertising something it did not choose to", got, want)
		}
	}
}

// TestObserveFlagsBindToTheirValues is the receipt every one of those six
// surfaces needed: parsing them must actually reach the struct main() reads.
// A flag that parses and is then dropped on the floor is this codebase's
// recurring defect, and these had no test at all.
func TestObserveFlagsBindToTheirValues(t *testing.T) {
	var o observeOpts
	args := []string{
		"-brain", "https://brain.example",
		"-token", "tok-123",
		"-addr", "127.0.0.1:9999",
		"-open", "-ping", "-version",
	}
	if err := observeFlags(&o).Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, c := range []struct {
		name, got, want string
	}{
		{"brain", o.brain, "https://brain.example"},
		{"token", o.token, "tok-123"},
		{"addr", o.addr, "127.0.0.1:9999"},
	} {
		if c.got != c.want {
			t.Errorf("-%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !o.open || !o.ping || !o.ver {
		t.Errorf("boolean flags did not bind: open=%v ping=%v version=%v", o.open, o.ping, o.ver)
	}
}

// TestPickPrefersFlagThenEnvThenDefault covers the precedence every one of
// those values actually resolves through — the flag, then the environment
// variable named in its own help text, then the built-in default. Getting this
// backwards would make CORRAL_BRAIN silently beat an explicit --brain.
func TestPickPrefersFlagThenEnvThenDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		vals []string
		want string
	}{
		{"flag wins over env and default", []string{"from-flag", "from-env", "default"}, "from-flag"},
		{"env wins when no flag", []string{"", "from-env", "default"}, "from-env"},
		{"default is the floor", []string{"", "", "default"}, "default"},
		{"nothing set at all", []string{"", ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pick(tc.vals...); got != tc.want {
				t.Errorf("pick(%q) = %q, want %q", tc.vals, got, tc.want)
			}
		})
	}
}
