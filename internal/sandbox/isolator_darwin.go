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

	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n")
	sb.WriteString("(allow process*)\n")
	sb.WriteString("(allow signal*)\n")
	sb.WriteString("(allow sysctl*)\n")
	sb.WriteString("(allow file-read*)\n")

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
