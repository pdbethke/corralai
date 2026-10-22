// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

func trustFor(s *testSigstore, t *testing.T, repo string) ledger.Trust {
	return ledger.Trust{Repo: repo, SigstoreRoot: s.ledgerRoot(t)}
}

func only(t *testing.T, checks []ledger.ChainCheck) ledger.ChainCheck {
	t.Helper()
	if len(checks) != 1 {
		t.Fatalf("want one check, got %+v", checks)
	}
	return checks[0]
}

func TestKeylessVerifies(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, testRepo)))
	if c.Problem != "" || !c.Signed || !c.SigOK || !c.HashOK || !c.LinkOK {
		t.Fatalf("a keyless entry from %s must verify: %+v", testRepo, c)
	}
	if c.Signer != "https://github.com/"+testRepo {
		t.Fatalf("signer must name the repository that signed: %q", c.Signer)
	}
}

func TestKeylessOtherRepoFails(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, "acme/other")))
	if c.SigOK || !strings.Contains(c.Problem, "https://github.com/acme/widgets") || !strings.Contains(c.Problem, "https://github.com/acme/other") {
		t.Fatalf("want a failure naming both repositories, got %+v", c)
	}
}

func TestKeylessWrongHashFails(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	other := s.keylessEntry(t, "", 2, spec{})
	e.Sigstore = other.Sigstore // a real bundle, over a different entry's hash
	e = reparse(t, e)
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, testRepo)))
	if c.SigOK || c.Problem == "" || !c.HashOK {
		t.Fatalf("a bundle over another hash must fail, and the entry's own hash still check: %+v", c)
	}
}

// Review Focus 5: no repository named is never a guess.
func TestKeylessNoRepoNamed(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, "")))
	const want = "keyless entry: the ledger's repository is not named (pass --ledger-repo owner/name)"
	if c.Problem != want || c.SigOK {
		t.Fatalf("problem = %q, want %q", c.Problem, want)
	}
	// Reported first, even for a bundle that would fail anyway.
	e.Sigstore = json.RawMessage(`{"mediaType":"nonsense"}`)
	c = only(t, ledger.Verify([]ledger.Entry{reparse(t, e)}, trustFor(s, t, "")))
	if c.Problem != want {
		t.Fatalf("problem = %q, want %q", c.Problem, want)
	}
}

func TestKeylessNeedsSigstoreRoot(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	c := only(t, ledger.Verify([]ledger.Entry{e}, ledger.Trust{Repo: testRepo}))
	if c.SigOK || !strings.Contains(c.Problem, "Sigstore trust root") {
		t.Fatalf("no trust root must fail naming it: %+v", c)
	}
}

func TestKeylessMustBeV6(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	e.Format = ledger.FileFormat
	e = s.seal(t, e, spec{})
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, testRepo)))
	if c.SigOK || !strings.Contains(c.Problem, ledger.Format6) {
		t.Fatalf("keyid sigstore in a %s entry must fail: %+v", ledger.FileFormat, c)
	}
}

// The other direction: format 6 is ONLY the keyless format.
func TestV6MustBeKeyless(t *testing.T) {
	s := newTestSigstore(t)
	root := key(0x41)
	for _, keyid := range []string{"", ledger.KeyFingerprint(root.Public().(ed25519.PublicKey))} {
		e := ledger.Entry{Format: ledger.Format6, KeyID: keyid, Pushed: time.Now().UTC().Truncate(time.Microsecond),
			Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "c", ScanID: 1}}}
		e.ScanUID = ledger.ScanUID(e.Bundle.Scan, e.Pushed)
		e.Bundle.Scan.ScanUID = e.ScanUID
		h, err := ledger.EntryHash(e)
		if err != nil {
			t.Fatal(err)
		}
		e.Hash = h
		if keyid != "" {
			raw, _ := hex.DecodeString(h)
			e.Signature = hex.EncodeToString(ed25519.Sign(root, raw))
		}
		c := only(t, ledger.Verify([]ledger.Entry{reparse(t, e)}, ledger.Trust{RootPub: root.Public().(ed25519.PublicKey), Repo: testRepo, SigstoreRoot: s.ledgerRoot(t)}))
		// SigOK may be true (the ed25519 bytes do verify); the Problem is
		// what fails the entry.
		if !strings.Contains(c.Problem, `"`+ledger.KeyIDSigstore+`"`) {
			t.Fatalf("a %s entry with keyid %q must fail: %+v", ledger.Format6, keyid, c)
		}
	}
	// And a non-keyless entry may not carry a bundle at all.
	e := s.keylessEntry(t, "", 1, spec{})
	e.Format, e.KeyID, e.Raw = ledger.FileFormat, "", nil
	h, _ := ledger.EntryHash(e)
	e.Hash = h
	c := only(t, ledger.Verify([]ledger.Entry{reparse(t, e)}, trustFor(s, t, testRepo)))
	if !c.HashOK || !strings.Contains(c.Problem, "not a keyless entry") {
		t.Fatalf("a sigstore bundle on a non-keyless entry must be a Problem: %+v", c)
	}
}

