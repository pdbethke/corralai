// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runJailed runs command in a real sandbox-exec jail over a fresh t.TempDir()
// workspace. On macOS t.TempDir() lives under /var/folders, a symlink into
// /private/var, so every test here also proves the workspace path is
// resolved before it reaches the profile (spec finding 7).
func runJailed(t *testing.T, command string, opts Options) Result {
	t.Helper()
	iso := sandboxExecIsolator{}
	if opts.Workspace == "" {
		opts.Workspace = t.TempDir()
	}
	opts.Backend = iso
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Minute
	}
	return Run(context.Background(), command, opts)
}

func TestSandboxExecRunsAShellInASymlinkedWorkspace(t *testing.T) {
	res := runJailed(t, "echo hi > f && cat f", Options{})
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) != "hi" {
		t.Fatalf("exit=%d err=%q output=%q", res.ExitCode, res.Err, res.Output)
	}
}

func TestSandboxExecPinsTheEnvironment(t *testing.T) {
	t.Setenv("CORRAL_HOST_ONLY_MARKER", "leak")
	t.Setenv("CORRAL_HOST_ONLY_UNFORWARDED", "leak") // in the host env, NOT in Options.Env
	ws := t.TempDir()
	env := append(MinimalEnv(), "CORRAL_HOST_ONLY_MARKER=leak")
	res := runJailed(t, "env", Options{Workspace: ws, Env: env})
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%q output=%q", res.ExitCode, res.Err, res.Output)
	}
	d := sandboxExecIsolator{}.jailDirs(resolved(ws))
	for _, want := range []string{"HOME=" + d.Home, "TMPDIR=" + d.Tmp, "GOTOOLCHAIN=local", "CORRAL_HOST_ONLY_MARKER=leak"} {
		if !strings.Contains(res.Output, want+"\n") {
			t.Errorf("env lacks %q:\n%s", want, res.Output)
		}
	}
	if strings.Contains(res.Output, "CORRAL_HOST_ONLY_UNFORWARDED") {
		t.Errorf("env -i let an unforwarded host variable through:\n%s", res.Output)
	}
	host, _ := os.UserHomeDir()
	if strings.Contains(res.Output, "HOME="+host+"\n") {
		t.Errorf("the operator's HOME reached the jail:\n%s", res.Output)
	}
}

func TestSandboxExecHomeAndTmpAreWritable(t *testing.T) {
	res := runJailed(t, `touch "$HOME/p" "$TMPDIR/p" && echo ok`, Options{})
	if strings.TrimSpace(res.Output) != "ok" {
		t.Fatalf("exit=%d err=%q output=%q", res.ExitCode, res.Err, res.Output)
	}
}

func TestSandboxExecDeniesTheOperatorsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory: nothing to deny")
	}
	res := runJailed(t, "ls "+shQuote(home), Options{})
	if res.ExitCode == 0 {
		t.Fatalf("listing %s succeeded inside the jail:\n%s", home, res.Output)
	}
}

