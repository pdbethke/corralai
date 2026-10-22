// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/rekor/pkg/generated/models"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/transparency"
)

// The keyless signer: a ledger entry signed through Sigstore with the GitHub
// Actions run's own OIDC identity, so the record is vouched for by the
// repository that ran the audit rather than by a key someone has to hold
// (and that an ephemeral runner cannot keep). The entry it produces is
// corral-ledger-6, checked by internal/ledger's verifier against the
// repository its reader names.
//
// It produces exactly the one bundle shape that verifier accepts — a
// v0.3 bundle with a Fulcio leaf certificate, a message signature over
// sha256 of the entry's hash, and one Rekor v1 hashedrekord entry carrying
// its signed entry timestamp, inclusion proof and checkpoint — and nothing
// that would change it: there is no timestamp authority anywhere in this
// code (a timestampVerificationData block is refused by name), no
// certificate chain, one log. And it checks every bundle with that verifier
// before the entry is placed, so an entry its readers would refuse is never
// written.
//
// The protocol client is ours, not sigstore-go's sign package: that package
// imports rekor's pki for every key format it might log, which links the
// deprecated golang.org/x/crypto/openpgp into the binary, and govulncheck
// (GO-2026-5932, no fix) fails the security gate on it. What is left is two
// HTTP calls — Fulcio's v2 signingCert, Rekor's create through rekor's own
// generated client (internal/transparency.SubmitLogEntry, shared with the
// dsse witness) — an ECDSA signature from the standard library, and the
// protobuf bundle assembled from protobuf-specs' own types, as sign.Bundle
// assembles it.

// LedgerSignerEnv selects how ledger entries are signed. LedgerSignerSigstore
// is the one value it takes; unset (or blank) keeps the local certify key.
const (
	LedgerSignerEnv      = "CORRALAI_LEDGER_SIGNER"
	LedgerSignerSigstore = "sigstore"
)

// SigstoreFulcioURL and SigstoreRekorURL are Sigstore's PUBLIC-GOOD
// instances: the CA that certifies the run's identity and the log that
// witnesses the signature. Named here, not left to a library's default, and
// with no fallback: the verifier's trust root is the public-good root, so a
// bundle from anywhere else could not verify. Every keyless entry is a
// public, permanent record in that log, naming the repository and workflow.
const (
	SigstoreFulcioURL = "https://fulcio.sigstore.dev"
	SigstoreRekorURL  = "https://rekor.sigstore.dev"
)

const (
	// sigstoreOIDCAudience is the audience Fulcio requires of the token.
	sigstoreOIDCAudience = "sigstore"
	// sigstoreNetworkTimeout bounds each request to the token service and
	// Fulcio (Rekor is bounded by signTimeout through its context).
	sigstoreNetworkTimeout = 30 * time.Second
	// signTimeout bounds one whole signature: token, certificate and log.
	signTimeout = 2 * time.Minute
	// certReuseMargin: a run's certificate signs a further entry only while
	// at least this much of its (ten-minute) validity remains, so the log
	// integrates the signature inside it — the verifier checks the
	// certificate at the log's integrated time.
	certReuseMargin = 2 * time.Minute
)

// ErrNoOIDCToken is the refusal when the run cannot get an OIDC token: the
// workflow did not grant it, or this is not a GitHub Actions run at all.
var ErrNoOIDCToken = errors.New(`keyless signing needs the workflow's OIDC token: add "permissions: id-token: write"`)

// KeylessSignerSelected reads LedgerSignerEnv: true for "sigstore", false
// when unset or blank, and an error for anything else — a misspelled signer
// is refused, never read as "unset", because that would quietly sign with a
// key the operator meant to replace (or write unsigned).
func KeylessSignerSelected() (bool, error) {
	switch v := strings.TrimSpace(os.Getenv(LedgerSignerEnv)); v {
	case "":
		return false, nil
	case LedgerSignerSigstore:
		return true, nil
	default:
		return false, fmt.Errorf("%s=%q is not a ledger signer corral knows: the one accepted value is %q (keyless, through the GitHub Actions run's identity); leave it unset to sign with the local certify key", LedgerSignerEnv, v, LedgerSignerSigstore)
	}
}

