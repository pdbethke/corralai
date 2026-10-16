// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

// The list and single-test commands are built from the operator's own
// command, so a package filter and the runner both survive: libtest takes
// its arguments after `--`, nextest lists with its own subcommand and selects
// with a filterset. The outputs parsed here are verbatim from cargo 1.90 and
// cargo-nextest 0.9.148 on a crate with a #[path] test module, an inline
// test module, an integration test and a doc test.
func TestRustCommandTestTargeting(t *testing.T) {
	p := rustPlugin{}
	for _, c := range []struct {
		name     string
		testCmd  []string
		list     []string
		single   []string
		listOK   bool
		singleOK bool
		listOut  string
		wantIDs  []string
	}{
		{
			name:     "libtest",
			testCmd:  []string{"cargo", "test", "-p", "fuzzy_match"},
			list:     []string{"cargo", "test", "-p", "fuzzy_match", "--", "--list"},
			single:   []string{"cargo", "test", "-p", "fuzzy_match", "--", "--exact", "tests::a"},
			listOK:   true,
			singleOK: true,
			listOut:  "inline::a: test\ninline::a_longer: test\ntests::a: test\ntests::b: test\na: test\nsrc/lib.rs - doc (line 3): test\n",
			wantIDs:  []string{"inline::a", "inline::a_longer", "tests::a", "tests::b", "a", "src/lib.rs - doc (line 3)"},
		},
		{
			name:     "libtest with its own binary arguments",
			testCmd:  []string{"cargo", "test", "--", "--test-threads", "1"},
			list:     []string{"cargo", "test", "--", "--test-threads", "1", "--list"},
			single:   []string{"cargo", "test", "--", "--test-threads", "1", "--exact", "tests::a"},
			listOK:   true,
			singleOK: true,
		},
		{
			name:     "nextest",
			testCmd:  []string{"cargo", "nextest", "run", "-p", "fuzzy_match"},
			list:     []string{"cargo", "nextest", "list", "-p", "fuzzy_match", "--message-format", "oneline"},
			single:   []string{"cargo", "nextest", "run", "-p", "fuzzy_match", "-E", "test(=tests::a)"},
			listOK:   true,
			singleOK: true,
			listOut:  "demo inline::a\ndemo inline::a_longer\ndemo tests::a\ndemo tests::b\ndemo::integ a\n",
			wantIDs:  []string{"inline::a", "inline::a_longer", "tests::a", "tests::b", "a"},
		},
		{
			// Arguments after `--` go to the test binaries; listing needs none
			// of them, and the filterset must stay on nextest's side of `--`.
			name:     "nextest with passthrough arguments",
			testCmd:  []string{"cargo", "nextest", "run", "--", "--nocapture"},
			list:     []string{"cargo", "nextest", "list", "--message-format", "oneline"},
			single:   []string{"cargo", "nextest", "run", "-E", "test(=tests::a)", "--", "--nocapture"},
			listOK:   true,
			singleOK: true,
		},
		{
			// The operator's own filterset would be OR-ed with ours, and the
			// single test would run beside others: a kill could be theirs.
			name:    "nextest with a filterset of its own",
			testCmd: []string{"cargo", "nextest", "run", "-E", "test(fuzzy)"},
			list:    []string{"cargo", "nextest", "list", "-E", "test(fuzzy)", "--message-format", "oneline"},
			listOK:  true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			list, ok := ListTestsCmd(p, c.testCmd, "")
			if ok != c.listOK || (ok && !reflect.DeepEqual(list, c.list)) {
				t.Errorf("ListTestsCmdFor = %q, %v; want %q, %v", list, ok, c.list, c.listOK)
			}
			single, ok := SingleTestCmd(p, c.testCmd, "", "tests::a")
			if ok != c.singleOK || (ok && !reflect.DeepEqual(single, c.single)) {
				t.Errorf("SingleTestCmdFor = %q, %v; want %q, %v", single, ok, c.single, c.singleOK)
			}
			if c.listOut != "" {
				// Cargo's own progress lines are on stderr, but a merged
				// stream must not add tests either.
				out := "   Compiling demo v0.1.0 (/tmp/demo)\n    Finished `test` profile\n     Running unittests src/lib.rs (target/debug/deps/demo-1)\n" + c.listOut
				if got := ParseTestList(p, c.testCmd, out); !reflect.DeepEqual(got, c.wantIDs) {
					t.Errorf("ParseTestListFor =\n  %q\nwant\n  %q", got, c.wantIDs)
				}
			}
		})
	}
}

// A file-relative name resolves to the one listed test it names. The same
// name in two places (two modules, or a test binary and an integration test)
// resolves to none: running it would run both, and a kill could belong to
// the other one.
func TestResolveListedTest(t *testing.T) {
	listed := []string{"inline::a", "inline::a_longer", "tests::a", "tests::b", "a", "tests::b_x", "other::tests::c", "tests::c"}
	for name, want := range map[string]string{
		"b":        "tests::b",
		"tests::b": "tests::b",
		"a_longer": "inline::a_longer",
		"a":        "",
		"c":        "",
		"tests::c": "",
		"missing":  "",
		"":         "",
	} {
		got, ok := ResolveListedTest(listed, name)
		if got != want || ok != (want != "") {
			t.Errorf("ResolveListedTest(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, ok := ResolveListedTest([]string{"tests::b", "tests::b"}, "tests::b"); ok {
		t.Error("an id listed twice (two test binaries) must not resolve")
	}
}
