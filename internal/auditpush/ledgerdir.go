// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A LEDGER DIRECTORY is a push target that is a directory instead of a
// database: every push writes ONE JSON file — the bundle exactly as a
// warehouse would have received it, under the push's own scan_uid and
// timestamp — into <dir>/scans/, and nothing is ever rewritten. It is the
// store for a runner that has no database to reach and a branch to commit
// to: append-only, per-run files that git handles well, diffable, readable
// by a fork's pull request, and each one the bytes a signed statement's
// warehouseRowsSha256 can be checked against. A DuckDB file in a branch —
// the first cut — was a binary that grew with every commit and had to be
// squashed; this replaces it.
//
// DuckDB is then a VIEW over the files, not their owner: LoadDir replays
// every bundle into an in-memory database with the warehouse's own schema,
// so `verify --db <dir>`, `seal --db <dir>` and `models rank --db <dir>`
// read a ledger directory exactly as they read a warehouse.

// ScansSubdir is where bundle files live under a ledger directory.
const ScansSubdir = "scans"

// IsLedgerDir reports whether target names a directory (an existing one,
// or a path spelled with a trailing separator) rather than a database.
func IsLedgerDir(target string) bool {
	t := strings.TrimSpace(target)
	if t == "" || strings.HasPrefix(t, "md:") {
		return false
	}
	if strings.HasSuffix(t, "/") || strings.HasSuffix(t, string(os.PathSeparator)) {
		return true
	}
	st, err := os.Stat(t)
	return err == nil && st.IsDir()
}

// LedgerEntry is the on-disk form: the bundle plus the push stamp, the
// hash of the entry before it, and a signature — so the directory is a
// SIGNED, HASH-LINKED LEDGER, not a folder of files. Editing an entry
// breaks its signature; deleting or reordering one breaks the next entry's
// Prev; and a reader with the public key can check both without trusting
// whoever holds the branch. It is the part of a blockchain that was always
// a good idea — an append-only log where every entry is signed and names
// its predecessor — with one writer per directory and, when a stranger
// must be able to trust it, Sigstore's public log as the outside witness,
// instead of consensus.
//
// Hash is sha256 over the canonical sparse JSON (CanonicalSparseJSON) of
// the entry with Hash and Signature cleared; Prev is the previous entry's
// Hash, "" on the first entry (a stated genesis, never a fabricated link).
// Signature is Ed25519 over Hash's bytes, KeyID naming the key convention
// ("corral-certify" — the same key --local verdicts and --attest use).
// Unsigned entries are legal (no key configured) and are SAID to be, by
// every reader: a chain still orders them; only a signature says who.
type LedgerEntry struct {
	Format string `json:"format"`
	// Pushed is when the entry was PLACED in this directory (see
	// AppendLedgerEntry), which is what orders the files; the run's own
	// time is Bundle.Scan.StartedAt.
	Pushed  time.Time `json:"pushed"`
	ScanUID string    `json:"scan_uid"`
	Prev    string    `json:"prev,omitempty"`
	Bundle  Bundle    `json:"bundle"`
	// Hash and Signature are computed over everything above.
	Hash      string `json:"hash,omitempty"`
	KeyID     string `json:"keyid,omitempty"`
	Signature string `json:"signature,omitempty"` // hex Ed25519 over Hash's raw bytes
}

// ledgerFile is the historical name; kept as the alias readers use.
type ledgerFile = LedgerEntry

// LedgerFileFormat is the document version a ledger entry declares.
const LedgerFileFormat = "corral-ledger-2"

// LedgerSigner signs an entry's hash. cmd/corral supplies one from the
// local certify key when it exists; nil writes an unsigned entry.
type LedgerSigner interface {
	Sign(hash []byte) (keyID string, signature []byte, err error)
}

// Ed25519LedgerSigner is the stock signer.
type Ed25519LedgerSigner struct {
	KeyID string
	Key   ed25519.PrivateKey
}

func (s Ed25519LedgerSigner) Sign(hash []byte) (string, []byte, error) {
	return s.KeyID, ed25519.Sign(s.Key, hash), nil
}