// Positive control: a world-readable file under the denied home. It is
// readable outside the jail, so a failure inside is the profile's doing, not
// file permissions (/etc/master.passwd is 0600 root and proves nothing).
func TestSandboxExecDeniesAReadableFileUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir, err := os.MkdirTemp(home, ".corral-test-readable-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := filepath.Join(dir, "secret")
	if err := os.WriteFile(f, []byte("readable"), 0o644); err != nil { //nolint:gosec // the point: readable outside the jail
		t.Fatal(err)
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != "readable" {
		t.Fatalf("control failed: the file is not readable outside the jail: %v", err)
	}
	res := runJailed(t, "cat "+shQuote(f), Options{})
	if res.ExitCode == 0 || strings.Contains(res.Output, "readable\n") || !strings.Contains(res.Output, "Operation not permitted") {
		t.Fatalf("a world-readable file under the operator's home was readable in the jail: exit=%d output=%q", res.ExitCode, res.Output)
	}
}

func TestSandboxExecHomeDoesNotSurviveACommand(t *testing.T) {
	ws := t.TempDir()
	if res := runJailed(t, `echo x > "$HOME/marker" && echo x > "$TMPDIR/marker"`, Options{Workspace: ws}); res.ExitCode != 0 {
		t.Fatalf("first run: %+v", res)
	}
	for _, n := range []string{".corral-home", ".corral-tmp"} {
		if _, err := os.Stat(filepath.Join(ws, n)); err == nil {
			t.Errorf("%s left behind after the run", n)
		}
	}
	res := runJailed(t, `test ! -e "$HOME/marker" && test ! -e "$TMPDIR/marker" && echo clean`, Options{Workspace: ws})
	if strings.TrimSpace(res.Output) != "clean" {
		t.Fatalf("HOME/TMPDIR carried into a second command: exit=%d output=%q", res.ExitCode, res.Output)
	}
}

// Go's module cache is read-only; the cleanup must still remove it.
func TestSandboxExecCleanupRemovesReadOnlyTrees(t *testing.T) {
	ws := t.TempDir()
	res := runJailed(t, `mkdir -p "$HOME/ro/d" && echo x > "$HOME/ro/d/f" && chmod -R a-w "$HOME/ro"`, Options{Workspace: ws})
	if res.ExitCode != 0 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(ws, ".corral-home")); err == nil {
		t.Error(".corral-home with a read-only tree survived")
	}
}

func TestSandboxExecKeepsTheExitStatusThroughTheTrap(t *testing.T) {
	res := runJailed(t, "exit 7", Options{})
	if res.ExitCode != 7 {
		t.Fatalf("exit=%d err=%q, want 7", res.ExitCode, res.Err)
	}
}