// githubOIDCEnv returns the run's token-request URL and bearer, or
// ErrNoOIDCToken. GitHub sets both only when the job has id-token: write.
func githubOIDCEnv() (reqURL, reqToken string, err error) {
	reqURL = strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"))
	reqToken = strings.TrimSpace(os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"))
	if reqURL == "" || reqToken == "" {
		return "", "", ErrNoOIDCToken
	}
	return reqURL, reqToken, nil
}

// CheckKeylessSigning is the no-network half of NewGitHubSigstoreSigner:
// whether this process could sign keylessly at all. A caller that is about
// to spend runs it first, so a run that cannot keep its record is refused
// before it starts rather than at its end.
func CheckKeylessSigning() error {
	if _, _, err := githubOIDCEnv(); err != nil {
		return err
	}
	_, err := githubRepository()
	return err
}

func githubRepository() (string, error) {
	repo := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))
	if repo == "" {
		return "", errors.New("keyless signing needs GITHUB_REPOSITORY (owner/name), which every GitHub Actions run sets: the signer checks its own bundle for the repository its readers will name")
	}
	return repo, nil
}

// SigstoreLedgerSigner signs a ledger entry's hash keylessly. bundle returns
// the Sigstore bundle JSON over the hash (ledger.KeylessArtifact of the
// entry being placed); repo and root are what Sign checks that bundle
// against before it is used.
type SigstoreLedgerSigner struct {
	bundle func(hash []byte) ([]byte, error)
	repo   string
	root   *ledger.SigstoreRoot
}

// NewSigstoreLedgerSigner is a keyless signer over a Sigstore client the
// caller supplies: bundle returns the bundle JSON over a hash, and every
// bundle is checked for repo under root before it is used, exactly as
// NewGitHubSigstoreSigner's are. It cannot sign anything the verifier would
// refuse, whatever bundle returns; it exists so a writer can be exercised
// against an in-process Sigstore (internal/ledger/ledgertest).
func NewSigstoreLedgerSigner(bundle func(hash []byte) ([]byte, error), repo string, root *ledger.SigstoreRoot) SigstoreLedgerSigner {
	return SigstoreLedgerSigner{bundle: bundle, repo: repo, root: root}
}

// SigningKeyID is ledger.KeyIDSigstore: placeEntry reads it to write a
// corral-ledger-6 entry, with the bundle in place of a signature.
func (SigstoreLedgerSigner) SigningKeyID() string { return ledger.KeyIDSigstore }

// Sign returns the bundle over hash, after checking it exactly as the
// record's readers will. The check is the same verifier a reader runs, so a
// public-good response this signer did not expect (no inclusion proof, a
// timestamp block) refuses the write instead of placing a permanent entry
// that can never verify.
func (s SigstoreLedgerSigner) Sign(hash []byte) (string, []byte, error) {
	if s.bundle == nil {
		return "", nil, errors.New("keyless signer was not built with NewGitHubSigstoreSigner")
	}
	b, err := s.bundle(hash)
	if err != nil {
		return "", nil, fmt.Errorf("keyless signing: %w", err)
	}
	if err := ledger.VerifyKeylessSignature(b, hash, s.repo, s.root); err != nil {
		return "", nil, fmt.Errorf("keyless signing: the Sigstore bundle does not verify as the record's readers will check it, so nothing was written: %w", err)
	}
	return ledger.KeyIDSigstore, b, nil
}

