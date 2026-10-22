// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/pdbethke/corralai/internal/ledger"
)

// testSigstore is a Fulcio CA and a Rekor v1 log, in process, that issue
// exactly what the public-good instances issue for a GitHub Actions run.
//
// It is built here rather than taken from sigstore-go's pkg/testing/ca
// VirtualSigstore because VirtualSigstore emits no inclusion proof or
// checkpoint for a message-signature (hashedrekord) entry, and its Rekor key
// is unexported, so a test cannot sign one. Production never accepts a
// bundle without a proof, so the fixture has to carry one: every piece is
// therefore made here, and sigstore-go's verifier — the oracle — judges each
// bundle independently of the code under test.
type testSigstore struct {
	rootKey, interKey, rekorKey *ecdsa.PrivateKey
	rootCert, interCert         *x509.Certificate
	caStart, caEnd              time.Time
	logStart                    time.Time
	rootJSON                    []byte
}

const testRepo = "acme/widgets"

func newTestSigstore(t *testing.T) *testSigstore {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	s := &testSigstore{caStart: now.Add(-48 * time.Hour), caEnd: now.Add(48 * time.Hour), logStart: now.Add(-48 * time.Hour)}
	s.rootKey = mustKey(t)
	s.interKey = mustKey(t)
	s.rekorKey = mustKey(t)
	s.rootCert = mustCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-fulcio-root"},
		NotBefore: s.caStart, NotAfter: s.caEnd.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}, nil, &s.rootKey.PublicKey, s.rootKey)
	s.interCert = mustCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "test-fulcio-intermediate"},
		NotBefore: s.caStart, NotAfter: s.caEnd.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true, IsCA: true,
	}, s.rootCert, &s.interKey.PublicKey, s.rootKey)
	s.rootJSON = s.trustedRootJSON(t)
	return s
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustCert(t *testing.T, tmpl, parent *x509.Certificate, pub any, priv *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *testSigstore) logID(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return sum[:]
}

