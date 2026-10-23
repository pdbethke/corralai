// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func applyOne(t *testing.T, r Runner, p Plan, home string, st *State) []Result {
	t.Helper()
	return Apply(context.Background(), r, p, home, filepath.Join(t.TempDir(), "setup-state.json"), tbin, "vtest", st)
}

func TestApplyOneFailureDoesNotStopOthers(t *testing.T) {
	codex := []string{"codex", "mcp", "add", "corral", "--", tbin, "mcp"}
	gemini := []string{"gemini", "mcp", "add", "-s", "user", "corral", tbin, "mcp"}
	f := &fakeRunner{replies: map[string]reply{
		strings.Join(codex, " "):  {out: []byte("boom: no such server"), err: errors.New("exit 1")},
		strings.Join(gemini, " "): {out: []byte("ok")},
	}}
	p := Plan{Actions: []Action{
		{AgentID: "codex", Kind: ActionRegister, Argv: codex},
		{AgentID: "gemini_cli", Kind: ActionRegister, Argv: gemini},
	}}
	st := State{Agents: map[string]AgentRecord{}}
	res := applyOne(t, f, p, t.TempDir(), &st)
	if len(res) != 2 || res[0].OK || !strings.Contains(res[0].Detail, "boom") || !res[1].OK {
		t.Fatalf("%+v", res)
	}
	if _, ok := st.Agents["codex"]; ok {
		t.Error("a failed agent must not be recorded")
	}
	if _, ok := st.Agents["gemini_cli"]; !ok || len(st.Agents) != 1 {
		t.Errorf("%+v", st.Agents)
	}
}

func TestApplyRecordsState(t *testing.T) {
	argv := []string{"codex", "mcp", "add", "corral", "--", tbin, "mcp"}
	f := &fakeRunner{replies: map[string]reply{strings.Join(argv, " "): {}}}
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "skills", "corral")
	p := Plan{Actions: []Action{
		{AgentID: "codex", Kind: ActionRegister, Argv: argv},
		{AgentID: "codex", Kind: ActionWriteSkill, Path: dir},
	}}
	st := State{Agents: map[string]AgentRecord{}}
	statePath := filepath.Join(t.TempDir(), "s.json")
	res := Apply(context.Background(), f, p, home, statePath, tbin, "vtest", &st)
	if !res[0].OK {
		t.Fatalf("%+v", res)
	}
	rec := st.Agents["codex"]
	if len(rec.RegisterArgv) != 1 || strings.Join(rec.RegisterArgv[0], " ") != strings.Join(argv, " ") ||
		strings.Join(rec.Command, " ") != tbin+" mcp" || rec.SkillDir != dir {
		t.Errorf("%+v", rec)
	}
	if skillState(dir) != "managed" {
		t.Errorf("skill %s", skillState(dir))
	}
	saved, err := LoadState(statePath)
	if err != nil || saved.Agents["codex"].SkillDir != dir {
		t.Errorf("state not persisted: %+v %v", saved, err)
	}
}

func TestApplyRemoveFailureSkipsReplace(t *testing.T) {
	rm := []string{"claude", "mcp", "remove", "-s", "user", "corral"}
	add := []string{"claude", "mcp", "add", "-s", "user", "corral", "--", tbin, "mcp"}
	f := &fakeRunner{replies: map[string]reply{
		strings.Join(rm, " "):  {out: []byte("cannot remove"), err: errors.New("exit 1")},
		strings.Join(add, " "): {},
	}}
	p := Plan{Actions: []Action{
		{AgentID: "claude_code", Kind: ActionRemove, Argv: rm},
		{AgentID: "claude_code", Kind: ActionReplace, Argv: add},
	}}
	st := State{Agents: map[string]AgentRecord{}}
	res := applyOne(t, f, p, t.TempDir(), &st)
	if res[0].OK || !strings.Contains(res[0].Detail, "cannot remove") {
		t.Fatalf("%+v", res)
	}
	for _, c := range f.calls {
		if c == strings.Join(add, " ") {
			t.Error("replace ran after a failed remove")
		}
	}
	if len(st.Agents) != 0 {
		t.Errorf("%+v", st.Agents)
	}
}

