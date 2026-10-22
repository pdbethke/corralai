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

	"github.com/pdbethke/corralai/internal/dirlock"
	"github.com/pdbethke/corralai/internal/ledger"
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

// The entry types, kinds, formats and the pure rules over an entry live in
// internal/ledger, which builds for the browser. Everything here is an alias
// or a thin wrapper, so no caller, test or signed ledger changes.
type (
	LedgerEntry  = ledger.Entry
	Adjudication = ledger.Adjudication
	Checkpoint   = ledger.Checkpoint
)

const (
	KindScan         = ledger.KindScan
	KindRetract      = ledger.KindRetract
	KindCheckpoint   = ledger.KindCheckpoint
	KindReview       = ledger.KindReview
	KindAdjudication = ledger.KindAdjudication

	VerdictConfirmed = ledger.VerdictConfirmed
	VerdictRefuted   = ledger.VerdictRefuted

	LedgerFileFormat = ledger.FileFormat
	ledgerFormat4    = ledger.Format4
	ledgerFormat3    = ledger.Format3
	ledgerFormat2    = ledger.Format2
)

func knownLedgerFormat(f string) bool  { return ledger.KnownFormat(f) }
func keyIDIsHashed(format string) bool { return ledger.KeyIDIsHashed(format) }

// EntryShapeProblem is the one rule for what an entry of each kind must
// carry; see ledger.EntryShapeProblem.
func EntryShapeProblem(e LedgerEntry) string { return ledger.EntryShapeProblem(e) }

// EntryHash is what Hash holds; see ledger.EntryHash.
func EntryHash(e LedgerEntry) (string, error) { return ledger.EntryHash(e) }

// ledgerFile is the historical name; kept as the alias readers use.
type ledgerFile = LedgerEntry

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
	name, err := AppendLedgerEntry(dir, e, signer)
	if err != nil {
		return Counts{}, err
	}
	keyID := ""
	if signer != nil {
		keyID = signer.SigningKeyID()
	}
	return Counts{Scans: boolToInt(b.Scan != (ScanRow{})), Files: len(b.Files), Mutants: len(b.Mutants), Calls: len(b.Calls), Events: len(b.Events),
		Entry: name, KeyID: keyID}, nil
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
	// The directory is created here, before the lock, because the lock is
	// taken ON it; placeEntry would have created it a moment later anyway.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("auditpush: ledger dir: %w", err)
	}
	unlock, err := lockLedgerDir(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
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
		if err := requireRegisteredSigner(existing, signer); err != nil {
			return "", err
		}
	}
	return placeEntry(dir, e, prev, signer)
}

// lockLedgerDir takes dir's exclusive writer lock and returns its release.
// It is held from reading the head through placing the entry — by
// AppendLedgerEntry, and by WriteCheckpoint, which rewrites the directory —
// because "Prev is the head at placement time" is only true if nobody else
// can place between the read and the write. Without it, two runs appending
// to one machine ledger both linked to the same head and the chain was
// broken for good: every later append was refused (final whole-branch
// review, 2026-10-20; 8 concurrent pushes broke it in 3 of 3 runs).
//
// The lock is advisory and kernel-held (internal/dirlock), so a writer that
// dies mid-append releases it, and on unix it creates no file in the
// directory — nothing for a reader to skip, nothing for the `git add -A`
// that publishes a ledger branch to commit. It excludes corral's own
// writers; a person copying files into scans/ by hand is not excluded, and
// RequireIntactChain is what catches that.
//
// The release's error is dropped on purpose: by then the entry is placed or
// the append has already failed, and closing the descriptor releases the
// lock regardless, so reporting it would only misdescribe a write that
// succeeded.
func lockLedgerDir(dir string) (func(), error) {
	unlock, err := dirlock.Lock(dir)
	if err != nil {
		return nil, fmt.Errorf("auditpush: lock ledger dir: %w", err)
	}
	return func() { _ = unlock() }, nil
}

