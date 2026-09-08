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
	// KNOWN, and deliberately not "fixed" here: any existing directory reads
	// as a ledger, so a mistyped `--db /home/me` or `--db .` is not refused
	// — it becomes a new ledger directory instead. (Round four, R5.)
	//
	// The narrower rule tried first — "already holds scans/, or is empty" —
	// is WRONG: a caller legitimately writes other files into the directory
	// before the first push (internal/prior does exactly this), so the rule
	// refused real ledgers. Refusing a working path to catch a typo is the
	// worse trade, and inventing a marker file on launch day is worse still.
	// The finding stands on the record until it is done properly.
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

	// Raw is the entry's bytes as read from the directory, and File the
	// name they were read from. Set by the readers, never written: from
	// corral-ledger-3 the hash is over the bytes on disk (see EntryHash),
	// so a verifier re-hashes what is in the file, not what this binary's
	// struct would re-marshal — a field added to the struct later must not
	// change an older entry's hash.
	Raw  []byte `json:"-"`
	File string `json:"-"`
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
// corral-ledger-4 (2026-09-08): keyid — the name of WHO signed — is inside
// the hashed bytes, so editing it breaks the hash and the signature that
// covers it. Through corral-ledger-3 keyid was deleted before hashing and
// never compared to the verifying key, so a placed, signed entry could have
// its signer rewritten to any name and still verify clean: the record
// stating a custody nobody signed for. Found by a cold review, 2026-09-08.
// corral-ledger-3 (2026-09-07): the hash is over the entry's FULL canonical
// bytes (CanonicalFullJSON), and a scan entry's scan_uid derives from the
// scan row and the entry's own Pushed time, so RecomputeScanUID over the
// entry reproduces it. corral-ledger-2 entries are still read and verified
// under their own rules — sparse hash; a uid minted from a time the entry
// does not carry — and VerifyLedgerDir says so.
//
// Older entries keep verifying under their own rules; they are not
// rewritten, because rewriting a record to fix a record is the thing this
// whole directory exists to make impossible. VerifyLedgerDir notes on every
// pre-4 entry that its signer name is self-reported.
const (
	LedgerFileFormat = "corral-ledger-4"
	ledgerFormat3    = "corral-ledger-3"
	ledgerFormat2    = "corral-ledger-2"
)

// knownLedgerFormat is what a reader accepts.
func knownLedgerFormat(f string) bool {
	return f == LedgerFileFormat || f == ledgerFormat3 || f == ledgerFormat2
}

// keyIDIsHashed reports whether this format covers keyid in the hashed
// bytes. Pre-4 entries carry an unauthenticated signer label.
func keyIDIsHashed(format string) bool { return format != ledgerFormat3 && format != ledgerFormat2 }

// LedgerSigner signs an entry's hash. cmd/corral supplies one from the
// local certify key when it exists; nil writes an unsigned entry.
type LedgerSigner interface {
	Sign(hash []byte) (keyID string, signature []byte, err error)
	// SigningKeyID names the key BEFORE the entry is hashed. From
	// corral-ledger-4 the signer's name is inside the hashed bytes, so it
	// has to be known first; taking it from Sign's return value would mean
	// hashing bytes that do not yet contain it, which is how the name came
	// to be editable in the first place.
	SigningKeyID() string
}

// Ed25519LedgerSigner is the stock signer.
type Ed25519LedgerSigner struct {
	KeyID string
	Key   ed25519.PrivateKey
}

func (s Ed25519LedgerSigner) Sign(hash []byte) (string, []byte, error) {
	return s.KeyID, ed25519.Sign(s.Key, hash), nil
}

func (s Ed25519LedgerSigner) SigningKeyID() string { return s.KeyID }