// A keyless entry carries a bundle and nothing else that could sign it.
func TestKeylessBundleAlone(t *testing.T) {
	s := newTestSigstore(t)
	both := s.keylessEntry(t, "", 1, spec{})
	both.Signature = strings.Repeat("ab", 64)
	none := s.keylessEntry(t, "", 2, spec{})
	none.Sigstore = nil
	for want, e := range map[string]ledger.Entry{"as well as a sigstore bundle": both, "carries no sigstore bundle": none} {
		c := only(t, ledger.Verify([]ledger.Entry{reparse(t, e)}, trustFor(s, t, testRepo)))
		if c.SigOK || !strings.Contains(c.Problem, want) {
			t.Fatalf("want %q: %+v", want, c)
		}
	}
}

// Chain control stays with the root key: a keyless entry may not register or
// revoke a signer, or stand as a checkpoint.
func TestKeylessCannotControlTheChain(t *testing.T) {
	s := newTestSigstore(t)
	other := key(0x42).Public().(ed25519.PublicKey)
	reg := ledger.Entry{Format: ledger.Format6, KeyID: ledger.KeyIDSigstore, Kind: ledger.KindSigner, Pushed: time.Now().UTC().Truncate(time.Microsecond),
		Signer: &ledger.SignerChange{Action: ledger.SignerRegister, KeyID: ledger.KeyFingerprint(other), PubKey: hex.EncodeToString(other), Name: "x"}}
	cp := ledger.Entry{Format: ledger.Format6, KeyID: ledger.KeyIDSigstore, Kind: ledger.KindCheckpoint, Pushed: time.Now().UTC().Truncate(time.Microsecond),
		Checkpoint: &ledger.Checkpoint{Head: strings.Repeat("a", 64), Entries: 3, Through: time.Now().UTC().Truncate(time.Second)}}
	for _, e := range []ledger.Entry{reg, cp} {
		c := only(t, ledger.Verify([]ledger.Entry{s.seal(t, e, spec{})}, trustFor(s, t, testRepo)))
		if c.Problem == "" || !strings.Contains(c.Problem, "root key") {
			t.Fatalf("a keyless %s entry must be refused: %+v", e.Kind, c)
		}
	}
}

// The bundle is outside the hash, exactly as Signature is; everything hashed
// still is.
func TestSigstoreFieldNotHashed(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{})
	before, err := ledger.EntryHash(e)
	if err != nil {
		t.Fatal(err)
	}
	e2 := e
	e2.Sigstore = json.RawMessage(`{"different":true}`)
	e2 = reparse(t, e2)
	if after, _ := ledger.EntryHash(e2); after != before {
		t.Fatalf("changing the bundle changed the hash: %s -> %s", before, after)
	}
	e3 := e
	e3.Raw = nil
	e3.Sigstore = json.RawMessage(`{"different":true}`)
	if after, _ := ledger.EntryHash(e3); after != before {
		t.Fatalf("writer-side hash depends on the bundle: %s -> %s", before, after)
	}
	e4 := e
	e4.Bundle.Scan.Commit = "fedcba654321"
	e4.Raw = nil
	if after, _ := ledger.EntryHash(e4); after == before {
		t.Fatal("changing hashed content did not change the hash")
	}
}

func TestKeylessArtifact(t *testing.T) {
	e := ledger.Entry{Hash: strings.Repeat("ab", 32)}
	got, err := ledger.KeylessArtifact(e)
	if err != nil || hex.EncodeToString(got) != e.Hash {
		t.Fatalf("got %x, %v", got, err)
	}
	for _, bad := range []string{"", "zz", strings.Repeat("ab", 31)} {
		if _, err := ledger.KeylessArtifact(ledger.Entry{Hash: bad}); err == nil {
			t.Fatalf("hash %q must be refused", bad)
		}
	}
}

