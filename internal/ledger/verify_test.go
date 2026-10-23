// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/review"
)

func key(b byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
}

func signerFor(k ed25519.PrivateKey) auditpush.Ed25519LedgerSigner {
	return auditpush.Ed25519LedgerSigner{KeyID: ledger.KeyFingerprint(k.Public().(ed25519.PublicKey)), Key: k}
}

// readBytes is what a browser does: the entries come as bytes
// and are parsed with no directory reader in between.
func readBytes(t *testing.T, dir string) []ledger.Entry {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if err != nil {
		t.Fatal(err)
	}
	var out []ledger.Entry
	for _, d := range des {
		n := d.Name()
		raw, err := os.ReadFile(filepath.Join(dir, auditpush.ScansSubdir, n))
		if err != nil {
			t.Fatal(err)
		}
		e, err := ledger.ParseEntry(n, raw)
		if err != nil {
			t.Fatal(err)
		}
		e.File = n
		out = append(out, e)
	}
	// Chain order is Pushed order (names carry only seconds).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pushed.Before(out[j].Pushed) })
	return out
}

func scan(t *testing.T, dir string, id int64, signer auditpush.LedgerSigner) {
	t.Helper()
	auditpush.SetLedgerSigner(signer)
	t.Cleanup(func() { auditpush.SetLedgerSigner(nil) })
	b := ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "abcdef123456", ScanID: id, CorralVersion: "vtest"}}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
}

