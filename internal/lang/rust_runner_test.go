// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func rustCapture(t *testing.T, runner, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rust", runner, name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The captures are real runs (see PROVENANCE.txt beside them). Each is parsed
// by the SAME function, handed the grammar production reads off the output
// (rustGrammarOf) — which must be the capture's own runner's.
func TestParseRustRun_Captures(t *testing.T) {
	cases := []struct {
		runner, name string
		failures     []string
		flaky        []string
	}{
		{"libtest", "pass", nil, nil},
		{"libtest", "fail", []string{"stdtests::prop_ext_i16::big_endian", "stdtests::prop_ext_u16::big_endian", "test::prop_i16::big_endian", "test::prop_u16::big_endian"}, nil},
		{"libtest", "panic", []string{"test::planted_panic"}, nil},
		{"libtest", "compile_error", nil, nil},
		{"nextest", "pass", nil, nil},
		{"nextest", "fail", []string{"fuzzy_match tests::test_contains_wildcards"}, nil},
		{"nextest", "panic", []string{"fuzzy_match tests::planted_panic"}, nil},
		{"nextest", "compile_error", nil, nil},
		{"nextest", "flaky", nil, []string{"fuzzy_match tests::planted_flaky"}},
	}
	for _, c := range cases {
		t.Run(c.runner+"/"+c.name, func(t *testing.T) {
			out := rustCapture(t, c.runner, c.name)
			want := rustGrammarLibtest
			if c.runner == "nextest" {
				want = rustGrammarNextest
			}
			g := rustGrammarOf(out)
			// A compile error stops nextest before it prints anything of its
			// own, so its output carries no nextest marker and reads as
			// libtest's — which names no failure either, the right answer.
			if g != want && c.name != "compile_error" {
				t.Fatalf("rustGrammarOf = %v, want %v", g, want)
			}
			got := parseRustRun(g, out)
			if !reflect.DeepEqual(got.Failures, c.failures) {
				t.Fatalf("Failures = %q, want %q", got.Failures, c.failures)
			}
			if !reflect.DeepEqual(got.Flaky, c.flaky) {
				t.Fatalf("Flaky = %q, want %q", got.Flaky, c.flaky)
			}
		})
	}
}

// A grammar must not read the other runner's output as its own.
func TestParseRustRun_WrongGrammarNamesNothing(t *testing.T) {
	for _, c := range []struct {
		g      rustGrammar
		runner string
	}{{rustGrammarNextest, "libtest"}, {rustGrammarLibtest, "nextest"}} {
		if got := parseRustRun(c.g, rustCapture(t, c.runner, "fail")); len(got.Failures) != 0 {
			t.Fatalf("%s output parsed by the other grammar: %+v", c.runner, got)
		}
	}
}

func TestRustRunner_ChosenOnceFromTheCommand(t *testing.T) {
	for _, c := range []struct {
		cmd []string
		pre string
	}{
		{nil, "cargo"},
		{[]string{"cargo", "test"}, "cargo"},
		{[]string{"cargo", "nextest", "run", "-p", "x"}, "cargo-nextest"},
		{[]string{"/home/u/.cargo/bin/cargo-nextest", "nextest", "run"}, "/home/u/.cargo/bin/cargo-nextest"},
		{[]string{"/home/u/.cargo/bin/cargo", "test"}, "/home/u/.cargo/bin/cargo"},
	} {
		if row := rustRunner(c.cmd); row.preflightBin != c.pre {
			t.Errorf("%v: bin %q, want %q", c.cmd, row.preflightBin, c.pre)
		}
	}
}

func TestFirstFailure_RustBothRunners(t *testing.T) {
	var p FailureParser = rustPlugin{}
	for _, c := range []struct{ runner, name, want string }{
		{"libtest", "panic", "test::planted_panic"},
		{"libtest", "pass", ""},
		{"libtest", "compile_error", ""},
		{"nextest", "fail", "fuzzy_match tests::test_contains_wildcards"},
		{"nextest", "flaky", ""},
		{"nextest", "compile_error", ""},
	} {
		if got := p.FirstFailure(rustCapture(t, c.runner, c.name)); got != c.want {
			t.Errorf("%s/%s: FirstFailure = %q, want %q", c.runner, c.name, got, c.want)
		}
	}
}

