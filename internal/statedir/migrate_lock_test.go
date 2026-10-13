// SPDX-License-Identifier: Elastic-2.0

//go:build cgo && unix

package statedir

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// TestHelperHoldDuckDB is not a test: the parent re-execs the test binary
// into it so a DIFFERENT process holds the store open, like the daemon.
func TestHelperHoldDuckDB(t *testing.T) {
	p := os.Getenv("CORRAL_TEST_HOLD_DUCKDB")
	if p == "" {
		t.Skip("helper process only")
	}
	db, err := sql.Open("duckdb", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS t(x INT)"); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	_, _ = io.ReadAll(os.Stdin)
	_ = db.Close()
}

func TestMigrateRefusesWhileAnotherProcessHoldsAStore(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".claude")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": filepath.Join(home, "st")}, home)
	store := filepath.Join(legacy, "corralai_memory.duckdb")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldDuckDB$") // #nosec G204 -- re-exec of this test binary
	cmd.Env = append(os.Environ(), "CORRAL_TEST_HOLD_DUCKDB="+store)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() && sc.Text() != "ready" {
	}

	if _, _, err := PlanMigration(legacy, PlanOptions{}); err == nil || !strings.Contains(err.Error(), "stop the daemon") {
		t.Fatalf("want a refusal while another process holds the store, got %v", err)
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	if _, _, err := PlanMigration(legacy, PlanOptions{}); err != nil {
		t.Fatalf("after the holder exits, planning must succeed: %v", err)
	}
}