// NewGitHubSigstoreSigner is the keyless signer for a GitHub Actions run:
// an ephemeral ECDSA P-256 key, certified by the public-good Fulcio against
// the run's OIDC token (audience "sigstore"), each signature logged in the
// public-good Rekor. One certificate serves every entry the process signs
// while it has time left (fulcioSession), so a run's repo entry and its
// machine-ledger copy are two log entries under one certificate. Nothing
// touches the network until the first Sign.
func NewGitHubSigstoreSigner() (SigstoreLedgerSigner, error) {
	reqURL, reqToken, err := githubOIDCEnv()
	if err != nil {
		return SigstoreLedgerSigner{}, err
	}
	repo, err := githubRepository()
	if err != nil {
		return SigstoreLedgerSigner{}, err
	}
	root, err := ledger.PublicGoodTrustedRoot()
	if err != nil {
		return SigstoreLedgerSigner{}, fmt.Errorf("keyless signing: the embedded Sigstore root: %w", err)
	}
	cfg := publicGoodSigning()
	client := &http.Client{Timeout: cfg.timeout}
	token := func(ctx context.Context) (string, error) { return githubOIDCToken(ctx, client, reqURL, reqToken) }
	return SigstoreLedgerSigner{repo: repo, root: root, bundle: cfg.signer(client, token)}, nil
}

// sigstoreSigning is every choice the signer makes about Sigstore, in one
// value a test can inspect, or point at an in-process Fulcio and Rekor.
type sigstoreSigning struct {
	fulcioURL string
	rekorURL  string
	timeout   time.Duration
}

func publicGoodSigning() sigstoreSigning {
	return sigstoreSigning{fulcioURL: SigstoreFulcioURL, rekorURL: SigstoreRekorURL, timeout: sigstoreNetworkTimeout}
}

// signer is the bundle function over these endpoints: one fulcioSession
// for the signer's life, a fresh log entry per hash.
func (c sigstoreSigning) signer(client *http.Client, token func(context.Context) (string, error)) func(hash []byte) ([]byte, error) {
	sess := &fulcioSession{issue: func(ctx context.Context) (*ecdsa.PrivateKey, []byte, error) {
		tok, err := token(ctx)
		if err != nil {
			return nil, nil, err
		}
		// ECDSA P-256: Fulcio certifies it, and the verifier requires a
		// P-256 or P-384 key signing a SHA-256 digest.
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := fulcioCertificate(ctx, client, c.fulcioURL, key, tok)
		if err != nil {
			return nil, nil, fmt.Errorf("fulcio (%s): %w", c.fulcioURL, err)
		}
		return key, der, nil
	}}
	return func(hash []byte) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
		defer cancel()
		key, der, err := sess.get(ctx, time.Now())
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(hash)
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		if err != nil {
			return nil, err
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		le, err := transparency.SubmitLogEntry(ctx, c.rekorURL, hashedRekordEntry(digest[:], sig, certPEM))
		if err != nil {
			return nil, fmt.Errorf("rekor (%s): %w", c.rekorURL, err)
		}
		return assembleBundle(der, digest[:], sig, le)
	}
}

// hashedRekordEntry is the Rekor v1 hashedrekord 0.0.1 proposal: the digest
// that was signed, the signature, and the certificate that signed it — the
// fields sigstore-go's sign.Rekor fills through rekor's types package.
func hashedRekordEntry(digest, sig, certPEM []byte) models.ProposedEntry {
	return &models.Hashedrekord{
		APIVersion: strPtr("0.0.1"),
		Spec: &models.HashedrekordV001Schema{
			Data: &models.HashedrekordV001SchemaData{Hash: &models.HashedrekordV001SchemaDataHash{
				Algorithm: strPtr(models.HashedrekordV001SchemaDataHashAlgorithmSha256),
				Value:     strPtr(hex.EncodeToString(digest)),
			}},
			Signature: &models.HashedrekordV001SchemaSignature{
				Content:   sig,
				PublicKey: &models.HashedrekordV001SchemaSignaturePublicKey{Content: certPEM},
			},
		},
	}
}

func strPtr(v string) *string { return &v }

