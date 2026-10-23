// SPDX-License-Identifier: Elastic-2.0

// Package ledgertest is TEST SUPPORT for keyless ledger entries: an
// in-process Fulcio CA and Rekor v1 log that issue the one Sigstore bundle
// shape internal/ledger accepts, so a package other than ledger (auditpush,
// cmd/corral) can write and verify keyless entries without the network.
//
// It must never be imported by non-test code: it mints certificates for any
// repository it is asked to, which is exactly what a production binary must
// not be able to do. TestOnlyTestsImportLedgertest enforces that over every
// package in the module.
//
// It issues only VALID bundles (Issue), or the pieces of one — a leaf
// certificate (Certify) and a log entry (Integrate) — for a test that stands
// up a fake Fulcio and Rekor around a real client; Oracle judges a bundle
// with sigstore-go. internal/ledger's own tests keep their mutation-capable
// issuer (keyless_helper_test.go), which bends each field a check reads;
// this is the happy path for packages that need a bundle to pass.
package ledgertest

import (
	"bytes"
	"crypto"
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

// Sigstore is one CA and one log. RootJSON is its trusted_root.json, in the
// protobuf-JSON form the public-good root is published in.
type Sigstore struct {
	rootKey, interKey, rekorKey *ecdsa.PrivateKey
	rootCert, interCert         *x509.Certificate
	RootJSON                    []byte
}

// The inclusion proof places the entry at a tree-local index inside a small
// tree, with a global log index that differs from it, as on the real sharded
// Rekor log.
const (
	treeSize    = 7
	leafIndex   = 4
	globalIndex = 100004
)

// New builds a CA and a log valid from two days ago to two days ahead.
func New(t testing.TB) *Sigstore {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-48*time.Hour), now.Add(48*time.Hour)
	s := &Sigstore{rootKey: key(t), interKey: key(t), rekorKey: key(t)}
	s.rootCert = cert(t, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ledgertest-fulcio-root"},
		NotBefore: start, NotAfter: end.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}, nil, &s.rootKey.PublicKey, s.rootKey)
	s.interCert = cert(t, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ledgertest-fulcio-intermediate"},
		NotBefore: start, NotAfter: end.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true, IsCA: true,
	}, s.rootCert, &s.interKey.PublicKey, s.rootKey)
	logPKIX, err := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"mediaType": "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"tlogs": []any{map[string]any{
			"baseUrl":       "https://rekor.ledgertest",
			"hashAlgorithm": "SHA2_256",
			"publicKey": map[string]any{
				"rawBytes":   logPKIX,
				"keyDetails": "PKIX_ECDSA_P256_SHA_256",
				"validFor":   map[string]any{"start": start.Format(time.RFC3339)},
			},
			"logId": map[string]any{"keyId": s.logID(t)},
		}},
		"certificateAuthorities": []any{map[string]any{
			"subject":   map[string]any{"organization": "ledgertest", "commonName": "ledgertest-fulcio"},
			"uri":       "https://fulcio.ledgertest",
			"certChain": map[string]any{"certificates": []any{map[string]any{"rawBytes": s.interCert.Raw}, map[string]any{"rawBytes": s.rootCert.Raw}}},
			"validFor":  map[string]any{"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339)},
		}},
		"ctlogs":               []any{},
		"timestampAuthorities": []any{},
	}
	if s.RootJSON, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	return s
}

// Root is RootJSON parsed by the ledger, for a ledger.Trust.
func (s *Sigstore) Root(t testing.TB) *ledger.SigstoreRoot {
	t.Helper()
	r, err := ledger.ParseSigstoreRoot(s.RootJSON)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Issue returns a bundle signing artifact (for a ledger entry, its
// ledger.KeylessArtifact) as a GitHub Actions run of github.com/<repo>
// would get it from the public-good Fulcio and Rekor: one leaf certificate,
// a message signature over sha256(artifact), and one hashedrekord entry with
// its signed entry timestamp, inclusion proof and signed checkpoint.
func (s *Sigstore) Issue(t testing.TB, artifact []byte, repo string) []byte {
	t.Helper()
	leafKey := key(t)
	leaf := s.Certify(t, &leafKey.PublicKey, repo)
	digest := sha256.Sum256(artifact)
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	body := HashedRekordBody(t, digest[:], sig, leaf)
	le := s.Integrate(t, body)
	hashes := make([]string, len(le.Hashes))
	for i, h := range le.Hashes {
		hashes[i] = base64.StdEncoding.EncodeToString(h)
	}
	js, err := json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{
			"certificate": map[string]any{"rawBytes": leaf},
			"tlogEntries": []any{map[string]any{
				"logIndex":         strconv.FormatInt(le.LogIndex, 10),
				"logId":            map[string]any{"keyId": le.LogID},
				"kindVersion":      map[string]any{"kind": "hashedrekord", "version": "0.0.1"},
				"integratedTime":   strconv.FormatInt(le.IntegratedTime, 10),
				"inclusionPromise": map[string]any{"signedEntryTimestamp": le.SET},
				"inclusionProof": map[string]any{
					"logIndex":   strconv.FormatInt(le.ProofIndex, 10),
					"rootHash":   le.RootHash,
					"treeSize":   strconv.FormatInt(le.TreeSize, 10),
					"hashes":     hashes,
					"checkpoint": map[string]any{"envelope": le.Checkpoint},
				},
				"canonicalizedBody": body,
			}},
		},
		"messageSignature": map[string]any{
			"messageDigest": map[string]any{"algorithm": "SHA2_256", "digest": digest[:]},
			"signature":     sig,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// Certify is the CA: a ten-minute code-signing leaf for pub, as Fulcio
// issues one to a GitHub Actions run of github.com/<repo>. Returns its DER.
func (s *Sigstore) Certify(t testing.TB, pub crypto.PublicKey, repo string) []byte {
	t.Helper()
	notBefore := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	issuer, err := asn1.MarshalWithParams(ledger.GitHubActionsIssuer, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	source, err := asn1.MarshalWithParams("https://github.com/"+repo, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	san, err := url.Parse("https://github.com/" + repo + "/.github/workflows/audit.yml@refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	return cert(t, &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    notBefore, NotAfter: notBefore.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs: []*url.URL{san},
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}, Value: []byte(ledger.GitHubActionsIssuer)},
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}, Value: issuer},
			{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}, Value: source},
		},
	}, s.interCert, pub, s.interKey).Raw
}

