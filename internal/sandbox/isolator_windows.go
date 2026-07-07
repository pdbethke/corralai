//go:build windows

package sandbox

import (
	"fmt"
	"os/exec"
)

type windowsJobIsolator struct{}

func newWindowsJobIsolator() (Isolator, error) {
	s := windowsJobIsolator{}
	if err := s.Preflight(); err != nil {
		return nil, err
	}
	return s, nil
}

func (windowsJobIsolator) Name() string { return "windows-job" }

func (windowsJobIsolator) Preflight() error {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		return fmt.Errorf("cmd.exe not found on PATH: %w", err)
	}
	return nil
}

func (windowsJobIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	return []string{"cmd.exe", "/c", command}, nil
}
