// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"strings"
	"testing"
)

// Every language has exactly one Default row, and every row names only
// registered languages: a row for a language nobody registered, or a
// language with two defaults, would make RunnerFor's fallback a guess.
func TestEveryLanguageHasOneDefaultRunner(t *testing.T) {
	defaults := map[string]int{}
	for _, r := range runners {
		for _, l := range r.Languages {
			if _, ok := ByName(l); !ok {
				t.Errorf("row %s names unregistered language %q", r.Name, l)
			}
			if r.Default {
				defaults[l]++
			}
		}
	}
	for _, n := range Names() {
		if defaults[n] != 1 {
			t.Errorf("%s has %d Default rows, want 1", n, defaults[n])
		}
	}
}

// The more specific row wins where one command names two runners' words,
// and a command naming no row gets the language's Default.
func TestRunnerForPicksTheMostSpecificRow(t *testing.T) {
	rust, _ := ByName("rust")
	goP, _ := ByName("go")
	py, _ := ByName("python")
	js, _ := ByName("javascript")
	for _, c := range []struct {
		p    Plugin
		argv []string
		want string
	}{
		{rust, []string{"cargo", "nextest", "run"}, "cargo nextest"},
		{rust, []string{"cargo-nextest", "run"}, "cargo nextest"},
		{rust, []string{"cargo", "test", "-p", "x"}, "cargo test"},
		{rust, []string{"make", "test"}, "cargo test"},
		{goP, []string{"gotestsum", "--", "./..."}, "gotestsum"},
		{goP, []string{"go", "test", "./..."}, "go test"},
		// A package path ending in a runner's word is not that runner.
		{goP, []string{"go", "test", "./cmd/gotestsum"}, "go test"},
		{py, []string{"python3", "-m", "unittest", "discover"}, "unittest"},
		{py, []string{"sh", "-c", "cd src && pytest -q"}, "pytest"},
		{py, []string{"sh", "-c", "echo pytest"}, "pytest"}, // not in command position: the Default, not a match
		{js, []string{"npx", "vitest", "run"}, "vitest"},
		{js, []string{"npm", "test"}, "node --test"},
	} {
		if r, ok := RunnerFor(c.p, Cmd(c.argv)); !ok || r.Name != c.want {
			t.Errorf("RunnerFor(%s, %q) = %q, want %q", c.p.Name(), c.argv, r.Name, c.want)
		}
	}
	// The Default answered for an unnamed command must never add a flag.
	if a, ok := FailFastArgsFor(py, Cmd([]string{"sh", "-c", "echo pytest"})); ok {
		t.Errorf("a shell script that only mentions pytest got fail-fast %q", a)
	}
}

// The one deliberate change the runner table made: pytest's --deselect
// belonged to every Python command, and unittest exits 2 on an option it does
// not know, so a salvage rerun of a unittest suite could never pass. The
// table gives deselection to the pytest row only.
func TestUnittestGetsNoPytestDeselect(t *testing.T) {
	py, _ := ByName("python")
	if _, _, ok := DeselectorFor(py, Cmd([]string{"python3", "-m", "unittest", "discover"})); ok {
		t.Error("a unittest command was offered pytest's --deselect")
	}
	if _, _, ok := DeselectorFor(py, Cmd([]string{"python3", "-m", "pytest", "-q"})); !ok {
		t.Error("a pytest command lost --deselect")
	}
}

// withRunners swaps the runner table for one test, so a dispatcher rule can
// be pinned on a row the real table does not have yet.
func withRunners(t *testing.T, rows []Runner) {
	t.Helper()
	saved := runners
	runners = rows
	t.Cleanup(func() { runners = saved })
}