// BSD env would parse a key starting with "-" as an option.
func TestSandboxExecDropsDashKeyedEnv(t *testing.T) {
	argv, err := sandboxExecIsolator{}.Wrap("true", Options{Workspace: t.TempDir()}, []string{"-S=x", "--foo=bar", "OK=1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv[3:] {
		if strings.HasPrefix(a, "-S=") || strings.HasPrefix(a, "--foo=") {
			t.Fatalf("dash-keyed env entry reached env's argv: %q", a)
		}
	}
	if !strings.Contains(strings.Join(argv, "\x00"), "OK=1") {
		t.Fatal("a normal entry was dropped")
	}
}

// A toolchain under $HOME, passed in ReadOnlyBinds, is readable even though
// the rest of $HOME is denied (layers 4 then 5).
func TestSandboxExecReadsAToolchainUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tc, err := os.MkdirTemp(home, ".corral-test-toolchain-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tc) })
	if err := os.WriteFile(filepath.Join(tc, "tool"), []byte("toolchain-ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := runJailed(t, "cat "+shQuote(filepath.Join(tc, "tool")), Options{ReadOnlyBinds: []Bind{{Host: tc, Target: tc}}})
	if strings.TrimSpace(res.Output) != "toolchain-ok" {
		t.Fatalf("toolchain under $HOME unreadable: exit=%d output=%q", res.ExitCode, res.Output)
	}
}

func TestSandboxExecIsOfflineByDefault(t *testing.T) {
	res := runJailed(t, "/usr/bin/nc -z -G 3 1.1.1.1 443", Options{})
	if res.ExitCode == 0 {
		t.Fatal("an outbound connection succeeded without Options.Network")
	}
}

func TestSandboxExecNetworkWhenAsked(t *testing.T) {
	res := runJailed(t, "/usr/bin/curl -sS -o /dev/null -w '%{http_code}' https://proxy.golang.org/", Options{Network: true})
	if strings.TrimSpace(res.Output) != "200" {
		t.Fatalf("networked fetch failed: exit=%d output=%q", res.ExitCode, res.Output)
	}
}

// A real `go test` of a tiny module, with a cold cache in the per-run HOME.
func TestSandboxExecRunsGoTest(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	goRoot := resolved(filepath.Dir(filepath.Dir(goBin)))
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module p\ngo 1.21\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "p_test.go"), []byte("package p\nimport \"testing\"\nfunc TestX(t *testing.T) {}\n"), 0o600)
	res := runJailed(t, "go test ./...", Options{Workspace: ws, ReadOnlyBinds: []Bind{{Host: goRoot, Target: goRoot}}})
	if res.ExitCode != 0 || !strings.Contains(res.Output, "ok") {
		t.Fatalf("go test in the jail: exit=%d output=%s", res.ExitCode, res.Output)
	}
	if _, err := os.Stat(filepath.Join(ws, ".corral-home")); err == nil {
		t.Error("go test left .corral-home (module/build cache) behind")
	}
}

func TestSandboxExecPrependsUlimitPrelude(t *testing.T) {
	argv, err := sandboxExecIsolator{}.Wrap("echo hi", Options{Workspace: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := argv[len(argv)-1]
	if !strings.Contains(sh, "ulimit -u") || !strings.Contains(sh, "ulimit -f") || !strings.HasSuffix(sh, "echo hi") {
		t.Fatalf("sh -c command must carry the ulimit prelude and end with the command:\n%s", sh)
	}
}

// Review focus 3: the deny names the RESOLVED os.UserHomeDir(), not $HOME.
func TestSandboxExecDeniesTheRealHomeNotEnvHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	argv, err := sandboxExecIsolator{}.Wrap("true", Options{Workspace: t.TempDir()}, []string{"HOME=/elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `(deny file-read* (subpath "` + resolved(home) + `"))`; !strings.Contains(argv[2], want) {
		t.Fatalf("profile lacks %s", want)
	}
}

func TestSandboxExecDeniesSharedTmpWrite(t *testing.T) {
	res := runJailed(t, "touch /private/tmp/corral-should-not-exist", Options{})
	if res.ExitCode == 0 {
		os.Remove("/private/tmp/corral-should-not-exist")
		t.Fatal("the shared host /tmp was writable inside the jail")
	}
}

// TestSandboxExecMasksACargoHomeInAWholeBoundTree (layer 7, for real): a
// cargo home inside a tree the jail reads whole (/usr, /opt/homebrew) is
// exposed through that read, so its secrets are denied last. A resolved
// temp tree stands in for the whole-bound tree, bound read-only exactly as
// a toolchain is: bin/ stays readable, credentials.toml (and the rest of
// the cargo home) is "Operation not permitted" — refused by the profile,
// although the files are readable outside the jail.
func TestSandboxExecMasksACargoHomeInAWholeBoundTree(t *testing.T) {
	// Resolved: SBPL matches canonical paths.
	tree, ch := fakeSystemCargoHome(t)
	if err := os.WriteFile(filepath.Join(ch, "bin", "tool"), []byte("tool-ok\n"), 0o755); err != nil { //nolint:gosec // a readable toolchain binary is the point
		t.Fatal(err)
	}
	t.Setenv("CARGO_HOME", ch)
	old := wholeBoundTrees
	wholeBoundTrees = []string{tree}
	t.Cleanup(func() { wholeBoundTrees = old })

	cred := filepath.Join(ch, "credentials.toml")
	if b, err := os.ReadFile(cred); err != nil || !strings.Contains(string(b), "secret") {
		t.Fatalf("control failed: credentials.toml is not readable outside the jail: %v", err)
	}
	script := "cat " + shQuote(filepath.Join(ch, "bin", "tool")) +
		"; cat " + shQuote(cred) +
		"; cat " + shQuote(filepath.Join(ch, "config.toml")) +
		"; ls -A " + shQuote(filepath.Join(ch, "git"))
	res := runJailed(t, script, Options{ReadOnlyBinds: []Bind{{Host: tree, Target: tree}}})
	if !strings.Contains(res.Output, "tool-ok\n") {
		t.Errorf("the cargo home's bin/tool must stay readable:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "secret") || strings.Contains(res.Output, "checkouts") {
		t.Errorf("a masked secret was readable in the jail:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, cred+": Operation not permitted") {
		t.Errorf("credentials.toml must be refused by the profile (Operation not permitted):\n%s", res.Output)
	}
}