// ChainPEM is the CA's chain above a leaf, intermediate first, as Fulcio
// returns it after the leaf.
func (s *Sigstore) ChainPEM() []string {
	return []string{
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.interCert.Raw})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.rootCert.Raw})),
	}
}

// HashedRekordBody is the canonicalized body Rekor v1 logs for a
// hashedrekord 0.0.1 entry: the sha256 digest, the signature, and the
// signing certificate as PEM.
func HashedRekordBody(t testing.TB, digest, sig, leafDER []byte) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.1", "kind": "hashedrekord",
		"spec": map[string]any{
			"data":      map[string]any{"hash": map[string]any{"algorithm": "sha256", "value": hex.EncodeToString(digest)}},
			"signature": map[string]any{"content": sig, "publicKey": map[string]any{"content": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// LogEntry is what the log returns for an integrated body: the fields of a
// Rekor v1 LogEntryAnon, decoded.
type LogEntry struct {
	LogID          []byte
	LogIndex       int64 // global
	IntegratedTime int64
	SET            []byte
	ProofIndex     int64 // tree-local
	TreeSize       int64
	RootHash       []byte
	Hashes         [][]byte
	Checkpoint     string
}

// Integrate is the log: it records body now and returns its signed entry
// timestamp, an inclusion proof in a small tree, and a signed checkpoint.
func (s *Sigstore) Integrate(t testing.TB, body []byte) LogEntry {
	t.Helper()
	le := LogEntry{LogID: s.logID(t), LogIndex: globalIndex, IntegratedTime: time.Now().Unix(), ProofIndex: leafIndex, TreeSize: treeSize}
	le.SET = s.sign(t, []byte(fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString(body), le.IntegratedTime, hex.EncodeToString(le.LogID), le.LogIndex)))
	leaves := make([][]byte, treeSize)
	for i := range leaves {
		leaves[i] = leafHash([]byte("other entry " + strconv.Itoa(i)))
	}
	leaves[leafIndex] = leafHash(body)
	le.RootHash = mth(leaves)
	le.Hashes = auditPath(leafIndex, leaves)
	note := fmt.Sprintf("rekor.ledgertest - 1234\n%d\n%s\n", treeSize, base64.StdEncoding.EncodeToString(le.RootHash))
	pkixDER, err := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hint := sha256.Sum256(pkixDER)
	le.Checkpoint = note + "\n\u2014 rekor.ledgertest " + base64.StdEncoding.EncodeToString(append(hint[:4:4], s.sign(t, []byte(note))...)) + "\n"
	return le
}

// Oracle verifies bundle with sigstore-go, under the policy corral's own
// verifier enforces: the GitHub Actions issuer, the repository's
// SourceRepositoryURI, one transparency-log entry and its integrated time.
// A bundle both verifiers accept is in the shape sigstore-go itself
// produces and checks, not merely one corral's verifier happens to take.
func (s *Sigstore) Oracle(t testing.TB, bundleJSON, artifact []byte, repo string) error {
	t.Helper()
	tr, err := root.NewTrustedRootFromJSON(s.RootJSON)
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
	san, err := verify.NewSANMatcher("", ".*")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := verify.NewIssuerMatcher(ledger.GitHubActionsIssuer, "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := verify.NewCertificateIdentity(san, iss, certificate.Extensions{SourceRepositoryURI: "https://github.com/" + repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(&b, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(artifact)), verify.WithCertificateIdentity(id)))
	return err
}

func (s *Sigstore) logID(t testing.TB) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&s.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return sum[:]
}

func (s *Sigstore) sign(t testing.TB, msg []byte) []byte {
	t.Helper()
	d := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, s.rekorKey, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func key(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func cert(t testing.TB, tmpl, parent *x509.Certificate, pub crypto.PublicKey, priv *ecdsa.PrivateKey) *x509.Certificate {
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

// RFC 6962 §2.1 Merkle tree hashing.
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
