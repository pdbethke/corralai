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
)

// versionJail answers a version command by its argv, the way the jail the
// run already built would: some commands print, one fails, one never returns.
type versionJail struct {
	mu    sync.Mutex
	out   map[string]string
	fail  map[string]error
	block map[string]bool
	seen  []string
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
		toolchainRunner: j, toolchainBound: 150 * time.Millisecond,
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
	l := toolchainExec(j, []string{"node", "./node_modules/jest/bin/jest.js"})
	got := l.toolchainVersions(context.Background(), "javascript")
	if want := "not read: the jest row has no version command"; got["jest"] != want {
		t.Errorf("jest = %q, want %q (all: %v)", got["jest"], want, got)
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
	if !strings.HasPrefix(py["python3"], "not read: ") || !strings.Contains(py["python3"], "not found") {
		t.Errorf("python3 = %q, want a not-read line naming the reason", py["python3"])
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
