// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"os"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// NewSandboxVerify builds a VerifyFunc that certifies by EXECUTION: it runs the
// verify command under the given isolation backend, in the mission's own working
// copy (dir), with network off and a secret-free env, and reports whether the
// REAL exit code was 0. The worker's self-reported result is never consulted —
// this is the brain being a big dumb bouncer, not a judge that can be talked to.
func NewSandboxVerify(backend sandbox.Isolator) VerifyFunc {
	return func(ctx context.Context, dir, command string) (bool, string) {
		r := sandbox.Run(ctx, command, sandbox.Options{Workspace: dir, Backend: backend, Network: false})
		return r.ExitCode == 0, r.Output
	}
}

// workingCopyExists reports whether the brain has a materialized working copy at
// dir. Only then can it independently run a verify command; without one (a
// non-repo mission), the gate falls back to the recorded-execution lookup rather
// than fail every gated task.
func workingCopyExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}
