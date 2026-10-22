// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/rekor/pkg/generated/models"

	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/ledger/ledgertest"
)

const keylessTestRepo = "acme/widgets"

// keylessTestSigner is a SigstoreLedgerSigner whose bundles come from an
// in-process CA and log that sign as a GitHub Actions run of signedAs, and
// which checks its output for checkAs — the repository its readers will name.
func keylessTestSigner(t *testing.T, s *ledgertest.Sigstore, signedAs, checkAs string) SigstoreLedgerSigner {
	t.Helper()
	return SigstoreLedgerSigner{
		bundle: func(hash []byte) ([]byte, error) { return s.Issue(t, hash, signedAs), nil },
		repo:   checkAs,
		root:   s.Root(t),
	}
}

func TestSigstoreSignerWritesV6Entry(t *testing.T) {
	s := ledgertest.New(t)
	dir := t.TempDir()
	signer := keylessTestSigner(t, s, keylessTestRepo, keylessTestRepo)
	// Two entries: the second append runs RequireIntactChain over a chain
	// that already holds a keyless entry, with no repository to name.
	appendScan(t, dir, signer, "aaaa1111")
	appendScan(t, dir, signer, "bbbb2222")

	entries := mustRead(t, dir)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Format != ledger.Format6 || e.KeyID != ledger.KeyIDSigstore || e.Signature != "" || len(e.Sigstore) == 0 {
			t.Fatalf("entry %d: format %q keyid %q signature %q bundle %d bytes — want %s, %s, no signature, a bundle",
				i, e.Format, e.KeyID, e.Signature, len(e.Sigstore), ledger.Format6, ledger.KeyIDSigstore)
		}
	}
	trust := ledger.Trust{Repo: keylessTestRepo, SigstoreRoot: s.Root(t)}
	for i, c := range ledger.Verify(entries, trust) {
		if c.Problem != "" || !c.SigOK || !c.Keyless || c.Signer != "https://github.com/"+keylessTestRepo {
			t.Fatalf("entry %d does not verify for %s: %+v", i, keylessTestRepo, c)
		}
	}
	trust.Repo = "acme/other"
	for i, c := range ledger.Verify(entries, trust) {
		if c.SigOK || c.Problem == "" {
			t.Fatalf("entry %d verified for a repository that did not sign it: %+v", i, c)
		}
	}
	if err := RequireIntactChain(dir); err != nil {
		t.Fatalf("a keyless chain must read as intact to its writer: %v", err)
	}
}

// The signer checks its own bundle as the readers will, before the entry is
// placed: a bundle its readers would refuse is never written.
func TestSigstoreSignerRefusesItsOwnBadBundle(t *testing.T) {
	s := ledgertest.New(t)
	dir := t.TempDir()
	signer := keylessTestSigner(t, s, "acme/other", keylessTestRepo)
	_, err := AppendLedgerEntry(dir, scanEntry("aaaa1111"), signer)
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("a bundle signed as another repository must be refused before placing: %v", err)
	}
	if n := scanFileCount(t, dir); n != 0 {
		t.Fatalf("%d entries written by a refused signature", n)
	}
	failing := SigstoreLedgerSigner{bundle: func([]byte) ([]byte, error) { return nil, errors.New("fulcio is down") }, repo: keylessTestRepo, root: s.Root(t)}
	if _, err := AppendLedgerEntry(dir, scanEntry("aaaa1111"), failing); err == nil || !strings.Contains(err.Error(), "fulcio is down") {
		t.Fatalf("a signing failure must surface: %v", err)
	}
}

// Who may sign, and where a chain begins, stay with the root key; the writer
// refuses what the verifier would.
func TestSigstoreSignerCannotControlTheChain(t *testing.T) {
	s := ledgertest.New(t)
	signer := keylessTestSigner(t, s, keylessTestRepo, keylessTestRepo)
	dir := t.TempDir()
	if _, err := WriteSignerChange(dir, registerOf(seededKey(0x51), "laptop"), signer); err == nil || !strings.Contains(err.Error(), "root key") {
		t.Fatalf("a keyless signer change must be refused naming the root key: %v", err)
	}
	appendScan(t, dir, signer, "aaaa1111")
	if _, _, err := WriteCheckpoint(dir, signer, nil); err == nil || !strings.Contains(err.Error(), "root key") {
		t.Fatalf("a keyless checkpoint must be refused naming the root key: %v", err)
	}
}

