//go:build windows

package sandbox

import (
	"errors"
)

// newWindowsJobIsolator refuses to run. A Windows Job Object caps resources and
// kills child processes when the job closes, but provides NO filesystem or
// network isolation and no privilege drop — it is not a security boundary
// against a hijacked agent. Wrapping a command in `cmd.exe /c` while reporting
// backend name "windows-job" would masquerade as a sandbox, violating Resolve's
// contract ("NEVER falls back to a weaker backend"). So on Windows, agent
// execution refuses by default and the operator must make an explicit choice:
// a real container boundary, or an acknowledged-unsafe host.
func newWindowsJobIsolator() (Isolator, error) {
	return nil, errors.New("windows has no built-in agent sandbox (a Job Object gives no filesystem/network isolation); " +
		"set CORRALAI_EXEC_BACKEND=container (docker/podman) for real isolation, " +
		"or CORRALAI_EXEC_BACKEND=none with AGENT_EXEC_UNSAFE_HOST=1 to run UNISOLATED on an already-disposable host")
}
