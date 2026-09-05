// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A directory target writes one JSON file per push and nothing else;
// loading the directory back gives a warehouse-shaped view with the same
// identities (scan_uid, timestamp) the pushes minted — so a statement
// verified against the directory sees what it would see in a warehouse.
func TestLedgerDirIsOneFilePerPushAndLoadsAsAWarehouseView(t *testing.T) {
	dir := t.TempDir()
	kr := 0.5
	b := Bundle{
		Scan:    ScanRow{Repo: "r", Commit: "abcdef1234567890", ScanID: 1, CorralVersion: "vtest"},
		Files:   []Row{{Repo: "r", ScanID: 1, Path: "a.py", KillRate: &kr, Survivors: 2}},
		Mutants: []MutantRow{{Repo: "r", ScanID: 1, Path: "a.py", MutantID: "m1", Outcome: "survived", SpanStart: 3, SpanEnd: 3, Shape: "constant-changed", GeneratorModel: "g"}},
	}
	if _, err := PushBundle(dir+"/", b); err != nil {
		t.Fatal(err)
	}
	if _, err := PushBundle(dir, b); err != nil { // an existing directory, no trailing slash
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ScansSubdir))
	if len(entries) != 2 {
		t.Fatalf("want 2 files, one per push, got %d", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json.gz") {
			t.Errorf("a ledger dir holds gzipped JSON only, got %s", e.Name())
		}
	}
	files, err := ReadLedgerDir(dir)
	if err != nil || len(files) != 2 || files[0].ScanUID == "" || files[0].ScanUID == files[1].ScanUID {
		t.Fatalf("ReadLedgerDir: %v, %d files, uids %q/%q", err, len(files), files[0].ScanUID, files[1].ScanUID)
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var scans, mutants int
	var uid, shape string
	if err := db.QueryRow("SELECT count(*) FROM corral_scans").Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*), any_value(shape) FROM corral_mutants").Scan(&mutants, &shape); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT scan_uid FROM corral_scans ORDER BY ts LIMIT 1").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if scans != 2 || mutants != 2 || shape != "constant-changed" || uid != files[0].ScanUID {
		t.Errorf("view: scans=%d mutants=%d shape=%q uid=%q (file uid %q)", scans, mutants, shape, uid, files[0].ScanUID)
	}
	// The rows read back from the view are the bundle that was pushed.
	got, err := ReadBundleForScan(db, ScanRow{Repo: "r", ScanID: 1, ScanUID: files[0].ScanUID})
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "a.py" || len(got.Mutants) != 1 {
		t.Fatalf("ReadBundleForScan from the view: %v %+v", err, got)
	}
	// An empty directory is an empty view, not an error.
	if db2, err := LoadDir(t.TempDir()); err != nil {
		t.Fatal(err)
	} else {
		db2.Close()
	}
}

// The directory is a signed, hash-linked ledger: every entry names the
// previous one's hash and carries a signature; editing an entry breaks its
// hash, removing one breaks the next entry's link, and a reader with the
// public key can see both. Unsigned entries are said to be unsigned.
func TestLedgerDirIsASignedHashLinkedChain(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	SetLedgerSigner(Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv})
	t.Cleanup(func() { SetLedgerSigner(nil) })
	push := func(commit string) {
		if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: commit, ScanID: 1}, Files: []Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}); err != nil {
			t.Fatal(err)
		}
	}
	push("aaa1")
	push("bbb2")
	push("ccc3")
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 3 {
		t.Fatalf("want 3 entries, got %d", len(checks))
	}
	for i, c := range checks {
		if !c.HashOK || !c.LinkOK || !c.Signed || !c.SigOK || c.Problem != "" || c.KeyID != "corral-certify" {
			t.Errorf("entry %d: %+v", i, c)
		}
	}
	if !checks[0].Genesis || checks[1].Genesis {
		t.Errorf("only the first entry is a genesis: %+v %+v", checks[0], checks[1])
	}
	entries, _ := ReadLedgerDir(dir)
	if entries[0].Prev != "" || entries[1].Prev != entries[0].Hash || entries[2].Prev != entries[1].Hash {
		t.Fatalf("links: %q %q %q", entries[0].Prev, entries[1].Prev, entries[2].Prev)
	}

	// Tamper: edit the middle entry's bytes. Its hash no longer matches;
	// its successor's link still points at the stored (stale) hash, so the
	// edit is caught at the edited entry, by name.
	names, _ := ledgerFileNames(dir)
	mid := filepath.Join(dir, ScansSubdir, names[1])
	raw, _ := os.ReadFile(mid)
	plain, _ := readMaybeGzip(mid)
	edited := []byte(strings.Replace(string(plain), `"Path": "a.py"`, `"Path": "b.py"`, 1))
	if string(edited) == string(plain) {
		t.Fatal("fixture: the edit did not apply")
	}
	// Written back PLAIN under the .gz name would fail to gunzip, which is a
	// different failure; re-gzip so the only thing wrong is the bytes.
	_ = os.WriteFile(mid, gzipBytes(edited), 0o644)
	checks, _ = VerifyLedgerDir(dir, pub)
	if checks[1].HashOK || !strings.Contains(checks[1].Problem, "edited after it was written") || checks[2].Problem != "" {
		t.Errorf("edited entry not caught where it happened: %+v / %+v", checks[1], checks[2])
	}
	_ = os.WriteFile(mid, raw, 0o644)

	// Remove the middle entry: the third's link now names a hash that is
	// not its predecessor's.
	_ = os.Remove(mid)
	checks, _ = VerifyLedgerDir(dir, pub)
	if len(checks) != 2 || checks[1].LinkOK || !strings.Contains(checks[1].Problem, "removed, reordered or inserted") {
		t.Errorf("removed entry not caught: %+v", checks)
	}
	_ = os.WriteFile(mid, raw, 0o644)

	// A different key: signatures verify false, said as such; hashes and
	// links still hold — the chain is checkable without the key, only
	// authorship needs it.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	checks, _ = VerifyLedgerDir(dir, otherPub)
	if checks[0].SigOK || checks[0].Problem == "" || !checks[0].HashOK || !checks[0].LinkOK {
		t.Errorf("wrong key: %+v", checks[0])
	}
	// No key at all: signed but unverified, no problem claimed.
	checks, _ = VerifyLedgerDir(dir, nil)
	if !checks[0].Signed || checks[0].SigOK || checks[0].Problem != "" {
		t.Errorf("no key: %+v", checks[0])
	}

	// Unsigned entries: legal, linked, and said to be unsigned.
	SetLedgerSigner(nil)
	push("ddd4")
	checks, _ = VerifyLedgerDir(dir, pub)
	if last := checks[len(checks)-1]; last.Signed || !last.LinkOK || !last.HashOK {
		t.Errorf("unsigned entry: %+v", last)
	}
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}
