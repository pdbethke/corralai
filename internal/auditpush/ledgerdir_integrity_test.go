// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tamperedChain writes n entries, then edits the LAST one's bytes in place
// without touching its stored hash — the shape a verifier is supposed to
// catch: "entry bytes do not match its hash".
func tamperedChain(t *testing.T, n int) (dir string, pub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	SetLedgerSigner(Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv})
	t.Cleanup(func() { SetLedgerSigner(nil) })

	for i := 0; i < n; i++ {
		if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: string(rune('a'+i)) + "aa1", Audited: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil || len(entries) != n {
		t.Fatalf("setup: %v, %d entries", err, len(entries))
	}
	path := filepath.Join(dir, ScansSubdir, entries[n-1].File)
	raw, err := readMaybeGzip(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(raw, []byte(`"Audited": 1`), []byte(`"Audited": 9`), 1)
	if bytes.Equal(edited, raw) {
		t.Fatalf("setup: nothing was edited — the field this test tampers with is gone")
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(edited); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if checks[n-1].Problem == "" {
		t.Fatalf("setup: the tampered entry verifies clean, so this test proves nothing")
	}
	return dir, pub
}

// A checkpoint DELETES the entries it stands in for. If it does not verify
// the chain first, it is a laundering machine: the one verb that destroys
// evidence would be the one verb that never looks at it. Found by a cold
// review of internal/auditpush, 2026-09-08 (R1, reproduced).
func TestCheckpointRefusesToPruneAChainThatDoesNotVerify(t *testing.T) {
	dir, pub := tamperedChain(t, 3)

	_, pruned, err := WriteCheckpoint(dir, ledgerSigner)
	if err == nil {
		t.Fatalf("checkpoint pruned %d entries of a chain that does not verify — a tampered entry can be deleted and the genesis left verifying clean", pruned)
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("refused, but not for the stated reason: %v", err)
	}
	// and it must refuse BEFORE deleting anything
	entries, rerr := ReadLedgerDir(dir)
	if rerr != nil || len(entries) != 3 {
		t.Fatalf("entries were removed despite the refusal: %v, %d left", rerr, len(entries))
	}
	if _, verr := VerifyLedgerDir(dir, pub); verr != nil {
		t.Fatal(verr)
	}
}

// Appending links to the head's STORED hash. If that hash is not recomputed,
// a new signed entry extends a chain whose head was edited — corral's own
// signature vouching for a tampered predecessor. (R6, code-read.)
func TestAppendRefusesToExtendAChainThatDoesNotVerify(t *testing.T) {
	dir, _ := tamperedChain(t, 2)

	_, err := AppendLedgerEntry(dir, LedgerEntry{Kind: KindRetract, Retracts: "deadbeef", Reason: "x"}, ledgerSigner)
	if err == nil {
		t.Fatal("appended onto a chain whose head does not verify — the new entry signs a tampered predecessor as its parent")
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("refused, but not for the stated reason: %v", err)
	}
}

// keyid names WHO signed. Before corral-ledger-4 it was deleted before
// hashing and never compared to the verifying key, so rewriting it in a
// placed, signed entry left HashOK, SigOK and Problem untouched while the
// verifier printed the attacker's chosen name as the signer — the record
// lying about its own custody. (R2, reproduced.) From corral-ledger-4 the
// keyid is inside the hashed bytes, so editing it breaks the hash.
func TestKeyIDIsCoveredByTheHash(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	SetLedgerSigner(Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv})
	t.Cleanup(func() { SetLedgerSigner(nil) })

	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1", Audited: 1}}); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("setup: %v, %d entries", err, len(entries))
	}
	if entries[0].Format != LedgerFileFormat {
		t.Fatalf("setup: new entries must be written at %s, got %s", LedgerFileFormat, entries[0].Format)
	}
	before, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !before[0].HashOK || !before[0].SigOK || before[0].Problem != "" {
		t.Fatalf("setup: the untouched entry did not verify clean: %+v", before[0])
	}

	path := filepath.Join(dir, ScansSubdir, entries[0].File)
	raw, err := readMaybeGzip(path)
	if err != nil {
		t.Fatal(err)
	}
	old := []byte(`"keyid": "corral-certify"`)
	if !bytes.Contains(raw, old) {
		t.Fatalf("setup: %q not found in the entry as written", old)
	}
	edited := bytes.Replace(raw, old, []byte(`"keyid": "acme-corp-hsm-key-7"`), 1)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(edited); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].HashOK {
		t.Fatalf("the signer name was rewritten to %q and the entry still hashes clean — the record vouches for a custody claim nobody signed", after[0].KeyID)
	}
	if after[0].Problem == "" {
		t.Fatal("the signer name was rewritten and the verifier reported no problem")
	}
}

