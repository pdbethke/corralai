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

	"github.com/pdbethke/corralai/internal/review"
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
	// Kind is what the entry IS: "" (KindScan) for a run's own record —
	// every entry ever written before kinds existed, so the field is
	// omitted and their hashes stand — KindRetract for a judgment about an
	// earlier entry, KindCheckpoint for a genesis that stands in for
	// entries pruned before it. A reader that does not know a kind treats
	// the entry as opaque: it is still in the chain, still hashed, still
	// signed; it just is not a scan.
	Kind string `json:"kind,omitempty"`
	// Pushed is when the entry was PLACED in this directory (see
	// AppendLedgerEntry), which is what orders the files; the run's own
	// time is Bundle.Scan.StartedAt.
	Pushed  time.Time `json:"pushed"`
	ScanUID string    `json:"scan_uid"`
	Prev    string    `json:"prev,omitempty"`
	Bundle  Bundle    `json:"bundle"`
	// Retracts (KindRetract) is the Hash of the entry this one retracts,
	// and Reason says why, in the retractor's words. The retracted entry
	// STAYS — a bad run is still a fact — and every reader of the record
	// (the view, the prior, the verdict cache, `scans`) skips it from then
	// on. Deleting it instead would break the next entry's link, which is
	// the chain doing its job.
	Retracts string `json:"retracts,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Checkpoint (KindCheckpoint) stands where pruned history was: the
	// hash of the head it replaced and how many entries, through when.
	// A checkpoint is always a genesis (Prev == ""), and the verifier says
	// so — "chain begins at a checkpoint; N earlier entries not present"
	// — rather than pretending the chain was always this short.
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
	// Review (KindReview) is one review's record: the findings as
	// RECORDED (reproductions run, tiers demoted where a script did not
	// hold), the sound list, and the opinion. The entry is signed as a
	// whole, which signs the reproductions — script, output, exit — and
	// carries the opinion; the opinion is prose and is not what the
	// signature vouches for. See internal/review.
	Review *review.Review `json:"review,omitempty"`
	// Adjudication (KindAdjudication) is a person's verdict on one finding
	// — Adjudicates names it as <review entry hash>#<finding id> — with
	// who decided and why. Automatic passes never write these; a later one
	// by the same or another person is a new entry, and the newest stands.
	Adjudication *Adjudication `json:"adjudication,omitempty"`
	// Hash and Signature are computed over everything above.
	Hash      string `json:"hash,omitempty"`
	KeyID     string `json:"keyid,omitempty"`
	Signature string `json:"signature,omitempty"` // hex Ed25519 over Hash's raw bytes
}

// The entry kinds. KindScan is the empty string on purpose: it is what
// every entry written before kinds existed carries, and their hashes must
// not change.
const (
	KindScan         = ""
	KindRetract      = "retract"
	KindCheckpoint   = "checkpoint"
	KindReview       = "review"
	KindAdjudication = "adjudication"
)

// Adjudication is a person's verdict on one finding of a review entry.
type Adjudication struct {
	Adjudicates string `json:"adjudicates"` // "<review entry hash>#<finding id>"
	Verdict     string `json:"verdict"`     // VerdictConfirmed or VerdictRefuted
	By          string `json:"by"`          // the principal, as they named themselves
	Reason      string `json:"reason"`
}

// The two verdicts an adjudication carries.
const (
	VerdictConfirmed = "confirmed"
	VerdictRefuted   = "refuted"
)

// Checkpoint is what a KindCheckpoint entry carries about the history it
// replaced.
type Checkpoint struct {
	Head    string    `json:"head"`    // the Hash of the last pruned entry
	Entries int       `json:"entries"` // how many entries were pruned
	Through time.Time `json:"through"` // the Pushed time of the last pruned entry
}

// IsScan reports whether the entry is a run's own record.
func (e LedgerEntry) IsScan() bool { return e.Kind == KindScan }

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
	if e.Kind == KindCheckpoint {
		return "", fmt.Errorf("auditpush: a checkpoint is a genesis and cannot be appended to a chain — see WriteCheckpoint")
	}
	// The link: the newest entry's hash. Read, not remembered — the
	// directory is the state, and another writer may have appended.
	prev := ""
	if existing, err := ReadLedgerDir(dir); err != nil {
		return "", err
	} else if n := len(existing); n > 0 {
		prev = existing[n-1].Hash
	}
	return placeEntry(dir, e, prev, signer)
}

// placeEntry hashes, signs and writes e linked to prev. It is the one
// writer of entry files: AppendLedgerEntry links to the head, and
// WriteCheckpoint places a genesis.
func placeEntry(dir string, e LedgerEntry, prev string, signer LedgerSigner) (string, error) {
	scans := filepath.Join(dir, ScansSubdir)
	if err := os.MkdirAll(scans, 0o750); err != nil {
		return "", fmt.Errorf("auditpush: ledger dir: %w", err)
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
	// The file name: the placement time, then what the entry is about — a
	// scan's commit and uid, a retraction's target, a checkpoint's replaced
	// head — so a directory listing reads as the chain does.
	middle, tail := "", ""
	switch e.Kind {
	case KindRetract:
		middle, tail = "retract", e.Retracts
	case KindCheckpoint:
		middle, tail = "checkpoint", e.Checkpoint.Head
	case KindReview:
		middle, tail = "review", e.Review.Commit
	case KindAdjudication:
		// Its own hash, not the review's: two verdicts on one review can
		// land in the same second, and a name that collided would replace
		// the first entry rather than add the second.
		middle, tail = "adjudication", e.Hash
	default:
		middle, tail = e.Bundle.Scan.Commit, e.ScanUID
		if middle == "" {
			middle = "nocommit"
		}
	}
	if len(middle) > 12 {
		middle = middle[:12]
	}
	if len(tail) > 12 {
		tail = tail[:12]
	}
	// Gzipped: an entry with its events grain is ~550 KB of text and ~21 KB
	// compressed (measured on a psf/requests scan, 26×), and DuckDB's
	// read_json_auto reads .json.gz natively, so the branch pays for the
	// record and not for the whitespace. Still just text: `zcat` it.
	name := fmt.Sprintf("%s-%s-%s.json.gz", e.Pushed.Format("20060102T150405Z"), middle, tail)
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
	if _, err := os.Stat(filepath.Join(scans, name)); err == nil {
		return "", fmt.Errorf("auditpush: an entry named %s already exists — refusing to replace it", name)
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
	// Kind is the entry's kind; Note says what a retraction or checkpoint
	// entry means for the chain, in words, when nothing is wrong with it.
	Kind string
	Note string
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
	seen := map[string]bool{}
	for i, e := range entries {
		c := ChainCheck{ScanUID: e.ScanUID, Commit: e.Bundle.Scan.Commit, KeyID: e.KeyID, Genesis: i == 0, Kind: e.Kind}
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
		case e.Kind == KindCheckpoint && i != 0:
			c.Problem = fmt.Sprintf("a checkpoint at position %d — a checkpoint is a genesis and stands only at the start of a chain", i+1)
		case e.Kind == KindCheckpoint && e.Checkpoint == nil:
			c.Problem = "a checkpoint entry that names no replaced head"
		case e.Kind == KindCheckpoint:
			c.Note = fmt.Sprintf("chain begins at a checkpoint: %d earlier entries (through %s, head %.12s) are not present", e.Checkpoint.Entries, e.Checkpoint.Through.UTC().Format("2006-01-02"), e.Checkpoint.Head)
		case e.Kind == KindRetract && !seen[e.Retracts]:
			c.Problem = fmt.Sprintf("retracts %.12s, which is not an earlier entry of this chain", e.Retracts)
		case e.Kind == KindRetract:
			c.Note = fmt.Sprintf("retracts %.12s: %s", e.Retracts, e.Reason)
		case e.Kind == KindReview && e.Review == nil:
			c.Problem = "a review entry that carries no review"
		case e.Kind == KindReview:
			rep, cr, hy := e.Review.Counts()
			c.Commit = e.Review.Commit
			c.Note = fmt.Sprintf("review of %s by %s: %d reproduced, %d code-read, %d hypothesis", e.Review.Scope, e.Review.ReviewerModel, rep, cr, hy)
		case e.Kind == KindAdjudication && (e.Adjudication == nil || !seen[strings.SplitN(e.Adjudication.Adjudicates, "#", 2)[0]]):
			c.Problem = "an adjudication of a finding whose review is not an earlier entry of this chain"
		case e.Kind == KindAdjudication:
			c.Note = fmt.Sprintf("%s %s by %s: %s", e.Adjudication.Verdict, shortRef(e.Adjudication.Adjudicates), e.Adjudication.By, e.Adjudication.Reason)
		}
		out = append(out, c)
		prevHash = e.Hash
		seen[e.Hash] = true
	}
	return out, nil
}

// shortRef renders "<hash>#Rn" as "<hash12>#Rn".
func shortRef(ref string) string {
	h, id, _ := strings.Cut(ref, "#")
	if len(h) > 12 {
		h = h[:12]
	}
	if id == "" {
		return h
	}
	return h + "#" + id
}

// WriteReview appends a KindReview entry.
func WriteReview(dir string, r review.Review, signer LedgerSigner) (string, error) {
	if r.Commit == "" || r.Scope == "" {
		return "", fmt.Errorf("auditpush: a review names a commit and a scope")
	}
	return AppendLedgerEntry(dir, LedgerEntry{Kind: KindReview, Review: &r}, signer)
}

// FindReview resolves a review entry by its hash or an unambiguous prefix.
func FindReview(entries []LedgerEntry, target string) (LedgerEntry, error) {
	var found LedgerEntry
	n := 0
	for _, e := range entries {
		if e.Kind != KindReview {
			continue
		}
		if e.Hash == target || (len(target) >= 12 && strings.HasPrefix(e.Hash, target)) {
			found = e
			n++
		}
	}
	switch n {
	case 0:
		return LedgerEntry{}, fmt.Errorf("auditpush: no review entry %q", target)
	case 1:
		return found, nil
	}
	return LedgerEntry{}, fmt.Errorf("auditpush: %q names more than one review entry", target)
}

// WriteAdjudication appends a KindAdjudication entry for one finding of a
// review entry in dir. ref is "<review hash or prefix>#<finding id>".
func WriteAdjudication(dir, ref, verdict, by, reason string, signer LedgerSigner) (string, error) {
	hashPart, id, ok := strings.Cut(strings.TrimSpace(ref), "#")
	if !ok || id == "" {
		return "", fmt.Errorf("auditpush: an adjudication names a finding as <review hash>#<finding id>")
	}
	if verdict != VerdictConfirmed && verdict != VerdictRefuted {
		return "", fmt.Errorf("auditpush: verdict must be %s or %s", VerdictConfirmed, VerdictRefuted)
	}
	by, reason = strings.TrimSpace(by), strings.TrimSpace(reason)
	if by == "" || reason == "" {
		return "", fmt.Errorf("auditpush: an adjudication names who decided and why")
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return "", err
	}
	rev, err := FindReview(entries, hashPart)
	if err != nil {
		return "", err
	}
	known := false
	for _, f := range rev.Review.Findings {
		if f.ID == id {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("auditpush: review %.12s has no finding %q", rev.Hash, id)
	}
	a := &Adjudication{Adjudicates: rev.Hash + "#" + id, Verdict: verdict, By: by, Reason: reason}
	return AppendLedgerEntry(dir, LedgerEntry{Kind: KindAdjudication, Adjudication: a}, signer)
}

// Adjudications returns, for every finding ref, the NEWEST adjudication in
// entries — a later verdict by anyone supersedes an earlier one, and both
// stay in the chain.
func Adjudications(entries []LedgerEntry) map[string]Adjudication {
	out := map[string]Adjudication{}
	for _, e := range entries {
		if e.Kind == KindAdjudication && e.Adjudication != nil {
			out[e.Adjudication.Adjudicates] = *e.Adjudication
		}
	}
	return out
}

// WriteRetraction appends a KindRetract entry naming target (an entry's
// full Hash, or an unambiguous prefix) with reason. The target must be an
// entry of this chain: a retraction of something the ledger never held is
// a claim about nothing.
func WriteRetraction(dir, target, reason string, signer LedgerSigner) (string, error) {
	target, reason = strings.TrimSpace(target), strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("auditpush: a retraction needs a reason")
	}
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return "", err
	}
	var hash string
	for _, e := range entries {
		if e.Hash == target || (len(target) >= 12 && strings.HasPrefix(e.Hash, target)) {
			if hash != "" {
				return "", fmt.Errorf("auditpush: %q names more than one entry", target)
			}
			hash = e.Hash
		}
	}
	if hash == "" {
		return "", fmt.Errorf("auditpush: no entry %q in %s", target, dir)
	}
	for _, e := range entries {
		if e.Kind == KindRetract && e.Retracts == hash {
			return "", fmt.Errorf("auditpush: %.12s is already retracted", hash)
		}
	}
	return AppendLedgerEntry(dir, LedgerEntry{Kind: KindRetract, Retracts: hash, Reason: reason}, signer)
}

// WriteCheckpoint replaces every entry in dir with one KindCheckpoint
// genesis that names the head it stood in for. The pruned files are
// DELETED — that is the point of a checkpoint — after the checkpoint is
// safely placed, so a failure midway leaves a chain the verifier reports
// (a checkpoint not at position 1), never an empty directory. Returns the
// checkpoint's file name and how many entries were pruned.
func WriteCheckpoint(dir string, signer LedgerSigner) (string, int, error) {
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		return "", 0, fmt.Errorf("auditpush: %s has no entries to checkpoint", dir)
	}
	head := entries[len(entries)-1]
	names, err := ledgerFileNames(dir)
	if err != nil {
		return "", 0, err
	}
	cp := LedgerEntry{Kind: KindCheckpoint, Checkpoint: &Checkpoint{Head: head.Hash, Entries: len(entries), Through: head.Pushed}}
	name, err := placeEntry(dir, cp, "", signer)
	if err != nil {
		return "", 0, err
	}
	for _, n := range names {
		if n == name {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ScansSubdir, n)); err != nil {
			return name, 0, fmt.Errorf("auditpush: pruning %s: %w (the checkpoint is placed; the chain will verify as broken until the pruned entries are gone)", n, err)
		}
	}
	return name, len(entries), nil
}

// Retracted returns the hashes of every entry a KindRetract entry in
// entries names.
func Retracted(entries []LedgerEntry) map[string]LedgerEntry {
	out := map[string]LedgerEntry{}
	for _, e := range entries {
		if e.Kind == KindRetract {
			out[e.Retracts] = e
		}
	}
	return out
}

// ScanEntries is the record as a reader should see it: the scan entries,
// in chain order, with retracted ones left out. Every reader of the record
// — the view, the prior, the verdict cache — goes through this, so a
// retraction takes effect everywhere at once.
func ScanEntries(entries []LedgerEntry) []LedgerEntry {
	retracted := Retracted(entries)
	var out []LedgerEntry
	for _, e := range entries {
		if !e.IsScan() {
			continue
		}
		if _, gone := retracted[e.Hash]; gone {
			continue
		}
		out = append(out, e)
	}
	return out
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
	// The view is the record as it stands: scan entries, retracted ones
	// left out (ScanEntries). A retraction entry itself has no rows.
	files = ScanEntries(files)
	for _, f := range files {
		if _, err := insertBundle(db, f.Bundle, f.Pushed, f.ScanUID); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
