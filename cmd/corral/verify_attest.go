// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
)

// runVerifyAttest is `corral verify` — the checker that ships with the
// claim `certify --repo --attest [--transparency]` makes. It is a
// DIFFERENT command from `corral certify verify` (verify.go,
// runCertifyVerify): that one checks a `corral certify` BUILD RECORD
// (brain-signed or locally-signed, DSSE + optional Witness anchoring);
// this one checks a `--repo --attest` AUDIT STATEMENT — a different
// artifact, a different signing key convention (CORRALAI_CERTIFY_KEY_FILE
// via writeSignedStatementEnvelope, keyid "corral-certify"), and a
// different, simpler transparency path (transparency.Logger, not
// transparency.Witness — see internal/transparency/doc.go).
//
// Three independent checks, each printing ✓/✗/· (not checked) plus one
// plain sentence, never panicking and never treating a missing OPTIONAL
// input as a failure:
//
//  1. SIGNATURE — always attempted: parse the DSSE envelope beside --attest
//     (or an envelope path given directly), verify its ed25519 signature
//     against --pub or the local certify key, and report who signed
//     (the envelope's own keyid) either way.
//  2. WAREHOUSE ROWS — only with --db: read the pushed rows back
//     (auditpush.ReadBundle) and recompute warehouseRowsSHA256 — THE SAME
//     function writeAuditStatement itself calls, never a second
//     implementation — comparing it to the statement's own claim.
//  3. REKOR INCLUSION — with --rekor-index, or (failing that) the index
//     the SAME --db warehouse row recorded for this scan: fetch the entry
//     via transparency.Logger.Get and confirm its logged envelope hash
//     matches the local envelope file.
//
// Exit 1 only when at least one check produced a ✗; exit 0 otherwise
// (every check either passed or was honestly "not checked"); exit 2 on a
// usage error (no --attest, or nothing at that path to parse at all).
func runVerifyAttest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	attestFlag := fs.String("attest", "", "the --attest statement to verify (required) — the plain JSON path (its signed envelope is expected at <path>.dsse.json) or the envelope itself")
	dbFlag := fs.String("db", "", "also recompute the warehouse rows' hash from this pushed DuckDB (a path, or md:<db> for MotherDuck) and compare it to the statement's claim. A VACUUMed or twice-pushed warehouse can change row order and trip a false ✗ here without tampering")
	rekorIndexFlag := fs.Int64("rekor-index", -1, "also confirm this Rekor log index's entry matches the envelope (default: read the index --db recorded for this scan, if --db was given)")
	pubFlag := fs.String("pub", "", "hex-encoded Ed25519 public key to verify the signature against (default: the local certify key, CORRALAI_CERTIFY_KEY_FILE)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "corral verify: unexpected argument(s) %v\n", fs.Args())
		return 2
	}
	if strings.TrimSpace(*attestFlag) == "" {
		fmt.Fprintln(stderr, "corral verify: --attest is required")
		return 2
	}

	envPath, everr := resolveEnvelopePath(*attestFlag)
	if everr != nil {
		fmt.Fprintf(stderr, "corral verify: %v\n", everr)
		return 2
	}
	envelope, rerr := os.ReadFile(envPath) // #nosec G304 -- envPath is derived from the operator's own --attest argument
	if rerr != nil {
		fmt.Fprintf(stderr, "corral verify: reading %s: %v\n", envPath, rerr)
		return 2
	}

	stmt, keyID, perr := decodeDSSEPayloadUnverified(envelope)
	if perr != nil {
		fmt.Fprintf(stderr, "corral verify: %s is not a valid DSSE envelope: %v\n", envPath, perr)
		return 2
	}

	failed := false

	// --- 1. Signature ---
	sigResult := verifySignature(envelope, keyID, *pubFlag)
	printCheckResult(stdout, "signature", sigResult)
	if sigResult.checked && !sigResult.ok {
		failed = true
	}

	// --- 2. Warehouse rows ---
	rowsResult := verifyWarehouseRows(*dbFlag, stmt)
	printCheckResult(stdout, "warehouse rows", rowsResult)
	if rowsResult.checked && !rowsResult.ok {
		failed = true
	}

	// --- 3. Rekor inclusion ---
	rekorResult := verifyRekorInclusion(context.Background(), envelope, stmt, *dbFlag, *rekorIndexFlag)
	printCheckResult(stdout, "rekor inclusion", rekorResult)
	if rekorResult.checked && !rekorResult.ok {
		failed = true
	}

	if failed {
		return 1
	}
	return 0
}

