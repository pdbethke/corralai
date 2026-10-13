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

func TestSandboxExecDeniesMasterPasswd(t *testing.T) {
	res := runJailed(t, "head -c1 /etc/master.passwd", Options{})
	if res.ExitCode == 0 {
		t.Fatal("/etc/master.passwd was readable inside the jail")
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
