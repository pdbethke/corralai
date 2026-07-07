// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Isolator wraps an untrusted command in an OS-level isolation boundary. It wraps
// ONLY the command run_command spawns — never the agent process itself.
type Isolator interface {
	// Wrap returns the argv that runs `command` under isolation, given the
	// workspace/options and the (already minimal, secret-free) env. It does not exec.
	Wrap(command string, opts Options, env []string) (argv []string, err error)
	// Preflight verifies the backend can actually isolate on THIS host.
	Preflight() error
	// Name identifies the backend in logs and errors.
	Name() string
}

// Config selects a backend at startup.
type Config struct {
	Backend    string // "bwrap" (default) | "container" | "none"
	UnsafeHost bool   // required to select "none"
}

// Resolves a default backend for the current operating system when Backend is empty.
func defaultBackend() string {
	switch runtime.GOOS {
	case "linux":
		return "bwrap"
	case "darwin":
		return "sandbox-exec"
	case "windows":
		return "windows-job"
	default:
		return "none"
	}
}

// Resolve picks and preflights a backend. It NEVER falls back to a weaker
// backend: if the requested backend can't isolate, it returns an error and the
// caller must refuse to execute.
func Resolve(cfg Config) (Isolator, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = defaultBackend()
	}
	switch backend {
	case "bwrap":
		iso, err := newBwrapIsolator()
		if err != nil {
			return nil, fmt.Errorf("bwrap backend unavailable: %w", err)
		}
		return iso, nil
	case "sandbox-exec":
		iso, err := newSandboxExecIsolator()
		if err != nil {
			return nil, fmt.Errorf("sandbox-exec backend unavailable: %w", err)
		}
		return iso, nil
	case "windows-job":
		iso, err := newWindowsJobIsolator()
		if err != nil {
			return nil, fmt.Errorf("windows-job backend unavailable: %w", err)
		}
		return iso, nil
	case "none":
		if !cfg.UnsafeHost {
			return nil, errors.New(`backend "none" runs commands unisolated; set AGENT_EXEC_UNSAFE_HOST=1 to confirm this host is already a disposable sandbox`)
		}
		return noneIsolator{}, nil
	case "container":
		runtime := os.Getenv("CORRALAI_EXEC_RUNTIME")
		if runtime == "" {
			if _, err := exec.LookPath("podman"); err == nil {
				runtime = "podman"
			} else if _, err := exec.LookPath("docker"); err == nil {
				runtime = "docker"
			} else {
				return nil, errors.New("container backend: no container runtime found; install podman or docker, or set CORRALAI_EXEC_RUNTIME")
			}
		}
		image := os.Getenv("CORRALAI_EXEC_IMAGE")
		c := containerIsolator{runtime: runtime, image: image}
		if err := c.Preflight(); err != nil {
			return nil, fmt.Errorf("container backend unavailable: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown exec backend %q (want bwrap|container|none)", cfg.Backend)
	}
}

// --- none: raw execution, explicit + unsafe only ---

type noneIsolator struct{}

func (noneIsolator) Name() string     { return "none" }
func (noneIsolator) Preflight() error { return nil }
func (noneIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	return []string{"sh", "-c", command}, nil
}