// verifyCheckResult is one of the three checks' outcome: checked=false means
// "not checked" (a missing OPTIONAL input, never a failure); checked=true
// carries ok (✓/✗) and detail (the one plain sentence printed either way).
type verifyCheckResult struct {
	checked bool
	ok      bool
	detail  string
}

func printCheckResult(w io.Writer, name string, r verifyCheckResult) {
	switch {
	case !r.checked:
		fmt.Fprintf(w, "· %s: not checked (%s)\n", name, r.detail)
	case r.ok:
		fmt.Fprintf(w, "✓ %s: %s\n", name, r.detail)
	default:
		fmt.Fprintf(w, "✗ %s: %s\n", name, r.detail)
	}
}

// resolveEnvelopePath accepts EITHER the plain --attest statement path (the
// common case: derive its sibling <path>.dsse.json, per
// writeSignedStatementEnvelope's convention) OR an envelope path given
// directly (detected by actually looking like DSSE JSON) — "the
// embedded/--pub key" language in the spec implies a caller may point
// --attest straight at an envelope it already has in hand.
func resolveEnvelopePath(attestPath string) (string, error) {
	if looksLikeDSSEEnvelope(attestPath) {
		return attestPath, nil
	}
	envPath := dsseEnvelopePathFor(attestPath)
	if _, err := os.Stat(envPath); err == nil {
		return envPath, nil
	}
	return "", fmt.Errorf("no signed DSSE envelope found — looked for %s (as an envelope directly) and %s (its derived sibling); sign one with `--attest --transparency` (or a local CORRALAI_CERTIFY_KEY_FILE), or point --attest directly at a .dsse.json file", attestPath, envPath)
}

// looksLikeDSSEEnvelope is a cheap, honest probe: a file is treated as an
// envelope only when it actually parses as one (non-empty payload AND
// payloadType), never by filename convention alone — a caller pointing
// --attest at some other JSON file gets a clear "not a valid envelope"
// error instead of a silently wrong read.
func looksLikeDSSEEnvelope(path string) bool {
	b, err := os.ReadFile(path) // #nosec G304 -- path is the operator's own --attest argument
	if err != nil {
		return false
	}
	var probe struct {
		Payload     string `json:"payload"`
		PayloadType string `json:"payloadType"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return probe.Payload != "" && probe.PayloadType != ""
}

// decodeDSSEPayloadUnverified reads an envelope's payload and its first
// signature's keyid WITHOUT verifying anything — plain base64 + JSON, the
// same two fields certify.VerifyDSSE itself reads before it checks the
// signature. Kept separate from VerifyDSSE (never a re-implementation of
// the signature check) because checks 2 and 3 below need the statement's
// own claims (scanId, warehouseRowsSha256) EVEN WHEN --pub is absent or the
// signature does not verify — an unsigned or badly-signed statement can
// still name a real scan whose warehouse rows or Rekor entry are worth
// checking on their own terms.
func decodeDSSEPayloadUnverified(envelope []byte) (stmt map[string]any, keyID string, err error) {
	var env struct {
		Payload    string `json:"payload"`
		Signatures []struct {
			KeyID string `json:"keyid"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(envelope, &env); err != nil {
		return nil, "", fmt.Errorf("parsing the envelope: %w", err)
	}
	if env.Payload == "" {
		return nil, "", fmt.Errorf("the envelope carries no payload")
	}
	payload, derr := base64.StdEncoding.DecodeString(env.Payload)
	if derr != nil {
		payload, derr = base64.URLEncoding.DecodeString(env.Payload)
		if derr != nil {
			return nil, "", fmt.Errorf("decoding the payload: %w", derr)
		}
	}
	if err := json.Unmarshal(payload, &stmt); err != nil {
		return nil, "", fmt.Errorf("the payload is not a JSON statement: %w", err)
	}
	if len(env.Signatures) > 0 {
		keyID = env.Signatures[0].KeyID
	}
	return stmt, keyID, nil
}

// verifySignature is check 1. It ALWAYS reports who signed (the envelope's
// own keyid) — that fact needs no key at all — and additionally verifies
// the signature whenever a public key is available: --pub, else the local
// certify key. With neither, it names WHO the envelope claims signed it
// but reports the check itself as not checked (there is nothing to verify
// against).
func verifySignature(envelope []byte, keyID, pubFlag string) verifyCheckResult {
	who := keyID
	if who == "" {
		who = "an unnamed key"
	}

	var pub ed25519.PublicKey
	var pubSource string
	switch {
	case pubFlag != "":
		decoded, err := hex.DecodeString(pubFlag)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("--pub is not a valid %d-byte hex-encoded Ed25519 public key", ed25519.PublicKeySize)}
		}
		pub, pubSource = ed25519.PublicKey(decoded), "--pub"
	default:
		priv, err := loadLocalCertifyKeyIfConfigured()
		if err != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("signed by %s — no --pub given and no local signing key configured to verify against", who)}
		}
		pub, pubSource = priv.Public().(ed25519.PublicKey), "the local certify key"
	}

	_, ok, err := certify.VerifyDSSE(envelope, pub)
	if err != nil || !ok {
		return verifyCheckResult{checked: true, ok: false, detail: fmt.Sprintf("signed by %s, but the signature does NOT verify against %s", who, pubSource)}
	}
	return verifyCheckResult{checked: true, ok: true, detail: fmt.Sprintf("signed by %s, verified against %s", who, pubSource)}
}

