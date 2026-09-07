// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The audited party, from the checkout: author and committer by name, the
// Co-authored-by trailers by name, never an address; a checkout that cannot
// say records nothing.
func TestCommitIdentityNamesThePartyWithoutAddresses(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada Writer", "GIT_AUTHOR_EMAIL=ada@example.test",
			"GIT_COMMITTER_NAME=Forge Bot", "GIT_COMMITTER_EMAIL=bot@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "--no-gpg-sign", "-m", "change\n\nCo-authored-by: Claude Code <noreply@anthropic.com>\nCo-authored-by: <onlyaddr@example.test>\nCo-authored-by: Codex\nCo-authored-by: Ada Writer <ada@example.test>\nCo-authored-by: claude code <x@y>")
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	sha := strings.TrimSpace(string(out))

	id := CommitIdentity(dir, sha)
	if id.Author != "Ada Writer" || id.Committer != "Forge Bot" {
		t.Fatalf("author/committer: %+v", id)
	}
	// The author repeated as a trailer, and a trailer repeated, fold: one
	// party is named once.
	if got := strings.Join(id.CoAuthorList(), "|"); got != "Claude Code|onlyaddr|Codex" {
		t.Fatalf("co-authors: %q", got)
	}
	if strings.Contains(id.CoAuthors, "@") {
		t.Fatalf("an address leaked: %q", id.CoAuthors)
	}
	if !CommitIdentity(t.TempDir(), sha).IsZero() || !CommitIdentity(dir, "").IsZero() {
		t.Fatal("a checkout that cannot say must record nothing")
	}
}

// The party rides the scan row through the ledger, the view and a read-back.
func TestScanRowCarriesThePartyThroughTheLedgerAndTheView(t *testing.T) {
	dir := t.TempDir() + "/"
	b := Bundle{Scan: ScanRow{Repo: "o/r", ScanID: 3, Commit: "deadbeef", Host: "h",
		Identity: Identity{Author: "Ada Writer", Committer: "Forge Bot", CoAuthors: "Claude Code\nCodex"}}}
	if _, err := PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	if entries[0].Bundle.Scan.Identity != b.Scan.Identity {
		t.Fatalf("entry: %+v", entries[0].Bundle.Scan.Identity)
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rb, err := ReadBundle(db, "o/r", 3)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Scan.Identity != b.Scan.Identity {
		t.Fatalf("view: %+v", rb.Scan.Identity)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM (SELECT unnest(string_split(co_authors, chr(10))) AS who FROM corral_scans) WHERE who = 'Claude Code'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("co_authors is one name per line for SQL: %d %v", n, err)
	}
}
