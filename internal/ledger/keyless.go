// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Keyless entries (corral-ledger-6) are signed through Sigstore with the
// GitHub Actions run's own OIDC identity instead of a key someone holds, and
// verified by the REPOSITORY that signed them. This file is the verifier:
// ONE implementation, standard library only, run by the CLI and, compiled to
// WASM, by the browser.
//
// It is ours rather than sigstore-go's because sigstore-go cannot be in the
// browser build: pkg/root pulls os/exec (digitorus/pkcs7) and pkg/tlog pulls
// in-toto-golang, which does not compile for GOOS=js (task-7 report). Writing
// a verifier is a large surface, so it accepts exactly ONE bundle shape —
// what sigstore-go v1.2.2's sign.Bundle emits with a Fulcio certificate and
// public-good Rekor (v1) — and refuses everything else by name. sigstore-go
// stays in the tests as the oracle: every check below is exercised against
// it in keyless_test.go.
//
// Signed certificate timestamps (CT) are not checked: the addendum's checks
// do not include them, and the property they add — that Fulcio publicly
// logged the certificate — is not what a ledger entry rests on. The
// certificate already chains to the trust root (check 1), and Rekor binds
// these exact certificate bytes into its signed log entry (checks 4–6).

// KeyIDSigstore is the keyid of a keyless entry, and Format6 its format;
// each implies the other.
const KeyIDSigstore = "sigstore"

// GitHubActionsIssuer is the OIDC issuer a keyless entry's certificate must
// name: the token came from a GitHub Actions run.
const GitHubActionsIssuer = "https://token.actions.githubusercontent.com"

// ProblemNoLedgerRepo is said when a keyless entry is read with no repository
// named. The repository is never guessed — not from the scan row, not from
// the certificate: the certificate saying who signed is exactly what is being
// checked.
const ProblemNoLedgerRepo = "keyless entry: the ledger's repository is not named (pass --ledger-repo owner/name)"

const bundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// KeylessArtifact is the bytes a keyless entry's signature covers: its Hash,
// hex-decoded. The signer signs exactly this and the verifier checks exactly
// this, so the two cannot disagree about what was signed.
func KeylessArtifact(e Entry) ([]byte, error) {
	b, err := hex.DecodeString(e.Hash)
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("ledger: entry hash %q is not %d bytes of hex", e.Hash, sha256.Size)
	}
	return b, nil
}

// keylessProblem is the keyless half of Verify for one entry: "" when e is a
// keyless entry whose bundle verifies for t.Repo, and signedBy is the
// repository URI from its certificate. It is also where the format/keyid
// pairing is enforced, for every entry: v6 ⇔ keyid sigstore, and only a
// keyless entry may carry a bundle.
func keylessProblem(e Entry, t Trust, integrityOnly bool) (problem, signedBy string) {
	keyless := e.KeyID == KeyIDSigstore
	switch {
	case !keyless && e.Format == Format6:
		return fmt.Sprintf("%s is the keyless format; its keyid must be %q, not %q", Format6, KeyIDSigstore, e.KeyID), ""
	case !keyless && len(e.Sigstore) > 0:
		return fmt.Sprintf("carries a sigstore bundle but is not a keyless entry (keyid %q)", e.KeyID), ""
	case !keyless:
		return "", ""
	case integrityOnly:
		// VerifyIntegrity: no signature is checked; the shape rules are.
		return keylessShapeProblem(e), ""
	case t.Repo == "":
		// First, before any other check: the operator must name what to
		// expect, and saying so is the most useful thing to read.
		return ProblemNoLedgerRepo, ""
	}
	if p := keylessShapeProblem(e); p != "" {
		return p, ""
	}
	art, err := KeylessArtifact(e)
	if err != nil {
		return "keyless entry: " + err.Error(), ""
	}
	if err := VerifyKeylessSignature(e.Sigstore, art, t.Repo, t.SigstoreRoot); err != nil {
		return "keyless entry: " + err.Error(), ""
	}
	return "", githubURL(t.Repo)
}

