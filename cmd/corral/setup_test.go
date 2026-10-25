// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/agentsetup"
)

const fakeBin = "/opt/corral/corral"

// stateful stands in for the codex CLI: absent until its add runs, then it
// lists the server with the launch command that was added.
type setupFake struct {
	mu    sync.Mutex
	added bool
	calls []string
}

func (f *setupFake) Run(_ context.Context, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(argv, " "))
	switch strings.Join(argv, " ") {
	case "codex mcp get corral":
		if f.added {
			return []byte("corral\n  enabled: true\n  transport: stdio\n  command: " + fakeBin + "\n  args: mcp\n"), nil
		}
		return []byte("Error: No MCP server named 'corral' found.\n"), exec.Command("sh", "-c", "exit 1").Run()
	case "codex mcp add corral -- " + fakeBin + " mcp":
		f.added = true
		return nil, nil
	case "codex mcp remove corral":
		f.added = false
		return nil, nil
	}
	return nil, errors.New("unexpected argv " + strings.Join(argv, " "))
}

func (f *setupFake) mutated() bool {
	for _, c := range f.calls {
		if strings.Contains(c, " mcp add ") || strings.Contains(c, " mcp remove ") {
			return true
		}
	}
	return false
}

type testSetup struct {
	env    setupEnv
	run    *setupFake
	home   string
	state  string
	checks int
}

func newTestSetup(t *testing.T, tty bool) *testSetup {
	t.Helper()
	ts := &testSetup{run: &setupFake{}, home: t.TempDir()}
	// The agent table reads XDG_CONFIG_HOME (opencode's paths); no test may
	// reach the operator's real configuration.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	ts.state = filepath.Join(t.TempDir(), "state", "setup.json")
	ts.env = setupEnv{
		Home:   ts.home,
		Runner: ts.run,
		LookPath: func(n string) (string, error) {
			if n == "codex" {
				return "/usr/bin/codex", nil
			}
			return "", errors.New("not found")
		},
		IsTTY:   func() bool { return tty },
		Exe:     fakeBin,
		OnPath:  fakeBin,
		TmpDir:  "/nonexistent-tmp",
		Version: "test",
		StatePath: func(write bool) (string, error) {
			if write {
				if err := os.MkdirAll(filepath.Dir(ts.state), 0o700); err != nil {
					return "", err
				}
			}
			return ts.state, nil
		},
		CheckMCP: func(context.Context, string) error { ts.checks++; return nil },
	}
	return ts
}

func (ts *testSetup) run_(args []string, stdin string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runSetupWith(args, strings.NewReader(stdin), &out, &errb, func() (setupEnv, error) { return ts.env, nil })
	return code, out.String(), errb.String()
}