// Review Focus 3: ed25519 v5 entries then keyless v6 entries, one chain.
func TestMixedLedger(t *testing.T) {
	s := newTestSigstore(t)
	rootKey := key(0x43)
	pub := rootKey.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	rs := signerFor(rootKey)
	scan(t, dir, 1, rs)
	scan(t, dir, 2, rs)
	entries := readBytes(t, dir)
	k1 := s.keylessEntry(t, entries[len(entries)-1].Hash, 3, spec{})
	k2 := s.keylessEntry(t, k1.Hash, 4, spec{})
	entries = append(entries, k1, k2)

	tr := ledger.Trust{RootPub: pub, Repo: testRepo, SigstoreRoot: s.ledgerRoot(t)}
	for i, c := range ledger.Verify(entries, tr) {
		if c.Problem != "" || !c.SigOK {
			t.Fatalf("entry %d of a mixed chain: %+v", i, c)
		}
	}

	var raw []ledger.RawEntry
	des, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range des {
		b, err := os.ReadFile(filepath.Join(dir, auditpush.ScansSubdir, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, ledger.RawEntry{Name: d.Name(), Bytes: b})
	}
	for _, k := range []ledger.Entry{k1, k2} {
		raw = append(raw, ledger.RawEntry{Name: k.File, Bytes: k.Raw})
	}
	d, err := ledger.Derive(raw, tr)
	if err != nil {
		t.Fatal(err)
	}
	if d.Problem != "" || len(d.Scans) != 4 {
		t.Fatalf("want 4 scans from both kinds of signature, got %d (%s) %+v", len(d.Scans), d.Problem, d.Entries)
	}
}

// A keyless chain needs no ed25519 root key: its trust is the repository.
func TestKeylessChainNeedsNoRootKey(t *testing.T) {
	s := newTestSigstore(t)
	k1 := s.keylessEntry(t, "", 1, spec{})
	k2 := s.keylessEntry(t, k1.Hash, 2, spec{})
	d, err := ledger.Derive([]ledger.RawEntry{{Name: k1.File, Bytes: k1.Raw}, {Name: k2.File, Bytes: k2.Raw}}, trustFor(s, t, testRepo))
	if err != nil {
		t.Fatal(err)
	}
	if d.Problem != "" || len(d.Scans) != 2 {
		t.Fatalf("want 2 verified scans with no root key, got %d (%s) %+v", len(d.Scans), d.Problem, d.Entries)
	}
	for _, st := range d.Entries {
		if st.Status != ledger.StatusVerified {
			t.Fatalf("status %+v", st)
		}
	}
}

func TestEmbeddedRootParses(t *testing.T) {
	ours, err := ledger.PublicGoodTrustedRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("sigstore_trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := root.NewTrustedRootFromJSON(raw)
	if err != nil {
		t.Fatalf("sigstore-go refuses the embedded root: %v", err)
	}
	if got, want := ours.CertificateAuthorities(), len(theirs.FulcioCertificateAuthorities()); got != want || got == 0 {
		t.Fatalf("certificate authorities: ours %d, sigstore-go %d", got, want)
	}
	for id := range theirs.RekorLogs() {
		if !ours.HasLog(id) {
			t.Fatalf("log %s is in sigstore-go's reading of the root and not ours", id)
		}
	}
}

// The verifier is our own BECAUSE sigstore-go cannot be in the browser build
// (task-7 report): keep it out of the non-test closure for good.
func TestLedgerHasNoSigstoreDependency(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/pdbethke/corralai/internal/ledger")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/sigstore/") {
			t.Errorf("internal/ledger depends on %s", dep)
		}
	}
}