// assembleBundle is the v0.3 bundle over one signature and its log entry,
// built from protobuf-specs' types and written by protojson, as sign.Bundle
// writes it. The log entry is converted field by field as rekor's
// tle.GenerateTransparencyLogEntry converts it (that function needs rekor's
// types registry, which is what links openpgp). Kind and version are what
// was submitted; the verifier checks them against the logged body.
func assembleBundle(certDER, digest, sig []byte, le models.LogEntryAnon) ([]byte, error) {
	v := le.Verification
	switch {
	case le.LogID == nil || le.LogIndex == nil || le.IntegratedTime == nil:
		return nil, errors.New("rekor returned a log entry without its id, index or time")
	case v == nil || len(v.SignedEntryTimestamp) == 0:
		return nil, errors.New("rekor returned a log entry without a signed entry timestamp")
	case v.InclusionProof == nil || v.InclusionProof.LogIndex == nil || v.InclusionProof.TreeSize == nil ||
		v.InclusionProof.RootHash == nil || v.InclusionProof.Checkpoint == nil:
		return nil, errors.New("rekor returned a log entry without an inclusion proof, even when fetched again")
	}
	logID, err := hex.DecodeString(*le.LogID)
	if err != nil {
		return nil, fmt.Errorf("rekor log id: %w", err)
	}
	rootHash, err := hex.DecodeString(*v.InclusionProof.RootHash)
	if err != nil {
		return nil, fmt.Errorf("rekor inclusion proof root hash: %w", err)
	}
	hashes := make([][]byte, len(v.InclusionProof.Hashes))
	for i, h := range v.InclusionProof.Hashes {
		if hashes[i], err = hex.DecodeString(h); err != nil {
			return nil, fmt.Errorf("rekor inclusion proof hash: %w", err)
		}
	}
	var body []byte
	switch b := le.Body.(type) {
	case string:
		if body, err = base64.StdEncoding.DecodeString(b); err != nil {
			return nil, fmt.Errorf("rekor entry body: %w", err)
		}
	case []byte:
		body = b
	default:
		return nil, fmt.Errorf("rekor entry body is %T, not base64", le.Body)
	}
	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: certDER}},
			TlogEntries: []*protorekor.TransparencyLogEntry{{
				LogIndex:          *le.LogIndex,
				LogId:             &protocommon.LogId{KeyId: logID},
				KindVersion:       &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.1"},
				IntegratedTime:    *le.IntegratedTime,
				InclusionPromise:  &protorekor.InclusionPromise{SignedEntryTimestamp: v.SignedEntryTimestamp},
				CanonicalizedBody: body,
				InclusionProof: &protorekor.InclusionProof{
					LogIndex:   *v.InclusionProof.LogIndex,
					RootHash:   rootHash,
					TreeSize:   *v.InclusionProof.TreeSize,
					Hashes:     hashes,
					Checkpoint: &protorekor.Checkpoint{Envelope: *v.InclusionProof.Checkpoint},
				},
			}},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: digest},
			Signature:     sig,
		}},
	}
	return protojson.Marshal(pb)
}