// trustedRootJSON is the test instance as a Sigstore trusted_root.json, in
// the same protobuf-JSON form the public-good root is published in, so both
// verifiers read the SAME bytes.
func (s *testSigstore) trustedRootJSON(t *testing.T) []byte {
	t.Helper()
	pkix, err := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"mediaType": "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"tlogs": []any{map[string]any{
			"baseUrl":       "https://rekor.test",
			"hashAlgorithm": "SHA2_256",
			"publicKey": map[string]any{
				"rawBytes":   pkix,
				"keyDetails": "PKIX_ECDSA_P256_SHA_256",
				"validFor":   map[string]any{"start": s.logStart.Format(time.RFC3339)},
			},
			"logId": map[string]any{"keyId": s.logID(t)},
		}},
		"certificateAuthorities": []any{map[string]any{
			"subject":   map[string]any{"organization": "test", "commonName": "test-fulcio"},
			"uri":       "https://fulcio.test",
			"certChain": map[string]any{"certificates": []any{map[string]any{"rawBytes": s.interCert.Raw}, map[string]any{"rawBytes": s.rootCert.Raw}}},
			"validFor":  map[string]any{"start": s.caStart.Format(time.RFC3339), "end": s.caEnd.Format(time.RFC3339)},
		}},
		"ctlogs":               []any{},
		"timestampAuthorities": []any{},
	}
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func (s *testSigstore) ledgerRoot(t *testing.T) *ledger.SigstoreRoot {
	t.Helper()
	r, err := ledger.ParseSigstoreRoot(s.rootJSON)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// spec is one bundle to issue. The zero-valued hooks issue a valid bundle for
// testRepo; each mutation in the differential table changes one thing at the
// point where an attacker (or a bug) would change it, so everything signed
// AFTER that point stays consistent and only the check under test can catch it.
type spec struct {
	repo, issuer     string
	integrated       time.Time // zero: within the leaf's validity
	leafKey          *ecdsa.PrivateKey
	signingCAKey     *ecdsa.PrivateKey // zero: the trusted intermediate
	signingCACert    *x509.Certificate
	digestOverride   []byte // messageDigest.digest
	sigFlip          bool   // flip a byte of the signature everywhere
	bodySigFlip      bool   // flip it in the Rekor body only
	bodyHashOverride string // Rekor body's data.hash.value
	setFlip          bool
	proofHashFlip    bool
	checkpointFlip   bool
	checkpointRoot   bool   // the checkpoint signs another root hash
	checkpointOrigin string // non-empty: the checkpoint's first line
	bodyCertOther    bool   // the Rekor body names another certificate
	checkpointSize   int64  // non-zero: checkpoint names this size
	logIDOverride    []byte
	extraTlog        bool // a second (duplicate) tlog entry
	dsse             bool // a DSSE envelope instead of a message signature
	tsa              bool // an RFC 3161 timestamp block
	noProof          bool
	mediaType        string
}

// treeSize and leafIndex place the entry inside a 7-leaf tree at a
// tree-local index that differs from its global log index, as on the real
// sharded Rekor log, so the inclusion proof has a non-trivial path and the
// two indexes cannot be confused without a test failing.
const (
	treeSize    = 7
	leafIndex   = 4
	globalShift = 100000
)

func (s *testSigstore) issue(t *testing.T, artifact []byte, sp spec) []byte {
	t.Helper()
	if sp.repo == "" {
		sp.repo = testRepo
	}
	if sp.issuer == "" {
		sp.issuer = ledger.GitHubActionsIssuer
	}
	if sp.leafKey == nil {
		sp.leafKey = mustKey(t)
	}
	if sp.signingCAKey == nil {
		sp.signingCAKey, sp.signingCACert = s.interKey, s.interCert
	}
	notBefore := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if sp.integrated.IsZero() {
		sp.integrated = notBefore.Add(30 * time.Second)
	}
	issuerDER, _ := asn1.MarshalWithParams(sp.issuer, "utf8")
	repoDER, _ := asn1.MarshalWithParams("https://github.com/"+sp.repo, "utf8")
	san := mustURL(t, "https://github.com/"+sp.repo+"/.github/workflows/audit.yml@refs/heads/main")
	leaf := mustCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    notBefore, NotAfter: notBefore.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs: san,
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}, Value: []byte(sp.issuer)},
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}, Value: issuerDER},
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}, Value: repoDER},
		},
	}, sp.signingCACert, &sp.leafKey.PublicKey, sp.signingCAKey)

	digest := sha256.Sum256(artifact)
	sig, err := ecdsa.SignASN1(rand.Reader, sp.leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if sp.sigFlip {
		sig[len(sig)-1] ^= 1
	}
	bodySig := append([]byte(nil), sig...)
	if sp.bodySigFlip {
		bodySig[len(bodySig)-1] ^= 1
	}
	bodyHash := hex.EncodeToString(digest[:])
	if sp.bodyHashOverride != "" {
		bodyHash = sp.bodyHashOverride
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	if sp.bodyCertOther {
		// Same key, another certificate: only a byte comparison tells them apart.
		other := mustCert(t, &x509.Certificate{SerialNumber: big.NewInt(99), NotBefore: notBefore, NotAfter: notBefore.Add(10 * time.Minute)},
			nil, &sp.leafKey.PublicKey, sp.leafKey)
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw})
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.1", "kind": "hashedrekord",
		"spec": map[string]any{
			"data":      map[string]any{"hash": map[string]any{"algorithm": "sha256", "value": bodyHash}},
			"signature": map[string]any{"content": bodySig, "publicKey": map[string]any{"content": certPEM}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	logID := s.logID(t)
	if sp.logIDOverride != nil {
		logID = sp.logIDOverride
	}
	globalIndex := int64(globalShift + leafIndex)
	set := s.sign(t, []byte(fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString(body), sp.integrated.Unix(), hex.EncodeToString(logID), globalIndex)))
	if sp.setFlip {
		set[len(set)-1] ^= 1
	}

	leaves := make([][]byte, treeSize)
	for i := range leaves {
		leaves[i] = leafHash([]byte("other entry " + strconv.Itoa(i)))
	}
	leaves[leafIndex] = leafHash(body)
	rootHash := mth(leaves)
	proof := auditPath(leafIndex, leaves)
	if sp.proofHashFlip {
		proof[0][0] ^= 1
	}
	cpSize := int64(treeSize)
	if sp.checkpointSize != 0 {
		cpSize = sp.checkpointSize
	}
	cpRoot := rootHash
	if sp.checkpointRoot {
		cpRoot = leafHash([]byte("another tree"))
	}
	origin := "rekor.test - 1234"
	if sp.checkpointOrigin != "" {
		origin = sp.checkpointOrigin
	}
	note := fmt.Sprintf("%s\n%d\n%s\n", origin, cpSize, base64.StdEncoding.EncodeToString(cpRoot))
	noteSig := s.sign(t, []byte(note))
	if sp.checkpointFlip {
		noteSig[len(noteSig)-1] ^= 1
	}
	pkixDER, _ := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	hint := sha256.Sum256(pkixDER)
	envelope := note + "\n— rekor.test " + base64.StdEncoding.EncodeToString(append(hint[:4:4], noteSig...)) + "\n"

	hashes := make([]any, len(proof))
	for i, h := range proof {
		hashes[i] = base64.StdEncoding.EncodeToString(h)
	}
	tle := map[string]any{
		"logIndex":         strconv.FormatInt(globalIndex, 10),
		"logId":            map[string]any{"keyId": logID},
		"kindVersion":      map[string]any{"kind": "hashedrekord", "version": "0.0.1"},
		"integratedTime":   strconv.FormatInt(sp.integrated.Unix(), 10),
		"inclusionPromise": map[string]any{"signedEntryTimestamp": set},
		"inclusionProof": map[string]any{
			"logIndex":   strconv.Itoa(leafIndex),
			"rootHash":   rootHash,
			"treeSize":   strconv.Itoa(treeSize),
			"hashes":     hashes,
			"checkpoint": map[string]any{"envelope": envelope},
		},
		"canonicalizedBody": body,
	}
	if sp.noProof {
		delete(tle, "inclusionProof")
	}
	tlogs := []any{tle}
	if sp.extraTlog {
		tlogs = append(tlogs, tle)
	}
	vm := map[string]any{"certificate": map[string]any{"rawBytes": leaf.Raw}, "tlogEntries": tlogs}
	if sp.tsa {
		vm["timestampVerificationData"] = map[string]any{"rfc3161Timestamps": []any{map[string]any{"signedTimestamp": []byte{0x30, 0x00}}}}
	}
	mt := "application/vnd.dev.sigstore.bundle.v0.3+json"
	if sp.mediaType != "" {
		mt = sp.mediaType
	}
	doc := map[string]any{"mediaType": mt, "verificationMaterial": vm}
	if sp.dsse {
		payload := []byte(`{"_type":"https://in-toto.io/Statement/v1"}`)
		pae := []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len("application/vnd.in-toto+json"), "application/vnd.in-toto+json", len(payload), payload))
		pd := sha256.Sum256(pae)
		dsig, _ := ecdsa.SignASN1(rand.Reader, sp.leafKey, pd[:])
		doc["dsseEnvelope"] = map[string]any{"payload": payload, "payloadType": "application/vnd.in-toto+json", "signatures": []any{map[string]any{"sig": dsig}}}
	} else {
		md := digest[:]
		if sp.digestOverride != nil {
			md = sp.digestOverride
		}
		doc["messageSignature"] = map[string]any{"messageDigest": map[string]any{"algorithm": "SHA2_256", "digest": md}, "signature": sig}
	}
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func (s *testSigstore) sign(t *testing.T, msg []byte) []byte {
	t.Helper()
	d := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, s.rekorKey, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// RFC 6962 §2.1, written out independently of the code under test; the
// oracle (transparency-dev/merkle via sigstore-go) checks it too.
func leafHash(b []byte) []byte {
	h := sha256.Sum256(append([]byte{0}, b...))
	return h[:]
}

func nodeHash(l, r []byte) []byte {
	h := sha256.Sum256(append(append([]byte{1}, l...), r...))
	return h[:]
}

func split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

func mth(leaves [][]byte) []byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	k := split(len(leaves))
	return nodeHash(mth(leaves[:k]), mth(leaves[k:]))
}