// The differential table: one valid bundle and single mutations of it,
// judged by our verifier and by sigstore-go's. They must agree, except where
// a row names a deliberate strictness of ours (then ours rejects and the
// reason is stated).
func TestKeylessDifferential(t *testing.T) {
	s := newTestSigstore(t)
	art := sha256.Sum256([]byte("an entry hash"))
	artifact := art[:]
	ourRoot := s.ledgerRoot(t)
	untrustedCA := newTestSigstore(t)
	// A log whose key is trusted only from an hour from now.
	lateLog := newTestSigstore(t)
	lateLog.logStart = time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	lateLog.rootJSON = lateLog.trustedRootJSON(t)
	// A Fulcio authority whose stated validity ended ten minutes ago, though
	// its certificates are still within their own dates.
	edLog := newTestSigstore(t)
	edLog.edLog = true
	edLog.rootJSON = edLog.trustedRootJSON(t)
	expiredLog := newTestSigstore(t)
	expiredLog.logEnd = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	expiredLog.rootJSON = expiredLog.trustedRootJSON(t)
	// An intermediate whose EKU is server auth only: the chain may not
	// lend code signing to a leaf it could not grant.
	serverAuthCA := newTestSigstore(t)
	serverAuthCA.interCert = mustCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "server-auth-intermediate"},
		NotBefore: serverAuthCA.caStart, NotAfter: serverAuthCA.caEnd.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}, serverAuthCA.rootCert, &serverAuthCA.interKey.PublicKey, serverAuthCA.rootKey)
	serverAuthCA.rootJSON = serverAuthCA.trustedRootJSON(t)
	expiredCA := newTestSigstore(t)
	expiredCA.caEnd = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	expiredCA.rootJSON = expiredCA.trustedRootJSON(t)
	other := sha256.Sum256([]byte("other"))
	p521, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		ss     *testSigstore // nil: s
		sp     spec
		repo   string
		accept bool
		// want is a substring of OUR error, so each row is caught by the
		// check it targets and not by an earlier one by accident.
		want string
		// stricter: ours rejects where sigstore-go accepts, on purpose.
		stricter string
	}{
		{name: "valid", accept: true},
		// Fix round 1: rows for every check the first mutation pass left untested.
		{name: "leaf without any extended key usage", sp: spec{noEKU: true}, want: "not for code signing",
			stricter: "x509.Verify accepts a leaf with no EKU; code signing is required of the leaf itself"},
		{name: "bundle: case-variant key", sp: spec{bundleMutate: replaceOnce(`"mediaType"`, `"MEDIATYPE"`)}, want: `unknown field "MEDIATYPE"`},
		{name: "bundle: duplicate key, decoy first", sp: spec{bundleMutate: prefixKey(`"messageSignature":{"signature":"AAAA"},`)}, want: `duplicate field "messageSignature"`},
		{name: "bundle: trailing brace", sp: spec{bundleMutate: func(b []byte) []byte { return append(b, '}') }}, want: "trailing data"},
		{name: "body: case-variant key", sp: spec{bodyMutate: replaceOnce(`"apiVersion"`, `"APIVERSION"`)}, want: `unknown field "APIVERSION"`,
			stricter: "rekor parses the body with encoding/json leniencies; one reading of a signed document is required"},
		{name: "body: duplicate key, decoy first", sp: spec{bodyMutate: prefixKey(`"kind":"rekord",`)}, want: `duplicate field "kind"`,
			stricter: "rekor parses the body with encoding/json leniencies; one reading of a signed document is required"},
		{name: "body: trailing brace", sp: spec{bodyMutate: func(b []byte) []byte { return append(b, '}') }}, want: "trailing data",
			stricter: "rekor parses the body with encoding/json leniencies; one reading of a signed document is required"},
		{name: "bundle: int64 as a bare number", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) { tle["logIndex"] = 100004 })}, want: "quoted decimal",
			stricter: "protojson writes every int64 quoted; a bare number is not the shape the signer emits"},
		{name: "bundle: no certificate", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) {
			delete(doc["verificationMaterial"].(map[string]any), "certificate")
		})}, want: "no signing certificate"},
		{name: "bundle: no message signature", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) { delete(doc, "messageSignature") })}, want: "no message signature"},
		{name: "bundle: object where a scalar belongs", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) { doc["mediaType"] = map[string]any{} })}, want: "want a scalar"},
		{name: "bundle: scalar where an object belongs", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) { doc["messageSignature"] = "x" })}, want: "want an object"},
		{name: "bundle: object where an array belongs", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionProof"].(map[string]any)["hashes"] = map[string]any{}
		})}, want: "want an array"},
		// Fix round 1, mechanical mutation pass: one row per check it found untested.
		{name: "int64 not in canonical digits", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) { tle["logIndex"] = "0100004" })},
			want: "canonical decimal"},
		{name: "certificate rawBytes empty", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) {
			doc["verificationMaterial"].(map[string]any)["certificate"] = map[string]any{"rawBytes": ""}
		})}, want: "no signing certificate"},
		{name: "certificate not DER", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) {
			doc["verificationMaterial"].(map[string]any)["certificate"] = map[string]any{"rawBytes": "AAAA"}
		})}, want: "signing certificate: x509"},
		{name: "signed entry timestamp empty", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionPromise"] = map[string]any{"signedEntryTimestamp": ""}
		})}, want: "no signed entry timestamp"},
		{name: "integrated time negative", sp: spec{integratedUnix: -1}, want: "out of range"},
		{name: "log index beyond 2^53", sp: spec{logIndex: 1 << 54}, want: "out of range",
			stricter: "RFC 8785 writes integers above 2^53 as doubles; refusing them keeps the SET payload exact"},
		{name: "integrated before the leaf's NotBefore", sp: spec{notBefore: time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
			integrated: time.Now().UTC().Add(-5 * time.Minute)}, want: "not valid at the integrated time"},
		{name: "intermediate not for code signing", ss: serverAuthCA, want: "does not chain to a Fulcio authority"},
		{name: "source repository DER with trailing bytes", sp: spec{repoTrailing: true}, want: "source repository extension"},
		{name: "legacy 1.1 issuer only", sp: spec{issuerOnlyV1: true}, accept: true},
		{name: "no issuer extension", sp: spec{noIssuer: true}, want: "names no OIDC issuer"},
		{name: "issuer 1.8 not a DER string", sp: spec{issuerNotDER: true}, want: "issuer extension"},
		{name: "leaf certifies an ed25519 key", sp: spec{edLeaf: true}, want: "is not ECDSA"},
		{name: "proof index negative", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionProof"].(map[string]any)["logIndex"] = "-1"
		})}, want: "outside a tree"},
		{name: "checkpoint without its final newline", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			c := tle["inclusionProof"].(map[string]any)["checkpoint"].(map[string]any)
			c["envelope"] = strings.TrimSuffix(c["envelope"].(string), "\n")
		})}, want: "not a signed note"},
		{name: "log key expired before integrated time", ss: expiredLog, want: "key is not valid at"},
		{name: "bundle: no tlog entries", sp: spec{bundleMutate: editBundle(func(doc, _ map[string]any) {
			doc["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{}
		})}, want: "carries 0 transparency-log entries"},
		{name: "no signed entry timestamp", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) { delete(tle, "inclusionPromise") })}, want: "no signed entry timestamp"},
		{name: "tlog kind is not hashedrekord", sp: spec{tlogKind: "dsse"}, want: "want hashedrekord 0.0.1",
			stricter: "sigstore-go v1.2.2 reads the kind from the body and does not compare kindVersion on this path"},
		{name: "tlog version is not 0.0.1", sp: spec{tlogVersion: "0.0.2"}, want: "want hashedrekord 0.0.1",
			stricter: "sigstore-go v1.2.2 reads the kind from the body and does not compare kindVersion on this path"},
		{name: "negative log index", sp: spec{logIndex: -1}, want: "out of range"},
		{name: "integrated time beyond 2^53", sp: spec{integratedUnix: 1 << 54}, want: "out of range"},
		{name: "issuers 1.1 and 1.8 disagree", sp: spec{issuerV1: "https://accounts.google.com"}, want: "two issuers",
			stricter: "sigstore-go lets the later extension win; two issuers in one certificate are refused"},
		{name: "issuer DER with trailing bytes", sp: spec{issuerTrailing: true}, want: "trailing bytes"},
		{name: "no source repository extension", sp: spec{noSourceRepo: true}, want: "names no source repository"},
		{name: "message digest algorithm SHA2_384", sp: spec{digestAlg: "SHA2_384"}, want: "message digest"},
		{name: "body hash algorithm sha512", sp: spec{bodyHashAlg: "sha512"}, want: "different artifact digest"},
		{name: "body kind rekord", sp: spec{bodyKind: "rekord"}, want: "log entry body is"},
		{name: "body apiVersion 0.0.2", sp: spec{bodyAPIVersion: "0.0.2"}, want: "log entry body is"},
		{name: "body certificate PEM with trailing data", sp: spec{bodyPEMTrailing: true}, want: "different certificate"},
		{name: "body certificate PEM of another block type", sp: spec{bodyPEMType: "PUBLIC KEY"}, want: "different certificate"},
		{name: "log with an ed25519 key", ss: edLog, want: "only ECDSA logs"},
		{name: "proof index outside the tree", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionProof"].(map[string]any)["logIndex"] = strconv.Itoa(treeSize)
		})}, want: "outside a tree"},
		{name: "proof root hash not 32 bytes", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionProof"].(map[string]any)["rootHash"] = make([]byte, 31)
		})}, want: "root hash is not 32 bytes"},
		{name: "proof with an extra hash", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			p := tle["inclusionProof"].(map[string]any)
			p["hashes"] = append(p["hashes"].([]any), p["hashes"].([]any)[0])
		})}, want: "hashes, want"},
		{name: "proof hash not 32 bytes", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			p := tle["inclusionProof"].(map[string]any)
			p["hashes"].([]any)[0] = "AAAA"
		})}, want: "hash is not 32 bytes"},
		{name: "checkpoint is not a signed note", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			tle["inclusionProof"].(map[string]any)["checkpoint"] = map[string]any{"envelope": "rekor.test - 1\n7\n"}
		})}, want: "not a signed note"},
		{name: "checkpoint signature line malformed", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			c := tle["inclusionProof"].(map[string]any)["checkpoint"].(map[string]any)
			c["envelope"] = c["envelope"].(string) + "not a signature line\n"
		})}, want: "signature line is malformed"},
		{name: "checkpoint signature not base64", sp: spec{bundleMutate: editBundle(func(_, tle map[string]any) {
			c := tle["inclusionProof"].(map[string]any)["checkpoint"].(map[string]any)
			c["envelope"] = c["envelope"].(string) + "\u2014 rekor.test !!!\n"
		})}, want: "checkpoint signature is malformed"},
		{name: "checkpoint key hint is another key's", sp: spec{cpHintFlip: true}, want: "checkpoint signature does not verify"},
		{name: "checkpoint with no signature line", sp: spec{cpNoSig: true}, want: "carries no signature"},
		{name: "checkpoint: a second signature line that does not verify", sp: spec{cpExtraBadSig: true}, want: "checkpoint signature does not verify"},
		{name: "flipped signature byte", sp: spec{sigFlip: true}, want: "signature does not verify under the signing certificate"},
		{name: "wrong message digest", sp: spec{digestOverride: other[:]}, want: "message digest"},
		{name: "signed from another repository", sp: spec{repo: "acme/other"}, want: "signed from https://github.com/acme/other"},
		{name: "verified as another repository", repo: "acme/other", want: "not https://github.com/acme/other"},
		{name: "wrong issuer", sp: spec{issuer: "https://accounts.google.com"}, want: "not GitHub Actions"},
		{name: "rekor body hash mismatch", sp: spec{bodyHashOverride: hex.EncodeToString(other[:])}, want: "different artifact digest"},
		{name: "rekor body signature mismatch", sp: spec{bodySigFlip: true}, want: "different signature"},
		{name: "rekor body certificate mismatch", sp: spec{bodyCertOther: true}, want: "different certificate"},
		{name: "checkpoint over another root hash", sp: spec{checkpointRoot: true}, want: "different root hash"},
		{name: "checkpoint with a Rekor v2 origin", sp: spec{checkpointOrigin: "rekor.test"}, want: "not a Rekor v1 checkpoint"},
		{name: "log key not yet valid at integrated time", ss: lateLog, want: "key is not valid at"},
		{name: "Fulcio authority's validity over at integrated time", ss: expiredCA, want: "does not chain to a Fulcio authority"},
		{name: "P-521 signing key", sp: spec{leafKey: p521}, want: "P-256 or P-384",
			stricter: "the signer's ephemeral key is P-256; only P-256 and P-384 are accepted"},
		{name: "bad SET", sp: spec{setFlip: true}, want: "signed entry timestamp"},
		{name: "bad inclusion proof hash", sp: spec{proofHashFlip: true}, want: "inclusion proof does not lead"},
		{name: "bad checkpoint signature", sp: spec{checkpointFlip: true}, want: "checkpoint signature"},
		{name: "certificate outside validity at integrated time", sp: spec{integrated: time.Now().UTC().Add(30 * time.Minute)}, want: "not valid at the integrated time"},
		{name: "unknown log id", sp: spec{logIDOverride: other[:]}, want: "not in the Sigstore trust root"},
		{name: "duplicate tlog entry", sp: spec{extraTlog: true}, want: "want exactly 1"},
		{name: "DSSE bundle", sp: spec{dsse: true}, want: "\"dsseEnvelope\""},
		{name: "leaf from an untrusted CA", sp: spec{signingCAKey: untrustedCA.interKey, signingCACert: untrustedCA.interCert}, want: "does not chain to a Fulcio authority"},
		{name: "no inclusion proof", sp: spec{noProof: true}, want: "no inclusion proof"},
		{name: "bundle v0.2 media type", sp: spec{mediaType: "application/vnd.dev.sigstore.bundle+json;version=0.2"},
			want: "media type", stricter: "one bundle shape: sigstore-go's v1.2.2 signer emits v0.3, and nothing else is accepted"},
		{name: "RFC 3161 timestamp present", sp: spec{tsa: true},
			want: `"timestampVerificationData"`, stricter: "corral relies on the Rekor-integrated time alone; a TSA block is a shape it does not accept"},
		{name: "checkpoint names another tree size", sp: spec{checkpointSize: treeSize + 1},
			want: "tree size", stricter: "rekor's VerifyCheckpointSignature compares only the root hash; the size is checked too"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo
			if tc.repo != "" {
				repo = tc.repo
			}
			ss, r := s, ourRoot
			if tc.ss != nil {
				ss, r = tc.ss, tc.ss.ledgerRoot(t)
			}
			b := ss.issue(t, artifact, tc.sp)
			ours := ledger.VerifyKeylessBundle(b, artifact, repo, r)
			theirs := oracle(t, ss.rootJSON, b, artifact, repo)
			t.Logf("ours: %v | sigstore-go: %v", ours, theirs)
			if (ours == nil) != tc.accept {
				t.Fatalf("ours accepted=%v, want %v: %v", ours == nil, tc.accept, ours)
			}
			if !tc.accept && !strings.Contains(ours.Error(), tc.want) {
				t.Fatalf("rejected by the wrong check: %v (want %q)", ours, tc.want)
			}
			if tc.stricter != "" {
				if theirs != nil {
					t.Fatalf("row says sigstore-go accepts (%s), but it rejected: %v", tc.stricter, theirs)
				}
				return
			}
			if (theirs == nil) != (ours == nil) {
				t.Fatalf("disagreement: ours %v, sigstore-go %v", ours, theirs)
			}
		})
	}
	t.Logf("differential table: %d bundles", len(cases))
}

