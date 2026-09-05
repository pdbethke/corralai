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
	dbFlag := fs.String("db", "", "also recompute the warehouse rows' hash from this pushed DuckDB (a path, or md:<db> for MotherDuck) and compare it to the statement's claim. Every push of the scan the warehouse holds is tried (each has its own scan_uid); a VACUUMed warehouse can change row order and trip a false ✗ here without tampering")
	rekorIndexFlag := fs.Int64("rekor-index", -1, "also confirm this Rekor log index's entry matches the envelope (default: read the index --db recorded for this scan, if --db was given)")
	pubFlag := fs.String("pub", "", "hex-encoded Ed25519 public key to verify the signature against (default: the local certify key, CORRALAI_CERTIFY_KEY_FILE)")
	ledgerFlag := fs.String("ledger", "", "walk a LEDGER DIRECTORY (the JSON entries `--push <dir>/` writes, one per scan, each naming the previous entry's hash and carrying a signature): every entry's hash against its bytes, every link against its predecessor, every signature against --pub or the local certify key. One line per entry; an edited entry, a removed one, or a foreign signature is named. Instead of --attest, not with it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if d := strings.TrimSpace(*ledgerFlag); d != "" {
		if strings.TrimSpace(*attestFlag) != "" {
			fmt.Fprintln(stderr, "corral verify: --ledger walks a directory; --attest checks one statement — one or the other")
			return 2
		}
		return runVerifyLedger(d, *pubFlag, stdout, stderr)
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
	rowsResult := verifyWarehouseRows(*dbFlag, stmt, plainStatementSHA256(*attestFlag, envPath))
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
	// NOTHING CHECKED IS NOT A PASS. `failed` is set only by a check that ran
	// and failed, so an envelope nobody could verify — signed by an unknown
	// key, no --pub, no local key, no --db, no Rekor index — printed three
	// "not checked" lines and exited 0. A CI step keyed on that exit code
	// accepted a forged statement. `corral certify verify` already refuses
	// with 2 in the same situation; this verifier now does the same, and
	// says why: the absence of a failure is not the presence of a check.
	if !sigResult.checked && !rowsResult.checked && !rekorResult.checked {
		fmt.Fprintln(stderr, "corral verify: NOTHING was verified — no signature key (--pub or a local certify key), no --db, and no Rekor index were available, so every check above is \"not checked\". That is not a pass. Supply at least one of them.")
		return 2
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
	// rowsHashVersion is the form the signer hashed with (0 = v1, before
	// the field existed).
	rowsHashVersion int
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
	if v, ok := pred["warehouseRowsHashVersion"].(float64); ok {
		id.rowsHashVersion = int(v)
	}
	return id
}

// plainStatementSHA256 is the key PushBundle stamped on every row of the
// run's push: the sha256 of the PLAIN statement file (writeAuditStatement
// hashes the indented JSON it wrote, not the DSSE payload, which is a
// canonical re-encoding of the same map). It is recoverable only when the
// plain file is beside the envelope — "" otherwise, and the lookup falls
// back to (repo, scanId).
func plainStatementSHA256(attestPath, envPath string) string {
	candidates := []string{attestPath}
	if strings.HasSuffix(envPath, dsseEnvelopeSuffix) {
		candidates = append(candidates, strings.TrimSuffix(envPath, dsseEnvelopeSuffix))
	}
	for _, p := range candidates {
		if p == envPath {
			continue
		}
		b, err := os.ReadFile(p) // #nosec G304 -- derived from the operator's own --attest argument
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// locateRunPushes finds the rows in --db that could be the push this
// statement hashed: by the statement's own hash when the plain file was at
// hand (exact — that hash is stamped on the push, and only on pushes made
// with --attest), else by (repo, scanId). Each push is a separate set of
// rows keyed by its own scan_uid, so a repo pushed under scan_id 0 by every
// Action run, or a scan pushed twice, is not unioned into one bundle that
// no statement ever hashed.
func locateRunPushes(db *sql.DB, id scanIdentity, statementSHA string) ([]auditpush.ScanRow, error) {
	return auditpush.LocateScans(db, id.repo, id.scanID, statementSHA)
}

// verifyWarehouseRows is check 2: with --db, read the rows this scan
// actually landed back from the warehouse and recompute their hash with
// THE SAME warehouseRowsSHA256 (cmd/corral/certify_repo.go)
// writeAuditStatement itself called before signing — never a second,
// hand-rolled canonicalization. Every push of the scan the warehouse holds
// is tried; ONE match is a pass, because the hash binds one push and a
// later re-push is a second, different set of rows, not evidence against
// the first. See auditpush.ReadBundle's own doc for the ordering caveat
// this check inherits on a VACUUMed warehouse.
func verifyWarehouseRows(dbFlag string, stmt map[string]any, statementSHA string) verifyCheckResult {
	if strings.TrimSpace(dbFlag) == "" {
		return verifyCheckResult{checked: false, detail: "no --db given"}
	}
	id := statementScanIdentity(stmt)
	if id.warehouseRowsSHA256 == "" {
		return verifyCheckResult{checked: false, detail: "the statement carries no warehouseRowsSha256 — no --push accompanied the run it was written for"}
	}
	if statementSHA == "" && (id.repo == "" || id.scanID == 0) {
		return verifyCheckResult{checked: false, detail: "the plain statement file is not beside its envelope, and the statement names no repo/scanId to look up instead (scanId is 0 on every current statement, not a key)"}
	}

	db, err := openWarehouse(dbFlag)
	if err != nil {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("opening --db: %v", err)}
	}
	defer db.Close()
	own, lerr := locateRunPushes(db, id, statementSHA)
	if lerr != nil {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("reading --db: %v", lerr)}
	}
	if len(own) == 0 {
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("no rows found in --db for repo %q scan %d — was it ever pushed there?", id.repo, id.scanID)}
	}

	var got []string
	for _, sc := range own {
		bundle, rerr := auditpush.ReadBundleForScan(db, sc)
		if rerr != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("reading --db: %v", rerr)}
		}
		// Hash the rows the way the signer did. A v1 statement (no version
		// field) hashed the full JSON of the pushing binary's structs, which
		// this binary reproduces only if no warehouse column has been added
		// since — said in the mismatch detail rather than left to look like
		// tampering.
		var h string
		var herr error
		switch {
		case id.rowsHashVersion < 2:
			h, herr = warehouseRowsSHA256Legacy(bundle)
		default:
			h, herr = warehouseRowsSHA256At(bundle, id.rowsHashVersion)
		}
		if herr != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("hashing the rows read back: %v", herr)}
		}
		if h == id.warehouseRowsSHA256 {
			return verifyCheckResult{checked: true, ok: true, detail: fmt.Sprintf("the rows read back from --db (push %s) hash to exactly the value the statement claims", sc.ScanUID)}
		}
		got = append(got, h[:12])
	}
	detail := fmt.Sprintf("the rows read back from --db hash to a different value than the statement claims (%d push(es) of this scan tried: %s)", len(own), strings.Join(got, ", "))
	detail += " — note: a VACUUMed warehouse can change row order and trip this without tampering"
	if id.rowsHashVersion < 2 {
		detail += "; and this statement's hash is version 1 (the full JSON of the pushing binary's row structs), which a binary with columns added since cannot reproduce — a pre-1.0 limitation, not evidence of tampering; verify it with the corral version that pushed it"
	}
	return verifyCheckResult{checked: true, ok: false, detail: detail}
}