// A ledger that registers its signers still takes a keyless entry: its
// identity is the repository, checked by the reader, not a registration.
func TestRequireRegisteredSignerAcceptsKeyless(t *testing.T) {
	s := ledgertest.New(t)
	dir, root, _ := chainWithMember(t, 0x52, 0x53)
	appendScan(t, dir, keylessTestSigner(t, s, keylessTestRepo, keylessTestRepo), "cccc3333")
	trust := ledger.Trust{RootPub: pubOf(root), Repo: keylessTestRepo, SigstoreRoot: s.Root(t)}
	for i, c := range ledger.Verify(mustRead(t, dir), trust) {
		if c.Problem != "" || !c.SigOK {
			t.Fatalf("entry %d of a signer ledger with a keyless entry: %+v", i, c)
		}
	}
}

const wantNoOIDC = `keyless signing needs the workflow's OIDC token: add "permissions: id-token: write"`

func TestSigstoreSignerNeedsOIDC(t *testing.T) {
	for name, env := range map[string][2]string{
		"neither":   {"", ""},
		"no url":    {"", "tok"},
		"no token":  {"https://token.example/x?a=1", ""},
		"blank url": {"  ", "tok"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", env[0])
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", env[1])
			t.Setenv("GITHUB_REPOSITORY", keylessTestRepo)
			_, err := NewGitHubSigstoreSigner()
			if err == nil || err.Error() != wantNoOIDC {
				t.Fatalf("err = %v, want exactly %q", err, wantNoOIDC)
			}
		})
	}
}

func TestSigstoreSignerNeedsRepository(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://token.example/x?a=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "")
	if _, err := NewGitHubSigstoreSigner(); err == nil || !strings.Contains(err.Error(), "GITHUB_REPOSITORY") {
		t.Fatalf("no repository must be refused naming GITHUB_REPOSITORY: %v", err)
	}
	t.Setenv("GITHUB_REPOSITORY", keylessTestRepo)
	s, err := NewGitHubSigstoreSigner()
	if err != nil {
		t.Fatal(err)
	}
	if s.repo != keylessTestRepo || s.root == nil || s.bundle == nil || s.SigningKeyID() != ledger.KeyIDSigstore {
		t.Fatalf("signer not built for %s on the public-good root: %+v", keylessTestRepo, s)
	}
}

func TestLedgerSignerEnvRefusesUnknown(t *testing.T) {
	for _, v := range []string{"gpg", "Sigstore", "ed25519", "sigstore,gpg"} {
		t.Setenv(LedgerSignerEnv, v)
		keyless, err := KeylessSignerSelected()
		if err == nil || keyless || !strings.Contains(err.Error(), `"sigstore"`) || !strings.Contains(err.Error(), LedgerSignerEnv) {
			t.Fatalf("%s=%q: keyless %v err %v — want a refusal naming %s and the accepted value \"sigstore\"", LedgerSignerEnv, v, keyless, err, LedgerSignerEnv)
		}
	}
	for v, want := range map[string]bool{"": false, "  ": false, "sigstore": true, " sigstore\n": true} {
		t.Setenv(LedgerSignerEnv, v)
		if keyless, err := KeylessSignerSelected(); err != nil || keyless != want {
			t.Fatalf("%s=%q: keyless %v err %v, want %v", LedgerSignerEnv, v, keyless, err, want)
		}
	}
}

