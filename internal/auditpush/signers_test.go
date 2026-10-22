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
	"time"
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

// --- The walk: who may sign, entry by entry ---------------------------------

func pubOf(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

func signerOf(k ed25519.PrivateKey) Ed25519LedgerSigner {
	return Ed25519LedgerSigner{KeyID: KeyFingerprint(pubOf(k)), Key: k}
}

func registerOf(k ed25519.PrivateKey, name string) SignerChange {
	p := pubOf(k)
	return SignerChange{Action: SignerRegister, KeyID: KeyFingerprint(p), PubKey: hex.EncodeToString(p), Name: name}
}

func revokeOf(k ed25519.PrivateKey, reason string) SignerChange {
	p := pubOf(k)
	return SignerChange{Action: SignerRevoke, KeyID: KeyFingerprint(p), PubKey: hex.EncodeToString(p), Name: "-", Reason: reason}
}

func appendScan(t *testing.T, dir string, s LedgerSigner, commit string) {
	t.Helper()
	e := LedgerEntry{Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: commit, ScanID: 1}}}
	if _, err := AppendLedgerEntry(dir, e, s); err != nil {
		t.Fatalf("append %s: %v", commit, err)
	}
}

func mustSignerChange(t *testing.T, dir string, c SignerChange, s LedgerSigner) {
	t.Helper()
	if _, err := WriteSignerChange(dir, c, s); err != nil {
		t.Fatalf("signer change %s %s: %v", c.Action, c.KeyID, err)
	}
}

// placeForged links e to the head and writes it WITHOUT AppendLedgerEntry's
// intact-chain guard, so a test can build the chain a forger would: the
// verifier, not the writer, is what is under test.
func placeForged(t *testing.T, dir string, e LedgerEntry, s LedgerSigner) {
	t.Helper()
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := ""
	if n := len(entries); n > 0 {
		prev = entries[n-1].Hash
	}
	if _, err := placeEntry(dir, e, prev, s); err != nil {
		t.Fatal(err)
	}
}

