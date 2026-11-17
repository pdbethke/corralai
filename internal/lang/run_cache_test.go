// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
	"testing"
)

// The cache grading shares is the go rows' GOCACHE and nobody else's: every
// other runner has no row that declares one, so grading hands it none.
func TestRunCacheForIsGoOnly(t *testing.T) {
	g, _ := ByName("go")
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"gotestsum", "--", "./..."},
	} {
		env, ok := RunCacheFor(g, Invocation{Argv: argv})
		if !ok || env != "GOCACHE" {
			t.Errorf("%v: RunCacheFor = %q, %v; want GOCACHE, true", argv, env, ok)
		}
	}
	for _, c := range []struct {
		lang string
		argv []string
	}{
		{"python", []string{"python", "-m", "pytest", "tests"}},
		{"javascript", []string{"node", "./node_modules/jest/bin/jest.js"}},
		{"ruby", []string{"bundle", "exec", "rspec"}},
		{"php", []string{"vendor/bin/phpunit"}},
		{"rust", []string{"cargo", "test"}},
	} {
		p, _ := ByName(c.lang)
		if env, ok := RunCacheFor(p, Invocation{Argv: c.argv}); ok {
			t.Errorf("%v: RunCacheFor = %q, true; want no cache", c.argv, env)
		}
	}
}

// Grading shares a cache that also holds test RESULTS, so every command
// grading hands the jail that RUNS tests must say -count=1, exactly once. A
// command that runs no test is no-test; a command that cannot be told from a
// test command is unreadable, never no-test: the state is what lets the
// caller refuse to share a cache it cannot protect.
func TestRunCacheCmdNeverLetsAResultReplay(t *testing.T) {
	g, _ := ByName("go")
	py, _ := ByName("python")
	goInv := Invocation{Argv: []string{"go", "test", "./..."}}
	sumInv := Invocation{Argv: []string{"gotestsum", "--", "./..."}}
	pyInv := Invocation{Argv: []string{"python", "-m", "pytest", "tests"}}
	for _, c := range []struct {
		p     Plugin
		inv   Invocation
		cmd   []string
		want  string
		state RunCacheRewrite
	}{
		{g, goInv, []string{"go", "test", "./..."}, "go test ./... -count=1", RunCacheRewritten},
		// An explicit -count of the operator's, in any spelling, already forbids
		// a cached result: kept exactly as given, never replaced by -count=1.
		{g, goInv, []string{"go", "test", "-count=5", "./..."}, "go test -count=5 ./...", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "-count", "5", "./..."}, "go test -count 5 ./...", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "./...", "-count=3"}, "go test ./... -count=3", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "-test.count=5", "./..."}, "go test -test.count=5 ./...", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "--count=2", "./..."}, "go test --count=2 ./...", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "--count", "2", "./..."}, "go test --count 2 ./...", RunCacheRewritten},
		{g, sumInv, []string{"gotestsum", "--", "-count=4", "./pkg"}, "gotestsum -- -count=4 ./pkg", RunCacheRewritten},
		// A -count after -args belongs to the test binary, so -count=1 is still added.
		// A word that is the VALUE of another flag is never the flag it spells.
		{g, goInv, []string{"go", "test", "-run", "-count", "."}, "go test -run -count .", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "-run", "^TestX$", ".", "-failfast"}, "go test -run ^TestX$ . -failfast -count=1", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "-run", "^Test A=b$", "."}, "go test -run ^Test A=b$ . -count=1", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "-skip=Slow", "./pkg"}, "go test -skip=Slow ./pkg -count=1", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "./...", "-args", "-count=9"}, "go test ./... -count=1 -args -count=9", RunCacheRewritten},
		{g, goInv, []string{"go", "test", "./...", "--args", "-count=9"}, "go test ./... -count=1 --args -count=9", RunCacheRewritten},
		{g, sumInv, []string{"gotestsum", "--", "-run", "^TestX$", "./pkg"}, "gotestsum -- -run ^TestX$ ./pkg -count=1", RunCacheRewritten},
		{g, sumInv, []string{"gotestsum", "--", "-failfast", "./pkg"}, "gotestsum -- -failfast ./pkg -count=1", RunCacheRewritten},
		// Commands positively known to run no test: nothing to replay.
		{g, goInv, []string{"go", "vet", "./pkg"}, "go vet ./pkg", RunCacheNoTest},
		{g, goInv, []string{"go", "build", "./..."}, "go build ./...", RunCacheNoTest},
		{g, goInv, []string{"go", "test", "-c", "./pkg"}, "go test -c ./pkg", RunCacheNoTest},
		{g, goInv, []string{"go", "test", "-list", ".", "./pkg"}, "go test -list . ./pkg", RunCacheNoTest},
		{g, sumInv, []string{"go", "vet", "./pkg"}, "go vet ./pkg", RunCacheNoTest},
		// A -c or -list that is another flag's VALUE is not the flag, so it is
		// never no-test. The argument reader refuses a flag value that itself
		// begins with a dash, so the command is unreadable (unchanged, no
		// cache shared): the safe answer for a command it cannot place.
		{g, goInv, []string{"go", "test", "-run", "-c", "."}, "go test -run -c .", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "-tags", "-c", "."}, "go test -tags -c .", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "-o", "-c", "."}, "go test -o -c .", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "-run", "-list", "."}, "go test -run -list .", RunCacheUnreadable},
		{g, sumInv, []string{"gotestsum", "--", "-run", "-c", "."}, "gotestsum -- -run -c .", RunCacheUnreadable},
		// An unlisted flag before a -c: not readable, so unreadable, not no-test.
		{g, goInv, []string{"go", "test", "-nosuchflag", "-c", "."}, "go test -nosuchflag -c .", RunCacheUnreadable},
		// Anything else the rewrite cannot read is unreadable, unchanged.
		{g, goInv, []string{"sh", "-c", "go test ./..."}, "sh -c go test ./...", RunCacheUnreadable},
		{g, goInv, []string{"env", "X=1", "go", "test", "./..."}, "env X=1 go test ./...", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "./a.go"}, "go test ./a.go", RunCacheUnreadable},
		{g, goInv, []string{"go", "test", "-nosuchflag", "x", "./..."}, "go test -nosuchflag x ./...", RunCacheUnreadable},
		{g, goInv, []string{"make", "test"}, "make test", RunCacheUnreadable},
		{g, sumInv, []string{"sh", "-c", "gotestsum ./..."}, "sh -c gotestsum ./...", RunCacheUnreadable},
		{py, pyInv, []string{"python", "-m", "pytest", "tests"}, "python -m pytest tests", RunCacheNone},
	} {
		got, state := RunCacheCmd(c.p, c.inv, slices.Clone(c.cmd))
		if strings.Join(got, " ") != c.want || state != c.state {
			t.Errorf("RunCacheCmd(%v) = %q, %v; want %q, %v", c.cmd, strings.Join(got, " "), state, c.want, c.state)
		}
	}
}

