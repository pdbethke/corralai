// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// versionJail answers a version command by its argv, the way the jail the
// run already built would: some commands print, one fails, one never returns.
type versionJail struct {
	mu    sync.Mutex
	out   map[string]string
	fail  map[string]error
	block map[string]bool
	exit  map[string]int
	seen  []string
}

// EnumerateDetailed is Enumerate with the exit code the jail reports: a
// command named in exit exits with that code, printing its out.
func (v *versionJail) EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	o, err := v.Enumerate(ctx, files, cmd)
	if err != nil {
		return sandbox.EnumerateResult{Output: o, ExitCode: -1}, err
	}
	return sandbox.EnumerateResult{Output: o, ExitCode: v.exit[strings.Join(cmd, " ")]}, nil
}

func (v *versionJail) Enumerate(_ context.Context, _ map[string]string, cmd []string) (string, error) {
	key := strings.Join(cmd, " ")
	v.mu.Lock()
	v.seen = append(v.seen, key)
	v.mu.Unlock()
	if v.block[key] {
		// Ignores the context on purpose: a runner that does not honour it
		// must still not stall the audit.
		time.Sleep(5 * time.Second)
		return "", nil
	}
	if err := v.fail[key]; err != nil {
		return "", err
	}
	return v.out[key], nil
}

func toolchainExec(j *versionJail, checkArgv []string) *localExecutor {
	return &localExecutor{
		repoDir: "", checkArgv: checkArgv, substrate: substrateJail,
		toolchainRunner: j, toolchainBound: 150 * time.Millisecond, toolchainBudget: 5 * time.Second,
	}
}

func TestToolchainVersionsAreTheFirstLineOfEachCommand(t *testing.T) {
	j := &versionJail{out: map[string]string{
		"node --version": "v24.22.1\n",
		"node ./node_modules/vitest/vitest.mjs --version": "vitest/5.0.3 linux-x64 node-v24.22.1\nextra\n",
	}}
	l := toolchainExec(j, []string{"node", "./node_modules/vitest/vitest.mjs", "run"})
	got := l.toolchainVersions(context.Background(), "typescript")
	if got["node"] != "v24.22.1" || got["vitest"] != "vitest/5.0.3 linux-x64 node-v24.22.1" {
		t.Errorf("toolchainVersions = %v", got)
	}
}

func TestARunnerRowWithNoVersionCommandSaysSoInsteadOfBeingAbsent(t *testing.T) {
	j := &versionJail{out: map[string]string{"node --version": "v22.23.4\n"}}
	l := toolchainExec(j, []string{"mocha", "--bail"})
	got := l.toolchainVersions(context.Background(), "javascript")
	if want := "not read: the mocha row has no version command"; got["mocha"] != want {
		t.Errorf("mocha = %q, want %q (all: %v)", got["mocha"], want, got)
	}
}

// Review Focus 4: a toolchain that cannot be read costs a line in the record,
// never the audit.
func TestAToolchainThatCannotBeReadDoesNotStopTheRun(t *testing.T) {
	j := &versionJail{
		fail:  map[string]error{"python3 --version": errors.New("python3: not found")},
		block: map[string]bool{"node --version": true},
		out:   map[string]string{"python3 -m pytest --version": "pytest 9.1.1\n"},
	}
	l := toolchainExec(j, nil)
	start := time.Now()
	py := l.toolchainVersions(context.Background(), "python")
	js := l.toolchainVersions(context.Background(), "javascript")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("reading took %s: the bound did not hold", d)
	}
	// The reason is fixed by the error's class, never the error's text: that
	// text names argv[0] and host paths, and the record is published.
	if want := "not read: the python3 version command could not be run"; py["python3"] != want {
		t.Errorf("python3 = %q, want %q", py["python3"], want)
	}
	if py["pytest"] != "pytest 9.1.1" {
		t.Errorf("pytest = %q: one failing command must not hide the others", py["pytest"])
	}
	if !strings.HasPrefix(js["node"], "not read: ") {
		t.Errorf("node = %q, want a not-read line for a command that never returned", js["node"])
	}
	// And the record still builds: the scan goes on to grade.
	raw := l.toolchainsJSON(context.Background(), []string{"a.py", "b.js"})
	var rec map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("toolchainsJSON = %q: %v", raw, err)
	}
	if rec["python"]["pytest"] != "pytest 9.1.1" || !strings.HasPrefix(rec["javascript"]["node"], "not read: ") {
		t.Errorf("record = %v", rec)
	}
}

func TestNoBoundMeansNothingIsRead(t *testing.T) {
	j := &versionJail{out: map[string]string{"go version": "go version go1.26.10 linux/amd64\n"}}
	l := toolchainExec(j, nil)
	l.toolchainBound = 0
	got := l.toolchainVersions(context.Background(), "go")
	if !strings.HasPrefix(got["go"], "not read: ") || len(j.seen) != 0 {
		t.Errorf("got %v after running %v: with no bound set nothing may run", got, j.seen)
	}
}

