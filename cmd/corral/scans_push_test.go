// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// seedLedger builds a temp local ledger with two recorded scans, each
// carrying a file, a mutant, a model call and an event — the five grains
// `scans push` has to move. Returns the ledger's DSN; the caller opens it
// (scanstore.Open is a create-if-absent handle, so the seeding store must be
// closed before anything else opens the same file).
func seedLedger(t *testing.T) string {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "ledger.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer st.Close()

	for i, repo := range []string{"o/one", "o/two"} {
		scan := scanstore.Scan{
			Owner: "o", Repo: repo, Commit: "sha" + repo[2:],
			Substrate: "workspace", EngineVersion: "v9.9.9", ModelSet: "unset",
			Candidates: 2, Audited: 1, TotalFiles: 2,
			CorralVersion: "v9.9.9", Host: "box", Cores: 4,
			StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
			TotalMillis: 60000,
		}
		id, rerr := st.Record(context.Background(), scan, []scanstore.File{{
			Path: "pkg/a.go", Lang: "go", Disposition: "audited",
			KillRate: f64p(0.75), Survivors: 2, ProvenMissed: 1,
			ParentSHA256: "aaaa", Evidence: "proven", Status: "certified",
			MutantsGraded: 4, ModelsByRole: `{"mutant-generator":"gemini-3.6-flash"}`,
			// AuthoredTest and VerdictJSON are the source-bearing fields
			// `scans push` must never let leave the box.
			AuthoredTest: "func TestX(t *testing.T){}",
			VerdictJSON:  `{"dev_kill_rate":0.75}`,
		}})
		if rerr != nil {
			t.Fatalf("record scan %d: %v", i, rerr)
		}
		if merr := st.RecordMutants(context.Background(), []scanstore.Mutant{{
			ScanID: id, Path: "pkg/a.go", MutantID: "m1", Outcome: "killed",
			ParentSHA256: "aaaa", TestsRun: 3,
		}}); merr != nil {
			t.Fatalf("record mutants scan %d: %v", i, merr)
		}
		if cerr := st.RecordModelCalls(context.Background(), []scanstore.ModelCall{{
			ScanID: id, Path: "pkg/a.go", Role: "mutant-generator", Model: "gemini-3.6-flash",
			Calls: 1, InputTokens: 100, OutputTokens: 50, WallMillis: 500,
		}}); cerr != nil {
			t.Fatalf("record model calls scan %d: %v", i, cerr)
		}
		if eerr := st.RecordEvents(context.Background(), []scanstore.Event{{
			ScanID: id, Path: "pkg/a.go", Seq: 1, TS: time.Now(), Kind: "graded", Actor: "pool",
		}}); eerr != nil {
			t.Fatalf("record events scan %d: %v", i, eerr)
		}
	}
	return dsn
}

func f64p(v float64) *float64 { return &v }

func TestScansPush_MovesAllFiveGrainsAndBlanksSource(t *testing.T) {
	ledgerDSN := seedLedger(t)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")

	var out, errOut bytes.Buffer
	code := runScansPush([]string{"--db", target, "--all"},
		func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) },
		auditpush.PushBundle, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}

	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer db.Close()

	for _, tc := range []struct {
		table string
		want  int
	}{
		{"corral_scans", 2},
		{"corral_audits", 2},
		{"corral_mutants", 2},
		{"corral_model_calls", 2},
		{"corral_events", 2},
	} {
		var n int
		if qerr := db.QueryRow("SELECT count(*) FROM " + tc.table).Scan(&n); qerr != nil {
			t.Fatalf("count %s: %v", tc.table, qerr)
		}
		if n != tc.want {
			t.Errorf("%s rows = %d, want %d", tc.table, n, tc.want)
		}
	}

	// THE SECURITY PROPERTY: source never reaches the warehouse through
	// this verb, even though the ledger rows above carry AuthoredTest and
	// VerdictJSON.
	rows, qerr := db.Query(`SELECT authored_test, verdict_json FROM corral_audits`)
	if qerr != nil {
		t.Fatalf("query source columns: %v", qerr)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		var authored, verdict sql.NullString
		if serr := rows.Scan(&authored, &verdict); serr != nil {
			t.Fatalf("scan source columns: %v", serr)
		}
		if authored.Valid && authored.String != "" {
			t.Errorf("authored_test leaked: %q", authored.String)
		}
		if verdict.Valid && verdict.String != "" {
			t.Errorf("verdict_json leaked: %q", verdict.String)
		}
	}
	if n != 2 {
		t.Fatalf("expected to check 2 rows, checked %d", n)
	}

	if !strings.Contains(out.String(), "pushed 2 scan(s)") {
		t.Errorf("summary line missing from:\n%s", out.String())
	}
}