func TestRunCacheRewriteStrings(t *testing.T) {
	for st, want := range map[RunCacheRewrite]string{
		RunCacheNone: "none", RunCacheRewritten: "rewritten", RunCacheNoTest: "no-test", RunCacheUnreadable: "unreadable",
	} {
		if st.String() != want {
			t.Errorf("%d.String() = %q; want %q", int(st), st.String(), want)
		}
	}
}

// RunCacheArgv is the grader's rewrite hook: the command of RunCacheCmd.
func TestRunCacheArgvIsTheRewrittenCommand(t *testing.T) {
	g, _ := ByName("go")
	f := RunCacheArgv(g, Invocation{Argv: []string{"go", "test", "./..."}})
	if got := strings.Join(f([]string{"go", "test", "./pkg"}), " "); got != "go test ./pkg -count=1" {
		t.Errorf("got %q", got)
	}
}

// The widened handling is for grading only. The unit evidence pass reads the
// operator's argv exactly as it did before grading shared a cache: a command
// carrying -run, -skip or -args is left alone by EvidenceUnitCmd, named by no
// EvidenceNote, and still refused by every unit function.
func TestUnitEvidenceSideRefusesFiltersAsBefore(t *testing.T) {
	g, _ := ByName("go")
	for _, argv := range [][]string{
		{"go", "test", "-run", "X", "./..."},
		{"go", "test", "-skip", "X", "./..."},
		{"go", "test", "./...", "-args", "-v"},
		{"go", "test", "./...", "--args", "-v"},
	} {
		inv := Invocation{Argv: argv}
		if got := EvidenceUnitCmd(g, inv, slices.Clone(argv)); !slices.Equal(got, argv) {
			t.Errorf("%v: EvidenceUnitCmd = %v; want unchanged", argv, got)
		}
		if n := EvidenceNote(g, inv); n != "" {
			t.Errorf("%v: EvidenceNote = %q; want empty", argv, n)
		}
		if _, ok := ListUnitsCmd(g, inv); ok {
			t.Errorf("%v: ListUnitsCmd accepted it", argv)
		}
		if _, ok := UnitsRunCmd(g, inv, []string{"example.com/m/p"}); ok {
			t.Errorf("%v: UnitsRunCmd accepted it", argv)
		}
	}
}

// The cache is a parity capability judged on the runner rows: go has it, and
// every other language says why not, in words, not an empty string.
func TestRunCacheIsAParityCapability(t *testing.T) {
	var cap *Capability
	for i := range Capabilities {
		if Capabilities[i].Key == "run-cache" {
			cap = &Capabilities[i]
		}
	}
	if cap == nil {
		t.Fatal("no run-cache capability")
	}
	if cap.Title != "Share a build cache between sandboxed runs" {
		t.Errorf("title = %q", cap.Title)
	}
	for _, n := range Names() {
		p, _ := ByName(n)
		reason := ""
		for _, e := range ParityExemptions {
			if e.Lang == n && e.Capability == "run-cache" {
				reason = e.Reason
			}
		}
		switch {
		case n == "go" && (!cap.Has(p) || reason != ""):
			t.Errorf("go must have run-cache and no exemption (has=%v, reason=%q)", cap.Has(p), reason)
		case n != "go" && (cap.Has(p) || len(reason) < 40):
			t.Errorf("%s: has=%v, reason=%q; want exempted with a written reason", n, cap.Has(p), reason)
		}
	}
}
