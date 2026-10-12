// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
// by the SAME function, handed only the grammar the runner row names.
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
			cmd := []string{"cargo", "test"}
			if c.runner == "nextest" {
				cmd = []string{"cargo", "nextest", "run"}
			}
			got := parseRustRun(rustRunner(cmd).grammar, rustCapture(t, c.runner, c.name))
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
		cmd     []string
		grammar rustGrammar
		pre     string
	}{
		{nil, rustGrammarLibtest, "cargo"},
		{[]string{"cargo", "test"}, rustGrammarLibtest, "cargo"},
		{[]string{"cargo", "nextest", "run", "-p", "x"}, rustGrammarNextest, "cargo-nextest"},
		{[]string{"/home/u/.cargo/bin/cargo-nextest", "nextest", "run"}, rustGrammarNextest, "/home/u/.cargo/bin/cargo-nextest"},
		{[]string{"/home/u/.cargo/bin/cargo", "test"}, rustGrammarLibtest, "/home/u/.cargo/bin/cargo"},
	} {
		row := rustRunner(c.cmd)
		if row.grammar != c.grammar || row.preflightBin != c.pre {
			t.Errorf("%v: grammar %v bin %q, want %v %q", c.cmd, row.grammar, row.preflightBin, c.grammar, c.pre)
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
