// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

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
		{py, []string{"python3", "-m", "unittest", "discover"}, "unittest"},
		{py, []string{"sh", "-c", "cd src && pytest -q"}, "pytest"},
		{py, []string{"sh", "-c", "echo pytest"}, "pytest"}, // not in command position: the Default, not a match
		{js, []string{"npx", "vitest", "run"}, "vitest"},
		{js, []string{"npm", "test"}, "node --test"},
	} {
		if r, ok := RunnerFor(c.p, c.argv); !ok || r.Name != c.want {
			t.Errorf("RunnerFor(%s, %q) = %q, want %q", c.p.Name(), c.argv, r.Name, c.want)
		}
	}
	// The Default answered for an unnamed command must never add a flag.
	if a, ok := failFastViaRows(py, []string{"sh", "-c", "echo pytest"}); ok {
		t.Errorf("a shell script that only mentions pytest got fail-fast %q", a)
	}
}