// fulcioCertificate asks Fulcio's v2 API for a code-signing certificate for
// key, proving possession by signing the token's subject, and returns the
// leaf's DER. The chain Fulcio returns above the leaf is not kept: the
// bundle carries the leaf alone and the trust root holds the chain.
func fulcioCertificate(ctx context.Context, client *http.Client, fulcioURL string, key *ecdsa.PrivateKey, token string) ([]byte, error) {
	subject, err := tokenSubject(token)
	if err != nil {
		return nil, err
	}
	subjectDigest := sha256.Sum256([]byte(subject))
	pop, err := ecdsa.SignASN1(rand.Reader, key, subjectDigest[:])
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(map[string]any{"publicKeyRequest": map[string]any{
		"publicKey":         map[string]any{"algorithm": "ECDSA", "content": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))},
		"proofOfPossession": base64.StdEncoding.EncodeToString(pop),
	}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(fulcioURL, "/")+"/api/v2/signingCert", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req) // #nosec G107 G704 -- the Fulcio URL is a constant (or a test's server)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %s: %s", resp.Status, responseExcerpt(body, token))
	}
	type chain struct {
		Chain struct {
			Certificates []string `json:"certificates"`
		} `json:"chain"`
	}
	var out struct {
		Embedded *chain `json:"signedCertificateEmbeddedSct"`
		Detached *chain `json:"signedCertificateDetachedSct"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("response is not JSON: %w", err)
	}
	var certs []string
	switch {
	case out.Embedded != nil:
		certs = out.Embedded.Chain.Certificates
	case out.Detached != nil:
		certs = out.Detached.Chain.Certificates
	}
	if len(certs) == 0 {
		return nil, errors.New("returned no certificate")
	}
	blk, _ := pem.Decode([]byte(certs[0]))
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("returned a certificate that is not PEM")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("returned a certificate that does not parse: %w", err)
	}
	// Fulcio must have certified THIS key; anything else could not sign.
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("returned a certificate for another key")
	}
	return blk.Bytes, nil
}

// responseExcerpt is at most maxExcerpt bytes of a refusal's body, for the
// operator to read why: control characters dropped so it stays one line,
// and the token removed should a server ever echo it (it is sent only in a
// header, so Fulcio has no reason to).
func responseExcerpt(body []byte, token string) string {
	const maxExcerpt = 512
	s := string(body)
	if token != "" {
		s = strings.ReplaceAll(s, token, "[token]")
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > maxExcerpt {
		s = strings.ToValidUTF8(s[:maxExcerpt], "") + "…"
	}
	return s
}

// tokenSubject is the identity a Fulcio proof of possession signs: the
// token's verified email if it carries one, else its subject — as
// sigstore's oauthflow.SubjectFromUnverifiedToken reads it. The token is not
// verified here; Fulcio verifies it.
func tokenSubject(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", errors.New("the OIDC token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("the OIDC token's payload is not base64url")
	}
	var c struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Subject  string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", errors.New("the OIDC token's payload is not JSON")
	}
	switch {
	case c.Email != "" && !c.Verified:
		return "", errors.New("the OIDC token's email is not verified by its issuer")
	case c.Email != "":
		return c.Email, nil
	case c.Subject == "":
		return "", errors.New("the OIDC token names no subject")
	}
	return c.Subject, nil
}

// fulcioSession holds one ephemeral key and its Fulcio certificate for as
// long as the certificate has certReuseMargin left, then issues a fresh
// pair. Shared by every copy of the signer that holds it.
type fulcioSession struct {
	mu       sync.Mutex
	issue    func(ctx context.Context) (*ecdsa.PrivateKey, []byte, error)
	key      *ecdsa.PrivateKey
	der      []byte
	notAfter time.Time
}

func (f *fulcioSession) get(ctx context.Context, now time.Time) (*ecdsa.PrivateKey, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.der != nil && now.Add(certReuseMargin).Before(f.notAfter) {
		return f.key, f.der, nil
	}
	key, der, err := f.issue(ctx)
	if err != nil {
		return nil, nil, err
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("fulcio returned a certificate that does not parse: %w", err)
	}
	f.key, f.der, f.notAfter = key, der, c.NotAfter
	return key, der, nil
}

// githubOIDCToken asks the Actions token service for an ID token with the
// sigstore audience. The request bearer is a secret and never appears in an
// error.
func githubOIDCToken(ctx context.Context, client *http.Client, reqURL, reqToken string) (string, error) {
	u, err := url.Parse(reqURL)
	if err != nil {
		return "", errors.New("ACTIONS_ID_TOKEN_REQUEST_URL is not a URL")
	}
	// The request bearer is a credential; it goes over TLS or not at all.
	if u.Scheme != "https" {
		return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL is not https (scheme %q) — the request token is never sent in the clear", u.Scheme)
	}
	q := u.Query()
	q.Set("audience", sigstoreOIDCAudience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+reqToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req) // #nosec G107 G704 -- the URL is the one GitHub Actions gave this run
	if err != nil {
		return "", fmt.Errorf("requesting the workflow's OIDC token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("reading the workflow's OIDC token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the Actions token service answered %s", resp.Status)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Value == "" {
		return "", errors.New("the Actions token service returned no token")
	}
	return out.Value, nil
}