// placeRawEntry writes e exactly as given (format, keyid and all), linked to
// the head and signed by key — the shape an older binary, or a hand, put on
// disk. placeEntry would stamp format 5 and refuse a legacy keyid.
func placeRawEntry(t *testing.T, dir string, e LedgerEntry, key ed25519.PrivateKey) {
	t.Helper()
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(entries); n > 0 {
		e.Prev = entries[n-1].Hash
	}
	e.Pushed = time.Now().UTC().Truncate(time.Microsecond)
	if e.IsScan() && e.Bundle.Scan != (ScanRow{}) {
		e.ScanUID = scanUID(e.Bundle.Scan, e.Pushed)
		e.Bundle.Scan.ScanUID = e.ScanUID
	}
	e.Hash, e.Signature, e.Raw = "", "", nil
	h, err := EntryHash(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Hash = h
	raw, _ := hex.DecodeString(h)
	e.Signature = hex.EncodeToString(ed25519.Sign(key, raw))
	js, _ := json.MarshalIndent(e, "", " ")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(js)
	_ = zw.Close()
	if err := os.MkdirAll(filepath.Join(dir, ScansSubdir), 0o750); err != nil {
		t.Fatal(err)
	}
	name := e.Pushed.Format("20060102T150405.000000Z") + "-raw-" + h[:12] + ".json.gz"
	if err := os.WriteFile(filepath.Join(dir, ScansSubdir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifyChecks(t *testing.T, dir string, pub ed25519.PublicKey) []ChainCheck {
	t.Helper()
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

func wantClean(t *testing.T, c ChainCheck) {
	t.Helper()
	if c.Problem != "" {
		t.Fatalf("%s (%s): unexpected problem %q", c.File, c.Kind, c.Problem)
	}
}

func wantProblem(t *testing.T, c ChainCheck, substr string) {
	t.Helper()
	if !strings.Contains(c.Problem, substr) {
		t.Fatalf("%s (%s): problem %q, want it to contain %q", c.File, c.Kind, c.Problem, substr)
	}
}

func TestVerifyRegisteredKey(t *testing.T) {
	dir := t.TempDir()
	root, m := seededKey(0x10), seededKey(0x11)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(m), "aaaa1111")
	checks := verifyChecks(t, dir, pubOf(root))
	if len(checks) != 2 {
		t.Fatalf("%d checks, want 2", len(checks))
	}
	for _, c := range checks {
		wantClean(t, c)
	}
	if checks[0].Signer != "root" || !checks[0].SigOK {
		t.Fatalf("the registration: signer %q sigOK %v, want root/true", checks[0].Signer, checks[0].SigOK)
	}
	if checks[1].Signer != "laptop" || !checks[1].SigOK {
		t.Fatalf("the registered key's entry: signer %q sigOK %v, want laptop/true", checks[1].Signer, checks[1].SigOK)
	}
}

func TestVerifyKeyBeforeRegistration(t *testing.T) {
	dir := t.TempDir()
	root, m := seededKey(0x12), seededKey(0x13)
	appendScan(t, dir, signerOf(m), "aaaa1111")
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	checks := verifyChecks(t, dir, pubOf(root))
	wantProblem(t, checks[0], "not a registered signer at this point in the chain")
	if checks[0].SigOK || checks[0].Signer != "" {
		t.Fatalf("an unregistered key's entry: sigOK %v signer %q, want false/\"\"", checks[0].SigOK, checks[0].Signer)
	}
	wantClean(t, checks[1])
}

func TestVerifyRevokedKey(t *testing.T) {
	dir := t.TempDir()
	root, m := seededKey(0x14), seededKey(0x15)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(m), "aaaa1111")
	// Register and revoke of one key inside one second must not collide on
	// the file name (the tail is the entry's own hash).
	mustSignerChange(t, dir, revokeOf(m, "laptop stolen"), signerOf(root))
	// Forged past the writer guard, which now refuses a revoked key: the
	// verifier, not the writer, is what this test is about.
	placeForged(t, dir, scanEntry("bbbb2222"), signerOf(m))
	checks := verifyChecks(t, dir, pubOf(root))
	if len(checks) != 4 {
		t.Fatalf("%d checks, want 4", len(checks))
	}
	wantClean(t, checks[0])
	wantClean(t, checks[1])
	wantClean(t, checks[2])
	wantProblem(t, checks[3], "not a registered signer at this point in the chain")
}

func TestVerifyForgedRegistration(t *testing.T) {
	dir := t.TempDir()
	root, m, x := seededKey(0x16), seededKey(0x17), seededKey(0x18)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	c := registerOf(x, "intruder")
	placeForged(t, dir, LedgerEntry{Kind: KindSigner, Signer: &c}, signerOf(m))
	placeForged(t, dir, LedgerEntry{Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: "cccc3333", ScanID: 1}}}, signerOf(x))
	checks := verifyChecks(t, dir, pubOf(root))
	wantClean(t, checks[0])
	wantProblem(t, checks[1], "a signer entry must be signed by the ledger's root key")
	wantProblem(t, checks[2], "not a registered signer at this point in the chain")
	// Without a key this forgery is still visible, but only because the
	// forger signed under its OWN keyid: with no key the authority rule is
	// by name. TestVerifyForgedRootKeyID is the forger who copies the root's.
	nopub := verifyChecks(t, dir, nil)
	wantProblem(t, nopub[1], "a signer entry must be signed by the ledger's root key")
	if rootID, active := ActiveSigners(mustRead(t, dir)); rootID != KeyFingerprint(pubOf(root)) || len(active) != 1 || active[0].Name != "laptop" {
		t.Fatalf("ActiveSigners after a forged registration: root %q active %+v, want only laptop", rootID, active)
	}
}

func mustRead(t *testing.T, dir string) []LedgerEntry {
	t.Helper()
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestVerifyLegacyKeyIDIsRoot(t *testing.T) {
	root := seededKey(0x19)
	scan := func() LedgerEntry {
		return LedgerEntry{KeyID: LegacyRootKeyID, Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: "dddd4444", ScanID: 1}}}
	}
	dir := t.TempDir()
	e := scan()
	e.Format = ledgerFormat4
	placeRawEntry(t, dir, e, root)
	checks := verifyChecks(t, dir, pubOf(root))
	wantClean(t, checks[0])
	if checks[0].Signer != "root" || !checks[0].SigOK {
		t.Fatalf("a format-4 corral-certify entry: signer %q sigOK %v, want root/true", checks[0].Signer, checks[0].SigOK)
	}

	dir5 := t.TempDir()
	e = scan()
	e.Format = LedgerFileFormat
	placeRawEntry(t, dir5, e, root)
	for _, pub := range []ed25519.PublicKey{pubOf(root), nil} {
		wantProblem(t, verifyChecks(t, dir5, pub)[0], `a corral-ledger-5 entry names its key by fingerprint, not "corral-certify"`)
	}
	// The writer refuses it too: the rule at both doors.
	if _, err := AppendLedgerEntry(t.TempDir(), scan(), Ed25519LedgerSigner{KeyID: LegacyRootKeyID, Key: root}); err == nil || !strings.Contains(err.Error(), "by fingerprint") {
		t.Fatalf("placing a format-5 entry keyed %q: err %v, want a refusal", LegacyRootKeyID, err)
	}
}

