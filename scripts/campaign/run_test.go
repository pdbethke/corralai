// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type call struct {
	dir  string
	argv []string
}

// fakeRunner records every command and answers from a script. hook may write
// files (as corral certify would) and may return an error for one command.
type fakeRunner struct {
	calls []call
	hook  func(f *fakeRunner, dir string, argv []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, dir string, argv ...string) (string, error) {
	f.calls = append(f.calls, call{dir, argv})
	if err := checkAllowed(argv); err != nil {
		return "", err
	}
	if f.hook != nil {
		return f.hook(f, dir, argv)
	}
	return "", nil
}

func (f *fakeRunner) with(prefix ...string) []call {
	var out []call
	for _, c := range f.calls {
		if len(c.argv) >= len(prefix) && reflect.DeepEqual(c.argv[:len(prefix)], prefix) {
			out = append(out, c)
		}
	}
	return out
}

type layout struct{ campaign, root, clone, ledger, corral string }

func newLayout(t *testing.T, tg Target) layout {
	t.Helper()
	l := layout{campaign: t.TempDir()}
	l.root = filepath.Join(l.campaign, tg.Name)
	l.clone = filepath.Join(l.root, "clone")
	l.ledger = filepath.Join(l.root, "ledger")
	l.corral = filepath.Join(l.campaign, "_corral-ledger")
	for _, d := range []string{l.clone, l.ledger, l.corral} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func writeEntry(t *testing.T, ledger, name, commit, scope string) {
	t.Helper()
	e := auditpush.LedgerEntry{Format: auditpush.LedgerFileFormat}
	e.Bundle.Scan.Commit, e.Bundle.Scan.Scope = commit, scope
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ledger, auditpush.ScansSubdir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// happy answers rev-parse and, on certify, writes one entry into the ledger.
func happy(l layout, tg Target) func(*fakeRunner, string, []string) (string, error) {
	return func(f *fakeRunner, dir string, argv []string) (string, error) {
		switch {
		case argv[0] == "git" && argv[1] == "-C":
			return testSHA + "\n", nil
		case argv[0] == "corral" && argv[1] == "certify":
			writeEntry(nil2t, l.ledger, "20261020T000000-new.json", testSHA, tg.Path)
		}
		return "", nil
	}
}

var nil2t *testing.T // set per test by run helper; writeEntry needs a t

func runIt(t *testing.T, f *fakeRunner, tg Target, l layout) (string, string, error) {
	t.Helper()
	nil2t = t
	var out, errb bytes.Buffer
	err := RunTarget(context.Background(), f, tg, l.campaign, &out, &errb)
	return out.String(), errb.String(), err
}

func TestCertifyArgvNamesEverySeatAndCap(t *testing.T) {
	tg := validTarget()
	got := CertifyArgv(tg, "/a", "/l", "")
	want := []string{"corral", "certify", "--repo", "/a", "--path", "starlette", "--substrate", "jail",
		"--mutant-model", "gemini-3.6-pro", "--writer-model", "claude-code:claude-sonnet-5",
		"--shadow-writer-model", "codex", "--critic-model", "gemini-3.6-flash",
		"--max-tokens", "2000000", "--timeout", "90m", "--ledger", "/l",
		"--derive-model", "gemini-3.6-pro", "--", "python", "-m", "pytest", "tests"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derive form:\n got %q\nwant %q", got, want)
	}
	got = CertifyArgv(tg, "/a", "/l", "/g.json")
	exp := []string{"corral", "certify", "--repo", "/a", "--path", "starlette", "--substrate", "jail",
		"--mutant-model", "gemini-3.6-pro", "--writer-model", "claude-code:claude-sonnet-5",
		"--shadow-writer-model", "codex", "--critic-model", "gemini-3.6-flash",
		"--max-tokens", "2000000", "--timeout", "90m", "--ledger", "/l",
		"--goals", "/g.json", "--", "python", "-m", "pytest", "tests"}
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("goals form:\n got %q\nwant %q", got, exp)
	}
}

func TestRunSkipsWhenHeadAlreadyAudited(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	writeEntry(t, l.ledger, "20260101T000000-old.json", testSHA, tg.Path)
	f := &fakeRunner{hook: happy(l, tg)}
	out, _, err := runIt(t, f, tg, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.with("corral", "certify")) != 0 {
		t.Fatal("certify ran on an already-audited commit")
	}
	if !strings.Contains(out, "already audited "+testSHA) {
		t.Fatalf("stdout %q", out)
	}
}

func TestRunAuditsWhenNewestEntryIsADifferentCommitOrPath(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	writeEntry(t, l.ledger, "20260101T000000-old.json", "deadbeef", tg.Path)
	f := &fakeRunner{hook: happy(l, tg)}
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	if len(f.with("corral", "certify")) != 1 {
		t.Fatal("expected one certify")
	}
}

func TestRunDoesNotRetryAFailedAudit(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	base := happy(l, tg)
	f := &fakeRunner{hook: func(f *fakeRunner, dir string, argv []string) (string, error) {
		out, _ := base(f, dir, argv)
		if argv[0] == "corral" && argv[1] == "certify" {
			return out, errors.New("exit status 1")
		}
		return out, nil
	}}
	_, _, err := runIt(t, f, tg, l)
	if err == nil {
		t.Fatal("a failed audit must return an error")
	}
	if n := len(f.with("corral", "certify")); n != 1 {
		t.Fatalf("certify ran %d times", n)
	}
	if len(f.with("git", "commit")) == 0 || len(f.with("git", "push")) == 0 {
		t.Fatal("entries present after a failed audit must still be committed and pushed")
	}
}

func TestRunRelinksWhenLedgerBranchMoved(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	base := happy(l, tg)
	pushes := 0
	f := &fakeRunner{hook: func(f *fakeRunner, dir string, argv []string) (string, error) {
		if argv[0] == "git" && argv[1] == "push" && dir == l.ledger {
			pushes++
			if pushes == 1 {
				return "", errors.New("! [rejected] HEAD -> corral/ledger (non-fast-forward)")
			}
		}
		if argv[0] == "git" && argv[1] == "reset" {
			// the reset deletes the entries, as the real one would
			_ = os.RemoveAll(filepath.Join(l.ledger, auditpush.ScansSubdir))
		}
		return base(f, dir, argv)
	}}
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	if len(f.with("git", "fetch", "origin", "corral/ledger")) != 1 {
		t.Fatal("no fetch of the ledger branch")
	}
	rs := f.with("git", "reset", "--hard", "origin/corral/ledger")
	if len(rs) != 1 || rs[0].dir != l.ledger {
		t.Fatalf("reset: %v", rs)
	}
	var replays []call
	for _, c := range f.with("corral", "ledger", "append") {
		if c.dir == l.ledger {
			replays = append(replays, c)
		}
	}
	if len(replays) != 1 {
		t.Fatalf("want one re-append in ledger/, got %d", len(replays))
	}
	if filepath.Dir(replays[0].argv[3]) == filepath.Join(l.ledger, "scans") {
		t.Fatal("re-append must read the saved copy, not the file the reset deletes")
	}
	if pushes != 2 {
		t.Fatalf("want 2 pushes of ledger/, got %d", pushes)
	}
	for _, c := range f.calls {
		for _, a := range c.argv {
			if a == "--force" || a == "-f" || a == "--force-with-lease" {
				t.Fatalf("forced argv: %q", c.argv)
			}
		}
	}
}

func TestRunNeverTouchesTheCurationCheckout(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	f := &fakeRunner{hook: happy(l, tg)}
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c.dir != l.clone {
			continue
		}
		ok := (c.argv[0] == "git" && c.argv[1] == "fetch") || (c.argv[0] == "git" && c.argv[1] == "worktree")
		if !ok {
			t.Fatalf("command in the curation checkout: %q", c.argv)
		}
	}
}

func TestRunOnlyCallsAllowedCommands(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	f := &fakeRunner{hook: happy(l, tg)}
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, w := range []string{"corral certify", "corral ledger", "git fetch", "git worktree", "git -C",
		"git add", "git commit", "git push", "git reset", "git rev-parse", "gh repo"} {
		allowed[w] = true
	}
	if len(f.calls) == 0 {
		t.Fatal("nothing ran")
	}
	for _, c := range f.calls {
		if !allowed[c.argv[0]+" "+c.argv[1]] {
			t.Fatalf("not allowlisted: %q", c.argv)
		}
		if c.argv[0] == "gh" && !reflect.DeepEqual(c.argv, []string{"gh", "repo", "sync", tg.Fork}) {
			t.Fatalf("gh may only sync the fork: %q", c.argv)
		}
		if c.argv[0] == "git" && c.argv[1] == "-C" && c.argv[3] != "rev-parse" {
			t.Fatalf("git -C is for rev-parse only: %q", c.argv)
		}
		if c.argv[0] == "corral" && c.argv[1] == "ledger" && c.argv[2] != "append" {
			t.Fatalf("corral ledger is append only: %q", c.argv)
		}
	}
}

func TestCheckAllowedRefusesWhatTheCampaignMustNeverDo(t *testing.T) {
	for _, argv := range [][]string{
		{"git", "push", "--force", "origin", "x"},
		{"git", "push", "-f"},
		{"git", "push", "--force-with-lease"},
		{"gh", "repo", "sync", "a/b", "--force"},
		{"gh", "pr", "create"},
		{"gh", "issue", "comment", "1"},
		{"git", "-C", "x", "push"},
		{"git", "checkout", "main"},
		{"corral", "ledger", "prune"},
		{"curl", "http://x"},
	} {
		if checkAllowed(argv) == nil {
			t.Errorf("allowed %q", argv)
		}
	}
	// the target's own test command is not ours to judge
	if err := checkAllowed(CertifyArgv(validTarget(), "/a", "/l", "")); err != nil {
		t.Fatal(err)
	}
}

func TestRunCopiesNewEntriesToCorralLedger(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	f := &fakeRunner{hook: happy(l, tg)}
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(l.ledger, "scans", "20261020T000000-new.json")
	want := []string{"corral", "ledger", "append", entry, l.corral}
	found := false
	for _, c := range f.calls {
		if reflect.DeepEqual(c.argv, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q in %v", want, f.calls)
	}
	pushed := false
	for _, c := range f.with("git", "push") {
		if c.dir == l.corral {
			pushed = true
		}
	}
	if !pushed {
		t.Fatal("corral's ledger worktree was not pushed")
	}
}

func TestRunRefusesWithoutCampaignDir(t *testing.T) {
	var out, errb bytes.Buffer
	f := &fakeRunner{}
	code := cli(context.Background(), []string{"-targets", "targets.json", "run", "starlette"},
		func(string) string { return "" }, f, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "CORRAL_CAMPAIGN_DIR") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if len(f.calls) != 0 {
		t.Fatal("ran a command")
	}
}

func TestRunRefusesAMissingLayoutBeforeAnyCommand(t *testing.T) {
	tg := validTarget()
	f := &fakeRunner{}
	var o, e bytes.Buffer
	err := RunTarget(context.Background(), f, tg, t.TempDir(), &o, &e)
	if err == nil || len(f.calls) != 0 {
		t.Fatalf("err %v, calls %v", err, f.calls)
	}
}
