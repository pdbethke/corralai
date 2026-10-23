// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type reply struct {
	out   []byte
	err   error
	delay time.Duration
}

// fakeRunner answers by joined argv and records every call. Safe for the
// concurrent calls Detect makes.
type fakeRunner struct {
	mu      sync.Mutex
	replies map[string]reply
	calls   []string
}

func (f *fakeRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	key := strings.Join(argv, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	r, ok := f.replies[key]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("fakeRunner: unexpected argv " + key)
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.out, r.err
}

func (f *fakeRunner) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func exit1(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	return err
}

// absentRunner answers every inspect with its absent fixture, as the real
// agent would (a nonzero exit for the get-style verbs).
func absentRunner(t *testing.T) *fakeRunner {
	f := &fakeRunner{replies: map[string]reply{}}
	for _, a := range Agents() {
		if a.Inspect == nil {
			continue
		}
		r := reply{out: fixture(t, a.ID, "inspect-absent.txt")}
		if a.AbsentOnExit1 {
			r.err = exit1(t)
		}
		f.replies[strings.Join(a.Inspect, " ")] = r
	}
	f.replies["claude plugin list"] = reply{out: fixture(t, "claude_code", "plugin-absent.txt")}
	return f
}

func allOn(string) (string, error) { return "/usr/bin/x", nil }

func byID(t *testing.T, ss []Status, id string) Status {
	t.Helper()
	for _, s := range ss {
		if s.Agent.ID == id {
			return s
		}
	}
	t.Fatalf("no status for %s", id)
	return Status{}
}

func TestDetectAllAbsent(t *testing.T) {
	ss := Detect(context.Background(), absentRunner(t), t.TempDir(), allOn)
	if len(ss) != 8 {
		t.Fatalf("%d statuses", len(ss))
	}
	for _, s := range ss {
		if !s.Installed || s.Registered || s.Unknown != "" || s.ViaPlugin || s.Skill != "" {
			t.Errorf("%s: %+v", s.Agent.ID, s)
		}
	}
}

func TestDetectPresentReportsCommand(t *testing.T) {
	f := absentRunner(t)
	a, _ := ByID("codex")
	f.replies[strings.Join(a.Inspect, " ")] = reply{out: fixture(t, "codex", "inspect-present.txt")}
	s := byID(t, Detect(context.Background(), f, t.TempDir(), allOn), "codex")
	if !s.Registered || len(s.Command) != 2 || s.Command[0] != "__CORRAL__" {
		t.Errorf("%+v", s)
	}
}

func TestDetectNonzeroExitWithoutAbsentRuleIsUnknown(t *testing.T) {
	f := absentRunner(t)
	a, _ := ByID("antigravity")
	f.replies[strings.Join(a.Inspect, " ")] = reply{out: []byte("boom"), err: exit1(t)}
	s := byID(t, Detect(context.Background(), f, t.TempDir(), allOn), "antigravity")
	if s.Registered || !strings.Contains(s.Unknown, "boom") {
		t.Errorf("%+v", s)
	}
}

func TestDetectTimeoutIsUnknown(t *testing.T) {
	f := absentRunner(t)
	a, _ := ByID("gemini_cli")
	f.replies[strings.Join(a.Inspect, " ")] = reply{err: context.DeadlineExceeded}
	ss := Detect(context.Background(), f, t.TempDir(), allOn)
	s := byID(t, ss, "gemini_cli")
	if s.Unknown == "" || s.Registered {
		t.Errorf("%+v", s)
	}
	for _, o := range ss {
		if o.Agent.ID != "gemini_cli" && (o.Unknown != "" || !o.Installed) {
			t.Errorf("%s was disturbed: %+v", o.Agent.ID, o)
		}
	}
}

func TestDetectCursorTimeoutDoesNotFallBackToFile(t *testing.T) {
	home := t.TempDir()
	cur, _ := ByID("cursor")
	p := cur.File.Path(home)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, fixture(t, "cursor", "mcp.json"), 0o600)
	f := absentRunner(t)
	f.replies[strings.Join(cur.Inspect, " ")] = reply{err: context.DeadlineExceeded}
	s := byID(t, Detect(context.Background(), f, home, allOn), "cursor")
	if s.Unknown == "" || s.Registered {
		t.Errorf("%+v", s)
	}
}

