// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
	"testing"
)

// The unit evidence pass runs every go unit with -count=1, through go's own
// argument parsing: it goes into the go test arguments (after gotestsum's
// "--"), replaces the operator's -count in any spelling, and keeps every
// other flag as written. It is correctness, not speed: the pass shares one
// build cache among its units, and a cached test RESULT replayed from it
// would be evidence of a run that never happened in this pass.
func TestGoTestEvidenceArgsCountOne(t *testing.T) {
	g, _ := ByName("go")
	for _, c := range []struct {
		name       string
		inv        []string
		unit, want []string
	}{
		{"go test, no -count", []string{"go", "test", "./..."},
			[]string{"go", "test", "example.com/m/p"},
			[]string{"go", "test", "example.com/m/p", "-count=1"}},
		{"go test, -count=3 replaced", []string{"go", "test", "./..."},
			[]string{"go", "test", "-v", "-count=3", "example.com/m/p"},
			[]string{"go", "test", "-v", "example.com/m/p", "-count=1"}},
		{"go test, -count 3 in two words", []string{"go", "test", "./..."},
			[]string{"go", "test", "-count", "3", "-tags", "integration", "example.com/m/p"},
			[]string{"go", "test", "-tags", "integration", "example.com/m/p", "-count=1"}},
		{"go test, -test.count and --count", []string{"go", "test", "./..."},
			[]string{"go", "test", "-test.count=2", "--count=4", "-race", "example.com/m/p"},
			[]string{"go", "test", "-race", "example.com/m/p", "-count=1"}},
		// A flag VALUE spelled like the flag is a value: -tags count
		// keeps its tag and the flag after it.
		{"go test, a value spelled count", []string{"go", "test", "./..."},
			[]string{"go", "test", "-tags", "count", "-race", "example.com/m/p"},
			[]string{"go", "test", "-tags", "count", "-race", "example.com/m/p", "-count=1"}},
		{"go test, already -count=1", []string{"go", "test", "./..."},
			[]string{"go", "test", "example.com/m/p", "-count=1"},
			[]string{"go", "test", "example.com/m/p", "-count=1"}},
		{"gotestsum, after its --", []string{"gotestsum", "--format", "dots", "--", "./..."},
			[]string{"gotestsum", "--format", "dots", "--", "-count=2", "example.com/m/p"},
			[]string{"gotestsum", "--format", "dots", "--", "example.com/m/p", "-count=1"}},
		// gotestsum with no go test argument runs `go test -json ./...`, so
		// adding the "--" must keep ./... or it would run one package.
		{"gotestsum, no go test arguments", []string{"gotestsum"},
			[]string{"gotestsum"},
			[]string{"gotestsum", "--", "./...", "-count=1"}},
	} {
		got := EvidenceUnitCmd(g, Cmd(c.inv), c.unit)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: EvidenceUnitCmd(%q) = %q, want %q", c.name, c.unit, got, c.want)
		}
		// The coverage wrapper keeps the flag: it appends its own after
		// the command, and the command is what the jail runs.
		cov, ok := g.(CoverageReporter).CoverageCmd(got)
		if !ok || !strings.Contains(cov[len(cov)-1], "'-count=1' -coverpkg=./...") {
			t.Errorf("%s: CoverageCmd(%q) = %q, %v, want -count=1 kept ahead of -coverpkg", c.name, got, cov, ok)
		}
	}

	// The note names -count=1 and the operator's -count it replaces, by
	// meaning: the operator's own -count=1, in either spelling, is no
	// change at all.
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"go", "test", "./..."}, "the evidence pass runs each unit with -count=1"},
		{[]string{"go", "test", "-count=3", "./..."}, "the evidence pass runs each unit with -count=1 in place of -count=3"},
		{[]string{"go", "test", "-count", "3", "./..."}, "the evidence pass runs each unit with -count=1 in place of -count 3"},
		{[]string{"go", "test", "-count=1", "./..."}, ""},
		{[]string{"go", "test", "-count", "1", "./..."}, ""},
		{[]string{"gotestsum", "--", "-count=1", "./..."}, ""},
		{[]string{"gotestsum", "--", "-count=5", "./..."}, "the evidence pass runs each unit with -count=1 in place of -count=5"},
		{[]string{"gotestsum", "--", "-count=3", "./..."}, "the evidence pass runs each unit with -count=1 in place of -count=3"},
		// The "--" and ./... the row adds to a gotestsum with no go test
		// arguments only keep the command's meaning: each unit still runs
		// its own package, and the note names only -count=1.
		{[]string{"gotestsum", "--format", "dots"}, "the evidence pass runs each unit with -count=1"},
		{[]string{"gotestsum"}, "the evidence pass runs each unit with -count=1"},
	} {
		if got := EvidenceNote(g, Cmd(c.argv)); got != c.want {
			t.Errorf("EvidenceNote(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// go test and gotestsum share one GOCACHE across the units of an evidence
// pass; a row with no shared cache names none.
func TestEvidenceCacheFor(t *testing.T) {
	g, _ := ByName("go")
	for _, argv := range [][]string{{"go", "test", "./..."}, {"gotestsum", "--", "./..."}} {
		c := EvidenceCacheFor(g, Cmd(argv))
		if c == nil || c.Env != "GOCACHE" || c.Args != nil {
			t.Errorf("EvidenceCacheFor(%q) = %+v, want GOCACHE and no args", argv, c)
		}
	}
	ts, _ := ByName("typescript")
	if c := EvidenceCacheFor(ts, Cmd([]string{"jest"})); c != nil {
		t.Errorf("EvidenceCacheFor(jest) = %+v, want nil", c)
	}
}
