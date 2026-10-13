// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// selfTester is a backend the shared self-test can probe without side
// effects on the host: it names its jail's HOME/TMPDIR and a command that
// SUCCEEDS only when the jail has a route off the machine.
type selfTester interface {
	Isolator
	jailDirs(workspace string) jailDirs
	netDenialProbe() string
}

// selfTestChecks are the guarantees the self-test proves, in the order the
// probe reports them. Each is an executable line of the macOS jail spec
// (D3): the first one missing from the output is the one that broke.
var selfTestChecks = []string{"sh-starts", "workspace-write", "env-pins", "home-denied", "net-denied"}

// SelfTestError names the guarantee that failed, so a broken jail stops the
// run loudly instead of surfacing as COULD-NOT-GRADE, which blames the
// operator's project.
type SelfTestError struct{ Backend, Check, OS, Output string }

func (e *SelfTestError) Error() string {
	return fmt.Sprintf("%s jail self-test failed at %s on %s: %s", e.Backend, e.Check, e.OS, e.Output)
}

// selfTest runs one probe script through the REAL Wrap → Run path, so it
// tests the exact argv an audit gets. It writes nothing outside a throwaway
// workspace: home-denied only lists the operator's home.
func selfTest(iso selfTester) error {
	ws, err := os.MkdirTemp("", "corral-selftest-*")
	if err != nil {
		return fmt.Errorf("%s jail self-test: creating a workspace: %w", iso.Name(), err)
	}
	defer os.RemoveAll(ws) // #nosec G104 -- best-effort cleanup of our own temp dir
	d := iso.jailDirs(resolved(ws))
	home, _ := os.UserHomeDir()
	homeCheck := "echo home-denied" // no home to deny
	if home != "" {
		homeCheck = "ls " + shQuote(home) + " >/dev/null 2>&1 || echo home-denied"
	}
	script := strings.Join([]string{
		"echo sh-starts",
		"echo x > selftest-probe && echo workspace-write",
		fmt.Sprintf(`[ "$HOME" = %s ] && [ "$TMPDIR" = %s ] && mkdir -p "$HOME" "$TMPDIR" && touch "$HOME/p" "$TMPDIR/p" && echo env-pins`, shQuote(d.Home), shQuote(d.Tmp)),
		homeCheck,
		iso.netDenialProbe() + " >/dev/null 2>&1 || echo net-denied",
	}, "\n")
	res := Run(context.Background(), script, Options{Workspace: ws, Backend: iso, Timeout: 30 * time.Second, Env: MinimalEnv()})
	reported := map[string]bool{}
	for _, l := range strings.Split(res.Output, "\n") {
		reported[strings.TrimSpace(l)] = true
	}
	for _, c := range selfTestChecks {
		if !reported[c] {
			out := strings.TrimSpace(res.Output + " " + res.Err)
			if out == "" {
				out = fmt.Sprintf("no output (exit %d)", res.ExitCode)
			}
			return &SelfTestError{Backend: iso.Name(), Check: c, OS: osDescription(), Output: out}
		}
	}
	return nil
}

var (
	selfTestMu    sync.Mutex
	selfTestCache = map[string]error{}
)

// cachedSelfTest runs selfTest once per backend per process (about 50 ms):
// a repo scan resolves the backend once, and doctor-then-certify in one
// process must not probe twice. A failure is cached too.
func cachedSelfTest(iso selfTester) error {
	selfTestMu.Lock()
	defer selfTestMu.Unlock()
	if err, ok := selfTestCache[iso.Name()]; ok {
		return err
	}
	err := selfTest(iso)
	selfTestCache[iso.Name()] = err
	return err
}

// resetSelfTestCache is for tests.
func resetSelfTestCache() {
	selfTestMu.Lock()
	defer selfTestMu.Unlock()
	selfTestCache = map[string]error{}
}

// osDescription names the host OS for a self-test failure: "macOS 27.0.1",
// else `uname -sr`, else GOOS.
func osDescription() string {
	if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		return "macOS " + strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return runtime.GOOS
}
