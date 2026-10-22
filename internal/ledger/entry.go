// SPDX-License-Identifier: Elastic-2.0

// Package ledger is the pure half of the signed record: the entry types, how
// an entry's bytes are read and canonically hashed, and the shape rule for
// each kind. It imports no database, keyring, process or filesystem package,
// so it builds for GOOS=js GOARCH=wasm and a browser can read a ledger with
// the same code the CLI verifies it with. Writing a ledger, locking its
// directory and verifying its chain stay in internal/auditpush.
package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/reviewrecord"
)

// Entry is the on-disk form: the bundle plus the push stamp, the
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
// Signature is Ed25519 over Hash's bytes, KeyID naming the key: from
// corral-ledger-5 its fingerprint (KeyFingerprint), resolved by the
// verifier to the root or a registered signer; through corral-ledger-4 the
// convention name "corral-certify", which means the root (LegacyRootKeyID).
// Unsigned entries are legal (no key configured) and are SAID to be, by
// every reader: a chain still orders them; only a signature says who.
type Entry struct {
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
	// signature vouches for. See internal/reviewrecord.
	Review *reviewrecord.Review `json:"review,omitempty"`
	// Adjudication (KindAdjudication) is a person's verdict on one finding
	// — Adjudicates names it as <review entry hash>#<finding id> — with
	// who decided and why. Automatic passes never write these; a later one
	// by the same or another person is a new entry, and the newest stands.
	Adjudication *Adjudication `json:"adjudication,omitempty"`
	// Signer (KindSigner) registers or revokes a signing key. See signer.go.
	Signer *SignerChange `json:"signer,omitempty"`
	// Hash and Signature are computed over everything above.
	Hash      string `json:"hash,omitempty"`
	KeyID     string `json:"keyid,omitempty"`
	Signature string `json:"signature,omitempty"` // hex Ed25519 over Hash's raw bytes
	// Sigstore (corral-ledger-6, keyid KeyIDSigstore) is a Sigstore bundle
	// over Hash's raw bytes (KeylessArtifact), made with a GitHub Actions
	// run's OIDC identity instead of a held key; see keyless.go. Like
	// Signature it is outside the hash: it signs the hash, so it cannot be
	// inside it.
	Sigstore json.RawMessage `json:"sigstore,omitempty"`

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
	// KindSigner is declared in signers.go beside its payload.
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
	// Signers is the active signer set at the pruned head (ActiveSigners).
	// A checkpoint deletes the register entries that made later entries'
	// keys good, so without this the first entry a registered key signs
	// after a prune would verify as "not a registered signer". It is
	// omitempty on a nil slice so a chain that never registered a signer
	// checkpoints byte-for-byte as before.
	Signers []SignerKey `json:"signers,omitempty"`
}

// IsScan reports whether the entry is a run's own record.
func (e Entry) IsScan() bool { return e.Kind == KindScan }

// FileFormat is the document version a ledger entry declares.
// corral-ledger-6 (2026-10-22): a KEYLESS entry — keyid "sigstore", a
// Sigstore bundle in place of an ed25519 signature, verified by the GitHub
// repository whose Actions run signed it (keyless.go). It is used only for
// keyless entries; key-signed and unsigned entries are still written as
// corral-ledger-5, so FileFormat stays 5.
// corral-ledger-5 (2026-10-19): keyid is a key FINGERPRINT
// ("ed25519:" + 16 hex of sha256(pubkey)), not a convention name, and a
// ledger may register signers (KindSigner), so a per-machine key can sign
// under a root's authority. Format-4 and older entries are read unchanged;
// their "corral-certify" keyid means "the root key" (LegacyRootKeyID).
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
	FileFormat = "corral-ledger-5"
	Format6    = "corral-ledger-6"
	Format4    = "corral-ledger-4"
	Format3    = "corral-ledger-3"
	Format2    = "corral-ledger-2"
)

// KnownFormat is what a reader accepts.
func KnownFormat(f string) bool {
	return f == Format6 || f == FileFormat || f == Format4 || f == Format3 || f == Format2
}

// KeyIDIsHashed reports whether this format covers keyid in the hashed
// bytes. Pre-4 entries carry an unauthenticated signer label.
func KeyIDIsHashed(format string) bool { return format != Format3 && format != Format2 }

// CutRef splits "<review entry hash>#<finding id>" at the first '#'.
func CutRef(ref string) (hash, id string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '#' {
			return ref[:i], ref[i+1:], true
		}
	}
	return ref, "", false
}

// EntryShapeProblem is the ONE rule for what an entry of each kind must
// carry, used by the writer (placeEntry refuses to place what it names) and
// by the verifier (a chain check reports it). It used to live in the
// verbs only — WriteAdjudication, WriteRetraction, WriteReview — so an
// entry that reached placeEntry another way (`corral ledger append` of a
// file written elsewhere) was placed, signed and verified with no finding
// id, an empty verdict and nobody deciding: the rule at one door and not
// the other (review 559719120ef0#R1, Codex reviewing, Claude Code
// verifying). "" when the entry is well-formed for its kind.
func EntryShapeProblem(e Entry) string {
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
		if p := CheckpointSignersProblem(e.Checkpoint.Signers); p != "" {
			return p
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
		if _, id, ok := CutRef(a.Adjudicates); !ok || strings.TrimSpace(id) == "" {
			return "an adjudication names a finding as <review hash>#<finding id>"
		}
		if a.Verdict != VerdictConfirmed && a.Verdict != VerdictRefuted {
			return fmt.Sprintf("an adjudication's verdict is %s or %s, not %q", VerdictConfirmed, VerdictRefuted, a.Verdict)
		}
		if strings.TrimSpace(a.By) == "" || strings.TrimSpace(a.Reason) == "" {
			return "an adjudication names who decided and why"
		}
	case KindSigner:
		return SignerShapeProblem(e.Signer)
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
func EntryHash(e Entry) (string, error) {
	if e.Format == Format2 {
		e.Hash, e.KeyID, e.Signature, e.Sigstore, e.Raw = "", "", "", nil, nil
		js, err := CanonicalSparseJSON(e)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(js)
		return hex.EncodeToString(sum[:]), nil
	}
	raw := e.Raw
	if raw == nil {
		e.Hash, e.Signature, e.Sigstore = "", "", nil
		if !KeyIDIsHashed(e.Format) {
			e.KeyID = ""
		}
		var err error
		if raw, err = json.Marshal(e); err != nil {
			return "", err
		}
	}
	js, err := canonicalEntryBytes(raw, KeyIDIsHashed(e.Format))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(js)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalEntryBytes is CanonicalFullJSON over an entry with the fields
// the hash cannot contain (hash, signature, sigstore; keyid before format 4)
// removed from the tree itself, so the
// writer (which has them empty) and the reader (which has them filled)
// hash the same bytes.
func canonicalEntryBytes(raw []byte, keepKeyID bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("ledger: entry is not a JSON object: %w", err)
	}
	delete(tree, "hash")
	delete(tree, "signature")
	delete(tree, "sigstore")
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