// The signer's endpoints are the public-good instances, named. Inspected,
// not exercised — no request leaves the test.
func TestPublicGoodSigningOptions(t *testing.T) {
	cfg := publicGoodSigning()
	if SigstoreFulcioURL != "https://fulcio.sigstore.dev" || SigstoreRekorURL != "https://rekor.sigstore.dev" {
		t.Fatalf("public-good URLs changed: %s %s", SigstoreFulcioURL, SigstoreRekorURL)
	}
	if cfg.fulcioURL != SigstoreFulcioURL || cfg.rekorURL != SigstoreRekorURL || cfg.timeout <= 0 {
		t.Fatalf("fulcio %q rekor %q timeout %v", cfg.fulcioURL, cfg.rekorURL, cfg.timeout)
	}
}

// fakeSigstore serves Fulcio's v2 signingCert and Rekor v1's create and
// get-by-uuid from an in-process CA and log, so the production client —
// token, proof of possession, certificate, log entry, bundle — runs end to
// end with no network. proofOnCreate false makes the create response omit
// the inclusion proof, as the real Rekor may, so the refetch is exercised.
type fakeSigstore struct {
	s             *ledgertest.Sigstore
	repo          string
	proofOnCreate bool
	certs, logs   int
	fetched       int
	entries       map[string]map[string]any
}

func (f *fakeSigstore) serve(t *testing.T) (fulcio, rekor *httptest.Server) {
	t.Helper()
	f.entries = map[string]map[string]any{}
	fulcio = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/signingCert" || r.Header.Get("Authorization") != "Bearer "+testJWT {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req struct {
			PublicKeyRequest struct {
				PublicKey struct {
					Content string `json:"content"`
				} `json:"publicKey"`
				ProofOfPossession string `json:"proofOfPossession"`
			} `json:"publicKeyRequest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		blk, _ := pem.Decode([]byte(req.PublicKeyRequest.PublicKey.Content))
		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Fulcio checks the proof of possession over the token's subject.
		pop, _ := base64.StdEncoding.DecodeString(req.PublicKeyRequest.ProofOfPossession)
		sub := sha256.Sum256([]byte("repo:" + f.repo + ":ref:refs/heads/main"))
		if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), sub[:], pop) {
			http.Error(w, "proof of possession", http.StatusUnauthorized)
			return
		}
		f.certs++
		leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.s.Certify(t, pub, f.repo)}))
		_ = json.NewEncoder(w).Encode(map[string]any{"signedCertificateEmbeddedSct": map[string]any{"chain": map[string]any{"certificates": append([]string{leaf}, f.s.ChainPEM()...)}}})
	}))
	rekor = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/log/entries":
			var pe struct {
				Spec struct {
					Data struct {
						Hash struct {
							Value string `json:"value"`
						} `json:"hash"`
					} `json:"data"`
					Signature struct {
						Content   []byte `json:"content"`
						PublicKey struct {
							Content []byte `json:"content"`
						} `json:"publicKey"`
					} `json:"signature"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pe); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			digest, _ := hex.DecodeString(pe.Spec.Data.Hash.Value)
			blk, _ := pem.Decode(pe.Spec.Signature.PublicKey.Content)
			body := ledgertest.HashedRekordBody(t, digest, pe.Spec.Signature.Content, blk.Bytes)
			le := f.s.Integrate(t, body)
			f.logs++
			uuid := fmt.Sprintf("%064x", f.logs)
			hashes := make([]string, len(le.Hashes))
			for i, h := range le.Hashes {
				hashes[i] = hex.EncodeToString(h)
			}
			full := map[string]any{
				"body": base64.StdEncoding.EncodeToString(body), "integratedTime": le.IntegratedTime,
				"logID": hex.EncodeToString(le.LogID), "logIndex": le.LogIndex,
				"verification": map[string]any{
					"signedEntryTimestamp": base64.StdEncoding.EncodeToString(le.SET),
					"inclusionProof": map[string]any{"logIndex": le.ProofIndex, "treeSize": le.TreeSize, "rootHash": hex.EncodeToString(le.RootHash),
						"hashes": hashes, "checkpoint": le.Checkpoint},
				},
			}
			f.entries[uuid] = full
			created := full
			if !f.proofOnCreate {
				created = map[string]any{}
				for k, v := range full {
					created[k] = v
				}
				created["verification"] = map[string]any{"signedEntryTimestamp": base64.StdEncoding.EncodeToString(le.SET)}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", uuid)
			w.Header().Set("Location", "/api/v1/log/entries/"+uuid)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{uuid: created})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/log/entries/"):
			uuid := strings.TrimPrefix(r.URL.Path, "/api/v1/log/entries/")
			e, ok := f.entries[uuid]
			if !ok {
				http.NotFound(w, r)
				return
			}
			f.fetched++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{uuid: e})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { fulcio.Close(); rekor.Close() })
	return fulcio, rekor
}