// EntryShapeProblem is the ONE rule for what an entry of each kind must
// carry, used by the writer (placeEntry refuses to place what it names) and
// by the verifier (a chain check reports it). It used to live in the
// verbs only — WriteAdjudication, WriteRetraction, WriteReview — so an
// entry that reached placeEntry another way (`corral ledger append` of a
// file written elsewhere) was placed, signed and verified with no finding
// id, an empty verdict and nobody deciding: the rule at one door and not
// the other (review 559719120ef0#R1, Codex reviewing, Claude Code
// verifying). "" when the entry is well-formed for its kind.
func EntryShapeProblem(e LedgerEntry) string {
	switch e.Kind {
	case KindScan:
		return ""
	case KindRetract:
		if strings.TrimSpace(e.Retracts) == "" || strings.TrimSpace(e.Reason) == "" {
			return "a retraction names the entry it retracts and a reason"
		}
	case KindCheckpoint:
		if e.Checkpoint == nil || e.Checkpoint.Head == "" {
			return "a checkpoint entry that names no replaced head"
		}
	case KindReview:
		if e.Review == nil {
			return "a review entry that carries no review"
		}
		if strings.TrimSpace(e.Review.Commit) == "" || strings.TrimSpace(e.Review.Scope) == "" {
			return "a review names a commit and a scope"
		}
	case KindAdjudication:
		if e.Adjudication == nil {
			return "an adjudication entry that carries no adjudication"
		}
		a := e.Adjudication
		if _, id, ok := cutRef(a.Adjudicates); !ok || strings.TrimSpace(id) == "" {
			return "an adjudication names a finding as <review hash>#<finding id>"
		}
		if a.Verdict != VerdictConfirmed && a.Verdict != VerdictRefuted {
			return fmt.Sprintf("an adjudication's verdict is %s or %s, not %q", VerdictConfirmed, VerdictRefuted, a.Verdict)
		}
		if strings.TrimSpace(a.By) == "" || strings.TrimSpace(a.Reason) == "" {
			return "an adjudication names who decided and why"
		}
	default:
		return fmt.Sprintf("an entry of unknown kind %q", e.Kind)
	}
	return ""
}

// EntryHash is what Hash holds. From corral-ledger-3: sha256 over the
// entry's FULL canonical JSON — its bytes as written (Raw, when a reader
// set it; this binary's marshalling of e otherwise, which is what the
// writer puts on disk) with the hash, keyid and signature keys removed.
// corral-ledger-2: sha256 over the canonical SPARSE JSON of the struct
// with those fields cleared, as those entries were written.
func EntryHash(e LedgerEntry) (string, error) {
	if e.Format == ledgerFormat2 {
		e.Hash, e.KeyID, e.Signature, e.Raw = "", "", "", nil
		js, err := CanonicalSparseJSON(e)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(js)
		return hex.EncodeToString(sum[:]), nil
	}
	raw := e.Raw
	if raw == nil {
		e.Hash, e.Signature = "", ""
		if !keyIDIsHashed(e.Format) {
			e.KeyID = ""
		}
		var err error
		if raw, err = json.Marshal(e); err != nil {
			return "", err
		}
	}
	js, err := canonicalEntryBytes(raw, keyIDIsHashed(e.Format))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(js)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalEntryBytes is CanonicalFullJSON over an entry with the three
// fields the hash cannot contain removed from the tree itself, so the
// writer (which has them empty) and the reader (which has them filled)
// hash the same bytes.
func canonicalEntryBytes(raw []byte, keepKeyID bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("auditpush: entry is not a JSON object: %w", err)
	}
	delete(tree, "hash")
	delete(tree, "signature")
	if !keepKeyID {
		// Pre-corral-ledger-4: keyid sat outside the hash, so the signer
		// name was editable without breaking anything. Kept for those
		// entries only, so they still verify as written.
		delete(tree, "keyid")
	}
	full, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	return CanonicalFullJSON(full)
}