func TestVerifyRootCannotBeRevoked(t *testing.T) {
	dir := t.TempDir()
	root := seededKey(0x1a)
	mustSignerChange(t, dir, revokeOf(root, "rotating"), signerOf(root))
	wantProblem(t, verifyChecks(t, dir, pubOf(root))[0], "the root cannot be revoked — a new root is a new ledger")
}

// The other three change rules: a key registered twice, the root
// registered as a signer, and a revoke of a key that is not active. Each
// is a Problem and none changes the set.
func TestVerifySignerChangeRules(t *testing.T) {
	root, m, x := seededKey(0x1b), seededKey(0x1c), seededKey(0x1d)
	cases := map[string]struct {
		changes []SignerChange
		want    string
	}{
		"register twice":    {[]SignerChange{registerOf(m, "a"), registerOf(m, "b")}, "already a registered signer"},
		"register the root": {[]SignerChange{registerOf(root, "root again")}, "the root is not registered"},
		"revoke a stranger": {[]SignerChange{registerOf(m, "a"), revokeOf(x, "gone")}, "not a registered signer at this point in the chain"},
		"revoke twice":      {[]SignerChange{registerOf(m, "a"), revokeOf(m, "gone"), revokeOf(m, "again")}, "not a registered signer at this point in the chain"},
		"unsigned register": {nil, "a signer entry must be signed by the ledger's root key"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.changes == nil {
				// An unsigned change after a signed one: the root is known.
				mustSignerChange(t, dir, registerOf(m, "a"), signerOf(root))
				c := registerOf(x, "x")
				placeForged(t, dir, LedgerEntry{Kind: KindSigner, Signer: &c}, nil)
			}
			for _, c := range tc.changes {
				placeForged(t, dir, LedgerEntry{Kind: KindSigner, Signer: &c}, signerOf(root))
			}
			checks := verifyChecks(t, dir, pubOf(root))
			for _, c := range checks[:len(checks)-1] {
				wantClean(t, c)
			}
			wantProblem(t, checks[len(checks)-1], tc.want)
		})
	}
}

