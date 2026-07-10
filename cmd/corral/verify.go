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
)

// certRecord is the shape a `corral certify --out` file (and report_build's
// tool response) carries: everything needed to verify a build attestation
// completely offline except, optionally, the public key.
type certRecord struct {
	Statement map[string]any   `json:"statement"`
	Signature string           `json:"signature"`
	Steps     []map[string]any `json:"steps"`
	Head      string           `json:"head"`
	PublicKey string           `json:"public_key,omitempty"`
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
// head, and (3) the statement's subject digest binding to that exact head
// must pass; the first failing check is named on stderr and the exit code
// is non-zero.
func runCertifyVerify(args []string, fetch pubkeyFetcher, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("certify verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pubkeyFlag := fs.String("pubkey", "", "hex-encoded Ed25519 public key to verify against")
	brainFlag := fs.String("brain", "", "fetch the public key from this brain's /api/certify/pubkey")
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

	// Check 1: the Ed25519 signature over the canonical statement — binds
	// the FULL predicate (repo/commit/command/exit code), not just the head.
	canonical, err := certify.CanonicalStatement(rec.Statement)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (statement: %v)\n", err)
		return 1
	}
	if !certify.VerifyStatement(canonical, rec.Signature, pub) {
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
	// paired with an unrelated (even individually valid) ledger.
	subjDigest, ok := statementSubjectDigest(rec.Statement)
	if !ok || subjDigest != rec.Head {
		fmt.Fprintf(stderr, "corral certify verify: FAILED (statement subject digest %q does not match ledger head %q)\n", subjDigest, rec.Head)
		return 1
	}

	fmt.Fprintln(stdout, "verified")
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