// writeLedgerFile is the directory half of PushBundle: the same
// canonicalised bundle the warehouse receives, as one signed, linked entry.
func writeLedgerFile(dir string, b Bundle, signer LedgerSigner) (Counts, error) {
	// Stamp the verb on a scan row that EXISTS: stamping an empty row made
	// it non-empty, and a bundle with no scan header grew a phantom
	// corral_scans row (review e00b52bab444#R1, Gemini reviewing, Codex
	// verifying).
	if b.Scan != (ScanRow{}) && b.Scan.PushedBy == "" {
		b.Scan.PushedBy = PushedByCertify
	}
	// The uid is minted where Pushed is stamped — placeEntry — so the two
	// agree by construction and a reader can derive one from the other.
	e := LedgerEntry{Format: LedgerFileFormat, Bundle: b}
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
	//
	// The head's hash is RECOMPUTED before it is used as the link. Taking
	// the stored hash on trust means a head whose bytes were edited (and
	// whose stored hash therefore no longer describes it) is accepted as a
	// parent, and the new entry's signature then vouches for a tampered
	// predecessor — corral extending, and signing, a chain a verifier would
	// already refuse. Found by a cold review, 2026-09-08 (R6).
	prev := ""
	if existing, err := ReadLedgerDir(dir); err != nil {
		return "", err
	} else if n := len(existing); n > 0 {
		// The WHOLE chain, not just the head. Re-hashing only the head let a
		// chain with an edited middle entry be extended and signed while the
		// error text this would have printed spoke of "a chain that is
		// already broken" — the check narrower than the claim it made. Caught
		// by a second cold review of this package on the first fix.
		if err := RequireIntactChain(dir); err != nil {
			return "", fmt.Errorf("%w — refusing to append, which would sign this chain as the new entry's history", err)
		}
		prev = existing[n-1].Hash
	}
	return placeEntry(dir, e, prev, signer)
}