func TestScansPush_DryRunTouchesNothing(t *testing.T) {
	ledgerDSN := seedLedger(t)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")

	pushCalled := false
	fakePush := func(target string, b auditpush.Bundle) (auditpush.Counts, error) {
		pushCalled = true
		return auditpush.Counts{}, nil
	}

	var out, errOut bytes.Buffer
	code := runScansPush([]string{"--db", target, "--all", "--dry-run"},
		func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) },
		fakePush, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if pushCalled {
		t.Fatal("--dry-run called the pusher — it must touch nothing")
	}
	if !strings.Contains(out.String(), "would push 2 scan(s)") {
		t.Errorf("dry-run summary missing from:\n%s", out.String())
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("--dry-run created the target warehouse file")
	}
}

func TestScansPush_UnknownScanIDRefuses(t *testing.T) {
	ledgerDSN := seedLedger(t)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")

	var out, errOut bytes.Buffer
	code := runScansPush([]string{"--db", target, "--scan", "9999"},
		func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) },
		auditpush.PushBundle, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "9999") {
		t.Errorf("error should name the missing scan id: %s", errOut.String())
	}
}

func TestScansPush_EmptyDBRefuses(t *testing.T) {
	ledgerDSN := seedLedger(t)
	var out, errOut bytes.Buffer
	code := runScansPush([]string{"--all"},
		func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) },
		auditpush.PushBundle, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "--db") {
		t.Errorf("error should name --db as missing: %s", errOut.String())
	}
}

func TestScansPush_NoSelectorRefuses(t *testing.T) {
	ledgerDSN := seedLedger(t)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	var out, errOut bytes.Buffer
	code := runScansPush([]string{"--db", target},
		func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) },
		auditpush.PushBundle, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errOut.String())
	}
}

// TestScansPush_RepeatPushAppends pins the documented contract: the
// warehouse tables are append-only, so pushing the same scan twice adds its
// rows a second time rather than overwriting or deduping them.
func TestScansPush_RepeatPushAppends(t *testing.T) {
	ledgerDSN := seedLedger(t)
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	opener := func(dsn string) (scansPushReader, error) { return scanstore.Open(ledgerDSN) }

	var out1, err1 bytes.Buffer
	if code := runScansPush([]string{"--db", target, "--scan", "1"}, opener, auditpush.PushBundle, &out1, &err1); code != 0 {
		t.Fatalf("first push exit = %d, stderr=%s", code, err1.String())
	}
	var out2, err2 bytes.Buffer
	if code := runScansPush([]string{"--db", target, "--scan", "1"}, opener, auditpush.PushBundle, &out2, &err2); code != 0 {
		t.Fatalf("second push exit = %d, stderr=%s", code, err2.String())
	}

	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer db.Close()
	var n int
	if qerr := db.QueryRow(`SELECT count(*) FROM corral_audits`).Scan(&n); qerr != nil {
		t.Fatalf("count corral_audits: %v", qerr)
	}
	if n != 2 {
		t.Errorf("corral_audits rows after two pushes of the same scan = %d, want 2 (append-only duplicates rather than dedupes)", n)
	}
}
