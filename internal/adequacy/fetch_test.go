// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// TestFetchStepIsTheOnlyNetworkedRun pins where the per-call difference
// lives: the fetch step gets the network and sandbox.FetchEnv, and a graded
// run in the same process — with the same proxy in the host environment —
// gets neither. The grading jail's options are unchanged by the fetch step's
// existence.
func TestFetchStepIsTheOnlyNetworkedRun(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	var fetchOpts, gradeOpts sandbox.Options

	dir := t.TempDir()
	fetch := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { fetchOpts = o }}
	if _, _, err := RunFetchStep(context.Background(), fetch, dir, []string{"true"}, time.Second); err != nil {
		t.Fatalf("RunFetchStep: %v", err)
	}
	if !fetchOpts.Network {
		t.Error("the fetch step must have the network")
	}
	if fetchOpts.Workspace != dir {
		t.Errorf("fetch Workspace = %q, want the caller's staging dir %q", fetchOpts.Workspace, dir)
	}
	if !slices.Contains(fetchOpts.Env, "HTTPS_PROXY=http://proxy.example:3128") {
		t.Error("the fetch step must carry the operator's proxy route (sandbox.FetchEnv)")
	}

	grade := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { gradeOpts = o }}
	if _, err := NewJail(grade, time.Second).RunTest(context.Background(), map[string]string{"a": "1"}, []string{"true"}); err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if gradeOpts.Network {
		t.Error("a graded run must stay offline")
	}
	for _, kv := range gradeOpts.Env {
		if strings.HasPrefix(kv, "HTTPS_PROXY=") {
			t.Error("a graded run must not receive fetch routes; its env is MinimalEnv")
		}
	}
	if !slices.Equal(gradeOpts.Env, sandbox.MinimalEnv()) {
		t.Errorf("graded env = %v, want MinimalEnv %v", gradeOpts.Env, sandbox.MinimalEnv())
	}
}

// TestFetchStepRefusesWithoutABackend: no host fallback, ever.
func TestFetchStepRefusesWithoutABackend(t *testing.T) {
	if _, _, err := RunFetchStep(context.Background(), nil, t.TempDir(), []string{"true"}, time.Second); err == nil {
		t.Fatal("a nil backend must refuse, not run the fetch on the host")
	}
}

func hostIsolator(t *testing.T) sandbox.Isolator {
	t.Helper()
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "none", UnsafeHost: true})
	if err != nil {
		t.Fatal(err)
	}
	return iso
}

// TestFetchStepSeparatesStdout: stdout is the step's DATA and comes back on
// its own; stderr stays in the result's diagnostic capture; the redirect
// file is removed.
func TestFetchStepSeparatesStdout(t *testing.T) {
	dir := t.TempDir()
	out, res, err := RunFetchStep(context.Background(), hostIsolator(t), dir, []string{"sh", "-c", "echo data; echo progress >&2"}, 5*time.Second)
	if err != nil {
		t.Fatalf("RunFetchStep: %v (%s)", err, res.Output)
	}
	if out != "data\n" {
		t.Errorf("stdout = %q, want %q", out, "data\n")
	}
	if !strings.Contains(res.Output, "progress") || strings.Contains(res.Output, "data") {
		t.Errorf("result output = %q, want stderr only", res.Output)
	}
	if _, serr := os.Lstat(filepath.Join(dir, fetchStdoutName)); !os.IsNotExist(serr) {
		t.Errorf("%s must be removed after the step; lstat err = %v", fetchStdoutName, serr)
	}
}

// TestFetchStepRefusesASymlinkedStdout: the jailed command is untrusted; if
// it swaps the stdout file for a symlink, corral must not read through it
// on the host.
func TestFetchStepRefusesASymlinkedStdout(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("host secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := []string{"sh", "-c", "rm -f " + fetchStdoutName + " && ln -s " + secret + " " + fetchStdoutName}
	out, _, err := RunFetchStep(context.Background(), hostIsolator(t), dir, cmd, 5*time.Second)
	if err == nil || strings.Contains(out, "host secret") {
		t.Fatalf("a symlinked stdout must be refused; got out=%q err=%v", out, err)
	}
}