// With no key, nothing is signature-checked — but signer entries are still
// shape-checked and applied, and every entry's Signer is named from the set.
func TestVerifyNoPubStillAppliesShape(t *testing.T) {
	dir := t.TempDir()
	root, m := seededKey(0x1e), seededKey(0x1f)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(m), "eeee5555")
	appendScan(t, dir, signerOf(root), "ffff6666")
	checks := verifyChecks(t, dir, nil)
	for i, want := range []string{"root", "laptop", "root"} {
		wantClean(t, checks[i])
		if checks[i].Signer != want || checks[i].SigOK {
			t.Fatalf("entry %d with no key: signer %q sigOK %v, want %q/false", i, checks[i].Signer, checks[i].SigOK, want)
		}
	}
	// A malformed signer change is still a problem with no key.
	bad := registerOf(seededKey(0x20), "x")
	bad.KeyID = "ed25519:0000000000000000"
	e := LedgerEntry{Format: LedgerFileFormat, Kind: KindSigner, Signer: &bad, KeyID: KeyFingerprint(pubOf(root))}
	placeRawEntry(t, dir, e, root)
	checks = verifyChecks(t, dir, nil)
	wantProblem(t, checks[len(checks)-1], "fingerprint")
}

func TestActiveSignersAtHead(t *testing.T) {
	dir := t.TempDir()
	root, a, b := seededKey(0x21), seededKey(0x22), seededKey(0x23)
	if rootID, active := ActiveSigners(nil); rootID != "" || len(active) != 0 {
		t.Fatalf("an empty chain: root %q active %+v, want none", rootID, active)
	}
	mustSignerChange(t, dir, registerOf(a, "A"), signerOf(root))
	mustSignerChange(t, dir, registerOf(b, "B"), signerOf(root))
	mustSignerChange(t, dir, revokeOf(a, "retired"), signerOf(root))
	rootID, active := ActiveSigners(mustRead(t, dir))
	if rootID != KeyFingerprint(pubOf(root)) {
		t.Fatalf("rootKeyID %q, want %q", rootID, KeyFingerprint(pubOf(root)))
	}
	want := SignerKey{KeyID: KeyFingerprint(pubOf(b)), PubKey: hex.EncodeToString(pubOf(b)), Name: "B"}
	if len(active) != 1 || active[0] != want {
		t.Fatalf("active %+v, want [%+v]", active, want)
	}
}

// The forger who copies the root's keyid. M signs a signer entry that NAMES
// the root's fingerprint (plaintext on every root entry) to register X. Under
// the root key the signature is checked against the root: a Problem, and the
// set does not move, so X's entry is unregistered. With no key nothing is
// signature-checked and the name is all there is: the forgery is accepted.
// Both halves are pinned — the first is the security property, the second is
// the documented limit of a keyless read (signerSet, ActiveSigners).
func TestVerifyForgedRootKeyID(t *testing.T) {
	dir := t.TempDir()
	root, m, x := seededKey(0x24), seededKey(0x25), seededKey(0x26)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	c := registerOf(x, "intruder")
	posingAsRoot := Ed25519LedgerSigner{KeyID: KeyFingerprint(pubOf(root)), Key: m}
	placeForged(t, dir, LedgerEntry{Kind: KindSigner, Signer: &c}, posingAsRoot)
	placeForged(t, dir, LedgerEntry{Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: "abab1212", ScanID: 1}}}, signerOf(x))

	checks := verifyChecks(t, dir, pubOf(root))
	wantClean(t, checks[0])
	wantProblem(t, checks[1], "signature does not verify under the given key")
	wantProblem(t, checks[2], "not a registered signer at this point in the chain")

	nopub := verifyChecks(t, dir, nil)
	for _, c := range nopub {
		wantClean(t, c)
	}
	if nopub[1].Signer != "root" || nopub[2].Signer != "intruder" {
		t.Fatalf("keyless read: signers %q, %q — want the forgery accepted by name (root, intruder)", nopub[1].Signer, nopub[2].Signer)
	}
	_, active := ActiveSigners(mustRead(t, dir))
	if len(active) != 2 || active[1].KeyID != KeyFingerprint(pubOf(x)) {
		t.Fatalf("ActiveSigners %+v: want the forged key present — a keyless set is a claim, not an authorization", active)
	}
}