func replaceOnce(old, new string) func([]byte) []byte {
	return func(b []byte) []byte {
		out := strings.Replace(string(b), old, new, 1)
		if out == string(b) {
			panic("fixture: nothing to replace: " + old)
		}
		return []byte(out)
	}
}

// prefixKey inserts a member at the start of the top-level object, so a
// decoy comes BEFORE the real key and encoding/json's last-wins would pick
// the real one: the duplicate itself must be refused.
func prefixKey(member string) func([]byte) []byte {
	return func(b []byte) []byte { return append([]byte("{"+member), b[1:]...) }
}

// A genuine public-good bundle: goreleaser v2.18.3's checksums.txt, signed
// keylessly in GitHub Actions (Fulcio + rekor.sigstore.dev), verified
// against the embedded root. Its RFC 3161 timestamp is removed first, because
// corral's one bundle shape carries none; every byte that is verified —
// certificate, signature, Rekor body, SET, inclusion proof, checkpoint — is
// as the public-good instances issued it.
func TestKeylessRealBundle(t *testing.T) {
	r, err := ledger.PublicGoodTrustedRoot()
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile("testdata/keyless/goreleaser-v2.18.3-checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile("testdata/keyless/goreleaser-v2.18.3-checksums.txt.sigstore.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.VerifyKeylessBundle(orig, artifact, "goreleaser/goreleaser", r); err == nil || !strings.Contains(err.Error(), "timestampVerificationData") {
		t.Fatalf("the original carries a TSA timestamp and must be refused naming it: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(orig, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["verificationMaterial"].(map[string]any), "timestampVerificationData")
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.VerifyKeylessBundle(b, artifact, "goreleaser/goreleaser", r); err != nil {
		t.Fatalf("a genuine public-good bundle must verify: %v", err)
	}
	if err := ledger.VerifyKeylessBundle(b, artifact, "goreleaser/other", r); err == nil {
		t.Fatal("verified as the wrong repository")
	}
	rootJSON, err := os.ReadFile("sigstore_trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := oracle(t, rootJSON, b, artifact, "goreleaser/goreleaser"); err != nil {
		t.Fatalf("sigstore-go rejects the fixture: %v", err)
	}
}

// Every refusal in ParseSigstoreRoot, one malformed root each.
func TestParseSigstoreRootRefuses(t *testing.T) {
	s := newTestSigstore(t)
	edit := func(fn func(doc map[string]any)) []byte {
		var doc map[string]any
		if err := json.Unmarshal(s.rootJSON, &doc); err != nil {
			t.Fatal(err)
		}
		fn(doc)
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ca := func(doc map[string]any) map[string]any {
		return doc["certificateAuthorities"].([]any)[0].(map[string]any)
	}
	tl := func(doc map[string]any) map[string]any { return doc["tlogs"].([]any)[0].(map[string]any) }
	cases := []struct {
		want string
		js   []byte
	}{
		{"unexpected EOF", []byte("{")},
		{"media type", edit(func(d map[string]any) { d["mediaType"] = "application/vnd.dev.sigstore.trustedroot+json;version=0.2" })},
		{"no certificate", edit(func(d map[string]any) { d["certificateAuthorities"] = []any{} })},
		{"no transparency", edit(func(d map[string]any) { d["tlogs"] = []any{} })},
		{"certificate authority 0 has no chain", edit(func(d map[string]any) { ca(d)["certChain"] = map[string]any{"certificates": []any{}} })},
		{"certificate authority 0 has no chain or no validity start", edit(func(d map[string]any) { delete(ca(d)["validFor"].(map[string]any), "start") })},
		{"certificate authority 0, certificate 0", edit(func(d map[string]any) {
			ca(d)["certChain"].(map[string]any)["certificates"].([]any)[0] = map[string]any{"rawBytes": "AAAA"}
		})},
		{"transparency log 0 has no log id", edit(func(d map[string]any) { delete(tl(d), "logId") })},
		{"transparency log 0 has no log id or no validity start", edit(func(d map[string]any) { delete(tl(d)["publicKey"].(map[string]any), "validFor") })},
		{"listed twice", edit(func(d map[string]any) { d["tlogs"] = []any{tl(d), tl(d)} })},
		{"tags don", edit(func(d map[string]any) { tl(d)["publicKey"].(map[string]any)["rawBytes"] = "AAAA" })},
	}
	for _, c := range cases {
		if _, err := ledger.ParseSigstoreRoot(c.js); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v", c.want, err)
		}
	}
	// Positive control: the unedited root parses.
	r := s.ledgerRoot(t)
	if r.CertificateAuthorities() != 1 || !r.HasLog(hex.EncodeToString(s.logID(t))) {
		t.Fatal("the valid test root must parse")
	}
}

// Important 1 of the security review: x509.Verify's KeyUsages accepts a leaf
// with no EKU at all, so the leaf's own code-signing EKU is required.
func TestKeylessLeafMustBeCodeSigning(t *testing.T) {
	s := newTestSigstore(t)
	e := s.keylessEntry(t, "", 1, spec{noEKU: true})
	c := only(t, ledger.Verify([]ledger.Entry{e}, trustFor(s, t, testRepo)))
	if c.SigOK || !strings.Contains(c.Problem, "not for code signing") {
		t.Fatalf("a leaf with no EKU must fail: %+v", c)
	}
}

// VerifyIntegrity is the reader that holds no trust at all — the writer's
// own RequireIntactChain before it appends. It checks hashes, links and the
// keyless shape rules, and no signature: a keyless entry is not a Problem
// there merely because no repository was named, or the chain could never be
// extended past its first keyless entry. It is a separate function, never a
// Trust setting, so a reader that names a repository always gets its check.
func TestVerifyIntegrity(t *testing.T) {
	s := newTestSigstore(t)
	k1 := s.keylessEntry(t, "", 1, spec{})
	k2 := s.keylessEntry(t, k1.Hash, 2, spec{})
	for i, c := range ledger.VerifyIntegrity([]ledger.Entry{k1, k2}) {
		if c.Problem != "" || !c.HashOK || !c.LinkOK || c.SigOK || !c.Signed {
			t.Fatalf("entry %d: an intact keyless chain read for integrity: %+v", i, c)
		}
	}
	// A forged bundle — a genuine one over ANOTHER entry — passes the
	// integrity read (no signature is checked: SigOK false) and fails the
	// full one on the same chain.
	other := s.keylessEntry(t, "", 9, spec{})
	forged := k2
	forged.Sigstore = other.Sigstore
	forged = reparse(t, forged)
	chain := []ledger.Entry{k1, forged}
	ic := ledger.VerifyIntegrity(chain)
	if c := ic[1]; c.Problem != "" || !c.HashOK || !c.LinkOK || c.SigOK {
		t.Fatalf("integrity read of a forged bundle: %+v", c)
	}
	fc := ledger.Verify(chain, trustFor(s, t, testRepo))
	if c := fc[1]; c.SigOK || !strings.Contains(c.Problem, "keyless entry:") {
		t.Fatalf("the full read must report the forged bundle: %+v", c)
	}
	// Every shape rule still holds under the integrity read.
	bad := k1
	bad.Signature = "00" // outside the hash, so the entry still hashes clean
	if c := only(t, ledger.VerifyIntegrity([]ledger.Entry{reparse(t, bad)})); !strings.Contains(c.Problem, "ed25519 signature as well") {
		t.Fatalf("shape rules hold under VerifyIntegrity: %+v", c)
	}
	noBundle := k1
	noBundle.Sigstore = nil
	if c := only(t, ledger.VerifyIntegrity([]ledger.Entry{reparse(t, noBundle)})); !strings.Contains(c.Problem, "carries no sigstore bundle") {
		t.Fatalf("a keyless entry with no bundle is a Problem under VerifyIntegrity: %+v", c)
	}
	v5 := k1
	v5.Format = ledger.FileFormat
	v5 = s.seal(t, v5, spec{})
	if c := only(t, ledger.VerifyIntegrity([]ledger.Entry{v5})); !strings.Contains(c.Problem, ledger.Format6) {
		t.Fatalf("the v6 pairing holds under VerifyIntegrity: %+v", c)
	}
	// And a Trust with no repository is still told so — the integrity read
	// is not what an empty Repo means.
	if c := ledger.Verify(chain, ledger.Trust{})[0]; c.Problem != ledger.ProblemNoLedgerRepo {
		t.Fatalf("Verify with no repository: %+v", c)
	}
}

// VerifyKeylessSignature is the bundle check alone, for a WRITER that holds
// only the hash it just signed: the keyless signer checks its own output
// with it before an entry is placed.
func TestVerifyKeylessSignature(t *testing.T) {
	s := newTestSigstore(t)
	art := sha256.Sum256([]byte("an entry"))
	b := s.issue(t, art[:], spec{})
	if err := ledger.VerifyKeylessSignature(b, art[:], testRepo, s.ledgerRoot(t)); err != nil {
		t.Fatalf("a good bundle: %v", err)
	}
	if err := ledger.VerifyKeylessSignature(b, art[:], "acme/other", s.ledgerRoot(t)); err == nil || !strings.Contains(err.Error(), "acme/other") {
		t.Fatalf("another repository must fail naming it: %v", err)
	}
	other := sha256.Sum256([]byte("another entry"))
	if err := ledger.VerifyKeylessSignature(b, other[:], testRepo, s.ledgerRoot(t)); err == nil {
		t.Fatal("a bundle over another artifact must fail")
	}
	if err := ledger.VerifyKeylessSignature(b, art[:], "", s.ledgerRoot(t)); err == nil || err.Error() != ledger.ProblemNoLedgerRepo {
		t.Fatalf("no repository: %v", err)
	}
	if err := ledger.VerifyKeylessSignature(b, art[:], testRepo, nil); err == nil || !strings.Contains(err.Error(), "Sigstore trust root") {
		t.Fatalf("no root: %v", err)
	}
}