// openWarehouse opens dbFlag (a plain DuckDB path or md:<db>, the same
// targets --push accepts).
func openWarehouse(dbFlag string) (*sql.DB, error) {
	if auditpush.IsLedgerDir(dbFlag) {
		return auditpush.LoadDir(strings.TrimRight(dbFlag, "/"))
	}
	db, err := sql.Open("duckdb", dbFlag)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
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
		db, err := openWarehouse(dbFlag)
		if err != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("no --rekor-index given, and opening --db to find one failed: %v", err)}
		}
		own, lerr := locateRunPushes(db, id, "")
		_ = db.Close()
		if lerr != nil {
			return verifyCheckResult{checked: false, detail: fmt.Sprintf("no --rekor-index given, and reading --db to find one failed: %v", lerr)}
		}
		var found *int64
		for _, sc := range own {
			if sc.RekorLogIndex != nil {
				found = sc.RekorLogIndex
				break
			}
		}
		if found == nil {
			return verifyCheckResult{checked: false, detail: "no --rekor-index given, and --db recorded none for this scan (was --transparency used?)"}
		}
		index = *found
	}

	entry, err := newTransparencyLogger(rekorBaseURL()).Get(ctx, index)
	if err != nil {
		// A FETCH failure (network down, the log unreachable, the entry not
		// found) is a MISSING input, not a mismatch: it says nothing about
		// whether this envelope was tampered with, only that this check
		// could not be run right now. checked=false keeps it out of the
		// exit-1 accounting and prints as "not checked", matching every
		// other missing-input case above — the usage sentence "exits 1
		// only on a real mismatch" stays true. A FETCHED entry whose
		// logged hash does not match, below, is the one real ✗ this check
		// can produce.
		return verifyCheckResult{checked: false, detail: fmt.Sprintf("fetching the log entry failed: %v", err)}
	}
	sum := sha256.Sum256(envelope)
	want := hex.EncodeToString(sum[:])
	if entry.EnvelopeSHA256 != want {
		return verifyCheckResult{checked: true, ok: false, detail: fmt.Sprintf("rekor index %d (uuid %s) does not match this envelope's hash", entry.LogIndex, entry.UUID)}
	}
	return verifyCheckResult{checked: true, ok: true, detail: fmt.Sprintf("rekor index %d (uuid %s) matches this envelope exactly", entry.LogIndex, entry.UUID)}
}