// chainWithMember builds root registers M; M appends; the shape every
// checkpoint test starts from.
func chainWithMember(t *testing.T, rootSeed, mSeed byte) (dir string, root, m ed25519.PrivateKey) {
	t.Helper()
	dir = t.TempDir()
	root, m = seededKey(rootSeed), seededKey(mSeed)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(m), "aaaa1111")
	return dir, root, m
}

func TestCheckpointKeepsSigners(t *testing.T) {
	dir, root, m := chainWithMember(t, 0x20, 0x21)
	if _, _, err := WriteCheckpoint(dir, signerOf(root), pubOf(root)); err != nil {
		t.Fatal(err)
	}
	appendScan(t, dir, signerOf(m), "bbbb2222")
	entries := mustRead(t, dir)
	if len(entries) != 2 || entries[0].Kind != KindCheckpoint {
		t.Fatalf("%d entries, first %q, want a checkpoint then one scan", len(entries), entries[0].Kind)
	}
	if got := entries[0].Checkpoint.Signers; len(got) != 1 || got[0].KeyID != KeyFingerprint(pubOf(m)) || got[0].Name != "laptop" {
		t.Fatalf("checkpoint signers %+v, want only laptop", got)
	}
	checks := verifyChecks(t, dir, pubOf(root))
	for _, c := range checks {
		wantClean(t, c)
	}
	if checks[1].Signer != "laptop" || !checks[1].SigOK {
		t.Fatalf("entry after the checkpoint: signer %q sigOK %v, want laptop/true", checks[1].Signer, checks[1].SigOK)
	}
	rootID, active := ActiveSigners(entries)
	if rootID != KeyFingerprint(pubOf(root)) || len(active) != 1 || active[0].Name != "laptop" {
		t.Fatalf("ActiveSigners across a checkpoint: root %q active %+v", rootID, active)
	}
	// The same chain with no key still finds the root: it is the keyid of
	// the checkpoint that carries the set.
	if rootID, _ := ActiveSigners(entries[:1]); rootID != KeyFingerprint(pubOf(root)) {
		t.Fatalf("keyless root of a checkpoint-led chain %q, want the checkpoint's keyid", rootID)
	}
}

func TestCheckpointByNonRootWithSignersIsProblem(t *testing.T) {
	// A hand-built genesis, since the writer refuses to make one.
	root, m := seededKey(0x22), seededKey(0x23)
	dir := t.TempDir()
	mk := SignerKey{KeyID: KeyFingerprint(pubOf(m)), PubKey: hex.EncodeToString(pubOf(m)), Name: "laptop"}
	cp := LedgerEntry{Format: LedgerFileFormat, KeyID: mk.KeyID, Kind: KindCheckpoint,
		Checkpoint: &Checkpoint{Head: "abc", Entries: 2, Signers: []SignerKey{mk}}}
	placeRawEntry(t, dir, cp, m)
	checks := verifyChecks(t, dir, pubOf(root))
	wantProblem(t, checks[0], "a checkpoint that carries signers must be signed by the root")
	// Its seed was not applied: a later entry by M is not good either.
	placeForged(t, dir, LedgerEntry{Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: "bbbb2222", ScanID: 1}}}, signerOf(m))
	checks = verifyChecks(t, dir, pubOf(root))
	wantProblem(t, checks[1], "not a registered signer at this point in the chain")
}