// EntryHash is what Hash holds: sha256 over the entry's canonical sparse
// JSON with Hash and Signature cleared.
func EntryHash(e LedgerEntry) (string, error) {
	e.Hash, e.KeyID, e.Signature = "", "", ""
	js, err := CanonicalSparseJSON(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(js)
	return hex.EncodeToString(sum[:]), nil
}

// writeLedgerFile is the directory half of PushBundle: the same
// canonicalised bundle the warehouse receives, as one signed, linked entry.
func writeLedgerFile(dir string, b Bundle, signer LedgerSigner) (Counts, error) {
	now, uid := mintPush(b)
	b.Scan.ScanUID = uid
	if b.Scan.PushedBy == "" {
		b.Scan.PushedBy = PushedByCertify
	}
	e := LedgerEntry{Format: LedgerFileFormat, Pushed: now, ScanUID: uid, Bundle: b}
	if _, err := AppendLedgerEntry(dir, e, signer); err != nil {
		return Counts{}, err
	}
	return Counts{Scans: boolToInt(b.Scan != (ScanRow{})), Files: len(b.Files), Mutants: len(b.Mutants), Calls: len(b.Calls), Events: len(b.Events)}, nil
}

// AppendLedgerEntry LINKS e to the directory's current head, hashes it,
// signs it when a signer is given, and places it. It is the one way an
// entry enters a directory — a push, and `corral ledger append` re-linking
// an entry written elsewhere (a runner's staging dir; a laptop whose branch
// moved under it) — so Prev is always the head at placement time, never a
// head remembered from earlier. A chain is one writer at a time; this is
// the verb the retry loop (fetch → append → push) runs. Returns the file's
// name.
func AppendLedgerEntry(dir string, e LedgerEntry, signer LedgerSigner) (string, error) {
	scans := filepath.Join(dir, ScansSubdir)
	if err := os.MkdirAll(scans, 0o750); err != nil {
		return "", fmt.Errorf("auditpush: ledger dir: %w", err)
	}
	// The link: the newest entry's hash. Read, not remembered — the
	// directory is the state, and another writer may have appended.
	prev := ""
	if existing, err := ReadLedgerDir(dir); err != nil {
		return "", err
	} else if n := len(existing); n > 0 {
		prev = existing[n-1].Hash
	}
	e.Format = LedgerFileFormat
	e.Prev = prev
	// Pushed is PLACEMENT time — when the entry entered THIS directory —
	// so file order and chain order agree by construction: an entry
	// re-linked here after the head moved is newer than the head it names,
	// whatever clock it was first written under. The run's own time is on
	// the bundle (Scan.StartedAt); the uid keeps the identity the first
	// push minted.
	e.Pushed = time.Now().UTC().Truncate(time.Microsecond)
	e.Hash, e.KeyID, e.Signature = "", "", ""
	h, err := EntryHash(e)
	if err != nil {
		return "", err
	}
	e.Hash = h
	if signer != nil {
		raw, _ := hex.DecodeString(h)
		keyID, sig, err := signer.Sign(raw)
		if err != nil {
			return "", fmt.Errorf("auditpush: sign ledger entry: %w", err)
		}
		e.KeyID, e.Signature = keyID, hex.EncodeToString(sig)
	}
	b, now, uid := e.Bundle, e.Pushed, e.ScanUID
	commit := b.Scan.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		commit = "nocommit"
	}
	// Gzipped: an entry with its events grain is ~550 KB of text and ~21 KB
	// compressed (measured on a psf/requests scan, 26×), and DuckDB's
	// read_json_auto reads .json.gz natively, so the branch pays for the
	// record and not for the whitespace. Still just text: `zcat` it.
	name := fmt.Sprintf("%s-%s-%s.json.gz", now.Format("20060102T150405Z"), commit, uid[:12])
	js, err := json.MarshalIndent(e, "", " ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(js); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	tmp := filepath.Join(scans, "."+name+".tmp")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil { // #nosec G306 -- a record the branch publishes
		return "", fmt.Errorf("auditpush: write ledger entry: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(scans, name)); err != nil {
		return "", fmt.Errorf("auditpush: place ledger entry: %w", err)
	}
	return name, nil
}

// ReadLedgerEntry reads one entry file (plain or gzipped).
func ReadLedgerEntry(path string) (LedgerEntry, error) {
	raw, err := readMaybeGzip(path)
	if err != nil {
		return LedgerEntry{}, err
	}
	var e LedgerEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return LedgerEntry{}, fmt.Errorf("auditpush: %s: %w", filepath.Base(path), err)
	}
	if e.Format != LedgerFileFormat {
		return LedgerEntry{}, fmt.Errorf("auditpush: %s: format %q, want %q", filepath.Base(path), e.Format, LedgerFileFormat)
	}
	return e, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ChainCheck is one entry's verdict from VerifyLedgerDir.
type ChainCheck struct {
	File    string
	ScanUID string
	Commit  string
	HashOK  bool // the stored Hash matches the entry's bytes
	LinkOK  bool // Prev names the previous entry's Hash (true for a genesis entry)
	Signed  bool // the entry carries a signature
	SigOK   bool // the signature verifies under pub (false when unsigned or no pub given)
	KeyID   string
	Problem string // "" when nothing is wrong
	Genesis bool
}

// VerifyLedgerDir walks the chain: every entry's hash against its bytes,
// every Prev against its predecessor, every signature against pub (when
// given). It never stops at the first failure — a reader wants the whole
// picture — and it never calls an unsigned entry verified.
func VerifyLedgerDir(dir string, pub ed25519.PublicKey) ([]ChainCheck, error) {
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return nil, err
	}
	names, _ := ledgerFileNames(dir)
	var out []ChainCheck
	prevHash := ""
	for i, e := range entries {
		c := ChainCheck{ScanUID: e.ScanUID, Commit: e.Bundle.Scan.Commit, KeyID: e.KeyID, Genesis: i == 0}
		if i < len(names) {
			c.File = names[i]
		}
		h, herr := EntryHash(e)
		c.HashOK = herr == nil && h == e.Hash
		c.LinkOK = e.Prev == prevHash
		c.Signed = e.Signature != ""
		if c.Signed && pub != nil {
			raw, _ := hex.DecodeString(e.Hash)
			sig, _ := hex.DecodeString(e.Signature)
			c.SigOK = ed25519.Verify(pub, raw, sig)
		}
		switch {
		case !c.HashOK:
			c.Problem = "entry bytes do not match its hash — edited after it was written"
		case !c.LinkOK:
			c.Problem = fmt.Sprintf("prev %.12s does not name the previous entry (%.12s) — an entry was removed, reordered or inserted", e.Prev, prevHash)
		case c.Signed && pub != nil && !c.SigOK:
			c.Problem = "signature does not verify under the given key"
		}
		out = append(out, c)
		prevHash = e.Hash
	}
	return out, nil
}

