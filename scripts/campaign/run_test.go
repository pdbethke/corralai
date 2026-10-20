// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// fakeRunner records every command and models just enough git and corral for
// the run: per-worktree untracked/ahead file sets, `corral ledger append`
// that fails when its source is missing (as the real one would), and a reset
// that deletes unpushed files. hook may override any command first.
type fakeRunner struct {
	t          *testing.T
	calls      []call
	hook       func(f *fakeRunner, dir string, argv []string) (out string, err error, handled bool)
	origin     map[string]string // worktree -> remote URL
	untracked  map[string][]string
	ahead      map[string][]string
	seq        int
	certifyErr error // returned by certify AFTER it wrote its entry
	sha        string
}

func newFake(t *testing.T, l layout, tg Target) *fakeRunner {
	return &fakeRunner{
		t: t, sha: testSHA,
		origin: map[string]string{
			l.ledger: "https://github.com/" + tg.Fork + ".git",
			l.corral: "git@github.com:pdbethke/corralai.git",
		},
		untracked: map[string][]string{}, ahead: map[string][]string{},
	}
}

func (f *fakeRunner) Run(_ context.Context, dir string, argv ...string) (string, error) {
	f.calls = append(f.calls, call{dir, argv})
	if err := checkAllowed(argv); err != nil {
		return "", err
	}
	if f.hook != nil {
		if out, err, ok := f.hook(f, dir, argv); ok {
			return out, err
		}
	}
	switch {
	case argv[0] == "git" && argv[1] == "remote":
		return f.origin[dir] + "\n", nil
	case argv[0] == "git" && argv[1] == "-C":
		return f.sha + "\n", nil
	case argv[0] == "git" && argv[1] == "status":
		out := "## corral/ledger...origin/corral/ledger"
		if len(f.ahead[dir]) > 0 {
			out += " [ahead 1]"
		}
		out += "\n"
		for _, n := range f.untracked[dir] {
			out += "?? scans/" + n + "\n"
		}
		return out, nil
	case argv[0] == "git" && argv[1] == "diff":
		out := ""
		for _, n := range f.ahead[dir] {
			out += "scans/" + n + "\n"
		}
		return out, nil
	case argv[0] == "git" && argv[1] == "commit":
		f.ahead[dir] = append(f.ahead[dir], f.untracked[dir]...)
		f.untracked[dir] = nil
	case argv[0] == "git" && argv[1] == "push":
		f.ahead[dir] = nil
	case argv[0] == "git" && argv[1] == "reset":
		for _, n := range f.ahead[dir] {
			_ = os.Remove(filepath.Join(dir, "scans", n))
		}
		f.ahead[dir] = nil
	case argv[0] == "corral" && argv[1] == "ledger":
		raw, err := os.ReadFile(argv[3])
		if err != nil {
			return "", fmt.Errorf("append: %w", err) // the real one cannot read a missing file either
		}
		f.seq++
		name := fmt.Sprintf("2026102%dT00000%d-%s", 1, f.seq, filepath.Base(argv[3]))
		if err := os.MkdirAll(filepath.Join(argv[4], "scans"), 0o750); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(argv[4], "scans", name), raw, 0o600); err != nil {
			return "", err
		}
		f.untracked[argv[4]] = append(f.untracked[argv[4]], name)
	case argv[0] == "corral" && argv[1] == "certify":
		name, ledger, path := "20261020T000000-new.json", "", ""
		for i, a := range argv {
			switch a {
			case "--ledger":
				ledger = argv[i+1]
			case "--path":
				path = argv[i+1]
			}
		}
		writeEntry(f.t, ledger, name, f.sha, normalizeScope(path))
		f.untracked[ledger] = append(f.untracked[ledger], name)
		return "", f.certifyErr
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

func runIt(t *testing.T, f *fakeRunner, tg Target, l layout) (string, string, error) {
	t.Helper()
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
	f := newFake(t, l, tg)
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

func TestRunSkipMatchesTheScopeCorralRecords(t *testing.T) {
	tg := validTarget()
	tg.Path = "starlette/" // corral records the cleaned form, "starlette"
	l := newLayout(t, tg)
	writeEntry(t, l.ledger, "20260101T000000-old.json", testSHA, "starlette")
	f := newFake(t, l, tg)
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	if len(f.with("corral", "certify")) != 0 {
		t.Fatal("a trailing slash made the commit look unaudited")
	}
	for in, want := range map[string]string{"a/b/": "a/b", "./a": "a", ".": "", "": "", "a//b": "a/b"} {
		if got := normalizeScope(in); got != want {
			t.Errorf("normalizeScope(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunAuditsWhenNewestEntryIsADifferentCommitOrPath(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	writeEntry(t, l.ledger, "20260101T000000-old.json", "deadbeef", tg.Path)
	f := newFake(t, l, tg)
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
	f := newFake(t, l, tg)
	f.certifyErr = errors.New("exit status 1")
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
	f := newFake(t, l, tg)
	pushes := 0
	f.hook = func(f *fakeRunner, dir string, argv []string) (string, error, bool) {
		if argv[0] == "git" && argv[1] == "push" && dir == l.ledger {
			pushes++
			if pushes == 1 {
				return "", errors.New("exit status 1: ! [rejected] HEAD -> corral/ledger (non-fast-forward)"), true
			}
		}
		return "", nil, false
	}
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
	if strings.HasPrefix(replays[0].argv[3], filepath.Join(l.ledger, "scans")) {
		t.Fatal("re-append must read the saved copy, not the file the reset deletes")
	}
	if pushes != 2 {
		t.Fatalf("want 2 pushes of ledger/, got %d", pushes)
	}
	// corral's copy must come from the file as re-placed, not the deleted one:
	// the fake append fails on a missing source, so reaching here proves it.
	cp := 0
	for _, c := range f.with("corral", "ledger", "append") {
		if c.dir == l.corral {
			cp++
			if !strings.HasPrefix(c.argv[3], filepath.Join(l.ledger, "scans")) {
				t.Fatalf("corral copy from %q", c.argv[3])
			}
			if strings.HasSuffix(c.argv[3], "20261020T000000-new.json") && !strings.Contains(c.argv[3], "-20261020T000000-new.json") {
				t.Fatalf("corral copy used the stale pre-relink path %q", c.argv[3])
			}
		}
	}
	if cp != 1 {
		t.Fatalf("want one copy to corral's ledger, got %d", cp)
	}
}

func TestRunNeverTouchesTheCurationCheckout(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	f := newFake(t, l, tg)
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
	f := newFake(t, l, tg)
	if _, _, err := runIt(t, f, tg, l); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 {
		t.Fatal("nothing ran")
	}
	for _, c := range f.calls {
		if !allowedCommands[c.argv[0]+" "+c.argv[1]] {
			t.Fatalf("not allowlisted: %q", c.argv)
		}
		if c.argv[0] == "gh" && !reflect.DeepEqual(c.argv, []string{"gh", "repo", "sync", tg.Fork}) {
			t.Fatalf("gh may only sync the fork: %q", c.argv)
		}
		if err := checkAllowed(c.argv); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckAllowedRefusesWhatTheCampaignMustNeverDo(t *testing.T) {
	for _, argv := range [][]string{
		{"git", "push", "--force", "origin", "x"},
		{"git", "push", "-f"},
		{"git", "push", "-fu", "origin", "x"},
		{"git", "push", "-uf", "origin", "x"},
		{"git", "push", "--force-with-lease"},
		{"git", "push", "--force-with-lease=x"},
		{"git", "push", "--force-if-includes"},
		{"git", "push", "origin", "+HEAD:corral/ledger"},
		{"gh", "repo", "sync", "a/b", "--force"},
		{"gh", "pr", "create"},
		{"gh", "issue", "comment", "1"},
		{"git", "-C", "x", "push"},
		{"git", "checkout", "main"},
		{"git", "remote", "add", "x", "y"},
		{"git", "remote", "set-url", "origin", "y"},
		{"git", "status"},
		{"corral", "ledger", "prune"},
		{"curl", "http://x"},
	} {
		if checkAllowed(argv) == nil {
			t.Errorf("allowed %q", argv)
		}
	}
	// the target's own test command is not ours to judge
	cmd := validTarget()
	cmd.TestCommand = []string{"pytest", "-fx"}
	if err := checkAllowed(CertifyArgv(cmd, "/a", "/l", "")); err != nil {
		t.Fatal(err)
	}
}

func TestRunCopiesNewEntriesToCorralLedger(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	f := newFake(t, l, tg)
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

func TestRunFinishesAnEarlierUnpublishedAuditInsteadOfStrandingIt(t *testing.T) {
	tg := validTarget()
	l := newLayout(t, tg)
	// the previous run audited this commit, wrote the entry, and died before
	// pushing it or copying it to corral's ledger
	writeEntry(t, l.ledger, "20260101T000000-old.json", testSHA, tg.Path)
	f := newFake(t, l, tg)
	f.untracked[l.ledger] = []string{"20260101T000000-old.json"}
	out, _, err := runIt(t, f, tg, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.with("corral", "certify")) != 0 || !strings.Contains(out, "already audited") {
		t.Fatalf("should skip the audit: %q", out)
	}
	pushedLedger, copied, pushedCorral := false, false, false
	for _, c := range f.with("git", "push") {
		pushedLedger = pushedLedger || c.dir == l.ledger
		pushedCorral = pushedCorral || c.dir == l.corral
	}
	for _, c := range f.with("corral", "ledger", "append") {
		copied = copied || c.dir == l.corral
	}
	if !pushedLedger || !copied || !pushedCorral {
		t.Fatalf("recovery incomplete: ledger pushed %v, copied %v, corral pushed %v", pushedLedger, copied, pushedCorral)
	}
}

func TestRunRefusesAnOriginThatIsNotTheTargetsRepoBeforeAnySpend(t *testing.T) {
	for name, set := range map[string]func(*fakeRunner, layout){
		"ledger": func(f *fakeRunner, l layout) { f.origin[l.ledger] = "https://github.com/someone/else.git" },
		"corral": func(f *fakeRunner, l layout) { f.origin[l.corral] = "git@github.com:someone/else.git" },
	} {
		tg := validTarget()
		l := newLayout(t, tg)
		f := newFake(t, l, tg)
		set(f, l)
		_, _, err := runIt(t, f, tg, l)
		if err == nil || !strings.Contains(err.Error(), "someone/else") {
			t.Fatalf("%s: err %v", name, err)
		}
		for _, c := range f.calls {
			if c.argv[0] != "git" || c.argv[1] != "remote" {
				t.Fatalf("%s: ran %q before refusing", name, c.argv)
			}
		}
	}
}

func TestRemoteNames(t *testing.T) {
	for _, u := range []string{"https://github.com/pdbethke/starlette", "https://github.com/pdbethke/starlette.git",
		"git@github.com:pdbethke/starlette.git", "ssh://git@github.com/pdbethke/starlette.git", "https://github.com/PDBethke/Starlette/"} {
		if !remoteNames(u, "pdbethke/starlette") {
			t.Errorf("rejected %q", u)
		}
	}
	for _, u := range []string{"", "https://github.com/Kludex/starlette.git", "https://evil.example/pdbethke/starlette",
		"https://github.com/pdbethke/starlette-evil", "file:///pdbethke/starlette"} {
		if remoteNames(u, "pdbethke/starlette") {
			t.Errorf("accepted %q", u)
		}
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

// git runs real git in dir with a fixed identity and no global config.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The relink path only works if the REAL runner's error carries git's own
// words. Reproduce a real rejection against a local bare repository (no
// network) and push it through execRunner.
func TestExecRunnerErrorLetsMovedRecogniseARealRejection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	bare, a, b := filepath.Join(base, "bare.git"), filepath.Join(base, "a"), filepath.Join(base, "b")
	git(t, base, "init", "--bare", "-q", bare)
	git(t, base, "clone", "-q", bare, a)
	git(t, a, "checkout", "-q", "-b", "corral/ledger")
	git(t, a, "commit", "-q", "--allow-empty", "-m", "genesis")
	git(t, a, "push", "-q", "origin", "HEAD:corral/ledger")
	git(t, base, "clone", "-q", "-b", "corral/ledger", bare, b)
	git(t, a, "commit", "-q", "--allow-empty", "-m", "head moves")
	git(t, a, "push", "-q", "origin", "HEAD:corral/ledger")
	git(t, b, "commit", "-q", "--allow-empty", "-m", "mine")

	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	var echo bytes.Buffer
	_, err := execRunner{echo: &echo}.Run(context.Background(), b, "git", "push", "origin", "HEAD:"+ledgerBranch)
	if err == nil {
		t.Fatal("the push should have been rejected")
	}
	if !moved(err) {
		t.Fatalf("moved() did not recognise a real rejection: %v", err)
	}
	// and an unrelated failure is not mistaken for one
	_, err = execRunner{echo: &echo}.Run(context.Background(), b, "git", "fetch", "nonexistent-remote")
	if err == nil || moved(err) {
		t.Fatalf("unrelated failure: %v", err)
	}
}