func fileList(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, ScansSubdir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteCheckpointRefusesForgedRegistration(t *testing.T) {
	dir, root, _ := chainWithMember(t, 0x27, 0x28)
	x := seededKey(0x29)
	c := registerOf(x, "intruder")
	// Keyless, naming the root's fingerprint is enough to be believed.
	placeForged(t, dir, LedgerEntry{Kind: KindSigner, Signer: &c}, Ed25519LedgerSigner{KeyID: KeyFingerprint(pubOf(root)), Key: x})
	before := fileList(t, dir)
	if _, _, err := WriteCheckpoint(dir, signerOf(root), pubOf(root)); err == nil {
		t.Fatal("a checkpoint over a forged registration was written")
	}
	if after := fileList(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused checkpoint changed the directory: %v -> %v", before, after)
	}
}

func TestWriteCheckpointRefusesNonRootSigner(t *testing.T) {
	dir, root, m := chainWithMember(t, 0x2a, 0x2b)
	before := fileList(t, dir)
	if _, _, err := WriteCheckpoint(dir, signerOf(m), pubOf(root)); err == nil {
		t.Fatal("a checkpoint by a non-root signer was written")
	}
	if after := fileList(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused checkpoint changed the directory: %v -> %v", before, after)
	}
}

func TestWriteCheckpointOnSignerChainNeedsRootPub(t *testing.T) {
	dir, root, _ := chainWithMember(t, 0x2c, 0x2d)
	before := fileList(t, dir)
	_, _, err := WriteCheckpoint(dir, signerOf(root), nil)
	if err == nil || !strings.Contains(err.Error(), "root public key") {
		t.Fatalf("err %v, want a refusal naming the root public key", err)
	}
	if after := fileList(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused checkpoint changed the directory: %v -> %v", before, after)
	}
}

func TestCheckpointWithoutSignersUnchanged(t *testing.T) {
	dir := t.TempDir()
	k := seededKey(0x24)
	appendScan(t, dir, signerOf(k), "aaaa1111")
	if _, _, err := WriteCheckpoint(dir, signerOf(k), nil); err != nil {
		t.Fatal(err)
	}
	entries := mustRead(t, dir)
	if entries[0].Checkpoint.Signers != nil {
		t.Fatalf("signers %+v on a chain that never registered any", entries[0].Checkpoint.Signers)
	}
	js, err := json.Marshal(entries[0].Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "signers") {
		t.Fatalf("checkpoint JSON %s has a signers key; a signer-less chain must checkpoint byte-for-byte as before", js)
	}
	wantClean(t, verifyChecks(t, dir, pubOf(k))[0])
}

func TestCheckpointRejectsMalformedSeedKey(t *testing.T) {
	root, m := seededKey(0x25), seededKey(0x26)
	good := SignerKey{KeyID: KeyFingerprint(pubOf(m)), PubKey: hex.EncodeToString(pubOf(m)), Name: "laptop"}
	cases := map[string]SignerKey{
		"short pubkey":      {KeyID: good.KeyID, PubKey: "abcd", Name: "laptop"},
		"not hex":           {KeyID: good.KeyID, PubKey: "zz", Name: "laptop"},
		"keyid not derived": {KeyID: "ed25519:0000000000000000", PubKey: good.PubKey, Name: "laptop"},
		"unnamed":           {KeyID: good.KeyID, PubKey: good.PubKey, Name: " "},
		"the root itself":   {KeyID: KeyFingerprint(pubOf(root)), PubKey: hex.EncodeToString(pubOf(root)), Name: "root again"},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cp := LedgerEntry{Format: LedgerFileFormat, KeyID: KeyFingerprint(pubOf(root)),
				Kind: KindCheckpoint, Checkpoint: &Checkpoint{Head: "abc", Entries: 1, Signers: []SignerKey{bad}}}
			placeRawEntry(t, dir, cp, root)
			wantProblem(t, verifyChecks(t, dir, pubOf(root))[0], "checkpoint")
			// And nothing was seeded, with or without a key (no panic either).
			for _, pub := range []ed25519.PublicKey{pubOf(root), nil} {
				_, set := verifyEntries(mustRead(t, dir), pub)
				if len(set.List()) != 0 {
					t.Fatalf("pub=%v: seeded %+v from a malformed checkpoint", pub != nil, set.List())
				}
			}
		})
	}
}

// --- Writers refuse a key the ledger never registered ------------------------

func scanFileCount(t *testing.T, dir string) int {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, ScansSubdir))
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