// fakeHomeCargo puts a rustup-proxy-shaped ~/.cargo/bin (cargo + rustup) on
// PATH under a fresh HOME and returns the HOME.
func fakeHomeCargo(t *testing.T, withRustup bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".cargo", "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{"cargo"}
	if withRustup {
		names = append(names, "rustup")
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- test stand-in must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

// withSystemRustupHome stands dir in for sandbox.SystemRustupHome for one
// test.
func withSystemRustupHome(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(SwapSystemRustupHomeForTest(dir))
}

// The jail runs the system-wide toolchain, so a cargo that is a rustup proxy
// under $HOME runs there only when that toolchain exists: JailPreflight says
// so and names the layout, before any spend. Preflight — the half every
// substrate needs — admits the same host, because a run that is not jailed
// (--substrate workspace) runs the operator's own toolchain and has no use
// for a system-wide one.
func TestRustJailPreflightUserHomeProxyNeedsTheSystemToolchain(t *testing.T) {
	fakeHomeCargo(t, true)
	withSystemRustupHome(t, t.TempDir()) // exists, but holds no toolchains

	if err := (rustPlugin{}).Preflight([]string{"cargo", "test"}); err != nil {
		t.Fatalf("Preflight must admit a stock ~/.cargo rustup install — nothing it checks needs a jail: %v", err)
	}
	p, _ := ByName("rust")
	err := JailPreflight(p, []string{"cargo", "test"})
	if err == nil {
		t.Fatal("a user-home rustup proxy with no system-wide toolchain must be refused for a jailed run")
	}
	for _, want := range []string{"beside rustup under your home", sandbox.SystemRustupHome, sandbox.SystemCargoHome, "rustup-init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}

	present := t.TempDir()
	if err := os.MkdirAll(filepath.Join(present, "toolchains"), 0o750); err != nil {
		t.Fatal(err)
	}
	withSystemRustupHome(t, present)
	if err := JailPreflight(p, []string{"cargo", "test"}); err != nil {
		t.Errorf("with the system-wide toolchain present the proxy is fine: %v", err)
	}
}

// cargo-nextest under ~/.cargo/bin is not itself a rustup proxy — cargo
// install puts a plain binary there — so the refusal must not call it one;
// it runs the toolchain through the cargo beside it, which is.
func TestRustJailPreflightNamesNextestWithoutCallingItAProxy(t *testing.T) {
	fakeHomeCargo(t, true)
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".cargo", "bin", "cargo-nextest"), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- test stand-in must be executable
		t.Fatal(err)
	}
	withSystemRustupHome(t, t.TempDir())
	p, _ := ByName("rust")
	err := JailPreflight(p, []string{"cargo-nextest", "nextest", "run"})
	if err == nil {
		t.Fatal("a user-home cargo-nextest with no system-wide toolchain must be refused for a jailed run")
	}
	if strings.Contains(err.Error(), `"cargo-nextest" resolves to a rustup proxy`) {
		t.Errorf("cargo-nextest is not a rustup proxy: %v", err)
	}
}

// A cargo under $HOME that is not a proxy (no rustup beside it) needs no
// system rustup, so it is never refused for lacking one.
func TestRustJailPreflightNonProxyUserHomeCargoPasses(t *testing.T) {
	fakeHomeCargo(t, false)
	withSystemRustupHome(t, filepath.Join(t.TempDir(), "absent"))
	p, _ := ByName("rust")
	if err := JailPreflight(p, []string{"cargo", "test"}); err != nil {
		t.Errorf("a non-proxy cargo must pass: %v", err)
	}
}

// A missing cargo is refused by Preflight, on every substrate, saying only
// what is true on every substrate: the tool is not on PATH. The system-wide
// layout is the jail's requirement, and a workspace run has no jail.
func TestRustPreflightMissingCargoSaysOnlyThatItIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := rustPlugin{}.Preflight([]string{"cargo", "test"})
	if err == nil || !strings.Contains(err.Error(), `"cargo" not found on PATH`) {
		t.Fatalf("a missing cargo must be refused as missing, got: %v", err)
	}
	for _, never := range []string{sandbox.SystemRustupHome, sandbox.SystemCargoHome, "system-wide"} {
		if strings.Contains(err.Error(), never) {
			t.Errorf("Preflight runs on every substrate and must not demand the jail's install (%q): %v", never, err)
		}
	}
}

// A plugin with no jail-only requirement answers nil through the seam.
func TestJailPreflightIsNilForAPluginWithoutOne(t *testing.T) {
	py, _ := ByName("python")
	if _, ok := py.(JailPreflighter); ok {
		t.Fatal("precondition: python declares no jail-only preflight")
	}
	if err := JailPreflight(py, []string{"definitely-not-on-path-xyz"}); err != nil {
		t.Errorf("JailPreflight(python) = %v, want nil", err)
	}
}
