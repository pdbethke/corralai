// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seededKey(b byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
}

func TestKeyFingerprintFormat(t *testing.T) {
	pub := seededKey(0x01).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	want := "ed25519:" + hex.EncodeToString(sum[:])[:16]
	got := KeyFingerprint(pub)
	if got != want {
		t.Fatalf("KeyFingerprint = %q, want %q", got, want)
	}
	if len(got) != 24 || !strings.HasPrefix(got, "ed25519:") {
		t.Fatalf("fingerprint %q must be 24 chars with an ed25519: prefix", got)
	}
}

func TestSignerShapeProblems(t *testing.T) {
	pub := seededKey(0x02).Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	id := KeyFingerprint(pub)
	reg := SignerChange{Action: SignerRegister, KeyID: id, PubKey: pubHex, Name: "laptop"}
	rev := SignerChange{Action: SignerRevoke, KeyID: id, PubKey: pubHex, Name: "laptop", Reason: "lost"}
	with := func(c SignerChange, f func(*SignerChange)) *SignerChange { f(&c); return &c }

	bad := map[string]*SignerChange{
		"nil signer":        nil,
		"unknown action":    with(reg, func(c *SignerChange) { c.Action = "add" }),
		"pubkey not hex":    with(reg, func(c *SignerChange) { c.PubKey = "zz" }),
		"pubkey wrong size": with(reg, func(c *SignerChange) { c.PubKey = pubHex[:62] }),
		"keyid mismatch":    with(reg, func(c *SignerChange) { c.KeyID = "ed25519:0000000000000000" }),
		"register no name":  with(reg, func(c *SignerChange) { c.Name = " " }),
		"revoke no reason":  with(rev, func(c *SignerChange) { c.Reason = "" }),
	}
	for name, s := range bad {
		if p := EntryShapeProblem(LedgerEntry{Kind: KindSigner, Signer: s}); p == "" {
			t.Errorf("%s: expected a shape problem, got none", name)
		}
	}
	for name, s := range map[string]*SignerChange{"register": &reg, "revoke": &rev} {
		if p := EntryShapeProblem(LedgerEntry{Kind: KindSigner, Signer: s}); p != "" {
			t.Errorf("well-formed %s: unexpected problem %q", name, p)
		}
	}
}

func signerFixture(t *testing.T) (SignerChange, LedgerSigner) {
	t.Helper()
	root := seededKey(0x03)
	pub := seededKey(0x04).Public().(ed25519.PublicKey)
	c := SignerChange{Action: SignerRegister, KeyID: KeyFingerprint(pub), PubKey: hex.EncodeToString(pub), Name: "laptop"}
	return c, Ed25519LedgerSigner{KeyID: KeyFingerprint(root.Public().(ed25519.PublicKey)), Key: root}
}

func TestWriteSignerChangePlacesFormat5(t *testing.T) {
	dir := t.TempDir()
	c, root := signerFixture(t)
	if _, err := WriteSignerChange(dir, c, root); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLedgerDir: %d entries, err %v", len(entries), err)
	}
	e := entries[0]
	if e.Format != "corral-ledger-5" || e.Kind != "signer" {
		t.Fatalf("format %q kind %q", e.Format, e.Kind)
	}
	if !strings.Contains(e.File, "-signer-") {
		t.Fatalf("file name %q lacks -signer-", e.File)
	}
	if e.Signer == nil || e.Signer.KeyID != c.KeyID {
		t.Fatalf("signer change did not round-trip: %+v", e.Signer)
	}
}

// TestFormat4StillRead: a format-5 writer must not strand the entries
// written before it. Rewrite a placed entry to format 4, rehash it as a
// format-4 entry would have been, and it still reads and re-hashes.
func TestFormat4StillRead(t *testing.T) {
	dir := t.TempDir()
	c, root := signerFixture(t)
	name, err := WriteSignerChange(dir, c, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ScansSubdir, name)
	e, err := ReadLedgerEntry(path)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(e.Raw, &tree); err != nil {
		t.Fatal(err)
	}
	tree["format"] = ledgerFormat4
	raw, _ := json.Marshal(tree)
	var old LedgerEntry
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.Raw = raw
	h, err := EntryHash(old)
	if err != nil {
		t.Fatal(err)
	}
	tree["hash"] = h
	raw, _ = json.Marshal(tree)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	p4 := filepath.Join(t.TempDir(), "20200101T000000Z-old-x.json.gz")
	if err := os.WriteFile(p4, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLedgerEntry(p4)
	if err != nil {
		t.Fatalf("a corral-ledger-4 entry must still be read: %v", err)
	}
	if got.Format != ledgerFormat4 || !keyIDIsHashed(got.Format) {
		t.Fatalf("format %q hashed=%v", got.Format, keyIDIsHashed(got.Format))
	}
	if again, _ := EntryHash(got); again != h {
		t.Fatalf("re-hash of the format-4 bytes = %s, want %s", again, h)
	}
}