func auditPath(m int, leaves [][]byte) [][]byte {
	if len(leaves) == 1 {
		return nil
	}
	k := split(len(leaves))
	if m < k {
		return append(auditPath(m, leaves[:k]), mth(leaves[k:]))
	}
	return append(auditPath(m-k, leaves[k:]), mth(leaves[:k]))
}

// oracle verifies a bundle with sigstore-go under the policy corral's own
// verifier enforces: the GitHub Actions issuer, the repository's
// SourceRepositoryURI, at least one transparency-log entry and one
// integrated timestamp. The SAN is not part of corral's policy, so it is
// matched by ".*" here.
func oracle(t *testing.T, rootJSON, bundleJSON, artifact []byte, repo string) error {
	t.Helper()
	tr, err := root.NewTrustedRootFromJSON(rootJSON)
	if err != nil {
		t.Fatalf("oracle: trusted root: %v", err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return err
	}
	v, err := verify.NewVerifier(tr, verify.WithTransparencyLog(1), verify.WithIntegratedTimestamps(1))
	if err != nil {
		t.Fatalf("oracle: verifier: %v", err)
	}
	sanM, err := verify.NewSANMatcher("", ".*")
	if err != nil {
		t.Fatal(err)
	}
	issM, err := verify.NewIssuerMatcher(ledger.GitHubActionsIssuer, "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := verify.NewCertificateIdentity(sanM, issM, certificate.Extensions{SourceRepositoryURI: "https://github.com/" + repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(&b, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(artifact)), verify.WithCertificateIdentity(id)))
	return err
}

// keylessEntry is a corral-ledger-6 scan entry after prev, signed through
// the test instance over KeylessArtifact. id distinguishes scans.
func (s *testSigstore) keylessEntry(t *testing.T, prev string, id int64, sp spec) ledger.Entry {
	t.Helper()
	e := ledger.Entry{Format: ledger.Format6, KeyID: ledger.KeyIDSigstore, Prev: prev,
		Pushed: time.Now().UTC().Add(time.Duration(id) * time.Second).Truncate(time.Microsecond),
		Bundle: ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "abcdef123456", ScanID: id, CorralVersion: "vtest"}}}
	e.ScanUID = ledger.ScanUID(e.Bundle.Scan, e.Pushed)
	e.Bundle.Scan.ScanUID = e.ScanUID
	return s.seal(t, e, sp)
}

// seal hashes e, signs the hash keylessly, and round-trips it through bytes
// so Raw is set exactly as a reader sets it.
func (s *testSigstore) seal(t *testing.T, e ledger.Entry, sp spec) ledger.Entry {
	t.Helper()
	e.Raw, e.Hash, e.Sigstore = nil, "", nil
	h, err := ledger.EntryHash(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Hash = h
	art, err := ledger.KeylessArtifact(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Sigstore = s.issue(t, art, sp)
	return reparse(t, e)
}

func reparse(t *testing.T, e ledger.Entry) ledger.Entry {
	t.Helper()
	name := e.File
	if name == "" {
		name = "keyless-" + e.Hash[:12] + ".json"
	}
	js, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ledger.ParseEntry(name, js)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustURL(t *testing.T, s string) []*url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return []*url.URL{u}
}