// runVerifyLedger is `corral verify --ledger <dir>`: the chain, entry by
// entry. Exit 1 when any entry has a problem; 0 when every entry's hash and
// link hold and every signature that could be checked verified — an
// unsigned entry is reported, not failed, since the chain still orders it;
// and a signed entry with no key to check against is "signed, unverified",
// never "verified".
func runVerifyLedger(dir, pubFlag string, stdout, stderr io.Writer) int {
	var pub ed25519.PublicKey
	pubSource := ""
	switch {
	case pubFlag != "":
		decoded, err := hex.DecodeString(pubFlag)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			fmt.Fprintf(stderr, "corral verify: --pub is not a valid %d-byte hex-encoded Ed25519 public key\n", ed25519.PublicKeySize)
			return 2
		}
		pub, pubSource = ed25519.PublicKey(decoded), "--pub"
	default:
		if priv, err := loadLocalCertifyKeyIfConfigured(); err == nil {
			pub, pubSource = priv.Public().(ed25519.PublicKey), "the local certify key"
		}
	}
	checks, err := auditpush.VerifyLedgerDir(strings.TrimRight(dir, "/"), pub)
	if err != nil {
		fmt.Fprintf(stderr, "corral verify: --ledger %s: %v\n", dir, err)
		return 2
	}
	if len(checks) == 0 {
		fmt.Fprintf(stdout, "· ledger %s: no entries\n", dir)
		return 0
	}
	bad := 0
	for _, c := range checks {
		mark := "✓"
		what := "hash ok, link ok"
		if c.Genesis {
			what = "hash ok, genesis"
		}
		switch {
		case c.Problem != "":
			mark, what = "✗", c.Problem
			bad++
		case c.Signed && pub != nil:
			what += fmt.Sprintf(", signed by %s and verified against %s", orUnnamed(c.KeyID), pubSource)
		case c.Signed:
			what += fmt.Sprintf(", signed by %s — unverified (no --pub, no local key)", orUnnamed(c.KeyID))
		default:
			what += ", UNSIGNED"
		}
		fmt.Fprintf(stdout, "%s %s  %.12s  %s\n", mark, c.File, c.Commit, what)
	}
	if bad > 0 {
		fmt.Fprintf(stdout, "%d of %d entries have a problem — the chain is not intact\n", bad, len(checks))
		return 1
	}
	fmt.Fprintf(stdout, "%d entries, chain intact\n", len(checks))
	return 0
}

func orUnnamed(keyID string) string {
	if strings.TrimSpace(keyID) == "" {
		return "an unnamed key"
	}
	return keyID
}