// scanIdentity is the handful of statement fields checks 2 and 3 both need:
// which scan, and — for check 2 — what it claims about its warehouse rows.
type scanIdentity struct {
	repo   string
	scanID int64
	// warehouseRowsSHA256 is "" when the statement never carried one (no
	// warehouse push accompanied the run it was written for).
	warehouseRowsSHA256 string
}

// statementScanIdentity reads the fields BuildAuditAttestation wrote — see
// certify.AuditStatement and BuildAuditAttestation in
// internal/certify/audit_statement.go — out of the generically-decoded
// payload map. Absent or malformed fields come back as the zero value,
// which the two callers below already treat as "nothing to check".
func statementScanIdentity(stmt map[string]any) scanIdentity {
	var id scanIdentity
	if subjects, ok := stmt["subject"].([]any); ok && len(subjects) > 0 {
		if s, ok := subjects[0].(map[string]any); ok {
			if name, ok := s["name"].(string); ok {
				id.repo = name
			}
		}
	}
	pred, _ := stmt["predicate"].(map[string]any)
	if pred == nil {
		return id
	}
	if v, ok := pred["scanId"].(float64); ok {
		id.scanID = int64(v)
	}
	if v, ok := pred["warehouseRowsSha256"].(string); ok {
		id.warehouseRowsSHA256 = v
	}
	return id
}

