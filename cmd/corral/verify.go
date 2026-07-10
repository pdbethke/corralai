// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// defaultRekorURL is the public Sigstore Rekor instance used when neither
// --rekor-url nor $CORRALAI_REKOR_URL is given — the same default the brain
// wires up in main.go, so an unqualified `corral certify verify` checks the
// same log a default-configured brain anchors to.
const defaultRekorURL = "https://rekor.sigstore.dev"

// certRecord is the shape a `corral certify --out` file (and report_build's
// tool response) carries: everything needed to verify a build attestation
// completely offline except, optionally, the public key. Signature is a DSSE
// envelope (JSON, as text) that embeds its own copy of the signed statement;
// Statement is kept alongside it purely for human readability — verification
// checks the envelope's embedded statement, never this field.
type certRecord struct {
	Statement map[string]any   `json:"statement"`
	Signature string           `json:"signature"`
	Steps     []map[string]any `json:"steps"`
	Head      string           `json:"head"`
	PublicKey string           `json:"public_key,omitempty"`
	// Rekor is the marshaled transparency.Entry (JSON), present when
	// Anchored is true — the inclusion-proof evidence verify checks against
	// the TUF-rooted Rekor public key, entirely offline.
	Rekor string `json:"rekor,omitempty"`
	// Anchored reports whether Signature was submitted to and included in a
	// public transparency log. false means "signed but not (yet, or ever)
	// publicly witnessed" — a materially weaker claim; verify treats it as a
	// failure unless the operator explicitly opts in with --allow-unanchored.
	Anchored bool `json:"anchored,omitempty"`
}

// pubkeyFetcher fetches a brain's certify public key. The real
// implementation (httpPubkeyFetcher) GETs <brain>/api/certify/pubkey; tests
// inject a fake so no network call is needed to exercise runCertifyVerify.
type pubkeyFetcher func(brainURL string) (string, error)