// requireRegisteredSigner is the writer-side refusal to append with a key a
// signer ledger never registered. It exists to keep an HONEST operator from
// writing an entry the verifier would reject — a laptop key nobody added, or
// an unsigned entry on a ledger that now requires signatures — and it is not
// the security boundary. It reads the set through ActiveSigners, which is a
// keyless, name-based read: a signer entry that merely names the root's keyid
// is accepted by it, so anyone who can write the directory can get past this
// guard. Whether a key really was registered is answered only by
// VerifyLedgerDir under the root key (--pub), and that is where a forged
// registration is caught.
//
// A ledger with no signer entries and no signer-carrying checkpoint skips the
// guard entirely, so a stranger's keyless chain, and every ledger that has
// never used signers, append exactly as before. The root is always allowed:
// it is how a key is registered in the first place.
func requireRegisteredSigner(existing []LedgerEntry, signer LedgerSigner) error {
	rootID, active := ActiveSigners(existing)
	if rootID == "" {
		return nil
	}
	if signer == nil {
		return fmt.Errorf("auditpush: this ledger registers its signers, so the verifier reports an unsigned entry as UNSIGNED among signed ones; sign with the root key or a registered one (register a machine key with `corral ledger signer add`)")
	}
	id := signer.SigningKeyID()
	if id == rootID {
		return nil
	}
	for _, k := range active {
		if k.KeyID == id {
			return nil
		}
	}
	return fmt.Errorf("auditpush: key %s is not registered on this ledger, so the verifier would refuse its entries; register it with `corral ledger signer add` (signed by the root key) first", id)
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
		// Format 5 names keys by fingerprint; the verifier calls this
		// spelling a Problem, so the writer refuses it rather than place an
		// entry its own reader rejects.
		if e.KeyID == LegacyRootKeyID {
			return "", fmt.Errorf("auditpush: refusing to place %s", legacyKeyIDProblem)
		}
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
	case KindSigner:
		// Its own hash, as for reviews below. The tail was the key's
		// fingerprint, which read nicely and collided: a register and a
		// revoke of one key inside one second share a name, and the second
		// was refused ("already exists").
		middle, tail = "signer", e.Hash
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

// ReadLedgerEntry reads one entry file (plain or gzipped). The parsing is
// ledger.ParseEntry's, shared with the browser reader, so the two cannot
// disagree about what an entry is.
func ReadLedgerEntry(path string) (LedgerEntry, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a file under the ledger directory the operator named
	if err != nil {
		return LedgerEntry{}, err
	}
	return ledger.ParseEntry(filepath.Base(path), raw)
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
	Hash   string
	Commit string
	HashOK bool // the stored Hash matches the entry's bytes
	LinkOK bool // Prev names the previous entry's Hash (true for a genesis entry)
	Signed bool // the entry carries a signature
	SigOK  bool // the signature verifies under the key resolved for KeyID (false when unsigned, unresolved or no pub given)
	KeyID  string
	// Signer is who the chain says holds KeyID at this entry: "root", the
	// name a register entry gave the key, or "" (unsigned, or a key the
	// chain does not hold here). Filled with or without a key; only SigOK
	// says the signature was checked.
	Signer  string
	Problem string // "" when nothing is wrong
	Genesis bool
	// Kind is the entry's kind; Note says what a retraction or checkpoint
	// entry means for the chain, in words, when nothing is wrong with it.
	Kind string
	Note string
}

// VerifyLedgerDir walks the chain: every entry's hash against its bytes,
// every Prev against its predecessor, every signature against the key the
// chain held for its keyid at that point (when pub, the ROOT key, is
// given). It never stops at the first failure — a reader wants the whole
// picture — and it never calls an unsigned entry verified.
//
// Until corral-ledger-5 every signature was checked against pub alone: one
// key for the whole ledger, and keyid a label. Now pub is the root, and the
// root's signer entries say which other keys may sign from where on, so a
// second machine signs under its own key instead of holding a copy of the
// root's. A key is good only between its registration and its revocation;
// an entry it signed outside that window is a Problem, whatever its
// signature says.
func VerifyLedgerDir(dir string, pub ed25519.PublicKey) ([]ChainCheck, error) {
	entries, err := ReadLedgerDir(dir)
	if err != nil {
		return nil, err
	}
	checks, _ := verifyEntries(entries, pub)
	return checks, nil
}

// verifyEntries is THE walk: VerifyLedgerDir's checks, and the signer set
// it leaves at the head, which is what ActiveSigners returns. One loop for
// both, so a reader and a writer can never disagree about who was
// registered when.
func verifyEntries(entries []LedgerEntry, pub ed25519.PublicKey) ([]ChainCheck, *signerSet) {
	set := newSignerSet(entries, pub)
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
		// The key is resolved BEFORE the signature is checked, from the set
		// as it stands at this entry — never from the entry's own claim.
		name, key, keyProblem := set.resolve(e)
		c.Signer = name
		if c.Signed && key != nil {
			raw, _ := hex.DecodeString(e.Hash)
			sig, _ := hex.DecodeString(e.Signature)
			c.SigOK = ed25519.Verify(key, raw, sig)
		}
		shapeProblem := EntryShapeProblem(e)
		changeProblem := ""
		if e.Kind == KindSigner && shapeProblem == "" {
			changeProblem = set.changeProblem(e)
		}
		seedProblem := ""
		if e.Kind == KindCheckpoint && i == 0 && shapeProblem == "" {
			seedProblem = set.seedProblem(e)
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
		case seedProblem != "" && seedProblem == errCheckpointNotRoot:
			// Before keyProblem: a checkpoint by a non-root key is not "an
			// unregistered key" — the set is empty at a genesis — it is a
			// genesis claiming authority to hand out keys.
			c.Problem = seedProblem
		case keyProblem != "":
			c.Problem = keyProblem
		case c.Signed && key != nil && !c.SigOK && name == "root":
			c.Problem = "signature does not verify under the given key"
		case c.Signed && key != nil && !c.SigOK:
			c.Problem = fmt.Sprintf("signature does not verify under the key registered as %s (%s)", name, e.KeyID)
		case e.Kind == KindCheckpoint && i != 0:
			c.Problem = fmt.Sprintf("a checkpoint at position %d — a checkpoint is a genesis and stands only at the start of a chain", i+1)
		case shapeProblem != "":
			c.Problem = shapeProblem
		case seedProblem != "":
			c.Problem = seedProblem
		case changeProblem != "":
			c.Problem = changeProblem
		case e.Kind == KindSigner:
			c.Note = changeNote(e.Signer)
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
		// UNSIGNED among signed entries: disclosed, NOT a failure.
		//
		// It was a Problem for one round. That refused a legitimate chain —
		// one that gained a certify key partway, which is what a machine
		// without a key produces and is exactly how CI runs — and because
		// RequireIntactChain now guards checkpoint, append, push and
		// LoadDir, a mixed chain became unusable at all four doors. Caught
		// by CI, which has no key, while every local run passed because this
		// machine has one.
		//
		// A missing signature and a REMOVED one are indistinguishable from
		// inside the file, so the honest thing is to say what is true — this
		// entry is unsigned and its neighbours are not — and let the reader
		// judge. An over-strict fix is worse than the finding it closes; that
		// is now twice in one day.
		if !c.Signed && anySigned && c.Problem == "" {
			c.Note = strings.TrimPrefix(strings.TrimSpace(c.Note+" · UNSIGNED, in a chain whose other entries are signed — it was written where no certify key was configured, or a signature was removed; the chain itself is intact either way"), "· ")
		}
		if !keyIDIsHashed(e.Format) && c.Signed && c.Problem == "" {
			c.Note = strings.TrimPrefix(strings.TrimSpace(c.Note+" · signer name is self-reported: "+e.Format+" does not cover keyid in the hash, so it is not vouched for by the signature (the signature itself is checked)"), "· ")
		}
		// The set moves only on a signer entry that checked CLEAN, and only
		// after its own check: the change cannot vouch for itself, and a
		// tampered or forged one leaves the set as it was.
		if e.Kind == KindSigner && c.Problem == "" {
			set.apply(e.Signer)
		}
		if e.Kind == KindCheckpoint && i == 0 && c.Problem == "" {
			set.seed(e.Checkpoint.Signers)
		}
		out = append(out, c)
		prevHash = e.Hash
		seen[e.Hash] = true
		if e.Kind == KindReview {
			seenReview[e.Hash] = true
		}
	}
	return out, set
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

// ChainHasSigners reports whether the chain registers signers at all: a
// signer entry, or a leading checkpoint that carries a set.
func ChainHasSigners(entries []LedgerEntry) bool {
	for i, e := range entries {
		if e.Kind == KindSigner {
			return true
		}
		if i == 0 && e.Kind == KindCheckpoint && e.Checkpoint != nil && len(e.Checkpoint.Signers) > 0 {
			return true
		}
	}
	return false
}

// WriteCheckpoint replaces every entry in dir with one KindCheckpoint
// genesis that names the head it stood in for. The pruned files are
// DELETED — that is the point of a checkpoint — after the checkpoint is
// safely placed, so a failure midway leaves a chain the verifier reports
// (a checkpoint not at position 1), never an empty directory. Returns the
// checkpoint's file name and how many entries were pruned.
func WriteCheckpoint(dir string, signer LedgerSigner, rootPub ed25519.PublicKey) (string, int, error) {
	// Locked like an append (lockLedgerDir): a run appending while the prune
	// deletes would place an entry linked to a head that is about to vanish.
	// A missing directory is not created to be locked; it has no entries,
	// which the read below reports.
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		unlock, err := lockLedgerDir(dir)
		if err != nil {
			return "", 0, err
		}
		defer unlock()
	}
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
	// The set the pruned register/revoke entries left at the head travels in
	// the checkpoint (see Checkpoint.Signers), and the root's signature on
	// the checkpoint turns that set into an authorization. So on a chain
	// that has signers, the set must come from a walk the ROOT KEY vouches
	// for: the keyless walk accepts a signer entry by name, so carrying its
	// set would let anyone who can write the directory (no key needed) have
	// a forged registration signed by the root at the next prune. Every
	// refusal here precedes the first write or delete. nil, not empty, when
	// there is no set, so the JSON gains no key.
	var carried []SignerKey
	if ChainHasSigners(entries) {
		if rootPub == nil {
			return "", 0, fmt.Errorf("auditpush: %s registers signers, so a checkpoint needs the root public key to vouch for them (none was given) — refusing to prune", dir)
		}
		if signer == nil || signer.SigningKeyID() != KeyFingerprint(rootPub) {
			return "", 0, fmt.Errorf("auditpush: a checkpoint over a chain with signers must be signed by the root (%s) — refusing to prune, because the checkpoint would be a permanent Problem under the root key", KeyFingerprint(rootPub))
		}
		// The keyid is only a label the signer reports; an API caller can
		// attach the root's fingerprint to another key. Prove the key signs
		// as the root BEFORE anything is placed or deleted, or the prune
		// would leave a genesis the root key rejects, permanently.
		probeSum := sha256.Sum256([]byte("corral-ledger-checkpoint-root-probe-v1"))
		_, probeSig, perr := signer.Sign(probeSum[:])
		if perr != nil || !ed25519.Verify(rootPub, probeSum[:], probeSig) {
			return "", 0, fmt.Errorf("auditpush: the signer claims the root keyid (%s) but its key does not sign as the root — refusing to prune, because the checkpoint would be a permanent Problem under the root key", KeyFingerprint(rootPub))
		}
		checks, set := verifyEntries(entries, rootPub)
		for _, c := range checks {
			if c.Problem != "" {
				return "", 0, fmt.Errorf("auditpush: %s does not verify under the root key (%s: %s) — refusing to prune, because a checkpoint would sign a set the root never authorized", dir, c.File, c.Problem)
			}
		}
		carried = set.list()
		if len(carried) == 0 {
			carried = nil
		}
	}
	cp := LedgerEntry{Kind: KindCheckpoint, Checkpoint: &Checkpoint{Head: head.Hash, Entries: len(entries), Through: head.Pushed, Signers: carried}}
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
		raw, err := os.ReadFile(filepath.Join(scans, e.Name())) // #nosec G304 -- a file under the ledger directory the operator named
		if err != nil {
			return nil, err
		}
		f, err := ledger.ParseEntry(e.Name(), raw)
		if err != nil {
			return nil, err
		}
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
