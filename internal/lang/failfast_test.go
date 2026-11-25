// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		// Everything after go's -args (or --args) is the test binary's:
		// -failfast there is a flag the binary does not define, and a
		// passing suite exits 1 (TestGoFailFastBeforeArgsKeepsAPassingRunGreen).
		{"go", []string{"go", "test", "./...", "-args", "-update"}, "go test ./... -failfast -args -update"},
		{"go", []string{"go", "test", "./...", "--args", "-update"}, "go test ./... -failfast --args -update"},
		{"go", []string{"go", "test", "-v", "./...", "-args", "-failfast"}, "go test -v ./... -failfast -args -failfast"},
		{"go", []string{"go", "test", "./...", "-failfast", "-args", "-update"}, "go test ./... -failfast -args -update"},
		// After go's `--` the flag is inert (the run did not stop, go 1.26.11).
		{"go", []string{"go", "test", "./...", "--", "-update"}, "go test ./... -failfast -- -update"},
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

// The go-test row's options end at -args, measured: a passing suite whose
// test defines a flag of its own (`-args -update`, a golden-file switch) stays
// green with -failfast placed where FailFastCmd places it, and exits non-zero
// with -failfast appended after -args, where the test binary refuses it as a
// flag it does not define. A real go run, since the claim is about what go
// does with the words.
func TestGoFailFastBeforeArgsKeepsAPassingRunGreen(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("SKIP: no go on PATH")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/argsfixture\n\ngo 1.26\n",
		"p_test.go": "package p\n\nimport (\n\t\"flag\"\n\t\"testing\"\n)\n\n" +
			"var update = flag.Bool(\"update\", false, \"rewrite golden files\")\n\n" +
			"func TestPasses(t *testing.T) { _ = *update }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(argv []string) (int, string) {
		c := exec.Command(goBin, argv[1:]...) // #nosec G204 -- go from PATH, words built by this test
		c.Dir = dir
		c.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
		out, err := c.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	p, _ := ByName("go")
	operator := []string{"go", "test", "-count=1", ".", "-args", "-update"}
	if code, out := run(operator); code != 0 {
		t.Fatalf("the operator's own command is red (exit %d):\n%s", code, out)
	}
	placed, ok := FailFastCmd(p, Cmd(operator))
	if !ok {
		t.Fatal("go test has a fail-fast flag")
	}
	if code, out := run(placed); code != 0 {
		t.Errorf("%q: exit %d, want 0 (a passing suite stays green):\n%s", placed, code, out)
	}
	appended := append(slices.Clone(operator), "-failfast")
	if code, out := run(appended); code == 0 || !strings.Contains(out, "flag provided but not defined: -failfast") {
		t.Errorf("%q: exit %d, want the test binary to refuse -failfast (the negative control):\n%s", appended, code, out)
	}
}

// One placement rule for go test: what FailFastCmd adds and the run cache's
// -count=1 both go where go's own options end, before -args, so neither ever
// reaches the test binary. A -count=1 after -args would be the binary's, and
// go would store and replay the run's result.
func TestGoFailFastAndRunCacheShareOnePlacement(t *testing.T) {
	p, _ := ByName("go")
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"go", "test", "./...", "-args", "-update"},
		{"go", "test", "-v", "./pkg", "--args", "-update", "-count=3"},
	} {
		ff, ok := FailFastCmd(p, Cmd(argv))
		if !ok {
			t.Fatalf("%q: no fail-fast command", argv)
		}
		rc, state := RunCacheCmd(p, Cmd(argv), argv)
		if state != RunCacheRewritten {
			t.Fatalf("%q: run cache state %v", argv, state)
		}
		end := optionsEnd(goTestRow(t), argv, 1)
		if ff[end] != "-failfast" || rc[end] != "-count=1" {
			t.Errorf("%q: options end at %d; fail-fast %q, run cache %q", argv, end, ff, rc)
		}
	}
}

func goTestRow(t *testing.T) Runner {
	t.Helper()
	for _, r := range runners {
		if r.ID == "go-test" {
			return r
		}
	}
	t.Fatal("no go-test row")
	return Runner{}
}