// keylessShapeProblem is the part of a keyless entry's check that needs no
// trust: its format, its kind, and that it carries a bundle and only a
// bundle. VerifyIntegrity runs exactly this.
func keylessShapeProblem(e Entry) string {
	switch {
	case e.Format != Format6:
		return fmt.Sprintf("keyless entry: keyid %q needs format %s, not %s", KeyIDSigstore, Format6, e.Format)
	case e.Kind == KindSigner || e.Kind == KindCheckpoint:
		// Who may sign, and where a chain begins, stay with the root key: a
		// keyless signer proves a repository, not authority over the chain.
		return "keyless entry: a signer change or checkpoint must be signed by the ledger's root key"
	case e.Signature != "":
		return "keyless entry: carries an ed25519 signature as well as a sigstore bundle"
	case len(e.Sigstore) == 0:
		return "keyless entry: carries no sigstore bundle"
	}
	return ""
}

// VerifyKeylessSignature is the bundle check on its own: bundleJSON is a
// Sigstore signature over artifact, made in a GitHub Actions run of
// github.com/<repo>, witnessed by a Rekor log in r. Verify runs it for every
// keyless entry; the keyless SIGNER runs it on its own output before an
// entry is placed, so an entry its readers would refuse is never written.
// Neither repo nor r has a default.
func VerifyKeylessSignature(bundleJSON, artifact []byte, repo string, r *SigstoreRoot) error {
	switch {
	case repo == "":
		return errors.New(ProblemNoLedgerRepo)
	case r == nil:
		return errors.New("no Sigstore trust root was given to check it against")
	}
	return verifyBundle(bundleJSON, artifact, repo, r)
}

func githubURL(repo string) string { return "https://github.com/" + repo }

// pbInt64 is a protobuf-JSON int64: a quoted decimal, which is how protojson
// writes every int64.
type pbInt64 int64

func (p *pbInt64) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("int64 must be a quoted decimal: %s", b)
	}
	// Canonical digits only ("7", not "07" or "+7"), so one value has one
	// spelling in the signed bytes. The round trip is the whole check:
	// ParseInt's error cases return 0 or ±MaxInt64, whose canonical
	// spelling differs from any input that failed to parse, so its error
	// adds nothing (a mutation pass confirmed the branch was unreachable).
	n, _ := strconv.ParseInt(s, 10, 64)
	if strconv.FormatInt(n, 10) != s {
		return fmt.Errorf("int64 must be a canonical decimal: %s", b)
	}
	*p = pbInt64(n)
	return nil
}

// sigstoreBundle is the ONE shape accepted. It is decoded only after
// strictjson.go's bundleShape walk, so a DSSE envelope, a certificate chain,
// a public key or a timestamp block fails naming the field, and a key in
// another case or a repeated key cannot change what is read.
type sigstoreBundle struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		Certificate *struct {
			RawBytes []byte `json:"rawBytes"`
		} `json:"certificate"`
		TlogEntries []tlogEntry `json:"tlogEntries"`
	} `json:"verificationMaterial"`
	MessageSignature *struct {
		MessageDigest struct {
			Algorithm string `json:"algorithm"`
			Digest    []byte `json:"digest"`
		} `json:"messageDigest"`
		Signature []byte `json:"signature"`
	} `json:"messageSignature"`
}