// CanonicalizeForWarehouse takes *Bundle and mutates in place. A Bundle
// copied by value still SHARES its slice backing arrays, so canonicalizing
// a copy silently truncated the caller's event timestamps and nil'd the
// caller's kill rates — the exact aliasing its partner BlankUnpushedSource
// copies to avoid. (R3, reproduced.)
func TestCanonicalizeDoesNotMutateTheCallersBundle(t *testing.T) {
	ts := time.Date(2026, 9, 8, 1, 2, 3, 123456789, time.UTC)
	rate := 0.5
	orig := Bundle{
		Events: []EventRow{{TS: ts}},
		Files:  []Row{{Uncovered: true, KillRate: &rate}},
	}
	cp := orig // by value — the slices are still shared
	CanonicalizeForWarehouse(&cp)

	if !orig.Events[0].TS.Equal(ts) {
		t.Errorf("the caller's event timestamp was truncated through the copy: %v, want %v", orig.Events[0].TS, ts)
	}
	if orig.Files[0].KillRate == nil {
		t.Error("the caller's kill rate was nil'd through the copy")
	}
	// and the copy must still have been canonicalized
	if cp.Events[0].TS.Nanosecond()%1000 != 0 {
		t.Error("the copy was not canonicalized")
	}
}

// Retracting a retraction should put the original entry back. Retracted()
// was built over every retraction including ones already retracted, so the
// undo was accepted and did nothing. (R7.)
func TestRetractingARetractionRestoresTheEntry(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })

	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1", Audited: 1}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	scanHash := entries[0].Hash

	if _, err := WriteRetraction(dir, scanHash, "mistake", signer); err != nil {
		t.Fatal(err)
	}
	entries, _ = ReadLedgerDir(dir)
	if got := len(ScanEntries(entries)); got != 0 {
		t.Fatalf("after retraction the scan is still in the record (%d)", got)
	}
	retractionHash := entries[len(entries)-1].Hash

	if _, err := WriteRetraction(dir, retractionHash, "the retraction was the mistake", signer); err != nil {
		t.Fatalf("retracting a retraction was refused: %v", err)
	}
	entries, _ = ReadLedgerDir(dir)
	if got := len(ScanEntries(entries)); got != 1 {
		t.Fatalf("retracting the retraction left the scan out of the record: %d scan entries, want 1 — a mistaken retraction cannot be undone", got)
	}
}

// Retraction depth. R1 of the SECOND review of this package: the first fix
// grew a monotone "void" set, but voiding is not monotone — once a
// retraction is itself retracted, everything it voided comes back — so at
// odd depth >= 3 the original entry was reported as NOT retracted. Resolved
// in one reverse pass instead: a retraction can only name an EARLIER entry,
// so by the time the walk reaches it, every retraction that could void it
// has been decided.
func TestRetractionDepthAlternates(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })

	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1", Audited: 1}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	target := entries[0].Hash

	// depth 1 retracts, 2 restores, 3 retracts, 4 restores, …
	for depth := 1; depth <= 5; depth++ {
		if _, err := WriteRetraction(dir, target, "depth", signer); err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		entries, _ = ReadLedgerDir(dir)
		target = entries[len(entries)-1].Hash // the retraction just written

		got := len(ScanEntries(entries))
		want := 0
		if depth%2 == 0 {
			want = 1
		}
		if got != want {
			t.Errorf("depth %d: %d scan entries in the record, want %d — an odd depth retracts, an even depth restores", depth, got, want)
		}
	}
}

// An adjudication rules on a FINDING, which only a review has. `seen` held
// every entry hash, so an adjudication naming a scan passed verification as
// though a finding had been ruled on. (Second review of this package, R4.)
func TestAdjudicationMustNameAReview(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	SetLedgerSigner(signer)
	t.Cleanup(func() { SetLedgerSigner(nil) })

	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1", Audited: 1}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	scanHash := entries[0].Hash

	// an adjudication naming the SCAN, which has no findings at all
	_, err = AppendLedgerEntry(dir, LedgerEntry{
		Kind:         KindAdjudication,
		Adjudication: &Adjudication{Adjudicates: scanHash + "#R1", Verdict: "confirmed", By: "someone", Reason: "because"},
	}, signer)
	if err != nil {
		return // refused at the writer's door: also correct
	}
	checks, verr := VerifyLedgerDir(dir, pub)
	if verr != nil {
		t.Fatal(verr)
	}
	last := checks[len(checks)-1]
	if last.Problem == "" {
		t.Fatal("an adjudication naming a SCAN verified clean — the record shows a finding ruled on that never existed")
	}
}

// Stripping a signature must not be a way to pass. VerifyLedgerDir assigned
// a Problem only for an INVALID signature, never for a MISSING one, so with
// a key in hand and every other entry signed, an entry whose signature was
// deleted verified clean. (Round four, R3, reproduced.)
func TestUnsignedEntryInASignedChainIsAProblem(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	SetLedgerSigner(Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv})
	t.Cleanup(func() { SetLedgerSigner(nil) })

	for _, c := range []string{"aaa1", "bbb2"} {
		if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: c, Audited: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := ReadLedgerDir(dir)
	// strip the signature from the genesis, leaving hash and links intact
	path := filepath.Join(dir, ScansSubdir, entries[0].File)
	raw, err := readMaybeGzip(path)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	delete(tree, "signature")
	out, _ := json.MarshalIndent(tree, "", " ")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(out); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Signed {
		t.Skip("the signature was not actually removed; the fixture needs updating")
	}
	if checks[0].Problem == "" {
		t.Fatal("an entry whose signature was DELETED verified clean in a chain where every other entry is signed — removing a signature must not be a way to pass")
	}
}
