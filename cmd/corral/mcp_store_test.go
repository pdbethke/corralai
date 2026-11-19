// SPDX-License-Identifier: Elastic-2.0

//go:build cgo && unix

package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/criticscore"
)

// These drive the reader `corral mcp` actually serves (storeFindings) against
// a real DuckDB file, with the competing session in a SEPARATE process:
// DuckDB's lock is per process, so a second handle in this one would prove
// nothing. Observed 2026-11-12: every Claude Code session spawns its own
// `corral mcp`, the first held the store read-write, and every later one died
// at startup with "Conflicting lock is held".

const mcpHoldEnv = "CORRAL_TEST_HOLD_FINDINGS"

// TestHelperHoldFindingsStore is not a test: the parent re-execs the test
// binary into it so a DIFFERENT process holds the store, as another session's
// `corral mcp` ("ro") or a running `certify --local` ("rw") would.
func TestHelperHoldFindingsStore(t *testing.T) {
	spec := os.Getenv(mcpHoldEnv)
	if spec == "" {
		t.Skip("helper process only")
	}
	mode, path, _ := strings.Cut(spec, ":")
	open := criticscore.OpenReadOnly
	if mode == "rw" {
		open = criticscore.Open
	}
	s, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = io.ReadAll(os.Stdin)
	_ = s.Close()
}

// startFindingsHolder runs the helper holding path in mode. It returns a
// stop function; the holder is also stopped at cleanup. ok is false when the
// holder could NOT open the store (its output is in msg).
func startFindingsHolder(t *testing.T, mode, path string) (msg string, ok bool, stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldFindingsStore$") // #nosec G204 -- re-exec of this test binary
	cmd.Env = append(os.Environ(), mcpHoldEnv+"="+mode+":"+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			_ = stdin.Close()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	return line, strings.TrimSpace(line) == "ready", stop
}

func seedFindingsStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cs.duckdb")
	rw, err := criticscore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.Record(context.Background(), sampleFindings()); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Two sessions at once: another process already serves the store, and this
// session's reader still lists and gets findings.
func TestMCPStoreReadsWhileAnotherSessionHoldsIt(t *testing.T) {
	path := seedFindingsStore(t)
	if msg, ok, _ := startFindingsHolder(t, "ro", path); !ok {
		t.Fatalf("holder could not open the store read-only: %q", msg)
	}
	r := storeFindings{path: path}
	all, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("second session could not read while another held the store: %v", err)
	}
	if len(all) != len(sampleFindings()) {
		t.Fatalf("read %d findings, want %d", len(all), len(sampleFindings()))
	}
	if _, ok, err := r.Get(context.Background(), "32:1"); err != nil || !ok {
		t.Fatalf("get 32:1: ok=%v err=%v", ok, err)
	}
}

// An idle `corral mcp` must not keep a writer out: after a call returns, a
// separate process can open the store read-write, as `certify --local` must.
func TestMCPStoreHoldsNoLockBetweenCalls(t *testing.T) {
	path := seedFindingsStore(t)
	r := storeFindings{path: path}
	if _, err := r.All(context.Background()); err != nil {
		t.Fatal(err)
	}
	if msg, ok, _ := startFindingsHolder(t, "rw", path); !ok {
		t.Fatalf("a writer could not open the store after an mcp call returned: %q", msg)
	}
}

// No store yet (no `certify --local` has run on this machine): the server
// answers with zero findings and does NOT create the file.
func TestMCPStoreMissingIsEmptyAndNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "corralai_criticscore.duckdb")
	r := storeFindings{path: path}
	all, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("missing store should read as no findings, got %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("missing store returned %d findings", len(all))
	}
	if _, ok, err := r.Get(context.Background(), "1:1"); err != nil || ok {
		t.Fatalf("get on a missing store: ok=%v err=%v, want not found", ok, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("reading created %s (stat err %v)", path, statErr)
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !os.IsNotExist(statErr) {
		t.Fatalf("reading created the directory %s", filepath.Dir(path))
	}
	out := callTool(t, r, "list_audit_findings", map[string]any{})
	if !strings.Contains(out, `"total":0`) {
		t.Fatalf("list on a missing store = %s, want total 0", out)
	}
}

// While a writer holds the store, a call fails with an error that says why in
// plain words, rather than DuckDB's lock text alone.
func TestMCPStoreBusyWriterIsExplained(t *testing.T) {
	path := seedFindingsStore(t)
	msg, ok, stop := startFindingsHolder(t, "rw", path)
	if !ok {
		t.Fatalf("holder could not open the store read-write: %q", msg)
	}
	defer stop()
	_, err := storeFindings{path: path}.All(context.Background())
	if err == nil {
		t.Fatal("read succeeded while another process held the store read-write; DuckDB's limit changed — revisit criticscore.OpenReadOnly's comment")
	}
	if !strings.Contains(err.Error(), "writing the findings store") {
		t.Fatalf("error does not explain the busy writer: %v", err)
	}
}