// verifyWarehouseRows is check 2: with --db, read the rows this scan
// actually landed back from the warehouse (auditpush.ReadBundle) and
// recompute their hash with THE SAME warehouseRowsSHA256
// (cmd/corral/certify_repo.go) writeAuditStatement itself called before
// signing — never a second, hand-rolled canonicalization. See
// auditpush.ReadBundle's own doc for the ordering caveat this check
// inherits: a mismatch on a warehouse an operator has VACUUMed, or that
// received more than one push for this (repo, scan_id), is not necessarily
// evidence of tampering.
func verifyWarehouseRows(dbFlag string, stmt map[string]any) verifyCheckResult {
	if strings.TrimSpace(dbFlag) == "" {
		return verifyCheckResult{checked: false, detail: "no --db given"}
	}
	id := statementScanIdentity(stmt)
	if id.warehouseRowsSHA256 == "" {
		return verifyCheckResult{checked: false, detail: "the statement carries no warehouseRowsSha256 — no --push accompanied the run it was written for"}
	}
	if id.repo == "" || id.scanID == 0 {
		return verifyCheckResult{checked: false, detail: "the statement names no repo/scanId to look up"}
	}

	db, bundle, err := openAndReadBundle(dbFlag, id.repo, id.scanID)
	if err != nil {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("opening --db: %v", err)}
	}
	defer db.Close()
	if len(bundle.Files) == 0 && bundle.Scan == (auditpush.ScanRow{}) {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("no rows found in --db for repo %q scan %d — was it ever pushed there?", id.repo, id.scanID)}
	}

	got, herr := warehouseRowsSHA256(bundle)
	if herr != nil {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("hashing the rows read back: %v", herr)}
	}
	if got != id.warehouseRowsSHA256 {
		return verifyCheckResult{checked: true, ok: false, detail: "the rows read back from --db hash to a different value than the statement claims — note: a VACUUMed or re-pushed warehouse changes row order and can trip this without tampering; re-verify against a warehouse that has only been pushed to once"}
	}
	return verifyCheckResult{checked: true, ok: true, detail: "the rows read back from --db hash to exactly the value the statement claims"}
}

// openAndReadBundle opens dbFlag (a plain DuckDB path or md:<db>, the same
// targets --push accepts) and reads back the bundle for one scan.
func openAndReadBundle(dbFlag, repo string, scanID int64) (*sql.DB, auditpush.Bundle, error) {
	db, err := sql.Open("duckdb", dbFlag)
	if err != nil {
		return nil, auditpush.Bundle{}, err
	}
	b, err := auditpush.ReadBundle(db, repo, scanID)
	if err != nil {
		_ = db.Close()
		return nil, auditpush.Bundle{}, err
	}
	return db, b, nil
}

// verifyRekorInclusion is check 3: with an index (given, or read from the
// --db warehouse row this scan pushed), fetch the entry via the SAME
// transparency.Logger seam --transparency uploads through (newTransparencyLogger,
// certify_repo_transparency.go — no second client construction) and confirm
// its logged envelope hash matches this envelope's own sha256. See
// transparency.LogEntry.EnvelopeSHA256's doc for exactly what this does and
// does not prove (a real check against the log's own record, NOT an
// offline Merkle-inclusion proof against the Sigstore TUF trust root — that
// stronger, separate check belongs to the Witness path, a different
// artifact entirely).
func verifyRekorInclusion(ctx context.Context, envelope []byte, stmt map[string]any, dbFlag string, indexFlag int64) verifyCheckResult {
	index := indexFlag
	if index < 0 {
		if strings.TrimSpace(dbFlag) == "" {
			return verifyCheckResult{checked: false, detail: "no --rekor-index given and no --db to read one from"}
		}
		id := statementScanIdentity(stmt)
		if id.repo == "" || id.scanID == 0 {
			return verifyCheckResult{checked: false, detail: "no --rekor-index given, and the statement names no repo/scanId to look one up with"}
		}
		db, bundle, err := openAndReadBundle(dbFlag, id.repo, id.scanID)
		if err != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("no --rekor-index given, and opening --db to find one failed: %v", err)}
		}
		_ = db.Close()
		if bundle.Scan.RekorLogIndex == nil {
			return verifyCheckResult{checked: false, detail: "no --rekor-index given, and --db recorded none for this scan (was --transparency used?)"}
		}
		index = *bundle.Scan.RekorLogIndex
	}

	entry, err := newTransparencyLogger(rekorBaseURL()).Get(ctx, index)
	if err != nil {
		return verifyCheckResult{checked: true, ok: false, detail: fmt.Sprintf("fetching rekor index %d: %v", index, err)}
	}
	sum := sha256.Sum256(envelope)
	want := hex.EncodeToString(sum[:])
	if entry.EnvelopeSHA256 != want {
		return verifyCheckResult{checked: true, ok: false, detail: fmt.Sprintf("rekor index %d (uuid %s) does not match this envelope's hash", entry.LogIndex, entry.UUID)}
	}
	return verifyCheckResult{checked: true, ok: true, detail: fmt.Sprintf("rekor index %d (uuid %s) matches this envelope exactly", entry.LogIndex, entry.UUID)}
}