// A ledger with every kind the rules read.
func fullLedger(t *testing.T, root, other ed25519.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()
	rs := signerFor(root)
	scan(t, dir, 1, rs)
	op := other.Public().(ed25519.PublicKey)
	if _, err := auditpush.WriteSignerChange(dir, ledger.SignerChange{Action: ledger.SignerRegister, KeyID: ledger.KeyFingerprint(op), PubKey: hex.EncodeToString(op), Name: "laptop"}, rs); err != nil {
		t.Fatal(err)
	}
	if _, err := auditpush.WriteReview(dir, review.Review{
		Repo: "o/r", Commit: "deadbeef", Scope: "internal/x", ReviewerModel: "m",
		Findings: []review.Finding{{ID: "R1", Claim: "c", Declared: "HYPOTHESIS", Tier: "HYPOTHESIS"}},
		Sound:    []string{"a", "b", "c"},
	}, signerFor(other)); err != nil {
		t.Fatal(err)
	}
	es := readBytes(t, dir)
	rev := es[len(es)-1].Hash
	if _, err := auditpush.WriteAdjudication(dir, rev+"#R1", ledger.VerdictConfirmed, "p", "real", rs); err != nil {
		t.Fatal(err)
	}
	scan(t, dir, 2, rs)
	es = readBytes(t, dir)
	if _, err := auditpush.WriteRetraction(dir, es[len(es)-1].Hash, "wrong repo", rs); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The browser and the CLI must run the same checks: the same entries, the
// same key, the SAME []ChainCheck, field for field.
func TestVerifyMatchesVerifyLedgerDir(t *testing.T) {
	root, other := key(0x31), key(0x32)
	dir := fullLedger(t, root, other)
	pub := root.Public().(ed25519.PublicKey)
	for _, p := range []ed25519.PublicKey{pub, nil} {
		want, err := auditpush.VerifyLedgerDir(dir, p)
		if err != nil {
			t.Fatal(err)
		}
		got := ledger.Verify(readBytes(t, dir), ledger.Trust{RootPub: p})
		if len(got) != 6 {
			t.Fatalf("want 6 checks, got %d", len(got))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pub=%v:\nledger.Verify  %+v\nVerifyLedgerDir %+v", p != nil, got, want)
		}
		if p != nil {
			for _, c := range got {
				if c.Problem != "" || !c.SigOK {
					t.Fatalf("clean ledger, bad check: %+v", c)
				}
			}
		}
	}
}

// Review Focus 1: a chain that begins at a checkpoint verifies, and one flipped
// byte after it is a Problem on that entry, not on its neighbours.
func TestVerifyAfterCheckpoint(t *testing.T) {
	root := key(0x33)
	rs := signerFor(root)
	pub := root.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	scan(t, dir, 1, rs)
	scan(t, dir, 2, rs)
	if _, _, err := auditpush.WriteCheckpoint(dir, rs, pub); err != nil {
		t.Fatal(err)
	}
	scan(t, dir, 3, rs)
	scan(t, dir, 4, rs)
	entries := readBytes(t, dir)
	checks := ledger.Verify(entries, ledger.Trust{RootPub: pub})
	if len(checks) != 3 || checks[0].Kind != ledger.KindCheckpoint {
		t.Fatalf("want checkpoint + 2 scans, got %+v", checks)
	}
	for _, c := range checks {
		if c.Problem != "" || !c.SigOK {
			t.Fatalf("clean post-checkpoint chain: %+v", c)
		}
	}
	// Flip one byte inside the last entry's bundle, in the bytes.
	last := entries[len(entries)-1]
	raw := bytes.Replace(last.Raw, []byte("abcdef123456"), []byte("abcdef123457"), 1)
	if bytes.Equal(raw, last.Raw) {
		t.Fatal("fixture: nothing to flip")
	}
	bad, err := ledger.ParseEntry(last.File, raw)
	if err != nil {
		t.Fatal(err)
	}
	bad.File = last.File
	entries[len(entries)-1] = bad
	checks = ledger.Verify(entries, ledger.Trust{RootPub: pub})
	if checks[2].Problem == "" || checks[1].Problem != "" {
		t.Fatalf("want a Problem on the tampered entry only: %+v", checks)
	}
}

// Review Focus 2: entries in older formats verify through ledger.Verify as
// they do through VerifyLedgerDir, whether or not a key is given.
func TestVerifyOldFormats(t *testing.T) {
	root := key(0x34)
	pub := root.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, auditpush.ScansSubdir), 0o750); err != nil {
		t.Fatal(err)
	}
	// corral-ledger-2: sparse hash, unsigned, scan_uid minted elsewhere.
	e2 := ledger.Entry{Format: ledger.Format2, Pushed: time.Now().UTC().Truncate(time.Microsecond), ScanUID: "minted-elsewhere",
		Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "deadbeef", Host: "h", ScanUID: "minted-elsewhere"}}}
	h2, err := ledger.EntryHash(e2)
	if err != nil {
		t.Fatal(err)
	}
	e2.Hash = h2
	js, _ := json.MarshalIndent(e2, "", " ")
	if err := os.WriteFile(filepath.Join(dir, auditpush.ScansSubdir, "20260901T000000Z-a-minted.json"), js, 0o600); err != nil {
		t.Fatal(err)
	}
	// corral-ledger-4: keyid is the legacy convention name, signed by the root.
	e4 := ledger.Entry{Format: ledger.Format4, KeyID: ledger.LegacyRootKeyID, Prev: h2, Pushed: time.Now().UTC().Truncate(time.Microsecond),
		Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "dddd4444", ScanID: 1}}}
	e4.ScanUID = auditpush.RecomputeScanUID(e4.Bundle.Scan, e4.Pushed)
	e4.Bundle.Scan.ScanUID = e4.ScanUID
	h4, err := ledger.EntryHash(e4)
	if err != nil {
		t.Fatal(err)
	}
	e4.Hash = h4
	raw, _ := hex.DecodeString(h4)
	e4.Signature = hex.EncodeToString(ed25519.Sign(root, raw))
	js, _ = json.MarshalIndent(e4, "", " ")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(js)
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(dir, auditpush.ScansSubdir, "20260901T000001Z-b-old.json.gz"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []ed25519.PublicKey{pub, nil} {
		want, err := auditpush.VerifyLedgerDir(dir, p)
		if err != nil {
			t.Fatal(err)
		}
		got := ledger.Verify(readBytes(t, dir), ledger.Trust{RootPub: p})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pub=%v:\n%+v\n%+v", p != nil, got, want)
		}
		for _, c := range got {
			if c.Problem != "" {
				t.Fatalf("old-format entry must verify: %+v", c)
			}
		}
		if !strings.Contains(got[0].Note, ledger.Format2) {
			t.Fatalf("a format-2 check must say so: %+v", got[0])
		}
	}
}

// The rules read the record the same through the ledger package as through
// auditpush's names for them.
func TestRulesLiveInLedger(t *testing.T) {
	dir := fullLedger(t, key(0x35), key(0x36))
	es := readBytes(t, dir)
	if got := ledger.Retracted(es); len(got) != 1 {
		t.Fatalf("retracted: %v", got)
	}
	if got := ledger.ScanEntries(es); len(got) != 1 {
		t.Fatalf("scan entries: %d, want the unretracted one", len(got))
	}
	if got := ledger.LiveEntries(es); len(got) != len(es)-1 {
		t.Fatalf("live: %d of %d", len(got), len(es))
	}
	if got := ledger.Adjudications(es); len(got) != 1 {
		t.Fatalf("adjudications: %v", got)
	}
	if !ledger.ChainHasSigners(es) {
		t.Fatal("chain registers a signer")
	}
}