// placeEntry hashes, signs and writes e linked to prev. It is the one
// writer of entry files: AppendLedgerEntry links to the head, and
// WriteCheckpoint places a genesis.
func placeEntry(dir string, e LedgerEntry, prev string, signer LedgerSigner) (string, error) {
	if p := EntryShapeProblem(e); p != "" {
		return "", fmt.Errorf("auditpush: refusing to place %s", p)
	}
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
	if e.IsScan() && e.Bundle.Scan != (ScanRow{}) {
		// The uid derives from the scan row and THIS Pushed — the promise
		// RecomputeScanUID makes — so an entry re-linked elsewhere is
		// re-identified by that placement, and the identity stays checkable.
		// (Under corral-ledger-2 the uid was minted from an earlier clock
		// the entry did not carry, so it never recomputed: ed079ca08965#R2.)
		e.ScanUID = scanUID(e.Bundle.Scan, e.Pushed)
		e.Bundle.Scan.ScanUID = e.ScanUID
	}
	e.Hash, e.KeyID, e.Signature, e.Raw = "", "", "", nil
	// The signer names itself BEFORE the hash is taken, because from
	// corral-ledger-4 the name is part of what is hashed and signed. Set it
	// after hashing and the name would sit outside the hash again, which is
	// precisely the hole this format closes.
	if signer != nil {
		e.KeyID = signer.SigningKeyID()
	}
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
		if keyID != e.KeyID {
			return "", fmt.Errorf("auditpush: the signer named itself %q before hashing and %q when signing — the hashed bytes would not describe the key that signed them", e.KeyID, keyID)
		}
		e.Signature = hex.EncodeToString(sig)
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
	case KindReview, KindAdjudication:
		// Its own hash: two reviews of one commit, or two verdicts on one
		// review, can land in the same second, and a name that collided
		// would refuse the second entry (the commit is inside the entry).
		middle, tail = e.Kind, e.Hash
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
	e.Raw, e.File = raw, filepath.Base(path)
	if !knownLedgerFormat(e.Format) {
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
	// Hash is the entry's own hash, so a caller can compare the chain's HEAD
	// against an anchor held outside the directory. Truncation from the end
	// leaves every surviving link valid and is invisible from within.
	Hash    string
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
	// Whether this chain is a signed one at all: an entirely unsigned ledger
	// is honest (no key was configured), but a single unsigned entry among
	// signed ones is a hole, not a style.
	anySigned := false
	for _, e := range entries {
		if e.Signature != "" {
			anySigned = true
			break
		}
	}
	var out []ChainCheck
	prevHash := ""
	seen := map[string]bool{}
	seenReview := map[string]bool{}
	for i, e := range entries {
		c := ChainCheck{ScanUID: e.ScanUID, Hash: e.Hash, Commit: e.Bundle.Scan.Commit, KeyID: e.KeyID, Genesis: i == 0, Kind: e.Kind, File: e.File}
		h, herr := EntryHash(e)
		c.HashOK = herr == nil && h == e.Hash
		c.LinkOK = e.Prev == prevHash
		c.Signed = e.Signature != ""
		if c.Signed && pub != nil {
			raw, _ := hex.DecodeString(e.Hash)
			sig, _ := hex.DecodeString(e.Signature)
			c.SigOK = ed25519.Verify(pub, raw, sig)
		}
		uidOK := true
		if e.Format != ledgerFormat2 && e.IsScan() && e.Bundle.Scan != (ScanRow{}) {
			uidOK = e.ScanUID == scanUID(e.Bundle.Scan, e.Pushed) && e.Bundle.Scan.ScanUID == e.ScanUID
		}
		switch {
		case !c.HashOK:
			c.Problem = "entry bytes do not match its hash — edited after it was written"
		case !uidOK:
			c.Problem = "scan_uid does not derive from the entry's scan row and pushed time — the identity was altered, or minted elsewhere"
		case !c.LinkOK:
			c.Problem = fmt.Sprintf("prev %.12s does not name the previous entry (%.12s) — an entry was removed, reordered or inserted", e.Prev, prevHash)
		case c.Signed && pub != nil && !c.SigOK:
			c.Problem = "signature does not verify under the given key"
		case !c.Signed && anySigned:
			// A MISSING signature was never a problem, only an invalid one —
			// so deleting a signature outright passed, in a chain where
			// every other entry carries one. Removing evidence must not be a
			// way to pass a check that having bad evidence fails. Judged
			// against the chain itself: a wholly unsigned ledger (no key was
			// ever configured) is a different, honest thing and still
			// verifies. NO KEY IS NEEDED for this: "some entries here are
			// signed and this one is not" is a fact about the chain, not
			// about a key. Gating it on pub != nil, as the round-four fix
			// did, meant a stripped signature still passed for anyone
			// verifying without one. (Round five, R1.)
			c.Problem = "unsigned, in a chain whose other entries are signed — a signature was removed, or this entry was written by something that could not sign"
		case e.Kind == KindCheckpoint && i != 0:
			c.Problem = fmt.Sprintf("a checkpoint at position %d — a checkpoint is a genesis and stands only at the start of a chain", i+1)
		case EntryShapeProblem(e) != "":
			c.Problem = EntryShapeProblem(e)
		case e.Kind == KindCheckpoint:
			c.Note = fmt.Sprintf("chain begins at a checkpoint: %d earlier entries (through %s, head %.12s) are not present", e.Checkpoint.Entries, e.Checkpoint.Through.UTC().Format("2006-01-02"), e.Checkpoint.Head)
		case e.Kind == KindRetract && !seen[e.Retracts]:
			c.Problem = fmt.Sprintf("retracts %.12s, which is not an earlier entry of this chain", e.Retracts)
		case e.Kind == KindRetract:
			c.Note = fmt.Sprintf("retracts %.12s: %s", e.Retracts, e.Reason)
		case e.Kind == KindReview:
			rep, cr, hy := e.Review.Counts()
			c.Commit = e.Review.Commit
			c.Note = fmt.Sprintf("review of %s by %s: %d reproduced, %d code-read, %d hypothesis", e.Review.Scope, e.Review.ReviewerModel, rep, cr, hy)
		case e.Kind == KindAdjudication && !seenReview[strings.SplitN(e.Adjudication.Adjudicates, "#", 2)[0]]:
			// The target must be a REVIEW. `seen` held every entry hash, so
			// an adjudication naming a scan — or another adjudication —
			// passed as though a finding had been ruled on. A ruling on
			// something that has no findings is not a ruling.
			c.Problem = "an adjudication of a finding whose review is not an earlier entry of this chain"
		case e.Kind == KindAdjudication:
			c.Note = fmt.Sprintf("%s %s by %s: %s", e.Adjudication.Verdict, shortRef(e.Adjudication.Adjudicates), e.Adjudication.By, e.Adjudication.Reason)
		}
		if e.Format == ledgerFormat2 && c.Problem == "" {
			c.Note = strings.TrimSpace(c.Note + " · " + ledgerFormat2 + ": hashed in the sparse form (a recorded false or zero is not distinguished from an absent one) and its scan_uid is not derivable from the entry")
			c.Note = strings.TrimPrefix(c.Note, "· ")
		}
		// Pre-corral-ledger-4 the signer NAME is not covered by the hash, so
		// it is a label the entry asserts about itself rather than something
		// the signature vouches for. Say so, rather than printing it as
		// though it were established.
		if !keyIDIsHashed(e.Format) && c.Signed && c.Problem == "" {
			c.Note = strings.TrimPrefix(strings.TrimSpace(c.Note+" · signer name is self-reported: "+e.Format+" does not cover keyid in the hash, so it is not vouched for by the signature (the signature itself is checked)"), "· ")
		}
		out = append(out, c)
		prevHash = e.Hash
		seen[e.Hash] = true
		if e.Kind == KindReview {
			seenReview[e.Hash] = true
		}
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

// RequireIntactChain refuses a ledger directory that does not verify.
//
// It exists because the guard kept being added ONE DOOR AT A TIME. Round one
// of a cold review found `checkpoint` pruning without verifying; round three
// found `append` checking only the head; round four found that `push` and
// `LoadDir` — the two doors that carry the record OUT, to a warehouse and to
// the UI — never verified at all. Four doors, three separate fixes, and the
// same rule.
//
// WHO CALLS IT, precisely — the earlier wording here said "every door",
// which was itself false and was reproduced as a finding (round five, R3):
//
//   - WriteCheckpoint  — it DELETES; it must look first.
//   - AppendLedgerEntry — a new entry signs the chain as its history.
//   - PushLedgerDir    — carries the record out to a warehouse.
//   - LoadDir          — serves the record to the UI and every --db reader.
//   - prior.Load       — feeds a LATER audit's priors, so a tampered chain
//     would steer a run that has not happened yet.
//
// The remaining ReadLedgerDir callers are narrow VIEWS — `scans list`, the
// verdict cache, a review lookup — which read one field and change nothing.
// They are not guarded, deliberately: verification is O(chain) and a listing
// that re-hashes every entry on every invocation is a different kind of
// wrong. `corral ledger verify` is the command that says whether the chain
// holds, and the writers refuse to build on one that does not.
//
// Signatures are not checked here: the callers do not all hold a key, and a
// missing key must not read as a broken chain. Hashes, links, shape and
// ordering are, which is what catches edited, removed or reordered bytes.
func RequireIntactChain(dir string) error {
	checks, err := VerifyLedgerDir(dir, nil)
	if err != nil {
		return err
	}
	for _, c := range checks {
		if c.Problem != "" {
			return fmt.Errorf("auditpush: %s does not verify (%s: %s)", dir, c.File, c.Problem)
		}
	}
	return nil
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
	// A checkpoint is the one verb that DESTROYS evidence, so it is the one
	// verb that must look at it first. Pruning without verifying launders a
	// chain: the tampered entry is deleted and the genesis that replaces it
	// verifies clean, with nothing left to say the record was ever broken.
	// Signatures are not checked here (no key is in hand); hashes and links
	// are, which is what catches edited or reordered bytes. Found by a cold
	// review, 2026-09-08 (R1, reproduced).
	if err := RequireIntactChain(dir); err != nil {
		return "", 0, fmt.Errorf("%w — refusing to prune, because a checkpoint over a broken chain deletes the evidence and leaves a genesis that verifies clean", err)
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
	removed := 0
	for _, n := range names {
		if n == name {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ScansSubdir, n)); err != nil {
			// Report what was ACTUALLY removed. Returning 0 here told the
			// operator "0 earlier entries pruned" for a directory already
			// partly emptied. (R10.)
			return name, removed, fmt.Errorf("auditpush: pruning %s after removing %d: %w (the checkpoint is placed; the chain will verify as broken until the pruned entries are gone)", n, removed, err)
		}
		removed++
	}
	return name, len(entries), nil
}

// Retracted returns the hashes of every entry a KindRetract entry in
// entries names.
func Retracted(entries []LedgerEntry) map[string]LedgerEntry {
	// A retraction that has ITSELF been retracted is not in force, so the
	// entry it named comes back — retract, undo, redo, and the record
	// alternates. Resolved in ONE REVERSE PASS: a retraction can only name
	// an EARLIER entry, so walking from the newest backwards means every
	// retraction that could void this one has already been decided.
	//
	// The first fix for this grew a monotone "void" set forward, which is
	// wrong because voiding is not monotone — once a retraction is voided,
	// everything it voided comes back — and it reported the original entry
	// as NOT retracted at odd depth >= 3. Caught by a second cold review of
	// this package, 2026-09-08, on the fix for the first one.
	targeted := map[string]bool{} // named by an in-force retraction seen later in the chain
	inForce := make([]LedgerEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind != KindRetract {
			continue
		}
		if targeted[e.Hash] {
			continue // a later, in-force retraction retracted this one
		}
		inForce = append(inForce, e)
		targeted[e.Retracts] = true
	}
	out := map[string]LedgerEntry{}
	for _, e := range inForce {
		out[e.Retracts] = e
	}
	return out
}

// ScanEntries is the record as a reader should see it: the scan entries,
// in chain order, with retracted ones left out. Every reader of the record
// — the view, the prior, the verdict cache — goes through this, so a
// retraction takes effect everywhere at once.
func ScanEntries(entries []LedgerEntry) []LedgerEntry {
	var out []LedgerEntry
	for _, e := range LiveEntries(entries) {
		if e.IsScan() {
			out = append(out, e)
		}
	}
	return out
}

// LiveEntries is every entry of every kind that stands: retracted entries
// left out, and an adjudication of a retracted review left out with it.
// A retraction used to reach scan entries only, so a retracted review's
// rows and findings still loaded into the view and pushed to a warehouse
// (ed079ca08965#R3); the view and the push go through this.
func LiveEntries(entries []LedgerEntry) []LedgerEntry {
	retracted := Retracted(entries)
	var out []LedgerEntry
	for _, e := range entries {
		if _, gone := retracted[e.Hash]; gone {
			continue
		}
		if e.Kind == KindAdjudication && e.Adjudication != nil {
			if h, _, ok := cutRef(e.Adjudication.Adjudicates); ok {
				if _, gone := retracted[h]; gone {
					continue
				}
			}
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
		if !knownLedgerFormat(f.Format) {
			return nil, fmt.Errorf("auditpush: %s: format %q, want %q", e.Name(), f.Format, LedgerFileFormat)
		}
		f.Raw, f.File = raw, e.Name()
		out = append(out, f)
	}
	// Chain order is Pushed order; the file name rides WITH its entry
	// (File), never re-paired by a second sort — names carry seconds,
	// Pushed microseconds, and two entries inside one second sorted
	// differently by each (ed079ca08965#R5).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pushed.Before(out[j].Pushed) })
	return out, nil
}

// LoadDir replays a ledger directory into an in-memory DuckDB with the
// warehouse's schema — the view. Every bundle is inserted under the
// scan_uid and timestamp it was pushed with, so statement checks and joins
// see the same identities a warehouse would.
func LoadDir(dir string) (*sql.DB, error) {
	// The record must not be SERVED from a chain that does not verify: this
	// is what the UI, `verify --db` and every reader open. (R1, round four.)
	if err := RequireIntactChain(dir); err != nil {
		return nil, fmt.Errorf("%w — refusing to load a record that does not verify", err)
	}
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
	for _, ddl := range schemaDDL {
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
	// left out (ScanEntries); every review and adjudication entry into
	// the review grains, scripts and outputs included — the directory
	// holds them, so the view over it does.
	files = LiveEntries(files)
	for _, f := range files {
		switch f.Kind {
		case KindReview:
			if _, err := insertReviewEntry(db, f, true); err != nil {
				db.Close()
				return nil, err
			}
		case KindAdjudication:
			if err := insertAdjudicationEntry(db, f); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	files = ScanEntries(files)
	for _, f := range files {
		if _, err := insertBundle(db, f.Bundle, f.Pushed, f.ScanUID); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
