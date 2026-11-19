// SPDX-License-Identifier: Elastic-2.0

//go:build cgo && unix

package criticscore_test

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

// holdEnv names the store, and the mode ("ro" or "rw"), a re-executed test
// binary should hold open. DuckDB's file lock is a per-PROCESS lock, so two
// handles inside one test process prove nothing about the defect this guards:
// every Claude Code session spawns its own `corral mcp`, and the second one
// failed with "Conflicting lock is held". The holder is a separate process
// for that reason. Same shape as statedir's TestHelperHoldDuckDB.
const holdEnv = "CRITICSCORE_TEST_HOLD"

// TestHelperHoldCriticStore is not a test: the parent re-execs the test
// binary into it so a DIFFERENT process holds the store open.
func TestHelperHoldCriticStore(t *testing.T) {
	spec := os.Getenv(holdEnv)
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

// holdStore starts a second process holding path open in mode and returns
// once it has. The holder exits at test cleanup.
func holdStore(t *testing.T, mode, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldCriticStore$") // #nosec G204 -- re-exec of this test binary
	cmd.Env = append(os.Environ(), holdEnv+"="+mode+":"+path)
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
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if strings.TrimSpace(line) != "ready" {
		t.Fatalf("holder process could not open the store %s: %q", mode, line)
	}
}

// seededStore writes one finding through the read-write Open and closes it,
// as a finished `certify --local` leaves the store.
func seededStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cs.duckdb")
	rw, err := criticscore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.Record(context.Background(), []criticscore.Finding{
		{ID: "1:1", RecordID: 1, Model: "m", Scope: "whole-test", Adjudication: "unadjudicated", Source: "auto"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Two `corral mcp` sessions at once: one process holds the store read-only,
// and a second read-only open in another process succeeds and reads.
func TestOpenReadOnlySharesAcrossProcesses(t *testing.T) {
	path := seededStore(t)
	holdStore(t, "ro", path)

	ro, err := criticscore.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("a second read-only open failed while another process held one: %v", err)
	}
	defer ro.Close()
	all, err := ro.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "1:1" {
		t.Fatalf("read-only handle read %+v, want the one recorded finding", all)
	}
}

// The limit OpenReadOnly's doc comment states, executed rather than asserted:
// a read-only open does NOT share with a concurrent read-write process. If a
// DuckDB upgrade lifts this, this test fails and the comment should change.
func TestOpenReadOnlyStillBlockedByAConcurrentWriter(t *testing.T) {
	path := seededStore(t)
	holdStore(t, "rw", path)

	ro, err := criticscore.OpenReadOnly(path)
	if err == nil {
		_ = ro.Close()
		t.Fatal("read-only open succeeded while another process held the store read-write; update OpenReadOnly's doc comment")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Fatalf("want DuckDB's lock conflict, got %v", err)
	}
}
