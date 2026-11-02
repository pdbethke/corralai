// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

func killerRow(t *testing.T, lang, id string) Runner {
	t.Helper()
	p, ok := ByName(lang)
	if !ok {
		t.Fatalf("no language %s", lang)
	}
	r, err := RunnerByID(p, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestKillerCmdNarrowsTheRunByName(t *testing.T) {
	cases := []struct {
		lang, runner string
		argv         []string
		id           string
		want         []string
	}{
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run", "--project", "domain"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "--project", "domain", "-t", `^A(?: | > )b$`}},
		{"javascript", "jest", []string{"npx", "jest"}, "Calc::adds", []string{"npx", "jest", "--testNamePattern", "^Calc adds$"}},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-q", "tests"}, "tests/test_a.py::test_x",
			[]string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"}},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-p", "no:cacheprovider", "tests"}, "tests/test_a.py::test_x",
			[]string{"python3", "-m", "pytest", "-p", "no:cacheprovider", "tests/test_a.py::test_x"}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/sub", []string{"go", "test", "-run", "^TestX$/^sub$", "./..."}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/case_(a)", []string{"go", "test", "-run", `^TestX$/^case_\(a\)$`, "./..."}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/x|y", []string{"go", "test", "-run", `^TestX$/^x\|y$`, "./..."}},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "watch"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", `^A(?: | > )b$`}},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", `^A(?: | > )b$`}},
	}
	for _, c := range cases {
		got, ok := killerRow(t, c.lang, c.runner).KillerCmd(c.argv, c.id)
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q ok=%v, want %q", c.runner, got, ok, c.want)
		}
	}
}

func TestKillerCmdRefusesAFilteredArgv(t *testing.T) {
	cases := []struct {
		lang, runner string
		argv         []string
		id           string
	}{
		{"go", "go-test", []string{"go", "test", "-run", "TestY", "./..."}, "TestX"},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", "y"}, "A::b"},
		{"javascript", "jest", []string{"npx", "jest", "--testNamePattern", "y"}, "A::b"},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-k", "y"}, "t.py::x"},
		{"go", "go-test", []string{"go", "test", "./..."}, ""},
		{"go", "go-test", []string{"make", "test"}, "TestX"},
		{"go", "go-test", []string{"sh", "-c", "go test ./..."}, "TestX"},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run"}, ""},
		{"javascript", "jest", []string{"npx", "jest"}, ""},
	}
	for _, c := range cases {
		if got, ok := killerRow(t, c.lang, c.runner).KillerCmd(c.argv, c.id); ok {
			t.Errorf("%s %q id %q: narrowed to %q, want a refusal", c.runner, c.argv, c.id, got)
		}
	}
}

func TestKillerCmdRefusesPytestErrorIDs(t *testing.T) {
	r := killerRow(t, "python", "pytest")
	for _, id := range []string{"error:tests/test_a.py", "test_x", ""} {
		if got, ok := r.KillerCmd([]string{"python3", "-m", "pytest", "-q"}, id); ok {
			t.Errorf("id %q: narrowed to %q, want a refusal", id, got)
		}
	}
}

func TestStripPyPositionalsKeepsOptionsAndTheirValues(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"python3", "-m", "pytest", "-q", "tests"}, []string{"python3", "-m", "pytest", "-q"}},
		{[]string{"pytest", "--maxfail", "3", "a/", "b/test_x.py::t"}, []string{"pytest", "--maxfail", "3"}},
		{[]string{"pytest", "--maxfail=3", "a/"}, []string{"pytest", "--maxfail=3"}},
		{nil, nil},
	}
	for _, c := range cases {
		if got := stripPyPositionals(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKillerCmdForIsNilWithoutARow(t *testing.T) {
	p, _ := ByName("ruby")
	if KillerCmdFor(p, Invocation{Argv: []string{"bundle", "exec", "rspec"}}) != nil {
		t.Error("rspec has no KillerCmd, KillerCmdFor must be nil")
	}
}

func TestKillerCmdForShapesThroughAPackageScriptAsSingleTestCmdDoes(t *testing.T) {
	p, _ := ByName("javascript")
	inv := Invocation{Argv: []string{"npm", "test"}, Script: []string{"jest"}, Pass: "--", Narrow: true}
	killer := KillerCmdFor(p, inv)
	if killer == nil {
		t.Fatal("KillerCmdFor is nil for a jest script")
	}
	got, ok := killer(inv.Argv, "Calc::adds")
	want := []string{"npm", "test", "--", "--testNamePattern", "^Calc adds$"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("got %q ok=%v, want %q", got, ok, want)
	}
	single, ok := SingleTestCmd(p, inv, "calc.test.js", "Calc::adds")
	if !ok || !slices.Equal(single[:3], want[:3]) {
		t.Fatalf("SingleTestCmd shapes as %q; killer %q does not match its prefix", single, got)
	}
}
