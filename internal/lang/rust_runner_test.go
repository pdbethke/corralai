// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		recognised   bool
		failures     []string
		flaky        []string
	}{
		{"libtest", "pass", true, nil, nil},
		{"libtest", "fail", true, []string{"stdtests::prop_ext_i16::big_endian", "stdtests::prop_ext_u16::big_endian", "test::prop_i16::big_endian", "test::prop_u16::big_endian"}, nil},
		{"libtest", "panic", true, []string{"test::planted_panic"}, nil},
		{"libtest", "compile_error", false, nil, nil},
		{"nextest", "pass", true, nil, nil},
		{"nextest", "fail", true, []string{"fuzzy_match tests::test_contains_wildcards"}, nil},
		{"nextest", "panic", true, []string{"fuzzy_match tests::planted_panic"}, nil},
		{"nextest", "compile_error", false, nil, nil},
		{"nextest", "flaky", true, nil, []string{"fuzzy_match tests::planted_flaky"}},
	}
	for _, c := range cases {
		t.Run(c.runner+"/"+c.name, func(t *testing.T) {
			cmd := []string{"cargo", "test"}
			if c.runner == "nextest" {
				cmd = []string{"cargo", "nextest", "run"}
			}
			got := parseRustRun(rustRunner(cmd).grammar, rustCapture(t, c.runner, c.name))
			if got.Recognised != c.recognised {
				t.Fatalf("Recognised = %v, want %v", got.Recognised, c.recognised)
			}
			if !reflect.DeepEqual(got.Failures, c.failures) {
				t.Fatalf("Failures = %q, want %q", got.Failures, c.failures)
			}
			if !reflect.DeepEqual(got.Flaky, c.flaky) {
				t.Fatalf("Flaky = %q, want %q", got.Flaky, c.flaky)
			}
		})
	}
}

// A grammar must not read the other runner's output as its own: libtest text
// handed to the nextest grammar (and the reverse) is unrecognised, never a
// measured pass.
func TestParseRustRun_WrongGrammarIsUnrecognised(t *testing.T) {
	if got := parseRustRun(rustGrammarNextest, rustCapture(t, "libtest", "pass")); got.Recognised {
		t.Fatalf("libtest output recognised by the nextest grammar: %+v", got)
	}
	if got := parseRustRun(rustGrammarLibtest, rustCapture(t, "nextest", "pass")); got.Recognised {
		t.Fatalf("nextest output recognised by the libtest grammar: %+v", got)
	}
	if got := parseRustRun(rustGrammarNextest, nil); got.Recognised {
		t.Fatalf("empty output recognised: %+v", got)
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

func TestRustRunner_SingleTestAndList(t *testing.T) {
	lt, nx := rustRunner([]string{"cargo", "test"}), rustRunner([]string{"cargo", "nextest", "run"})
	if got, want := strings.Join(lt.singleTest("src/lib.rs", "tests::a"), " "), "cargo test -- --exact tests::a"; got != want {
		t.Errorf("libtest single = %q, want %q", got, want)
	}
	if got, want := strings.Join(lt.singleTest("tests/it.rs", "a"), " "), "cargo test --test it -- --exact a"; got != want {
		t.Errorf("libtest integration single = %q, want %q", got, want)
	}
	if got, want := strings.Join(nx.singleTest("src/lib.rs", "tests::a"), " "), "cargo nextest run -E test(=tests::a)"; got != want {
		t.Errorf("nextest single = %q, want %q", got, want)
	}
	if strings.Join(lt.listCmd, " ") != "cargo test -- --list --format terse" || strings.Join(nx.listCmd, " ") != "cargo nextest list --message-format oneline" {
		t.Errorf("list commands: %v / %v", lt.listCmd, nx.listCmd)
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

func TestRustRunner_NextestProfileDisclosure(t *testing.T) {
	lt, nx := rustRunner([]string{"cargo", "test"}), rustRunner([]string{"cargo", "nextest", "run"})
	if lt.profileNote(true) == "" || !strings.Contains(lt.profileNote(true), "nextest") {
		t.Errorf("cargo test with a nextest config must say the profiles were not applied: %q", lt.profileNote(true))
	}
	if lt.profileNote(false) != "" || nx.profileNote(true) != "" || nx.profileNote(false) != "" {
		t.Error("the disclosure fires only for cargo test + a nextest config")
	}
}