func scanEntry(commit string) LedgerEntry {
	return LedgerEntry{Bundle: Bundle{Scan: ScanRow{Repo: "r", Commit: commit, ScanID: 1}}}
}

func TestAppendRefusesUnregisteredKey(t *testing.T) {
	dir := t.TempDir()
	root, stranger := seededKey(0x30), seededKey(0x31)
	mustSignerChange(t, dir, registerOf(seededKey(0x32), "laptop"), signerOf(root))
	before := scanFileCount(t, dir)
	_, err := AppendLedgerEntry(dir, scanEntry("bbbb2222"), signerOf(stranger))
	if err == nil {
		t.Fatal("an unregistered key appended to a signer ledger")
	}
	for _, want := range []string{"corral ledger signer add", KeyFingerprint(pubOf(stranger))} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if got := scanFileCount(t, dir); got != before {
		t.Fatalf("%d files, want %d: the refused entry was written", got, before)
	}
}

func TestAppendRefusesUnsignedOnSignerLedger(t *testing.T) {
	dir := t.TempDir()
	mustSignerChange(t, dir, registerOf(seededKey(0x33), "laptop"), signerOf(seededKey(0x34)))
	before := scanFileCount(t, dir)
	if _, err := AppendLedgerEntry(dir, scanEntry("cccc3333"), nil); err == nil {
		t.Fatal("an unsigned entry appended to a signer ledger")
	}
	if got := scanFileCount(t, dir); got != before {
		t.Fatalf("%d files, want %d: the refused entry was written", got, before)
	}
}

func TestAppendAllowsRootAndRegistered(t *testing.T) {
	dir := t.TempDir()
	root, m := seededKey(0x35), seededKey(0x36)
	mustSignerChange(t, dir, registerOf(m, "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(root), "dddd4444")
	appendScan(t, dir, signerOf(m), "eeee5555")
	// Revoking removes the permission again.
	mustSignerChange(t, dir, revokeOf(m, "retired"), signerOf(root))
	if _, err := AppendLedgerEntry(dir, scanEntry("ffff6666"), signerOf(m)); err == nil {
		t.Fatal("a revoked key appended")
	}
}

func TestAppendOnPlainLedgerUnchanged(t *testing.T) {
	dir := t.TempDir()
	appendScan(t, dir, signerOf(seededKey(0x37)), "aaaa7777")
	appendScan(t, dir, signerOf(seededKey(0x38)), "bbbb8888")
	appendScan(t, dir, nil, "cccc9999")
}

// A signer entry is not a warehouse row: a push plan reports it as a chain
// fact instead of letting it fall through unremarked.
func TestPushPlanCountsSignerEntry(t *testing.T) {
	dir := t.TempDir()
	root := seededKey(0x39)
	mustSignerChange(t, dir, registerOf(seededKey(0x3a), "laptop"), signerOf(root))
	appendScan(t, dir, signerOf(root), "abab1212")
	plan, err := PushLedgerDir(dir, filepath.Join(t.TempDir(), "wh.duckdb"), false, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NotPushable != 1 || plan.Scans != 1 {
		t.Fatalf("plan %+v, want 1 not-pushable (the signer entry) and 1 scan", plan)
	}
}

// The keyid is a label the signer reports, not proof of the key behind it. A
// signer that reads the root's fingerprint but holds another key would prune
// the chain and then leave a genesis the root key rejects for good.
func TestWriteCheckpointRefusesSignerThatOnlyClaimsTheRootKeyID(t *testing.T) {
	dir, root, _ := chainWithMember(t, 0x2e, 0x2f)
	liar := Ed25519LedgerSigner{KeyID: KeyFingerprint(pubOf(root)), Key: seededKey(0x30)}
	before := fileList(t, dir)
	if _, _, err := WriteCheckpoint(dir, liar, pubOf(root)); err == nil {
		t.Fatal("a checkpoint was written by a key that is not the root behind the root's keyid")
	}
	if after := fileList(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused checkpoint changed the directory: %v -> %v", before, after)
	}
}