// testJWT is an unsigned GitHub-Actions-shaped ID token: the signer reads
// only its subject; Fulcio (here, the fake) is what would verify it.
var testJWT = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"repo:acme/widgets:ref:refs/heads/main","iss":"https://token.actions.githubusercontent.com"}`)) + ".sig"

// The production signing path, end to end against an in-process Fulcio and
// Rekor: the entries it writes verify by repository, the run's two entries
// share one certificate, each is its own log entry, a create response
// without an inclusion proof is fetched again, and the bundle carries no
// timestamp block.
func TestSigstoreSignerEndToEnd(t *testing.T) {
	for _, proofOnCreate := range []bool{true, false} {
		f := &fakeSigstore{s: ledgertest.New(t), repo: keylessTestRepo, proofOnCreate: proofOnCreate}
		fulcio, rekor := f.serve(t)
		cfg := sigstoreSigning{fulcioURL: fulcio.URL, rekorURL: rekor.URL, timeout: 10 * time.Second}
		tokens := 0
		bundle := cfg.signer(fulcio.Client(), func(context.Context) (string, error) { tokens++; return testJWT, nil })
		signer := SigstoreLedgerSigner{bundle: bundle, repo: keylessTestRepo, root: f.s.Root(t)}

		dir := t.TempDir()
		appendScan(t, dir, signer, "aaaa1111")
		appendScan(t, dir, signer, "bbbb2222")
		entries := mustRead(t, dir)
		for i, c := range ledger.Verify(entries, ledger.Trust{Repo: keylessTestRepo, SigstoreRoot: f.s.Root(t)}) {
			if c.Problem != "" || !c.SigOK {
				t.Fatalf("proofOnCreate=%v entry %d: %+v", proofOnCreate, i, c)
			}
		}
		if f.certs != 1 || tokens != 1 || f.logs != 2 {
			t.Fatalf("proofOnCreate=%v: %d certificates, %d tokens, %d log entries — want 1, 1, 2", proofOnCreate, f.certs, tokens, f.logs)
		}
		if want := map[bool]int{true: 0, false: 2}[proofOnCreate]; f.fetched != want {
			t.Fatalf("proofOnCreate=%v: fetched %d entries again, want %d", proofOnCreate, f.fetched, want)
		}
		for _, e := range entries {
			art, err := ledger.KeylessArtifact(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.Oracle(t, e.Sigstore, art, keylessTestRepo); err != nil {
				t.Fatalf("proofOnCreate=%v: sigstore-go refuses the bundle the signer wrote: %v", proofOnCreate, err)
			}
			if strings.Contains(string(e.Sigstore), "timestampVerificationData") || strings.Contains(string(e.Sigstore), "x509CertificateChain") {
				t.Fatalf("the bundle carries a timestamp block or a chain: %s", e.Sigstore)
			}
		}
	}
}

// A log entry that comes back without an inclusion proof even when fetched
// again is refused, not assembled into a bundle the verifier would reject.
func TestAssembleBundleRefusesMissingProof(t *testing.T) {
	idx, it, id := int64(1), int64(1), "00"
	_, err := assembleBundle([]byte{1}, []byte{2}, []byte{3}, models.LogEntryAnon{LogIndex: &idx, IntegratedTime: &it, LogID: &id,
		Body: "", Verification: &models.LogEntryAnonVerification{SignedEntryTimestamp: []byte{1}}})
	if err == nil || !strings.Contains(err.Error(), "inclusion proof") {
		t.Fatalf("want a refusal naming the inclusion proof: %v", err)
	}
}

// Fulcio must certify the key that asked: a certificate for any other key
// could not sign this entry.
func TestFulcioCertificateMustBeForOurKey(t *testing.T) {
	s := ledgertest.New(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certify(t, &other.PublicKey, keylessTestRepo)}))
		_ = json.NewEncoder(w).Encode(map[string]any{"signedCertificateDetachedSct": map[string]any{"chain": map[string]any{"certificates": []string{leaf}}}})
	}))
	defer srv.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := fulcioCertificate(context.Background(), srv.Client(), srv.URL, key, testJWT); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("want a refusal of a certificate for another key: %v", err)
	}
}

func TestTokenSubject(t *testing.T) {
	jwt := func(claims string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	}
	for tok, want := range map[string]string{
		jwt(`{"sub":"repo:a/b:ref:refs/heads/main"}`):                     "repo:a/b:ref:refs/heads/main",
		jwt(`{"sub":"x","email":"me@example.com","email_verified":true}`): "me@example.com",
	} {
		if got, err := tokenSubject(tok); err != nil || got != want {
			t.Fatalf("subject %q, %v — want %q", got, err, want)
		}
	}
	for _, tok := range []string{"nodots", jwt(`{}`), jwt(`{"sub":"x","email":"me@example.com"}`), "h.!!!.s", jwt(`[`)} {
		if _, err := tokenSubject(tok); err == nil {
			t.Fatalf("%q: want a refusal", tok)
		}
	}
}

func TestGitHubOIDCTokenRequest(t *testing.T) {
	var gotAuth, gotAudience, gotOther string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAudience, gotOther = r.Header.Get("Authorization"), r.URL.Query().Get("audience"), r.URL.Query().Get("api-version")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "header.payload.sig"})
	}))
	defer srv.Close()
	tok, err := githubOIDCToken(context.Background(), srv.Client(), srv.URL+"/token?api-version=2.0", "request-secret")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "header.payload.sig" || gotAuth != "bearer request-secret" || gotAudience != "sigstore" || gotOther != "2.0" {
		t.Fatalf("token %q auth %q audience %q api-version %q", tok, gotAuth, gotAudience, gotOther)
	}

	for name, h := range map[string]http.HandlerFunc{
		"status":      func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusForbidden) },
		"empty value": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"value":""}`)) },
		"not json":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		bad := httptest.NewServer(h)
		_, err := githubOIDCToken(context.Background(), bad.Client(), bad.URL, "request-secret")
		bad.Close()
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if strings.Contains(err.Error(), "request-secret") {
			t.Fatalf("%s: the request token leaked into the error: %v", name, err)
		}
	}
}

// One certificate serves every entry a run signs while it has time left —
// the repo entry and its machine-ledger copy are two log entries, not two
// certificates — and a fresh one is fetched once it is near its end.
func TestFulcioSessionReusesCertificate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	issued := 0
	sess := &fulcioSession{issue: func(context.Context) (*ecdsa.PrivateKey, []byte, error) {
		issued++
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		return k, certValidUntil(t, now.Add(10*time.Minute)), nil
	}}
	k1, c1, err := sess.get(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	k2, c2, err := sess.get(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if issued != 1 || k1 != k2 || string(c1) != string(c2) {
		t.Fatalf("a second entry within the certificate's life requested %d certificates", issued)
	}
	if _, _, err := sess.get(context.Background(), now.Add(10*time.Minute-certReuseMargin)); err != nil {
		t.Fatal(err)
	}
	if issued != 2 {
		t.Fatalf("a certificate within %v of its end was reused (%d issued)", certReuseMargin, issued)
	}
}

func certValidUntil(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"}, NotBefore: notAfter.Add(-10 * time.Minute), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
