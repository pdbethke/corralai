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
// grading hands the jail that RUNS tests must say -count=1, exactly once, and
// every other command must be left alone.
func TestRunCacheCmdNeverLetsAResultReplay(t *testing.T) {
	g, _ := ByName("go")
	py, _ := ByName("python")
	goInv := Invocation{Argv: []string{"go", "test", "./..."}}
	sumInv := Invocation{Argv: []string{"gotestsum", "--", "./..."}}
	pyInv := Invocation{Argv: []string{"python", "-m", "pytest", "tests"}}
	for _, c := range []struct {
		name string
		p    Plugin
		inv  Invocation
		cmd  []string
		want string
	}{
		{"go test", g, goInv, []string{"go", "test", "./..."}, "go test ./... -count=1"},
		{"one -count only", g, goInv, []string{"go", "test", "-count=5", "./..."}, "go test ./... -count=1"},
		{"-run and -failfast kept", g, goInv, []string{"go", "test", "-run", "^TestX$", ".", "-failfast"}, "go test -run ^TestX$ . -failfast -count=1"},
		{"-skip kept", g, goInv, []string{"go", "test", "-skip=Slow", "./pkg"}, "go test -skip=Slow ./pkg -count=1"},
		{"-args stay the test binary's", g, goInv, []string{"go", "test", "./pkg", "-args", "-count=9"}, "go test ./pkg -count=1 -args -count=9"},
		{"gotestsum -run after --", g, sumInv, []string{"gotestsum", "--", "-run", "^TestX$", "./pkg"}, "gotestsum -- -run ^TestX$ ./pkg -count=1"},
		{"gotestsum -failfast", g, sumInv, []string{"gotestsum", "--", "-failfast", "./pkg"}, "gotestsum -- -failfast ./pkg -count=1"},
		// A command that runs no test has no result to replay, and it is
		// handed to the jail by the same door.
		{"go vet", g, goInv, []string{"go", "vet", "./pkg"}, "go vet ./pkg"},
		{"go build", g, goInv, []string{"go", "build", "./..."}, "go build ./..."},
		// go test -c compiles the test binary and stops: no test runs, so
		// there is nothing to replay and the command is left as written.
		{"go test -c", g, goInv, []string{"go", "test", "-c", "./pkg"}, "go test -c ./pkg"},
		{"go test -list", g, goInv, []string{"go", "test", "-list", ".", "./pkg"}, "go test -list . ./pkg"},
		// The rewrite cannot read inside a shell wrapper.
		{"sh -c wrapper", g, goInv, []string{"sh", "-c", "cd sub && go test ./..."}, "sh -c cd sub && go test ./..."},
		{"a runner with no cache", py, pyInv, []string{"python", "-m", "pytest", "tests"}, "python -m pytest tests"},
	} {
		got := RunCacheCmd(c.p, c.inv, slices.Clone(c.cmd))
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: RunCacheCmd(%v) = %q; want %q", c.name, c.cmd, strings.Join(got, " "), c.want)
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