// httpPubkeyFetcher is pubkeyFetcher backed by a real GET to
// <brain>/api/certify/pubkey (see internal/brain.CertifyPubkeyHandler).
func httpPubkeyFetcher(brainURL string) (string, error) {
	endpoint := strings.TrimRight(brainURL, "/") + "/api/certify/pubkey"
	hc := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching pubkey from %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching pubkey from %s: status %d", endpoint, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading pubkey response: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// witnessFactory builds a transparency.Witness to verify a record's Rekor
// inclusion proof against, given the rekor instance URL. The real
// implementation (newRekorVerifyWitness) fetches the Sigstore TUF trust
// root and talks to Rekor; tests inject one that returns
// transparency.NewFakeWitness() so no network call is needed to exercise
// runCertifyVerify's Rekor step.
type witnessFactory func(rekorURL string) (transparency.Witness, error)

// newRekorVerifyWitness is the real witnessFactory: it constructs a
// transparency.Witness whose VerifyInclusion checks entirely offline against
// the TUF-rooted Rekor public key. Verification never needs the signer's
// public key (only Anchor does), so no WithSignerPublicKey option is passed
// here — the trust anchor for the Rekor step is the TUF root fetched inside
// NewRekorWitness, never anything from the record.
func newRekorVerifyWitness(rekorURL string) (transparency.Witness, error) {
	return transparency.NewRekorWitness(rekorURL)
}

// runCertifyVerify implements `corral certify verify <record-file>
// [--pubkey <hex>|--brain <url>]`: it independently verifies a corral
// certify record — no trust in the brain's in-process state, and no trust
// in the record itself for the one thing a record must never be trusted
// for: which key to check it against. The verifying public key MUST come
// from an out-of-band source — an operator-supplied hex key or a GET to
// the brain's published pubkey endpoint. A record's own embedded
// public_key is never used as the trust anchor: a forger can rewrite the
// statement, resign it with a key of their own choosing, and stamp that
// same key into public_key, and every check would trivially pass — the
// record would be "verified" against itself. If neither --pubkey nor
// --brain is given, verification refuses outright. All of (1) the Ed25519
// signature over the canonical statement, (2) the ledger's hash chain +
// head, (3) the statement's subject digest binding to that exact head, and
// (4) — when the record claims to be anchored — the Rekor inclusion proof,
// must pass; the first failing check is named on stderr and the exit code
// is non-zero. An unanchored record ("signed" but never publicly witnessed)
// is ALSO a failure by default: --allow-unanchored is required to accept it,
// so a caller can't mistake corral's own signature for third-party proof.
func runCertifyVerify(args []string, fetch pubkeyFetcher, newWitness witnessFactory, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("certify verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pubkeyFlag := fs.String("pubkey", "", "hex-encoded Ed25519 public key to verify against")
	brainFlag := fs.String("brain", "", "fetch the public key from this brain's /api/certify/pubkey")
	rekorURLFlag := fs.String("rekor-url", "", "Rekor instance to verify the inclusion proof against (default $CORRALAI_REKOR_URL or "+defaultRekorURL+")")
	allowUnanchored := fs.Bool("allow-unanchored", false, "accept a signed-but-not-publicly-witnessed record (weaker: no third-party transparency guarantee)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "corral certify verify: usage: corral certify verify <record-file> [--pubkey <hex>|--brain <url>]")
		return 2
	}
	recordPath := rest[0]

	raw, err := os.ReadFile(recordPath) // #nosec G304 -- operator-supplied record path, the entire point of this command
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: reading %s: %v\n", recordPath, err)
		return 1
	}
	var rec certRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		fmt.Fprintf(stderr, "corral certify verify: %s is not a valid record: %v\n", recordPath, err)
		return 1
	}

	pubHex := strings.TrimSpace(*pubkeyFlag)
	if pubHex == "" && strings.TrimSpace(*brainFlag) != "" {
		v, err := fetch(*brainFlag)
		if err != nil {
			fmt.Fprintf(stderr, "corral certify verify: %v\n", err)
			return 1
		}
		pubHex = v
	}
	if pubHex == "" {
		// A record cannot authenticate itself: the verifying key must come
		// from an external, out-of-band source. Falling back to
		// rec.PublicKey here would let a forger rewrite the record, sign it
		// under a key of their own, stamp that same key into public_key,
		// and have every check pass — "verified" against nothing but itself.
		fmt.Fprintln(stderr, "corral certify verify: verify requires a trusted public key: pass --pubkey <hex> or --brain <url> — the public_key embedded in a record cannot be used to authenticate the record itself")
		return 2
	}

	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		fmt.Fprintln(stderr, "corral certify verify: FAILED (malformed public key)")
		return 1
	}
	pub := ed25519.PublicKey(pubBytes)

	// Cross-check: the record's own embedded public_key is at most a
	// hint about who signed it, never the trust anchor. If it disagrees
	// with the externally-supplied key, that's worth a loud warning — the
	// record is claiming a different signer than the one we're trusting —
	// but verification still proceeds against the EXTERNAL key only.
	if recPub := strings.TrimSpace(rec.PublicKey); recPub != "" && !strings.EqualFold(recPub, pubHex) {
		fmt.Fprintln(stderr, "corral certify verify: warning: record's embedded public_key does not match the trusted key — the record claims a different signer")
	}

	// Check 1: the DSSE envelope's Ed25519 signature — binds the FULL
	// predicate (repo/commit/command/exit code), not just the head. The
	// envelope (rec.Signature) carries its own embedded copy of the
	// statement it signed; that embedded copy — not rec.Statement, which is
	// kept only for human readability — is what checks 2 and 3 below verify
	// against, so a forger can't get a pass by leaving the envelope alone
	// and editing the record's separate "statement" field.
	envelopeStmt, ok, err := certify.VerifyDSSE([]byte(rec.Signature), pub)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (signature: %v)\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "corral certify verify: FAILED (signature does not verify against the statement)")
		return 1
	}

	// Check 2: the ledger's hash chain recomputes to the recorded head.
	stepsJSON, err := json.Marshal(rec.Steps)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (steps: %v)\n", err)
		return 1
	}
	steps, err := certify.UnmarshalSteps(stepsJSON)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (steps: %v)\n", err)
		return 1
	}
	if ok, msg := certify.VerifyLedger(steps, rec.Head); !ok {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (ledger: %s)\n", msg)
		return 1
	}

	// Check 3: the statement is bound to THIS exact ledger — its subject
	// digest must equal the ledger head, or a valid statement could be
	// paired with an unrelated (even individually valid) ledger. Checked
	// against envelopeStmt (the envelope's own embedded statement), the same
	// source of truth as check 1 — not rec.Statement.
	subjDigest, subjOK := statementSubjectDigest(envelopeStmt)
	if !subjOK || subjDigest != rec.Head {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (statement subject digest %q does not match ledger head %q)\n", subjDigest, rec.Head)
		return 1
	}

	// Check 4: public transparency. "Signed" (checks 1-3) is a claim about
	// what the brain says; "publicly witnessed" is an independently
	// checkable claim that a third party can confirm without trusting the
	// brain at all. A record that was never anchored is a materially weaker
	// artifact — trust corral's own signature and nothing else — so it is
	// rejected by default rather than silently passed off as equivalent.
	if !rec.Anchored {
		fmt.Fprintln(stderr, "corral certify verify: signed, NOT publicly witnessed (this build's attestation was never submitted to, or never included in, a public transparency log)")
		if !*allowUnanchored {
			fmt.Fprintln(stderr, "corral certify verify: FAILED (pass --allow-unanchored to accept a signed-but-unwitnessed record)")
			return 1
		}
		fmt.Fprintln(stderr, "corral certify verify: caveat: --allow-unanchored set — accepting signature-only trust, no third-party transparency guarantee")
		fmt.Fprintln(stdout, "verified (signed, NOT publicly witnessed)")
		return 0
	}

	var entry transparency.Entry
	if err := json.Unmarshal([]byte(rec.Rekor), &entry); err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (rekor: record's transparency entry is malformed: %v)\n", err)
		return 1
	}

	rekorURL := strings.TrimSpace(*rekorURLFlag)
	if rekorURL == "" {
		rekorURL = strings.TrimSpace(os.Getenv("CORRALAI_REKOR_URL"))
	}
	if rekorURL == "" {
		rekorURL = defaultRekorURL
	}
	witness, err := newWitness(rekorURL)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (rekor: constructing witness for %s: %v)\n", rekorURL, err)
		return 1
	}
	ok, detail := witness.VerifyInclusion(entry, []byte(rec.Signature))
	if !ok {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (rekor: %s)\n", detail)
		return 1
	}

	integrated := time.Unix(entry.IntegratedTime, 0).UTC().Format(time.RFC3339)
	fmt.Fprintf(stdout, "verified (publicly witnessed %s, Rekor #%d)\n", integrated, entry.LogIndex)
	return 0
}

// statementSubjectDigest extracts statement.subject[0].digest.sha256 from a
// decoded in-toto statement map.
func statementSubjectDigest(stmt map[string]any) (string, bool) {
	subjects, ok := stmt["subject"].([]any)
	if !ok || len(subjects) == 0 {
		return "", false
	}
	subj, ok := subjects[0].(map[string]any)
	if !ok {
		return "", false
	}
	digest, ok := subj["digest"].(map[string]any)
	if !ok {
		return "", false
	}
	sha, ok := digest["sha256"].(string)
	return sha, ok
}