func TestDetectCursorFallsBackToFileWhenListCannotRun(t *testing.T) {
	home := t.TempDir()
	cur, _ := ByID("cursor")
	p := cur.File.Path(home)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, fixture(t, "cursor", "mcp.json"), 0o600)
	f := absentRunner(t)
	f.replies[strings.Join(cur.Inspect, " ")] = reply{err: errors.New("cannot start")}
	s := byID(t, Detect(context.Background(), f, home, allOn), "cursor")
	if !s.Registered || len(s.Command) != 2 {
		t.Errorf("%+v", s)
	}
}

func TestDetectVSCodeReadsItsFile(t *testing.T) {
	home := t.TempDir()
	vs, _ := ByID("vscode")
	p := vs.File.Path(home)
	if s := byID(t, Detect(context.Background(), absentRunner(t), home, allOn), "vscode"); s.Registered || s.Unknown != "" {
		t.Errorf("missing file: %+v", s)
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, fixture(t, "vscode", "mcp.json"), 0o600)
	s := byID(t, Detect(context.Background(), absentRunner(t), home, allOn), "vscode")
	if !s.Registered || s.Command[0] != "__CORRAL__" {
		t.Errorf("present: %+v", s)
	}
	_ = os.WriteFile(p, fixture(t, "vscode", "inspect-removed.txt"), 0o600)
	if s := byID(t, Detect(context.Background(), absentRunner(t), home, allOn), "vscode"); s.Registered || s.Unknown != "" {
		t.Errorf("removed: %+v", s)
	}
	_ = os.WriteFile(p, []byte("{ not json"), 0o600)
	if s := byID(t, Detect(context.Background(), absentRunner(t), home, allOn), "vscode"); s.Registered || s.Unknown == "" {
		t.Errorf("unparseable: %+v", s)
	}
}

func TestDetectClaudePlugin(t *testing.T) {
	for _, tc := range []struct {
		fix  string
		want bool
	}{{"plugin-absent.txt", false}, {"plugin-present.txt", true}, {"plugin-removed.txt", false}} {
		f := absentRunner(t)
		f.replies["claude plugin list"] = reply{out: fixture(t, "claude_code", tc.fix)}
		ss := Detect(context.Background(), f, t.TempDir(), allOn)
		if got := byID(t, ss, "claude_code").ViaPlugin; got != tc.want {
			t.Errorf("%s: ViaPlugin = %v", tc.fix, got)
		}
		for _, s := range ss {
			if s.Agent.ID != "claude_code" && s.ViaPlugin {
				t.Errorf("%s: ViaPlugin set", s.Agent.ID)
			}
		}
	}
	f := absentRunner(t)
	f.replies["claude plugin list"] = reply{out: []byte("  ❯ corral@corralai\n    Status: ✘ disabled\n")}
	if byID(t, Detect(context.Background(), f, t.TempDir(), allOn), "claude_code").ViaPlugin {
		t.Error("a disabled plugin is not a route")
	}
}

func TestDetectNotInstalledSkipsInspect(t *testing.T) {
	f := absentRunner(t)
	look := func(n string) (string, error) {
		if n == "codex" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + n, nil
	}
	s := byID(t, Detect(context.Background(), f, t.TempDir(), look), "codex")
	if s.Installed || s.Registered || s.Unknown != "" || f.called("codex") {
		t.Errorf("%+v calls=%v", s, f.calls)
	}
}

func TestDetectReturnsTableOrder(t *testing.T) {
	f := absentRunner(t)
	for i, a := range Agents() {
		if a.Inspect == nil {
			continue
		}
		k := strings.Join(a.Inspect, " ")
		r := f.replies[k]
		r.delay = time.Duration(8-i) * 15 * time.Millisecond // first agent slowest
		f.replies[k] = r
	}
	start := time.Now()
	ss := Detect(context.Background(), f, t.TempDir(), allOn)
	for i, id := range wantIDs {
		if ss[i].Agent.ID != id {
			t.Fatalf("position %d = %s, want %s", i, ss[i].Agent.ID, id)
		}
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("took %v: agents were not run concurrently", d)
	}
}

func TestExecRunnerRunsFixedArgvAndTimesOut(t *testing.T) {
	out, err := ExecRunner{Timeout: Timeout}.Run(context.Background(), []string{"sh", "-c", "echo hi; echo err >&2"})
	if err != nil || !strings.Contains(string(out), "hi") || !strings.Contains(string(out), "err") {
		t.Errorf("out=%q err=%v", out, err)
	}
	_, err = ExecRunner{Timeout: 50 * time.Millisecond}.Run(context.Background(), []string{"sleep", "5"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if _, err := (ExecRunner{Timeout: Timeout}).Run(context.Background(), nil); err == nil {
		t.Error("empty argv accepted")
	}
}