// A runner that selects one test by a pattern over its full name also runs
// every other listed test with that full name, so ResolveForRun leaves such
// an id unchecked — the same rule ResolveListedTest applies to a name listed
// twice. A row without Coruns resolves exactly as ResolveListedTest does.
func TestResolveForRunRefusesACollidingId(t *testing.T) {
	js, _ := ByName("javascript")
	argv := []string{"npx", "fakerunner"}
	listed := []string{"Calc::adds", "Calc adds", "top level", "Calc::neg::adds"}

	withRunners(t, []Runner{{Name: "fake", Languages: []string{"javascript"}, Default: true, Words: []string{"fakerunner"}, Coruns: fullNameColl}})
	if id, ok := ResolveForRun(js, Cmd(argv), listed, "Calc::adds"); ok {
		t.Errorf("Calc::adds resolved to %q; its run would also run the top-level test named \"Calc adds\"", id)
	}
	if id, ok := ResolveForRun(js, Cmd(argv), listed, "top level"); !ok || id != "top level" {
		t.Errorf("top level = %q, %v; want it resolved", id, ok)
	}
	if _, ok := ResolveForRun(js, Cmd(argv), listed, "adds"); ok {
		t.Error("adds resolved; it names two listed tests")
	}

	withRunners(t, []Runner{{Name: "exact", Languages: []string{"javascript"}, Default: true, Words: []string{"fakerunner"}}})
	if id, ok := ResolveForRun(js, Cmd(argv), listed, "Calc::adds"); !ok || id != "Calc::adds" {
		t.Errorf("without Coruns, Calc::adds = %q, %v; want ResolveListedTest's answer", id, ok)
	}
}

func TestRunnerSelectHelpers(t *testing.T) {
	if got := fullName("Calc::neg::adds"); got != "Calc neg adds" {
		t.Errorf("fullName = %q", got)
	}
	if got := jsRegexQuote(`adds (a+b) $1.5 [x]|{y}/\z^`); got != `adds \(a\+b\) \$1\.5 \[x\]\|\{y\}\/\\z\^` {
		t.Errorf("jsRegexQuote = %q", got)
	}
	if !hasFlag([]string{"npx", "jest", "--testNamePattern=x"}, "-t", "--testNamePattern") || !hasFlag([]string{"jest", "-t", "x"}, "-t") {
		t.Error("hasFlag missed a filter")
	}
	if hasFlag([]string{"jest", "--testNamePatternX"}, "--testNamePattern") {
		t.Error("hasFlag matched a longer flag")
	}
	if narrowable([]string{"sh", "-c", "npx jest"}) || narrowable(nil) || narrowable([]string{"jest", "-t", "x"}, "-t") || !narrowable([]string{"npx", "jest"}, "-t") {
		t.Error("narrowable wrong")
	}
	if wordIndex([]string{"npx", "./node_modules/.bin/vitest", "run"}, "vitest") != 1 || wordIndex([]string{"go", "test"}, "vitest") != -1 {
		t.Error("wordIndex wrong")
	}
}

// Every row has an ID an operator can type after --runner, and no two share one.
func TestRunnerIDsAreUniqueFlagValues(t *testing.T) {
	flag := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	seen := map[string]string{}
	for _, r := range runners {
		if !flag.MatchString(r.ID) {
			t.Errorf("row %s has ID %q, not a flag value", r.Name, r.ID)
		}
		if prev, dup := seen[r.ID]; dup {
			t.Errorf("rows %s and %s share ID %q", prev, r.Name, r.ID)
		}
		seen[r.ID] = r.Name
	}
}

func TestRunnerByIDRefusesOtherLanguages(t *testing.T) {
	py, _ := ByName("python")
	if r, err := RunnerByID(py, "unittest"); err != nil || r.Name != "unittest" {
		t.Errorf("unittest = %q, %v", r.Name, err)
	}
	_, err := RunnerByID(py, "jest")
	if err == nil || !strings.Contains(err.Error(), "pytest, unittest") {
		t.Errorf("jest for python: err = %v, want one naming pytest, unittest", err)
	}
	if _, err := RunnerByID(py, "nope"); err == nil {
		t.Error("an unknown id was accepted")
	}
}
