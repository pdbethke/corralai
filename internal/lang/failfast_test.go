// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// The flag belongs to the RUNNER named in the command, never to the language.
// A wrong guess would make the runner exit non-zero, which the scorer reads as
// a kill — so an unrecognised runner MUST get nothing at all.
func TestFailFastArgsAreTheRunnersOwn(t *testing.T) {
	cases := []struct {
		lang string
		cmd  []string
		want string // "" = no flag
	}{
		{"python", []string{"python3", "-m", "pytest", "-q"}, "-x"},
		{"python", []string{".venv/bin/pytest", "tests/"}, "-x"},
		{"python", []string{"python3", "-m", "unittest", "discover"}, "--failfast"},
		{"python", []string{"python3", "-m", "nose2"}, ""},
		{"go", []string{"go", "test", "./..."}, "-failfast"},
		{"go", []string{"make", "test"}, ""},
		{"go", []string{"gotestsum", "--", "./..."}, ""},
		{"javascript", []string{"npx", "jest"}, "--bail"},
		{"javascript", []string{"npx", "mocha", "test/"}, "--bail"},
		{"javascript", []string{"node", "--test"}, ""},
		{"javascript", []string{"npx", "vitest", "run"}, ""},
		{"typescript", []string{"npx", "jest"}, "--bail"},
		{"typescript", []string{"node", "--experimental-strip-types", "--test"}, ""},
		{"ruby", []string{"bundle", "exec", "rspec"}, "--fail-fast"},
		{"ruby", []string{"ruby", "foo_test.rb"}, ""},
		{"php", []string{"vendor/bin/phpunit"}, "--stop-on-failure"},
		{"php", []string{"composer", "test"}, ""},
	}
	for _, c := range cases {
		p, ok := ByName(c.lang)
		if !ok {
			t.Fatalf("%s plugin not registered", c.lang)
		}
		args, got := FailFastArgsFor(p, c.cmd)
		if c.want == "" {
			if got {
				t.Errorf("%s %v: got %v, want no flag — an unrecognised runner must be left alone", c.lang, c.cmd, args)
			}
			continue
		}
		if !got || strings.Join(args, " ") != c.want {
			t.Errorf("%s %v: got %v ok=%v, want %q", c.lang, c.cmd, args, got, c.want)
		}
	}
}

// A `sh -c "<script>"` command's argv is not the runner's argv: appending a
// word there makes it sh's $0, not an option to the suite.
func TestShellWrappedCommandsAreNeverTouched(t *testing.T) {
	p, _ := ByName("python")
	if args, ok := FailFastArgsFor(p, []string{"sh", "-c", "pytest tests/"}); ok {
		t.Errorf("shell-wrapped command got %v — appending there changes sh's $0, not the runner's options", args)
	}
	if got := AppendFailFast([]string{"bash", "-c", "pytest"}, []string{"-x"}); len(got) != 3 {
		t.Errorf("AppendFailFast touched a shell-wrapped command: %v", got)
	}
}

// An operator who already asked for it does not get it twice, and an empty
// command is never turned into one.
func TestAppendFailFastIsIdempotentAndAppends(t *testing.T) {
	if got := AppendFailFast([]string{"pytest", "-x", "tests/"}, []string{"-x"}); len(got) != 3 {
		t.Errorf("appended a duplicate flag: %v", got)
	}
	if got := AppendFailFast(nil, []string{"-x"}); got != nil {
		t.Errorf("AppendFailFast(nil) = %v", got)
	}
	got := AppendFailFast([]string{"python3", "-m", "pytest", "tests/"}, []string{"-x"})
	if strings.Join(got, " ") != "python3 -m pytest tests/ -x" {
		t.Errorf("got %v — the flag must be APPENDED (an insert after argv[0] would break `python -m pytest`)", got)
	}
}
