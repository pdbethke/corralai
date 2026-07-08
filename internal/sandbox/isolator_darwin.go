//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type sandboxExecIsolator struct{}

func newSandboxExecIsolator() (Isolator, error) {
	s := sandboxExecIsolator{}
	if err := s.Preflight(); err != nil {
		return nil, err
	}
	return s, nil
}

func (sandboxExecIsolator) Name() string { return "sandbox-exec" }

func (sandboxExecIsolator) Preflight() error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("sandbox-exec not found on PATH: %w", err)
	}
	// Verify that we can execute a simple sandbox-exec command
	out, err := exec.Command("sandbox-exec", "-p", "(version 1) (allow default)", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox-exec preflight failed: %v: %s", err, string(out))
	}
	return nil
}

func (sandboxExecIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("sandbox-exec: workspace required")
	}

	// HOME comes from the (already minimal, secret-free) env. We deny reads of
	// it below so a command can't lift the operator's credentials.
	home := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			home = strings.TrimPrefix(kv, "HOME=")
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n")
	sb.WriteString("(allow process*)\n")
	sb.WriteString("(allow signal*)\n")
	sb.WriteString("(allow sysctl*)\n")
	sb.WriteString("(allow file-read*)\n")

	// Read confinement: the broad file-read* above lets a command read system
	// libraries/tools (as the Linux bwrap jail does via its /usr bind), but we
	// then DENY the operator's secret stores so a hijacked agent can't read
	// them — matching bwrap, which never binds $HOME into the jail. Last
	// matching rule wins in SBPL, so these denies override the allow, and the
	// workspace re-allow below overrides the $HOME deny when the workspace
	// lives under $HOME. Without this, ~/.ssh, ~/.aws, keychains, and corral's
	// own tokens were all readable — a regression from the Linux backend.
	if home != "" {
		sb.WriteString(fmt.Sprintf("(deny file-read* (subpath %q))\n", home))
	}
	sb.WriteString("(deny file-read* (subpath \"/Library/Keychains\"))\n")
	sb.WriteString("(deny file-read* (subpath \"/private/var/db/dslocal\"))\n")
	// Re-allow reads of the workspace itself (even if it lives under $HOME).
	sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", opts.Workspace))

	// Writable paths
	sb.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	sb.WriteString("(allow file-write* (subpath \"/tmp\"))\n")
	sb.WriteString("(allow file-write* (subpath \"/var/tmp\"))\n")
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath %q))\n", opts.Workspace))

	if opts.Network {
		sb.WriteString("(allow network*)\n")
		sb.WriteString("(allow mach-lookup)\n")
	}

	profile := sb.String()

	argv := []string{"sandbox-exec", "-p", profile, "sh", "-c", command}
	return argv, nil
}