// ledgerFileNames lists entry files in push order (the same order
// ReadLedgerDir returns, since names begin with the push timestamp).
func ledgerFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, ScansSubdir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && isLedgerEntryName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// isLedgerEntryName accepts .json and .json.gz, never a temp file.
func isLedgerEntryName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.gz")
}

// readMaybeGzip reads an entry whether or not it is compressed.
func readMaybeGzip(path string) ([]byte, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a file under the ledger directory the operator named
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// ReadLedgerDir returns every bundle in dir, oldest push first.
func ReadLedgerDir(dir string) ([]ledgerFile, error) {
	scans := filepath.Join(dir, ScansSubdir)
	entries, err := os.ReadDir(scans)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("auditpush: ledger dir: %w", err)
	}
	var out []ledgerFile
	for _, e := range entries {
		if e.IsDir() || !isLedgerEntryName(e.Name()) {
			continue
		}
		raw, err := readMaybeGzip(filepath.Join(scans, e.Name()))
		if err != nil {
			return nil, err
		}
		var f ledgerFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("auditpush: %s: %w", e.Name(), err)
		}
		if f.Format != LedgerFileFormat {
			return nil, fmt.Errorf("auditpush: %s: format %q, want %q", e.Name(), f.Format, LedgerFileFormat)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pushed.Before(out[j].Pushed) })
	return out, nil
}

// LoadDir replays a ledger directory into an in-memory DuckDB with the
// warehouse's schema — the view. Every bundle is inserted under the
// scan_uid and timestamp it was pushed with, so statement checks and joins
// see the same identities a warehouse would.
func LoadDir(dir string) (*sql.DB, error) {
	files, err := ReadLedgerDir(dir)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Attached under the same name a warehouse is, so the migration probe
	// (which asks duckdb_columns() about 'warehouse') and every reader see
	// the view exactly as they see a pushed database.
	if _, err := db.Exec("ATTACH ':memory:' AS warehouse; USE warehouse"); err != nil {
		db.Close()
		return nil, fmt.Errorf("auditpush: view attach: %w", err)
	}
	for _, ddl := range []string{scansSchema, auditsSchema, mutantsSchema, modelCallsSchema, eventsSchema} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("auditpush: view schema: %w", err)
		}
	}
	if err := EnsureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	for _, f := range files {
		if _, err := insertBundle(db, f.Bundle, f.Pushed, f.ScanUID); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
