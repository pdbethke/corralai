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

	"github.com/pdbethke/corralai/internal/review"
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

// TestRetractionIsAnEntryNotADeletion: a retracted scan STAYS in the chain
// (the chain still verifies), and every reader of the record stops seeing
// it — the view, and ScanEntries, which the prior and the verdict cache go
// through. A retraction of something the chain never held is refused, a
// second retraction of the same entry is refused, and a retraction with no
// reason is refused.
func TestRetractionIsAnEntryNotADeletion(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })
	push := func(commit string) {
		if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: commit}, Files: []Row{{Repo: "r", Commit: commit, Path: "a.py", Disposition: "audited"}}}); err != nil {
			t.Fatal(err)
		}
	}
	push("aaa1")
	push("bbb2")
	push("ccc3")
	before, _ := ReadLedgerDir(dir)
	bad := before[1]

	// Negative controls first.
	if _, err := WriteRetraction(dir, bad.Hash, "   ", signer); err == nil {
		t.Fatal("a retraction with no reason was accepted")
	}
	if _, err := WriteRetraction(dir, "0000000000000000", "not here", signer); err == nil {
		t.Fatal("a retraction of a hash the chain never held was accepted")
	}

	name, err := WriteRetraction(dir, bad.Hash[:16], "the environment was broken: pytest could not import the package", signer)
	if err != nil {
		t.Fatalf("WriteRetraction: %v", err)
	}
	if !strings.Contains(name, "-retract-"+bad.Hash[:12]) {
		t.Errorf("file name %q does not say what it retracts", name)
	}
	if _, err := WriteRetraction(dir, bad.Hash, "again", signer); err == nil {
		t.Fatal("a second retraction of the same entry was accepted")
	}

	// The chain: four entries, all intact, the retraction noted in words.
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 4 {
		t.Fatalf("want 4 entries (3 scans + 1 retraction), got %d", len(checks))
	}
	for i, c := range checks {
		if c.Problem != "" || !c.SigOK {
			t.Errorf("entry %d: %+v", i, c)
		}
	}
	if checks[3].Kind != KindRetract || !strings.Contains(checks[3].Note, "retracts "+bad.Hash[:12]) || !strings.Contains(checks[3].Note, "pytest could not import") {
		t.Errorf("the retraction's note: %+v", checks[3])
	}

	// The record: readers see two scans, and never the retracted one.
	all, _ := ReadLedgerDir(dir)
	scans := ScanEntries(all)
	if len(all) != 4 || len(scans) != 2 {
		t.Fatalf("ReadLedgerDir=%d ScanEntries=%d, want 4 and 2", len(all), len(scans))
	}
	for _, e := range scans {
		if e.Hash == bad.Hash {
			t.Fatal("ScanEntries still returns the retracted entry")
		}
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_scans WHERE commit_sha = 'bbb2'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the view still holds the retracted scan: n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM corral_scans`).Scan(&n); err != nil || n != 2 {
		t.Errorf("the view holds %d scans, want 2 (err %v)", n, err)
	}

	// A forged retraction — one that names a hash not in the chain, hand
	// placed — is caught by the verifier by name.
	forged := LedgerEntry{Kind: KindRetract, Retracts: "deadbeefdeadbeefdeadbeef", Reason: "forged"}
	if _, err := AppendLedgerEntry(dir, forged, signer); err != nil {
		t.Fatal(err)
	}
	checks, _ = VerifyLedgerDir(dir, pub)
	if last := checks[len(checks)-1]; !strings.Contains(last.Problem, "not an earlier entry of this chain") {
		t.Errorf("a retraction of nothing must be a problem, got %+v", last)
	}
}

// TestCheckpointIsAGenesisThatNamesWhatItReplaced: after a checkpoint the
// directory holds one entry naming the pruned head and count, the chain
// verifies with that said in words, later entries link to the checkpoint,
// and a checkpoint anywhere but first is a problem by name.
func TestCheckpointIsAGenesisThatNamesWhatItReplaced(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })
	push := func(commit string) {
		if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: commit}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := WriteCheckpoint(dir, signer); err == nil {
		t.Fatal("a checkpoint of an empty directory was accepted")
	}
	push("aaa1")
	push("bbb2")
	push("ccc3")
	before, _ := ReadLedgerDir(dir)
	oldHead := before[2]

	name, pruned, err := WriteCheckpoint(dir, signer)
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if pruned != 3 || !strings.Contains(name, "-checkpoint-"+oldHead.Hash[:12]) {
		t.Errorf("pruned=%d name=%q", pruned, name)
	}
	after, _ := ReadLedgerDir(dir)
	if len(after) != 1 || after[0].Kind != KindCheckpoint || after[0].Prev != "" {
		t.Fatalf("after the checkpoint: %+v", after)
	}
	if cp := after[0].Checkpoint; cp == nil || cp.Head != oldHead.Hash || cp.Entries != 3 || !cp.Through.Equal(oldHead.Pushed) {
		t.Errorf("checkpoint names the wrong history: %+v", after[0].Checkpoint)
	}

	// New entries link to the checkpoint; the whole thing verifies, and
	// the verifier says where the history went.
	push("ddd4")
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0].Problem != "" || checks[1].Problem != "" || !checks[1].LinkOK {
		t.Fatalf("chain after checkpoint: %+v", checks)
	}
	if !checks[0].Genesis || checks[0].Kind != KindCheckpoint || !strings.Contains(checks[0].Note, "3 earlier entries") || !strings.Contains(checks[0].Note, oldHead.Hash[:12]) {
		t.Errorf("the checkpoint's note: %+v", checks[0])
	}
	if ScanEntries(after) != nil {
		t.Error("a checkpoint is not a scan")
	}

	// Negative control: a checkpoint cannot be appended mid-chain, and one
	// hand placed there is a problem by name.
	if _, err := AppendLedgerEntry(dir, LedgerEntry{Kind: KindCheckpoint, Checkpoint: &Checkpoint{Head: "x", Entries: 1}}, signer); err == nil {
		t.Fatal("a checkpoint was appended to a chain")
	}
	entries, _ := ReadLedgerDir(dir)
	if _, err := placeEntry(dir, LedgerEntry{Kind: KindCheckpoint, Checkpoint: &Checkpoint{Head: "x", Entries: 1}}, entries[len(entries)-1].Hash, signer); err != nil {
		t.Fatal(err)
	}
	checks, _ = VerifyLedgerDir(dir, pub)
	if last := checks[len(checks)-1]; !strings.Contains(last.Problem, "a checkpoint at position 3") {
		t.Errorf("a mid-chain checkpoint must be a problem, got %+v", last)
	}
}

// TestOldEntriesHashTheSameWithoutAKind: kinds are omitted when empty, so
// an entry written before kinds existed re-hashes to the hash it carries.
func TestOldEntriesHashTheSameWithoutAKind(t *testing.T) {
	e := LedgerEntry{Format: LedgerFileFormat, ScanUID: "u", Bundle: Bundle{Scan: ScanRow{Repo: "r"}}}
	h1, _ := EntryHash(e)
	e.Kind = KindScan
	h2, _ := EntryHash(e)
	if h1 != h2 {
		t.Fatal("KindScan changed an entry's hash — every existing ledger would stop verifying")
	}
	js, _ := CanonicalSparseJSON(e)
	if strings.Contains(string(js), `"kind"`) || strings.Contains(string(js), `"retracts"`) || strings.Contains(string(js), `"checkpoint"`) {
		t.Fatalf("a scan entry carries a kind field in its canonical bytes: %s", js)
	}
}

// TestReviewAndAdjudicationAreEntriesBesideTheScans: a review is one entry,
// an adjudication of one of its findings is another that names it, the
// chain verifies with both said in words, the newest adjudication per
// finding stands, and an adjudication of a finding the review does not
// have — or of a review the chain does not hold — is refused / named.
func TestReviewAndAdjudicationAreEntriesBesideTheScans(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })
	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteReview(dir, review.Review{Scope: "x"}, signer); err == nil {
		t.Fatal("a review with no commit was accepted")
	}
	code := 0
	r := review.Review{Repo: "r", Commit: "aaa1", Scope: "internal/x", ReviewerModel: "m", Opinion: "it trusts its callers",
		Findings: []review.Finding{
			{ID: "R1", Claim: "a", Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "exit 0", ExitCode: &code},
			{ID: "R2", Claim: "b", Declared: review.TierReproduced, Tier: review.TierCodeRead, Demoted: "exit 1"},
		}, Sound: []string{"the parser"}}
	name, err := WriteReview(dir, r, signer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "-review-aaa1") {
		t.Errorf("review file name %q", name)
	}
	entries, _ := ReadLedgerDir(dir)
	rev := entries[1]

	// Negative controls.
	for _, c := range []struct{ ref, verdict, by, reason string }{
		{rev.Hash + "#R9", VerdictConfirmed, "pdb", "no such finding"},
		{"0000000000000000#R1", VerdictConfirmed, "pdb", "no such review"},
		{rev.Hash + "#R1", "maybe", "pdb", "bad verdict"},
		{rev.Hash + "#R1", VerdictConfirmed, "", "nobody decided"},
		{rev.Hash + "#R1", VerdictConfirmed, "pdb", " "},
		{rev.Hash, VerdictConfirmed, "pdb", "no finding id"},
	} {
		if _, err := WriteAdjudication(dir, c.ref, c.verdict, c.by, c.reason, signer); err == nil {
			t.Errorf("accepted an adjudication that should be refused: %+v", c)
		}
	}

	if _, err := WriteAdjudication(dir, rev.Hash[:16]+"#R1", VerdictRefuted, "pdb", "the script proves a narrower claim", signer); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAdjudication(dir, rev.Hash+"#R1", VerdictConfirmed, "pdb", "on a second look, it holds", signer); err != nil {
		t.Fatal(err)
	}
	all, _ := ReadLedgerDir(dir)
	if len(all) != 4 {
		t.Fatalf("want 4 entries, got %d", len(all))
	}
	adj := Adjudications(all)
	if a := adj[rev.Hash+"#R1"]; a.Verdict != VerdictConfirmed || a.By != "pdb" {
		t.Errorf("the newest adjudication must stand: %+v", a)
	}
	if ScanEntries(all) == nil || len(ScanEntries(all)) != 1 {
		t.Error("reviews and adjudications are not scans")
	}
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range checks {
		if c.Problem != "" || !c.SigOK {
			t.Errorf("entry %d: %+v", i, c)
		}
	}
	if !strings.Contains(checks[1].Note, "review of internal/x by m: 1 reproduced, 1 code-read, 0 hypothesis") {
		t.Errorf("review note: %q", checks[1].Note)
	}
	if !strings.Contains(checks[3].Note, "confirmed "+rev.Hash[:12]+"#R1 by pdb") {
		t.Errorf("adjudication note: %q", checks[3].Note)
	}
	// A hand-placed adjudication of a review that is not in the chain.
	if _, err := AppendLedgerEntry(dir, LedgerEntry{Kind: KindAdjudication, Adjudication: &Adjudication{Adjudicates: "deadbeefdeadbeef#R1", Verdict: VerdictConfirmed, By: "x", Reason: "y"}}, signer); err != nil {
		t.Fatal(err)
	}
	checks, _ = VerifyLedgerDir(dir, pub)
	if last := checks[len(checks)-1]; !strings.Contains(last.Problem, "whose review is not an earlier entry") {
		t.Errorf("must be a problem by name: %+v", last)
	}
}
