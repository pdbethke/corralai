// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/review"
)

// rankViewDir writes a ledger directory of one scan entry (3 audited file
// rows, excluded rows that a ranking never reads, mutants, model calls and
// events) followed by one review entry carrying two findings. The excluded
// count is 2,000 so a load that inserts them anyway is visible, not just
// slow.
func rankViewDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	kr := 0.5
	b := Bundle{Scan: ScanRow{Repo: "r", Commit: "abcdef1234567890", ScanID: 1, CorralVersion: "vtest"}}
	for i := 0; i < 3; i++ {
		b.Files = append(b.Files, Row{Repo: "r", ScanID: 1, Path: fmt.Sprintf("a%d.py", i), Disposition: "audited", KillRate: &kr, Survivors: i})
	}
	for i := 0; i < 2000; i++ {
		b.Files = append(b.Files, Row{Repo: "r", ScanID: 1, Path: fmt.Sprintf("x%d.py", i), Disposition: "excluded", Reason: "test file"})
	}
	for i := 0; i < 40; i++ {
		b.Mutants = append(b.Mutants, MutantRow{Repo: "r", ScanID: 1, Path: "a0.py", MutantID: fmt.Sprintf("m%d", i), Outcome: "survived"})
	}
	for i := 0; i < 12; i++ {
		b.Calls = append(b.Calls, ModelCallRow{Repo: "r", ScanID: 1, Path: "a0.py", Role: fmt.Sprintf("role%d", i), Model: "m", Calls: 1})
	}
	for i := 0; i < 30; i++ {
		b.Events = append(b.Events, EventRow{Repo: "r", ScanID: 1, Path: "a0.py", Seq: int64(i), Kind: "k", Actor: "a", Subject: "s", Model: "m", TS: time.Now().UTC().Truncate(time.Microsecond)})
	}
	if _, err := PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	rev := LedgerEntry{Kind: KindReview, Pushed: time.Now().UTC().Truncate(time.Microsecond), Review: &review.Review{
		Repo: "o/r", Commit: "c", Scope: "s", ReviewerModel: "m",
		Findings: []review.Finding{
			{ID: "R1", Claim: "one", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"},
			{ID: "R2", Claim: "two", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"},
		},
	}}
	if _, err := AppendLedgerEntry(dir, rev, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

func countOf(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestLoadDirForRankHoldsOnlyWhatRankReads(t *testing.T) {
	db, err := LoadDirForRank(rankViewDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		`SELECT count(*) FROM corral_scans`:                                1,
		`SELECT count(*) FROM corral_audits`:                               3,
		`SELECT count(*) FROM corral_audits WHERE disposition = 'audited'`: 3,
		`SELECT count(*) FROM corral_mutants`:                              0,
		`SELECT count(*) FROM corral_model_calls`:                          0,
		`SELECT count(*) FROM corral_events`:                               0,
		`SELECT count(*) FROM corral_reviews`:                              1,
		`SELECT count(*) FROM corral_findings`:                             2,
	} {
		if got := countOf(t, db, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

func TestLoadDirForRankAgreesWithLoadDirOnAuditedRows(t *testing.T) {
	dir := rankViewDir(t)
	full, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	slim, err := LoadDirForRank(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer slim.Close()
	dump := func(db *sql.DB) []string {
		rows, err := db.Query(`SELECT * FROM corral_audits WHERE disposition = 'audited' ORDER BY path`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out []string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(vals...))
		}
		return out
	}
	a, b := dump(full), dump(slim)
	if len(a) != 3 || len(a) != len(b) {
		t.Fatalf("audited rows: full %d, rank %d, want 3 each", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("row %d differs:\n full %s\n rank %s", i, a[i], b[i])
		}
	}
}

func TestLoadDirForRankRefusesABrokenChain(t *testing.T) {
	dir, _ := tamperedChain(t, 2)
	_, errFull := LoadDir(dir)
	db, err := LoadDirForRank(dir)
	if err == nil {
		db.Close()
		t.Fatal("a chain that does not verify was loaded")
	}
	if errFull == nil || !strings.Contains(err.Error(), "refusing to load a record that does not verify") {
		t.Fatalf("LoadDirForRank error = %v; LoadDir error = %v", err, errFull)
	}
}

func TestLoadDirForRankIgnoresAnEntryStillBeingWritten(t *testing.T) {
	dir := rankViewDir(t)
	tmp := filepath.Join(dir, ScansSubdir, ".20261116T000000Z-x.json.gz.tmp")
	if err := os.WriteFile(tmp, []byte("not a gzip stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadDirForRank(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := countOf(t, db, `SELECT count(*) FROM corral_scans`); got != 1 {
		t.Errorf("corral_scans = %d, want 1", got)
	}
}