type tlogEntry struct {
	LogIndex pbInt64 `json:"logIndex"`
	LogID    struct {
		KeyID []byte `json:"keyId"`
	} `json:"logId"`
	KindVersion struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"kindVersion"`
	IntegratedTime   pbInt64 `json:"integratedTime"`
	InclusionPromise *struct {
		SignedEntryTimestamp []byte `json:"signedEntryTimestamp"`
	} `json:"inclusionPromise"`
	InclusionProof *struct {
		LogIndex   pbInt64  `json:"logIndex"`
		RootHash   []byte   `json:"rootHash"`
		TreeSize   pbInt64  `json:"treeSize"`
		Hashes     [][]byte `json:"hashes"`
		Checkpoint struct {
			Envelope string `json:"envelope"`
		} `json:"checkpoint"`
	} `json:"inclusionProof"`
	CanonicalizedBody []byte `json:"canonicalizedBody"`
}

// hashedRekord is a Rekor v1 hashedrekord v0.0.1 entry body.
type hashedRekord struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Data struct {
			Hash struct {
				Algorithm string `json:"algorithm"`
				Value     string `json:"value"`
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

// maxJSONInt is the largest integer RFC 8785 (JCS) writes as plain digits
// without loss; Rekor's SET is signed over JCS output.
const maxJSONInt = 1<<53 - 1

// verifyBundle checks that bundleJSON is a Sigstore signature over artifact,
// made in a GitHub Actions run of github.com/<repo>, witnessed by a Rekor log
// in r. Every check is required; the first failure is returned.
func verifyBundle(bundleJSON, artifact []byte, repo string, r *SigstoreRoot) error {
	var b sigstoreBundle
	if err := strictDecode(bundleJSON, bundleShape, &b); err != nil {
		return fmt.Errorf("the sigstore bundle is not the one shape corral accepts: %w", err)
	}
	vm := b.VerificationMaterial
	switch {
	case b.MediaType != bundleMediaType:
		return fmt.Errorf("bundle media type %q, want %q", b.MediaType, bundleMediaType)
	case vm.Certificate == nil || len(vm.Certificate.RawBytes) == 0:
		return errors.New("bundle carries no signing certificate")
	case b.MessageSignature == nil:
		return errors.New("bundle carries no message signature")
	case len(vm.TlogEntries) != 1:
		// Exactly one: a second entry is either a duplicate (which could
		// stand in for a threshold) or a second witness this shape does not
		// define.
		return fmt.Errorf("bundle carries %d transparency-log entries, want exactly 1", len(vm.TlogEntries))
	}
	tl := vm.TlogEntries[0]
	switch {
	case tl.KindVersion.Kind != "hashedrekord" || tl.KindVersion.Version != "0.0.1":
		return fmt.Errorf("log entry is %s %s, want hashedrekord 0.0.1", tl.KindVersion.Kind, tl.KindVersion.Version)
	case tl.InclusionPromise == nil || len(tl.InclusionPromise.SignedEntryTimestamp) == 0:
		return errors.New("log entry carries no signed entry timestamp")
	case tl.InclusionProof == nil:
		return errors.New("log entry carries no inclusion proof")
	case tl.IntegratedTime <= 0 || tl.IntegratedTime > maxJSONInt || tl.LogIndex < 0 || tl.LogIndex > maxJSONInt:
		return fmt.Errorf("log entry integrated time %d or index %d is out of range", tl.IntegratedTime, tl.LogIndex)
	}
	at := time.Unix(int64(tl.IntegratedTime), 0).UTC()

	// 1. The certificate was issued by a Fulcio authority the root trusts,
	// and everything in its chain was valid at the moment the log recorded
	// the signature. Defends against a self-made certificate claiming any
	// identity, and against a certificate used outside its ten minutes.
	leaf, err := x509.ParseCertificate(vm.Certificate.RawBytes)
	if err != nil {
		return fmt.Errorf("signing certificate: %w", err)
	}
	if at.Before(leaf.NotBefore) || at.After(leaf.NotAfter) {
		return fmt.Errorf("signing certificate is not valid at the integrated time %s", at.Format(time.RFC3339))
	}
	// x509.Verify's KeyUsages accepts a leaf with NO extended key usage at
	// all, so code signing is required of the leaf itself: a certificate
	// Fulcio issued for some other purpose must not sign a ledger. (Stricter
	// than sigstore-go, which relies on Verify alone.)
	if !hasEKU(leaf, x509.ExtKeyUsageCodeSigning) {
		return errors.New("signing certificate is not for code signing")
	}
	if !chainsToFulcio(leaf, at, r) {
		return fmt.Errorf("signing certificate does not chain to a Fulcio authority in the Sigstore trust root at %s", at.Format(time.RFC3339))
	}

	// 2. The identity: the token came from GitHub Actions, for exactly this
	// repository. Defends against a valid Sigstore signature by anyone else
	// — another repository, or a person's own account.
	issuer, sourceRepo, err := fulcioIdentity(leaf)
	if err != nil {
		return err
	}
	if issuer != GitHubActionsIssuer {
		return fmt.Errorf("signed with a token from %q, not GitHub Actions (%s)", issuer, GitHubActionsIssuer)
	}
	if want := githubURL(repo); sourceRepo != want {
		return fmt.Errorf("signed from %s, not %s (the ledger's repository)", sourceRepo, want)
	}

	// 3. The signature is over THIS entry. Defends against a genuine bundle
	// lifted from another entry or artifact.
	ms := b.MessageSignature
	digest := sha256.Sum256(artifact)
	if ms.MessageDigest.Algorithm != "SHA2_256" || !bytes.Equal(ms.MessageDigest.Digest, digest[:]) {
		return errors.New("the bundle's message digest is not sha256 of this entry's hash")
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || (pub.Curve != elliptic.P256() && pub.Curve != elliptic.P384()) {
		return errors.New("signing certificate's key is not ECDSA P-256 or P-384")
	}
	if !ecdsa.VerifyASN1(pub, digest[:], ms.Signature) {
		return errors.New("signature does not verify under the signing certificate")
	}

	// 4. What the log recorded is this signature, this certificate, this
	// digest. Without it the log's promise would vouch for some other entry
	// while the bundle presented this one.
	var body hashedRekord
	if err := strictDecode(tl.CanonicalizedBody, hashedRekordShape, &body); err != nil {
		return fmt.Errorf("log entry body: %w", err)
	}
	if body.Kind != "hashedrekord" || body.APIVersion != "0.0.1" {
		return fmt.Errorf("log entry body is %s %s, want hashedrekord 0.0.1", body.Kind, body.APIVersion)
	}
	if body.Spec.Data.Hash.Algorithm != "sha256" || body.Spec.Data.Hash.Value != hex.EncodeToString(digest[:]) {
		return errors.New("the log entry records a different artifact digest")
	}
	if !bytes.Equal(body.Spec.Signature.Content, ms.Signature) {
		return errors.New("the log entry records a different signature")
	}
	if blk, rest := pem.Decode(body.Spec.Signature.PublicKey.Content); blk == nil || blk.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(blk.Bytes, leaf.Raw) {
		return errors.New("the log entry records a different certificate")
	}

	// 5. The log promised to include the entry at that time: its signed
	// entry timestamp, under a log key the root trusts at that time. This is
	// what makes the integrated time — used in check 1 — trustworthy.
	logID := hex.EncodeToString(tl.LogID.KeyID)
	lg, ok := r.logs[logID]
	switch {
	case !ok:
		return fmt.Errorf("log %s is not in the Sigstore trust root", logID)
	case lg.key == nil:
		return fmt.Errorf("log %s has a %s key; only ECDSA logs are supported", logID, lg.keyDetails)
	case !covers(lg.start, lg.end, at):
		return fmt.Errorf("log %s's key is not valid at %s", logID, at.Format(time.RFC3339))
	}
	// The payload is RFC 8785 canonical JSON of {body, integratedTime,
	// logID, logIndex}: keys in that (sorted) order, no whitespace, integers
	// as plain digits (range-checked above), and strings that are base64 or
	// hex, so nothing needs escaping. Confirmed against rekor v1.5.3
	// pkg/verify VerifySignedEntryTimestamp and sigstore-go v1.2.2 pkg/tlog
	// VerifySET, both of which marshal that struct and run jsoncanonicalizer.
	// The index is the GLOBAL one (tl.LogIndex), as both of them use.
	payload := fmt.Sprintf(`{"body":"%s","integratedTime":%d,"logID":"%s","logIndex":%d}`,
		base64.StdEncoding.EncodeToString(tl.CanonicalizedBody), int64(tl.IntegratedTime), logID, int64(tl.LogIndex))
	setDigest := sha256.Sum256([]byte(payload))
	if !ecdsa.VerifyASN1(lg.key, setDigest[:], tl.InclusionPromise.SignedEntryTimestamp) {
		return errors.New("the log's signed entry timestamp does not verify")
	}

	// 6. The entry is in the log's Merkle tree, and the log signed that
	// tree. The promise alone is a log saying "I will"; the proof shows it
	// did, so a log that signed and then dropped the entry is caught.
	p := tl.InclusionProof
	leafHash := sha256.Sum256(append([]byte{0}, tl.CanonicalizedBody...))
	if err := verifyInclusion(int64(p.LogIndex), int64(p.TreeSize), leafHash[:], p.Hashes, p.RootHash); err != nil {
		return err
	}
	return verifyCheckpoint(p.Checkpoint.Envelope, int64(p.TreeSize), p.RootHash, lg.key)
}

func hasEKU(c *x509.Certificate, want x509.ExtKeyUsage) bool {
	for _, u := range c.ExtKeyUsage {
		if u == want {
			return true
		}
	}
	return false
}

// chainsToFulcio reports whether leaf chains, as a code-signing certificate,
// to some Fulcio authority in r whose validity holds at, with every
// certificate in the chain checked at that moment (the "hybrid model" the
// Sigstore spec names).
func chainsToFulcio(leaf *x509.Certificate, at time.Time, r *SigstoreRoot) bool {
	for _, ca := range r.cas {
		if !covers(ca.start, ca.end, at) {
			continue
		}
		roots, inter := x509.NewCertPool(), x509.NewCertPool()
		roots.AddCert(ca.root)
		for _, c := range ca.intermediates {
			inter.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: inter, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		}); err == nil {
			return true
		}
	}
	return false
}

var (
	oidIssuerV1      = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuerV2      = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidSourceRepoURI = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
)

// fulcioIdentity reads the OIDC issuer (1.8, a DER string; or, on an older
// certificate without it, 1.1, raw bytes) and SourceRepositoryURI (1.12)
// from a Fulcio certificate, as sigstore-go's fulcio/certificate
// ParseExtensions does. Both issuer forms present and disagreeing is refused
// rather than resolved by order.
func fulcioIdentity(c *x509.Certificate) (issuer, sourceRepo string, err error) {
	var v1, v2 string
	var hasV1, hasV2 bool
	for _, e := range c.Extensions {
		switch {
		case e.Id.Equal(oidIssuerV1):
			v1, hasV1 = string(e.Value), true
		case e.Id.Equal(oidIssuerV2):
			if v2, err = derString(e.Value); err != nil {
				return "", "", fmt.Errorf("certificate issuer extension: %w", err)
			}
			hasV2 = true
		case e.Id.Equal(oidSourceRepoURI):
			if sourceRepo, err = derString(e.Value); err != nil {
				return "", "", fmt.Errorf("certificate source repository extension: %w", err)
			}
		}
	}
	switch {
	case hasV1 && hasV2 && v1 != v2:
		return "", "", fmt.Errorf("certificate names two issuers, %q and %q", v1, v2)
	case hasV2:
		issuer = v2
	case hasV1:
		issuer = v1
	default:
		return "", "", errors.New("certificate names no OIDC issuer")
	}
	if sourceRepo == "" {
		return "", "", errors.New("certificate names no source repository")
	}
	return issuer, sourceRepo, nil
}

func derString(b []byte) (string, error) {
	var s string
	rest, err := asn1.Unmarshal(b, &s)
	if err != nil {
		return "", err
	}
	if len(rest) != 0 {
		return "", errors.New("trailing bytes after the DER string")
	}
	return s, nil
}

// verifyInclusion checks an RFC 6962 inclusion proof for the leaf at index in
// a tree of size, as transparency-dev/merkle proof.VerifyInclusion does.
// index is the proof's own (tree-local) index, which on Rekor's sharded log
// differs from the entry's global logIndex; sigstore-go v1.2.2 pkg/tlog
// VerifyInclusion passes the proof's index, and so does this.
func verifyInclusion(index, size int64, leaf []byte, proof [][]byte, root []byte) error {
	if index < 0 || index >= size { // also refuses size <= 0
		return fmt.Errorf("inclusion proof index %d is outside a tree of %d", index, size)
	}
	if len(root) != sha256.Size {
		return errors.New("inclusion proof root hash is not 32 bytes")
	}
	idx, last := uint64(index), uint64(size-1) // #nosec G115 -- 0 <= index < size was checked just above, so both are non-negative
	inner := bits.Len64(idx ^ last)
	border := bits.OnesCount64(idx >> uint(inner))
	if len(proof) != inner+border {
		return fmt.Errorf("inclusion proof has %d hashes, want %d", len(proof), inner+border)
	}
	node := func(l, r []byte) []byte {
		h := sha256.Sum256(append(append([]byte{1}, l...), r...))
		return h[:]
	}
	h := leaf
	for i, p := range proof {
		if len(p) != sha256.Size {
			return errors.New("inclusion proof hash is not 32 bytes")
		}
		switch {
		case i >= inner:
			h = node(p, h) // the right border: always a left sibling
		case (idx>>uint(i))&1 == 0:
			h = node(h, p)
		default:
			h = node(p, h)
		}
	}
	if !bytes.Equal(h, root) {
		return errors.New("inclusion proof does not lead to the log's root hash")
	}
	return nil
}

// rekorV1Origin is a Rekor v1 checkpoint's first line: "<host> - <tree id>".
// sigstore-go verifies a checkpoint as Rekor v1's only when it matches this
// (hasRekorV1STH); any other is Rekor v2's, which this shape does not take.
var rekorV1Origin = regexp.MustCompile(`^.+ - [0-9]+$`)

// verifyCheckpoint checks a Rekor v1 signed checkpoint: a signed note whose
// text names the tree size and root hash the proof used, signed by the log.
// The format follows rekor v1.5.3 pkg/util SignedNote: the text runs to the
// blank line; each signature line is "— <name> <base64(4-byte key hint ||
// ASN.1 ECDSA signature)>", the hint being the first four bytes of
// sha256(PKIX DER of the log key), the signature over sha256(text). As
// there, every signature line must be the log's and verify.
func verifyCheckpoint(envelope string, size int64, root []byte, key *ecdsa.PublicKey) error {
	i := strings.LastIndex(envelope, "\n\n")
	if i < 0 || !strings.HasSuffix(envelope, "\n") {
		return errors.New("checkpoint is not a signed note")
	}
	text, sigs := envelope[:i+1], envelope[i+2:]
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 || !rekorV1Origin.MatchString(lines[0]) {
		return errors.New("checkpoint is not a Rekor v1 checkpoint")
	}
	if lines[1] != strconv.FormatInt(size, 10) {
		return fmt.Errorf("checkpoint names tree size %s, the proof %d", lines[1], size)
	}
	if lines[2] != base64.StdEncoding.EncodeToString(root) {
		return errors.New("checkpoint names a different root hash than the proof")
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return err
	}
	keySum := sha256.Sum256(der)
	hint := binary.BigEndian.Uint32(keySum[:4])
	textSum := sha256.Sum256([]byte(text))
	if sigs == "" {
		return errors.New("checkpoint carries no signature")
	}
	// The signer NAME on a line is deliberately not compared to the
	// origin's host, as rekor does not: the key hint and the signature
	// under the log's key are what bind the note to the log.
	for _, line := range strings.Split(strings.TrimSuffix(sigs, "\n"), "\n") {
		f := strings.Fields(strings.TrimPrefix(line, "— "))
		if !strings.HasPrefix(line, "— ") || len(f) != 2 {
			return errors.New("checkpoint signature line is malformed")
		}
		raw, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(raw) < 5 {
			return errors.New("checkpoint signature is malformed")
		}
		if binary.BigEndian.Uint32(raw[:4]) != hint || !ecdsa.VerifyASN1(key, textSum[:], raw[4:]) {
			return errors.New("checkpoint signature does not verify under the log's key")
		}
	}
	return nil
}