func TestSetupRefusesNoSelectionWithoutTTY(t *testing.T) {
	ts := newTestSetup(t, false)
	code, _, errs := ts.run_(nil, "")
	if code != 2 || !strings.Contains(errs, "--client") || !strings.Contains(errs, "--all-detected") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestSetupUnknownClient(t *testing.T) {
	ts := newTestSetup(t, false)
	code, _, errs := ts.run_([]string{"--client", "nope"}, "")
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	for _, id := range []string{"claude_code", "codex", "gemini_cli", "cursor", "opencode", "antigravity", "copilot_cli", "vscode"} {
		if !strings.Contains(errs, id) {
			t.Errorf("stderr does not list %s: %q", id, errs)
		}
	}
}

func TestSetupSkillFalseRefusedWithUninstall(t *testing.T) {
	ts := newTestSetup(t, false)
	if code, _, _ := ts.run_([]string{"--uninstall", "--skill=false", "--client", "codex"}, ""); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
}

func TestSetupHelpDetectsNothing(t *testing.T) {
	ts := newTestSetup(t, false)
	var out, errb bytes.Buffer
	code := runSetupWith([]string{"-h"}, strings.NewReader(""), &out, &errb, func() (setupEnv, error) {
		t.Fatal("-h must not build the environment")
		return setupEnv{}, nil
	})
	if code != 0 || !strings.Contains(errb.String(), "Exit codes") || len(ts.run.calls) != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestResolveBinaryRefusesTemp(t *testing.T) {
	_, err := resolveSetupBinary("/tmp/go-build123/b001/exe/corral", "", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveBinaryRefusesMismatch(t *testing.T) {
	_, err := resolveSetupBinary("/opt/a/corral", "/usr/local/bin/corral", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "/opt/a/corral") || !strings.Contains(err.Error(), "/usr/local/bin/corral") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveBinaryNotOnPath(t *testing.T) {
	got, err := resolveSetupBinary("/opt/a/corral", "", "/tmp")
	if err != nil || got != "/opt/a/corral" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSetupSaysWhenNotOnPath(t *testing.T) {
	ts := newTestSetup(t, false)
	ts.env.OnPath = ""
	_, out, _ := ts.run_([]string{"--client", "codex", "--dry-run"}, "")
	if !strings.Contains(out, "corral is not on your shell's PATH; agents will launch "+fakeBin) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	ts := newTestSetup(t, false)
	code, out, errs := ts.run_([]string{"--client", "codex", "--dry-run"}, "")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if !strings.Contains(out, "codex mcp add corral -- "+fakeBin+" mcp") {
		t.Errorf("plan not printed: %q", out)
	}
	if ts.run.mutated() {
		t.Errorf("runner saw a mutating call: %v", ts.run.calls)
	}
	for _, c := range ts.run.calls {
		if !strings.HasPrefix(c, "codex mcp get") {
			t.Errorf("non-inspect call %q", c)
		}
	}
	for _, root := range []string{ts.home, filepath.Dir(ts.state)} {
		entries, _ := os.ReadDir(root)
		if len(entries) != 0 {
			t.Errorf("%s was written to: %v", root, entries)
		}
	}
}

func TestSetupNeedsApproval(t *testing.T) {
	ts := newTestSetup(t, false)
	code, out, _ := ts.run_([]string{"--client", "codex"}, "n\n")
	if code != 1 || ts.run.mutated() || !strings.Contains(out, "Apply this plan? [y/N]") {
		t.Fatalf("declined: code=%d mutated=%v out=%q", code, ts.run.mutated(), out)
	}
	if _, err := os.Stat(ts.state); err == nil {
		t.Errorf("state written after declining")
	}

	ts = newTestSetup(t, false)
	code, out, errs := ts.run_([]string{"--client", "codex", "--yes"}, "")
	if code != 0 || !ts.run.added {
		t.Fatalf("--yes: code=%d added=%v out=%q err=%q", code, ts.run.added, out, errs)
	}
	// codex is proven (docs/corral/agent-setup.md), so its result line carries no label.
	if !strings.Contains(out, "verified") || !strings.Contains(out, "mcp answered") || strings.Contains(out, "not yet proven") {
		t.Errorf("result line: %q", out)
	}
	if ts.checks != 1 {
		t.Errorf("CheckMCP ran %d times, want 1", ts.checks)
	}
	if _, err := os.Stat(filepath.Join(ts.home, ".codex", "skills", "corral", ".corral-setup.json")); err != nil {
		t.Errorf("skill marker missing: %v", err)
	}
	if !strings.Contains(out, "Start a new Codex session.") {
		t.Errorf("restart note missing: %q", out)
	}
}

func TestSetupAlreadyWiredIsNotRegistered(t *testing.T) {
	ts := newTestSetup(t, false)
	ts.run.added = true
	code, out, _ := ts.run_([]string{"--client", "codex", "--skill=false"}, "")
	if code != 0 || ts.run.mutated() {
		t.Fatalf("code=%d calls=%v", code, ts.run.calls)
	}
	if !strings.Contains(out, "already registered") || strings.Contains(out, "verified") {
		t.Errorf("out = %q", out)
	}
}

func TestSetupUninstallRemovesWhatSetupAdded(t *testing.T) {
	ts := newTestSetup(t, false)
	if code, _, e := ts.run_([]string{"--client", "codex", "--yes"}, ""); code != 0 {
		t.Fatalf("setup: %d %q", code, e)
	}
	code, out, e := ts.run_([]string{"--uninstall", "--client", "codex", "--yes"}, "")
	if code != 0 || ts.run.added {
		t.Fatalf("code=%d added=%v out=%q err=%q", code, ts.run.added, out, e)
	}
	if _, err := os.Stat(filepath.Join(ts.home, ".codex", "skills", "corral")); err == nil {
		t.Errorf("skill copy still present")
	}
	if ts.checks != 1 {
		t.Errorf("uninstall ran CheckMCP (checks=%d)", ts.checks)
	}
}

func TestSetupMCPFailureIsUnverified(t *testing.T) {
	ts := newTestSetup(t, false)
	ts.env.CheckMCP = func(context.Context, string) error { return errors.New("no handshake") }
	code, out, _ := ts.run_([]string{"--client", "codex", "--yes"}, "")
	if code != 1 || !strings.Contains(out, "NOT verified") || !strings.Contains(out, "no handshake") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestInteractiveListUnticksNewlyDetected(t *testing.T) {
	ag := agentsetup.Agents()
	st := []agentsetup.Status{
		{Agent: ag[0], Installed: true, ViaPlugin: true}, // claude_code wired by the plugin
		{Agent: ag[1], Installed: true},                  // codex detected, not wired
		{Agent: ag[2], Installed: true, Registered: true},
		{Agent: ag[3]},
	}
	var b bytes.Buffer
	got, err := pickAgents(bufio.NewReader(strings.NewReader("\n")), &b, st)
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"1. [x] Claude Code", "2. [ ] Codex CLI", "3. [x] Gemini CLI", "4. [ ] Cursor (not installed)", "(not yet proven)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Join(got, ",") != "claude_code,gemini_cli" {
		t.Errorf("Enter accepted %v, want only the wired agents", got)
	}
	got, _ = pickAgents(bufio.NewReader(strings.NewReader("2 1\n\n")), &bytes.Buffer{}, st)
	if strings.Join(got, ",") != "codex,gemini_cli" {
		t.Errorf("toggling 2 and 1 gave %v", got)
	}

	// End to end: toggling 2 on then accepting selects codex.
	ts := newTestSetup(t, true)
	code, out, errs := ts.run_([]string{"--dry-run"}, "2\n\n")
	if code != 0 || !strings.Contains(out, "mcp add corral") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestInteractiveEmptySelectionDoesNothing(t *testing.T) {
	ts := newTestSetup(t, true)
	code, out, _ := ts.run_(nil, "\n")
	if code != 0 || !strings.Contains(out, "nothing to do") || ts.run.mutated() {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestUsageListsSetup(t *testing.T) {
	if !strings.Contains(usageText(), "corral setup") {
		t.Fatal("usageText does not mention corral setup")
	}
}

func TestSetupAllDetectedSelectsInstalledOnly(t *testing.T) {
	ts := newTestSetup(t, false)
	code, out, errs := ts.run_([]string{"--all-detected", "--dry-run"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if !strings.Contains(out, "codex") || strings.Contains(out, "gemini_cli") {
		t.Fatalf("plan should name only the installed codex:\n%s", out)
	}
}

func TestSetupUninstallAfterSkillOnlySetupLeavesRegistration(t *testing.T) {
	ts := newTestSetup(t, false)
	ts.run.added = true // the person's own registration pre-exists
	if code, out, _ := ts.run_([]string{"--client", "codex", "--yes"}, ""); code != 0 {
		t.Fatalf("setup: %d %q", code, out)
	}
	code, out, _ := ts.run_([]string{"--uninstall", "--client", "codex", "--yes"}, "")
	if code != 0 || !ts.run.added {
		t.Fatalf("code=%d added=%v out=%q", code, ts.run.added, out)
	}
	if !strings.Contains(out, "registration left alone") || !strings.Contains(out, "skill removed") || strings.Contains(out, "still registered") {
		t.Errorf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(ts.home, ".codex", "skills", "corral")); err == nil {
		t.Errorf("skill copy still present")
	}
}

func TestSetupUninstallNothingToRemoveExitsZero(t *testing.T) {
	ts := newTestSetup(t, false)
	if code, _, _ := ts.run_([]string{"--client", "codex", "--yes"}, ""); code != 0 {
		t.Fatal("setup failed")
	}
	ts.run.added = false // the registration vanished behind setup's back
	code, out, _ := ts.run_([]string{"--uninstall", "--client", "codex", "--yes"}, "")
	if code != 0 || !strings.Contains(out, "no longer registered") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestSetupBlockedNoopExitsOne(t *testing.T) {
	ts := newTestSetup(t, false)
	code, out, _ := ts.run_([]string{"--client", "gemini_cli", "--yes"}, "")
	if code != 1 || !strings.Contains(out, "not installed") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestResultLineUninstallUnmanagedSkillIsNotRemoved(t *testing.T) {
	ag, _ := agentsetup.ByID("codex")
	plan := agentsetup.Plan{Actions: []agentsetup.Action{{AgentID: "codex", Kind: agentsetup.ActionRemoveSkill, Path: "/x"}}}
	line, good := resultLine("codex", setupFlags{uninstall: true, skill: true}, "", plan, true,
		agentsetup.Result{AgentID: "codex", OK: true}, agentsetup.Status{Agent: ag, Skill: "unmanaged"}, false, nil)
	if good || !strings.Contains(line, "skill left in place (unmanaged)") || strings.Contains(line, "skill removed") {
		t.Fatalf("good=%v line=%q", good, line)
	}
}

func TestResolveBinaryRefusesTempThroughSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "var")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	_, err := resolveSetupBinary(filepath.Join(resolved, "go-build", "corral"), "", link)
	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("a binary under the resolved temp dir escaped the refusal: %v", err)
	}
}

func TestResolveOnPath(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	ok := func(string) (string, error) { return "/usr/bin/corral", nil }
	bad := func(string) (string, error) { return "", errors.New("loop") }
	if p, err := resolveOnPath(notFound, bad); p != "" || err != nil {
		t.Errorf("not on PATH: %q, %v", p, err)
	}
	if p, err := resolveOnPath(ok, func(s string) (string, error) { return "/opt/corral", nil }); p != "/opt/corral" || err != nil {
		t.Errorf("resolved: %q, %v", p, err)
	}
	if _, err := resolveOnPath(ok, bad); err == nil || !strings.Contains(err.Error(), "/usr/bin/corral") {
		t.Errorf("an unresolvable PATH hit must be an error, got %v", err)
	}
}

func TestSetupSkillFalseRegistersWithoutSkill(t *testing.T) {
	ts := newTestSetup(t, false)
	code, out, errs := ts.run_([]string{"--client", "codex", "--yes", "--skill=false"}, "")
	if code != 0 || !ts.run.added {
		t.Fatalf("code=%d added=%v out=%q err=%q", code, ts.run.added, out, errs)
	}
	if _, err := os.Stat(filepath.Join(ts.home, ".codex", "skills", "corral")); err == nil {
		t.Errorf("skill dir created despite --skill=false")
	}
	if !strings.Contains(out, "skill skipped (--skill=false)") {
		t.Errorf("out = %q", out)
	}
}
