// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
	"testing"
)

// The caches grading shares are the go rows' GOCACHE, the cargo rows'
// CARGO_TARGET_DIR and jest's cache directory (named by the command, for
// JavaScript and TypeScript alike): every other runner has no row that
// declares one, so grading hands it none.
func TestRunCacheForIsGoCargoAndJestOnly(t *testing.T) {
	g, _ := ByName("go")
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"gotestsum", "--", "./..."},
	} {
		env, ok := RunCacheFor(g, Invocation{Argv: argv})
		if !ok || env.Env != "GOCACHE" {
			t.Errorf("%v: RunCacheFor = %+v, %v; want GOCACHE, true", argv, env, ok)
		}
	}
	for _, n := range []string{"javascript", "typescript"} {
		p, _ := ByName(n)
		for _, argv := range [][]string{{"npx", "jest"}, {"node", "./node_modules/jest/bin/jest.js", "src"}} {
			c, ok := RunCacheFor(p, Invocation{Argv: argv})
			if !ok || c.Env != "" || c.Args == nil || c.Label() != "jest cache directory" {
				t.Errorf("%s %v: RunCacheFor = %+v, %v; want jest's cache directory, named by the command", n, argv, c, ok)
			}
		}
	}
	for _, c := range []struct {
		lang string
		argv []string
	}{
		{"python", []string{"python", "-m", "pytest", "tests"}},
		{"javascript", []string{"node", "./node_modules/vitest/vitest.mjs", "run"}},
		{"typescript", []string{"npx", "vitest", "run"}},
		{"javascript", []string{"npx", "mocha"}},
		{"ruby", []string{"bundle", "exec", "rspec"}},
		{"php", []string{"vendor/bin/phpunit"}},
	} {
		p, _ := ByName(c.lang)
		if env, ok := RunCacheFor(p, Invocation{Argv: c.argv}); ok {
			t.Errorf("%v: RunCacheFor = %+v, true; want no cache", c.argv, env)
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
	shares := map[string]bool{"go": true, "rust": true, "javascript": true, "typescript": true}
	for _, n := range Names() {
		p, _ := ByName(n)
		reason := ""
		for _, e := range ParityExemptions {
			if e.Lang == n && e.Capability == "run-cache" {
				reason = e.Reason
			}
		}
		switch {
		case shares[n] && (!cap.Has(p) || reason != ""):
			t.Errorf("%s must have run-cache and no exemption (has=%v, reason=%q)", n, cap.Has(p), reason)
		case !shares[n] && (cap.Has(p) || len(reason) < 40):
			t.Errorf("%s: has=%v, reason=%q; want exempted with a written reason", n, cap.Has(p), reason)
		}
	}
}

// cargo's rows share its target directory: every `cargo test` and `cargo
// nextest run` points CARGO_TARGET_DIR at it, which cargo prefers to a
// project's build.target-dir (measured on cargo 1.90). A command naming its
// own --target-dir, which cargo prefers to the variable, shares none.
func TestCargoRowsShareTheTargetDirectory(t *testing.T) {
	r, _ := ByName("rust")
	for _, argv := range [][]string{
		{"cargo", "test"},
		{"cargo", "+nightly", "test", "--workspace"},
		{"cargo", "nextest", "run"},
		{"cargo", "test", "--", "--target-dir", "x"}, // a test binary's argument, not cargo's
	} {
		env, ok := RunCacheFor(r, Invocation{Argv: argv})
		if !ok || env.Env != "CARGO_TARGET_DIR" {
			t.Errorf("%v: RunCacheFor = %+v, %v; want CARGO_TARGET_DIR, true", argv, env, ok)
		}
	}
	for _, argv := range [][]string{
		{"cargo", "test", "--target-dir", "out"},
		{"cargo", "test", "--target-dir=out"},
		{"cargo", "nextest", "run", "--target-dir", "out"},
	} {
		if env, ok := RunCacheFor(r, Invocation{Argv: argv}); ok {
			t.Errorf("%v: RunCacheFor = %+v, true; want no cache: cargo uses --target-dir in place of CARGO_TARGET_DIR", argv, env)
		}
	}
}

// cargo stores no test result, so a cargo command that runs tests already
// forbids a replay and is returned as it stands (rewritten, in RunCacheCmd's
// terms); one that runs none is no-test; anything corral cannot read as cargo
// is unreadable and, as the baseline, shares no cache.
func TestCargoRunCacheCmdChangesNothingAndReadsTheCommand(t *testing.T) {
	r, _ := ByName("rust")
	inv := Invocation{Argv: []string{"cargo", "test"}}
	nx := Invocation{Argv: []string{"cargo", "nextest", "run"}}
	for _, c := range []struct {
		inv   Invocation
		cmd   []string
		state RunCacheRewrite
	}{
		{inv, []string{"cargo", "test"}, RunCacheRewritten},
		{inv, []string{"cargo", "t", "--lib"}, RunCacheRewritten},
		{inv, []string{"cargo", "+stable", "test", "--", "--exact", "tests::adds"}, RunCacheRewritten},
		{inv, []string{"/usr/local/cargo/bin/cargo", "test", "--", "--skip", "a"}, RunCacheRewritten},
		{nx, []string{"cargo", "nextest", "run", "-E", "test(=a)"}, RunCacheRewritten},
		{inv, []string{"cargo", "test", "--no-run"}, RunCacheNoTest},
		{inv, []string{"cargo", "build"}, RunCacheNoTest},
		{inv, []string{"cargo", "check", "--tests"}, RunCacheNoTest},
		{inv, []string{"sh", "-c", "cargo test"}, RunCacheUnreadable},
		{inv, []string{"env", "RUSTFLAGS=-Dwarnings", "cargo", "test"}, RunCacheUnreadable},
		{inv, []string{"cargo", "run"}, RunCacheUnreadable},
		{inv, []string{"cargo"}, RunCacheUnreadable},
		{inv, []string{"make", "test"}, RunCacheUnreadable},
	} {
		got, state := RunCacheCmd(r, c.inv, c.cmd)
		if state != c.state || !slices.Equal(got, c.cmd) {
			t.Errorf("%v: got %v, %v; want the command unchanged, %v", c.cmd, got, state, c.state)
		}
	}
}

// jest keeps no test result it could serve in place of a run, so a command
// that runs tests is read as one that forbids it, and gets the shared
// cache's directory, appended once; the operator's words are kept as given.
// A command that runs no test is no-test, and one jest's words cannot be
// read in (a shell wrapper, a package script, "--", a watch mode) is
// unreadable and unchanged.
func TestJestRunCacheCmdNamesTheSharedCacheAndReadsTheCommand(t *testing.T) {
	js, _ := ByName("javascript")
	inv := Invocation{Argv: []string{"npx", "jest"}}
	const dir = "--cacheDirectory=/corral-cache"
	for _, c := range []struct {
		cmd   []string
		state RunCacheRewrite
	}{
		{[]string{"npx", "jest"}, RunCacheRewritten},
		{[]string{"node", "./node_modules/jest/bin/jest.js", "-i", "src/calc.test.js"}, RunCacheRewritten},
		{[]string{"npx", "jest", "--testNamePattern", "^adds$", "calc.test.js"}, RunCacheRewritten},
		{[]string{"npx", "jest", "--json", "--outputFile=/tmp/r.json"}, RunCacheRewritten},
		{[]string{"npx", "jest", "--listTests", "--json"}, RunCacheNoTest},
		{[]string{"npx", "jest", "--showConfig"}, RunCacheNoTest},
		{[]string{"npx", "jest", "--clear-cache"}, RunCacheNoTest},
		{[]string{"npx", "jest", "--watch"}, RunCacheUnreadable},
		{[]string{"npx", "jest", "--watchAll"}, RunCacheUnreadable},
		{[]string{"npx", "jest", "--", "calc"}, RunCacheUnreadable},
		{[]string{"sh", "-c", "npx jest"}, RunCacheUnreadable},
		{[]string{"npm", "test"}, RunCacheUnreadable},
		{[]string{"npx", "vitest", "run"}, RunCacheUnreadable},
	} {
		got, state := RunCacheCmd(js, inv, c.cmd)
		want := c.cmd
		if c.state == RunCacheRewritten {
			want = append(slices.Clone(c.cmd), dir)
		}
		if state != c.state || !slices.Equal(got, want) {
			t.Errorf("%v: got %v, %v; want %v, %v", c.cmd, got, state, want, c.state)
		}
	}
}

// A jest command that sets the cache itself, or chooses its tests from
// jest's records of earlier runs or from version control, shares none: on
// a warm cache --onlyFailures ran no test (internal/adequacy's
// TestJestOnlyFailuresOverASharedCacheRunsNoTest).
func TestJestCommandsThatSetOrSelectFromTheCacheShareNone(t *testing.T) {
	js, _ := ByName("javascript")
	for _, argv := range [][]string{
		{"npx", "jest", "--cacheDirectory", "/tmp/c"},
		{"npx", "jest", "--cache-directory=/tmp/c"},
		{"npx", "jest", "--no-cache"},
		{"npx", "jest", "--cache=false"},
		{"npx", "jest", "--onlyFailures"},
		{"npx", "jest", "-f"},
		{"npx", "jest", "-if"},
		{"npx", "jest", "-o"},
		{"npx", "jest", "--only-changed"},
		{"npx", "jest", "--lastCommit"},
		{"npx", "jest", "--changedSince=main"},
		{"npx", "jest", "--findRelatedTests", "src/calc.js"},
	} {
		if c, ok := RunCacheFor(js, Invocation{Argv: argv}); ok {
			t.Errorf("%v: RunCacheFor = %+v, true; want no shared cache", argv, c)
		}
	}
	// Words before jest's are not jest's.
	if _, ok := RunCacheFor(js, Invocation{Argv: []string{"node", "--no-cache", "./node_modules/jest/bin/jest.js"}}); !ok {
		t.Error("node's own option was read as jest's")
	}
}
