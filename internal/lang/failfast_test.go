// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
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
		{"rust", []string{"cargo", "nextest", "run"}, "--fail-fast"},
		{"rust", []string{"cargo", "test"}, ""},
	}
	for _, c := range cases {
		p, ok := ByName(c.lang)
		if !ok {
			t.Fatalf("%s plugin not registered", c.lang)
		}
		args, got := FailFastArgsFor(p, Cmd(c.cmd))
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

// FailFastCmd places the flag where the runner reads it: after the runner's
// word and before the runner's OWN `--`, the rule ReportCmd uses, because
// every word after that separator is the runner's positional argument (a
// path to pytest) or the test binary's (cargo). Appended after it, nextest's
// --fail-fast would reach libtest as an unknown option and fail the run,
// which the scorer would read as a kill on every mutant were the probe not
// there to catch it. Through a launcher script the separator is the
// launcher's, so the flag still follows it. A command that already carries
// the flag before the separator is returned unchanged.
func TestFailFastCmdGoesBeforeTheRunnersOwnSeparator(t *testing.T) {
	for _, c := range []struct {
		lang string
		cmd  []string
		want string // "" = not ok
	}{
		{"rust", []string{"cargo", "nextest", "run", "--workspace", "--", "parses"}, "cargo nextest run --workspace --fail-fast -- parses"},
		{"rust", []string{"cargo", "nextest", "run"}, "cargo nextest run --fail-fast"},
		{"rust", []string{"cargo", "nextest", "run", "--fail-fast", "--", "parses"}, "cargo nextest run --fail-fast -- parses"},
		{"rust", []string{"cargo", "test", "--", "--nocapture"}, ""},
		{"python", []string{"python3", "-m", "pytest", "-q", "--", "tests/"}, "python3 -m pytest -q -x -- tests/"},
		{"python", []string{"python3", "-m", "pytest", "tests/"}, "python3 -m pytest tests/ -x"},
		{"python", []string{"pytest", "-x", "tests/"}, "pytest -x tests/"},
		{"go", []string{"go", "test", "./..."}, "go test ./... -failfast"},
	} {
		p, _ := ByName(c.lang)
		got, ok := FailFastCmd(p, Cmd(c.cmd))
		if c.want == "" {
			if ok {
				t.Errorf("%v: got %q, want no fail-fast command", c.cmd, got)
			}
			continue
		}
		if !ok || strings.Join(got, " ") != c.want {
			t.Errorf("%v: got %q ok=%v, want %q", c.cmd, got, ok, c.want)
		}
	}
	dir := repoWith(t, map[string]string{"package.json": pkgJSON})
	for argv, want := range map[string]string{"npm test": "npm test -- --bail", "npm test -- --ci": "npm test -- --ci --bail"} {
		p, inv := resolve(t, "javascript", dir, strings.Fields(argv), "")
		if got, ok := FailFastCmd(p, inv); !ok || strings.Join(got, " ") != want {
			t.Errorf("%s: got %q ok=%v, want %q", argv, got, ok, want)
		}
	}
}

// The capture script's nextest fail-fast checks run the command FailFastCmd
// builds, and the one a plain append would have built, against the real
// runner (scripts/capture-runner-fixtures.sh, "fail-fast"): the first stops
// at the first failure under a profile that turned fail-fast off, the second
// is refused. Held equal here so a change to the row or the placement rule
// cannot leave the script checking a command corral no longer runs.
func TestFailFastCaptureIsTheRowsCommand(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	p, _ := ByName("rust")
	base := []string{"cargo", "nextest", "run", "-j", "1", "--", "tests::"}
	placed, ok := FailFastCmd(p, Cmd(base))
	if !ok {
		t.Fatalf("%q: no fail-fast command", base)
	}
	for name, cmd := range map[string][]string{
		"nextest-profile-no-fail-fast":              base,
		"nextest-fail-fast-before-separator":        placed,
		"nextest-fail-fast-after-separator-refused": AppendFailFast(base, []string{"--fail-fast"}),
	} {
		var line string
		for _, l := range strings.Split(script, "\n") {
			if strings.HasPrefix(l, "want "+name+" ") {
				line = l
			}
		}
		if want := "&& " + strings.Join(cmd, " ") + " "; !strings.Contains(line, want) {
			t.Errorf("%s: capture line %q does not run %q", name, line, strings.Join(cmd, " "))
		}
	}
}

// A `sh -c "<script>"` command's argv is not the runner's argv: appending a
// word there makes it sh's $0, not an option to the suite.
func TestShellWrappedCommandsAreNeverTouched(t *testing.T) {
	p, _ := ByName("python")
	if args, ok := FailFastArgsFor(p, Cmd([]string{"sh", "-c", "pytest tests/"})); ok {
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

// A shell behind a prefix is a wrapper too: `env A=1 sh -c pytest` + -x
// makes -x the script's $1, not pytest's option. FailFastArgsFor refuses it
// by the rule ReportCmd uses (shellWrappedBefore).
func TestFailFastRefusesAShellBehindAPrefix(t *testing.T) {
	p, _ := ByName("python")
	for _, argv := range [][]string{
		{"env", "A=1", "sh", "-c", "pytest"},
		{"xvfb-run", "bash", "-e", "-c", "python3 -m pytest tests"},
	} {
		if args, ok := FailFastArgsFor(p, Cmd(argv)); ok {
			t.Errorf("%v got fail-fast %v: a shell behind a prefix reads no appended flag", argv, args)
		}
		if _, ok := ReportCmd(p, Cmd(argv), argv); ok {
			t.Errorf("%v got report flags: the two functions must refuse the same wrappers", argv)
		}
	}
	// A prefix with no shell is still read as before.
	if args, ok := FailFastArgsFor(p, Cmd([]string{"env", "A=1", "pytest", "tests"})); !ok || strings.Join(args, " ") != "-x" {
		t.Errorf("env A=1 pytest tests: got %v ok=%v, want -x", args, ok)
	}
}