func TestNilExecutorRecordsNoToolchains(t *testing.T) {
	var l *localExecutor
	if got := l.toolchainsJSON(context.Background(), []string{"a.py"}); got != "" {
		t.Errorf("toolchainsJSON on no executor = %q, want empty", got)
	}
}

// One budget covers every language and tool: a fake that sleeps past it, across
// two languages, costs the budget and no more, and what it cut off says so.
func TestTheOverallBudgetBoundsTheWholeRead(t *testing.T) {
	j := &versionJail{block: map[string]bool{
		"python3 --version": true, "python3 -m pytest --version": true,
		"node --version": true,
	}}
	l := toolchainExec(j, nil)
	l.toolchainBound = 10 * time.Second // the per-command bound alone would allow 30s+
	l.toolchainBudget = 200 * time.Millisecond
	start := time.Now()
	raw := l.toolchainsJSON(context.Background(), []string{"a.py", "b.js"})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the read took %s with a 200ms overall budget", d)
	}
	var rec map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("toolchainsJSON = %q: %v", raw, err)
	}
	want := "not read: the version read's overall budget ran out"
	for _, c := range []struct{ lang, tool string }{{"python", "python3"}, {"python", "pytest"}, {"javascript", "node"}} {
		if rec[c.lang][c.tool] != want {
			t.Errorf("%s/%s = %q, want %q (record %v)", c.lang, c.tool, rec[c.lang][c.tool], want, rec)
		}
	}
}

func TestAnInterruptedReadSaysSo(t *testing.T) {
	j := &versionJail{out: map[string]string{"node --version": "v22.23.4\n"}}
	l := toolchainExec(j, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := l.toolchainVersions(ctx, "javascript")["node"]; got != "not read: interrupted" {
		t.Errorf("node = %q, want not read: interrupted", got)
	}
}

func TestAnEmptyErrorStillGivesAReason(t *testing.T) {
	j := &versionJail{fail: map[string]error{"ruby --version": errors.New("")}, out: map[string]string{}}
	got := toolchainExec(j, nil).toolchainVersions(context.Background(), "ruby")
	if got["ruby"] != "not read: the ruby version command could not be run" {
		t.Errorf("ruby = %q", got["ruby"])
	}
	j2 := &versionJail{out: map[string]string{"go version": "  \n"}}
	got = toolchainExec(j2, nil).toolchainVersions(context.Background(), "go")
	if got["go"] != "not read: the go version command printed nothing" {
		t.Errorf("go = %q", got["go"])
	}
}

// A jail the run never prepared is not built here: the read must not spend
// unbounded time seeding a repository after the audit.
func TestNoJailPreparedMeansNothingIsBuiltOrRun(t *testing.T) {
	built := 0
	l := &localExecutor{
		substrate: substrateJail, toolchainBound: time.Second, toolchainBudget: time.Second,
		seeds: newSeedCache(func(string) (repoSeed, error) { built++; return repoSeed{}, nil }),
	}
	got := l.toolchainVersions(context.Background(), "python")
	want := "not read: no jail was prepared for python in this run"
	if got["python3"] != want || got["pytest"] != want {
		t.Errorf("got %v, want every tool %q", got, want)
	}
	if built != 0 {
		t.Errorf("the read built %d seed(s); it must only use one the run already built", built)
	}
	if _, ok := l.seeds.peek("python"); ok {
		t.Error("peek reported a seed nobody built")
	}
}

func TestLocalToolchainsNeverSilentlyAbsent(t *testing.T) {
	got := localToolchainsNotRead("go")
	want := `{"go":{"toolchains":"not read: certify --local does not read toolchain versions yet"}}`
	if got != want {
		t.Errorf("localToolchainsNotRead = %q, want %q", got, want)
	}
}

// A version command that exits non-zero printed an error, not a version: a
// conftest ImportError's first line must never be recorded as pytest's
// version. The jail's Enumerate returns no error for a non-zero exit, so only
// the exit code tells the two apart.
func TestAVersionCommandThatExitsNonZeroIsNotRecordedAsAVersion(t *testing.T) {
	j := &versionJail{
		out: map[string]string{
			"python3 --version":           "Python 3.14.1\n",
			"python3 -m pytest --version": "ImportError while loading conftest '/repo/conftest.py'.\n",
		},
		exit: map[string]int{"python3 -m pytest --version": 4},
	}
	got := toolchainExec(j, nil).toolchainVersions(context.Background(), "python")
	if want := "not read: the pytest version command exited 4"; got["pytest"] != want {
		t.Errorf("pytest = %q, want %q", got["pytest"], want)
	}
	if got["python3"] != "Python 3.14.1" {
		t.Errorf("python3 = %q: an exit 0 is still a version", got["python3"])
	}
}