func TestApplyCursorBacksUpFirst(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{\n  \"mcpServers\": {\n    \"other\": {\"command\": \"x\"}\n  }\n}\n")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	enable := []string{"cursor-agent", "mcp", "enable", "corral"}
	f := &fakeRunner{replies: map[string]reply{strings.Join(enable, " "): {}}}
	p := Plan{Actions: []Action{
		{AgentID: "cursor", Kind: ActionWriteFile, Path: path},
		{AgentID: "cursor", Kind: ActionEnable, Argv: enable},
	}}
	st := State{Agents: map[string]AgentRecord{}}
	res := applyOne(t, f, p, home, &st)
	if !res[0].OK {
		t.Fatalf("%+v", res)
	}
	bk, err := os.ReadFile(path + ".corral.backup")
	if err != nil || string(bk) != string(orig) {
		t.Fatalf("backup %q %v", bk, err)
	}
	now, _ := os.ReadFile(path)
	if !strings.Contains(string(now), `"corral"`) || !strings.Contains(string(now), `"other"`) {
		t.Errorf("%s", now)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(path + ".corral.backup"); fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", fi.Mode().Perm())
	}
	rec := st.Agents["cursor"]
	if strings.Join(rec.Command, " ") != tbin+" mcp" || len(rec.RegisterArgv) != 1 || rec.RegisterArgv[0][2] != "enable" {
		t.Errorf("%+v", rec)
	}
}

func TestApplyKeepsFirstBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := editConfigFile(path, func(old []byte) ([]byte, error) { return []byte(`{"a":1}`), nil }); err != nil {
		t.Fatal(err)
	}
	if err := editConfigFile(path, func(old []byte) ([]byte, error) { return []byte(`{"a":2}`), nil }); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path + ".corral.backup")
	if string(first) != "{}" {
		t.Errorf("first backup was overwritten: %q", first)
	}
	m, _ := filepath.Glob(path + ".corral.backup.*")
	if len(m) != 1 {
		t.Fatalf("%v", m)
	}
	if b, _ := os.ReadFile(m[0]); string(b) != `{"a":1}` {
		t.Errorf("%q", b)
	}
}

func TestApplyCursorCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "mcp.json")
	enable := []string{"cursor-agent", "mcp", "enable", "corral"}
	f := &fakeRunner{replies: map[string]reply{strings.Join(enable, " "): {}}}
	p := Plan{Actions: []Action{{AgentID: "cursor", Kind: ActionWriteFile, Path: path}, {AgentID: "cursor", Kind: ActionEnable, Argv: enable}}}
	st := State{Agents: map[string]AgentRecord{}}
	if res := applyOne(t, f, p, home, &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(path + ".corral.backup"); err == nil {
		t.Error("no backup expected for a new file")
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"corral"`) {
		t.Errorf("%s", b)
	}
}

func TestApplyUninstallClearsRecordOnlyOnSuccess(t *testing.T) {
	rm := []string{"codex", "mcp", "remove", "corral"}
	f := &fakeRunner{replies: map[string]reply{strings.Join(rm, " "): {out: []byte("nope"), err: errors.New("exit 1")}}}
	st := State{Agents: map[string]AgentRecord{"codex": {Command: []string{tbin, "mcp"}}}}
	p := Plan{Actions: []Action{{AgentID: "codex", Kind: ActionRemove, Argv: rm}}}
	if res := applyOne(t, f, p, t.TempDir(), &st); res[0].OK {
		t.Fatal("expected failure")
	}
	if _, ok := st.Agents["codex"]; !ok {
		t.Fatal("record dropped on failure")
	}
	f.replies[strings.Join(rm, " ")] = reply{}
	if res := applyOne(t, f, p, t.TempDir(), &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
	if _, ok := st.Agents["codex"]; ok {
		t.Fatal("record kept after a successful removal")
	}
}

func TestApplyUnwriteFileBacksUpAndRemovesOnlyCorral(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeCursorConfig([]byte(`{"mcpServers":{"other":{"command":"x"}}}`), tbin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		t.Fatal(err)
	}
	st := State{Agents: map[string]AgentRecord{"cursor": {Command: []string{tbin, "mcp"}}}}
	p := Plan{Actions: []Action{{AgentID: "cursor", Kind: ActionUnwriteFile, Path: path}}}
	if res := applyOne(t, &fakeRunner{}, p, home, &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
	now, _ := os.ReadFile(path)
	if strings.Contains(string(now), "corral") || !strings.Contains(string(now), "other") {
		t.Errorf("%s", now)
	}
	if bk, _ := os.ReadFile(path + ".corral.backup"); string(bk) != string(merged) {
		t.Errorf("backup %q", bk)
	}
	if _, ok := st.Agents["cursor"]; ok {
		t.Error("record kept")
	}
	// A missing file means nothing to do.
	p = Plan{Actions: []Action{{AgentID: "opencode", Kind: ActionUnwriteFile, Path: filepath.Join(home, "none.json")}}}
	st = State{Agents: map[string]AgentRecord{"opencode": {Command: []string{tbin, "mcp"}}}}
	if res := applyOne(t, &fakeRunner{}, p, home, &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
}

func TestApplyRemoveSkillOnUninstall(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "skills", "corral")
	files := map[string][]byte{"SKILL.md": []byte("x")}
	if err := WriteSkill(dir, "v", files); err != nil {
		t.Fatal(err)
	}
	st := State{Agents: map[string]AgentRecord{"codex": {SkillDir: dir}}}
	p := Plan{Actions: []Action{{AgentID: "codex", Kind: ActionRemoveSkill, Path: dir}}}
	if res := applyOne(t, &fakeRunner{}, p, home, &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
	if skillState(dir) != "absent" {
		t.Error("skill still there")
	}
}

func TestApplyTwoRemovesSecondFailsKeepsRecord(t *testing.T) {
	rm1, rm2 := []string{"x", "rm", "1"}, []string{"x", "rm", "2"}
	f := &fakeRunner{replies: map[string]reply{
		"x rm 1": {},
		"x rm 2": {out: []byte("stuck"), err: errors.New("exit 1")},
	}}
	st := State{Agents: map[string]AgentRecord{"codex": {Command: []string{tbin, "mcp"}}}}
	p := Plan{Actions: []Action{{AgentID: "codex", Kind: ActionRemove, Argv: rm1}, {AgentID: "codex", Kind: ActionRemove, Argv: rm2}}}
	statePath := filepath.Join(t.TempDir(), "s.json")
	res := Apply(context.Background(), f, p, t.TempDir(), statePath, tbin, "v", &st)
	if res[0].OK {
		t.Fatal("expected failure")
	}
	if len(st.Agents["codex"].Command) == 0 {
		t.Fatalf("record cleared though a remove failed: %+v", st.Agents)
	}
	if saved, _ := LoadState(statePath); len(saved.Agents["codex"].Command) == 0 {
		t.Fatal("persisted record was cleared")
	}
	f.replies["x rm 2"] = reply{}
	if res := Apply(context.Background(), f, p, t.TempDir(), statePath, tbin, "v", &st); !res[0].OK {
		t.Fatalf("%+v", res)
	}
	if _, ok := st.Agents["codex"]; ok {
		t.Fatal("record kept after all removes succeeded")
	}
}

func TestApplyReplaceFailureAfterRemoveSaysUnregistered(t *testing.T) {
	rm := []string{"claude", "mcp", "remove", "-s", "user", "corral"}
	add := []string{"claude", "mcp", "add", "-s", "user", "corral", "--", tbin, "mcp"}
	f := &fakeRunner{replies: map[string]reply{
		strings.Join(rm, " "):  {},
		strings.Join(add, " "): {out: []byte("add failed"), err: errors.New("exit 1")},
	}}
	st := State{Agents: map[string]AgentRecord{"claude_code": {Command: []string{"/old/corral", "mcp"}}}}
	p := Plan{Actions: []Action{{AgentID: "claude_code", Kind: ActionRemove, Argv: rm}, {AgentID: "claude_code", Kind: ActionReplace, Argv: add}}}
	statePath := filepath.Join(t.TempDir(), "s.json")
	res := Apply(context.Background(), f, p, t.TempDir(), statePath, tbin, "v", &st)
	if res[0].OK || !strings.Contains(res[0].Detail, "UNREGISTERED") || !strings.Contains(res[0].Detail, "add failed") {
		t.Fatalf("%+v", res)
	}
	if rec, ok := st.Agents["claude_code"]; ok {
		t.Fatalf("state still claims a registration that is gone: %+v", rec)
	}
	if saved, _ := LoadState(statePath); len(saved.Agents) != 0 {
		t.Fatalf("persisted: %+v", saved.Agents)
	}
}

func TestApplyRefusesSymlinkedConfig(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "dotfiles", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	enable := []string{"cursor-agent", "mcp", "enable", "corral"}
	f := &fakeRunner{replies: map[string]reply{strings.Join(enable, " "): {}}}
	st := State{Agents: map[string]AgentRecord{}}
	for _, k := range []ActionKind{ActionWriteFile, ActionUnwriteFile} {
		p := Plan{Actions: []Action{{AgentID: "cursor", Kind: k, Path: path}, {AgentID: "cursor", Kind: ActionEnable, Argv: enable}}}
		res := applyOne(t, f, p, home, &st)
		if res[0].OK || !strings.Contains(res[0].Detail, path) || !strings.Contains(res[0].Detail, "symlink") {
			t.Fatalf("%s: %+v", k, res)
		}
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}
	if b, _ := os.ReadFile(target); string(b) != "{}" {
		t.Errorf("target changed: %s", b)
	}
	if m, _ := filepath.Glob(path + ".corral.backup*"); len(m) != 0 {
		t.Errorf("backup made: %v", m)
	}
	if len(f.calls) != 0 {
		t.Errorf("enable ran after a refused write: %v", f.calls)
	}
}
