// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"
)

// Trust is what a verifier holds from outside the ledger: the root key the
// operator names, the repository the ledger is published from (owner/name),
// and the Sigstore trust root that keyless entries are checked against.
//
// A zero RootPub reads key-signed entries without checking their signatures:
// hashes, links and shape are checked, no signature is. Repo and
// SigstoreRoot are read only for keyless (corral-ledger-6) entries, and both
// are required there: an empty Repo is the Problem ProblemNoLedgerRepo, a nil
// SigstoreRoot a Problem naming it. Neither has a default — the repository
// is never guessed, and the public-good root is passed explicitly
// (PublicGoodTrustedRoot) by whoever means it.
//
// IntegrityOnly is the reader that holds no trust at all and says so: a
// keyless entry's bundle is not checked (as a zero RootPub leaves ed25519
// signatures unchecked), while every hash, link and shape rule — the v6 ⇔
// keyid pairing among them — still is. It is for a WRITER guarding its own
// append (auditpush.RequireIntactChain), which has no repository to name and
// must still extend a chain that holds keyless entries. It is a field the
// caller sets, not what an empty Repo means: a reader that forgot the
// repository is told so (ProblemNoLedgerRepo), never quietly skipped.
type Trust struct {
	RootPub       ed25519.PublicKey
	Repo          string
	SigstoreRoot  *SigstoreRoot
	IntegrityOnly bool
}

// ChainCheck is one entry's verdict from Verify.
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
	Signed bool // the entry carries a signature (ed25519, or a keyless Sigstore bundle)
	SigOK  bool // the signature verifies under the key resolved for KeyID, or the keyless bundle verifies for Trust.Repo (false when unsigned, unresolved or no pub given)
	// Keyless is set for a keyless (keyid KeyIDSigstore) entry: its trust is
	// the repository that signed, not the root key, so a reader needs no
	// RootPub to call it verified.
	Keyless bool
	KeyID   string
	// Signer is who the chain says holds KeyID at this entry: "root", the
	// name a register entry gave the key, or "" (unsigned, or a key the
	// chain does not hold here). Filled with or without a key; only SigOK
	// says the signature was checked. For a keyless entry it is the
	// repository URI its certificate names, and only once that verified.
	Signer  string
	Problem string // "" when nothing is wrong
	Genesis bool
	// Kind is the entry's kind; Note says what a retraction or checkpoint
	// entry means for the chain, in words, when nothing is wrong with it.
	Kind string
	Note string
}

// Verify walks the chain in the order given: every entry's hash against its
// bytes, every Prev against its predecessor, every signature against the key
// the chain held for its keyid at that point (when t.RootPub, the ROOT key,
// is given). It never stops at the first failure — a reader wants the whole
// picture — and it never calls an unsigned entry verified.
//
// Entries must be in chain order, oldest first, each Prev naming its
// predecessor: Verify does not reorder, and a misordered slice is reported as
// broken links.
//
// It takes entries, not a directory, because the browser reads a ledger as
// bytes and the CLI reads it from disk and both must run THIS walk.
//
// Until corral-ledger-5 every signature was checked against the key alone:
// one key for the whole ledger, and keyid a label. Now it is the root, and the
// root's signer entries say which other keys may sign from where on, so a
// second machine signs under its own key instead of holding a copy of the
// root's. A key is good only between its registration and its revocation;
// an entry it signed outside that window is a Problem, whatever its
// signature says.
func Verify(entries []Entry, t Trust) []ChainCheck {
	checks, _ := VerifyWithSigners(entries, t)
	return checks
}

// VerifyWithSigners is THE walk: Verify's checks, and the signer set it leaves
// at the head, which is what auditpush.ActiveSigners returns. One loop for
// both, so a reader and a writer can never disagree about who was
// registered when.
func VerifyWithSigners(entries []Entry, t Trust) ([]ChainCheck, *SignerSet) {
	pub := t.RootPub
	set := newSignerSet(entries, pub)
	// Whether this chain is a signed one at all: an entirely unsigned ledger
	// is honest (no key was configured), but a single unsigned entry among
	// signed ones is a hole, not a style.
	anySigned := false
	for _, e := range entries {
		if e.Signature != "" || len(e.Sigstore) > 0 {
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
		c.Signed = e.Signature != "" || len(e.Sigstore) > 0
		c.Keyless = e.KeyID == KeyIDSigstore
		// A keyless entry is checked by the repository that signed it and
		// never resolved against the signer set: it holds no key the set
		// could name. keylessProblem also enforces, for EVERY entry, that
		// format 6 and keyid "sigstore" go together.
		keyless, signedBy := keylessProblem(e, t)
		var name, keyProblem string
		var key ed25519.PublicKey
		if c.Keyless {
			c.SigOK = keyless == "" && signedBy != ""
			c.Signer = signedBy
		} else {
			// The key is resolved BEFORE the signature is checked, from the
			// set as it stands at this entry — never from the entry's own
			// claim.
			name, key, keyProblem = set.resolve(e)
			c.Signer = name
			if e.Signature != "" && key != nil {
				raw, _ := hex.DecodeString(e.Hash)
				sig, _ := hex.DecodeString(e.Signature)
				c.SigOK = ed25519.Verify(key, raw, sig)
			}
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
		if e.Format != Format2 && e.IsScan() && e.Bundle.Scan != (ScanRow{}) {
			uidOK = e.ScanUID == ScanUID(e.Bundle.Scan, e.Pushed) && e.Bundle.Scan.ScanUID == e.ScanUID
		}
		switch {
		case !c.HashOK:
			c.Problem = "entry bytes do not match its hash — edited after it was written"
		case !uidOK:
			c.Problem = "scan_uid does not derive from the entry's scan row and pushed time — the identity was altered, or minted elsewhere"
		case !c.LinkOK:
			c.Problem = fmt.Sprintf("prev %.12s does not name the previous entry (%.12s) — an entry was removed, reordered or inserted", e.Prev, prevHash)
		case keyless != "":
			c.Problem = keyless
		case seedProblem != "" && seedProblem == errCheckpointNotRoot:
			// Before keyProblem: a checkpoint by a non-root key is not "an
			// unregistered key" — the set is empty at a genesis — it is a
			// genesis claiming authority to hand out keys.
			c.Problem = seedProblem
		case keyProblem != "":
			c.Problem = keyProblem
		case e.Signature != "" && key != nil && !c.SigOK && name == "root":
			c.Problem = "signature does not verify under the given key"
		case e.Signature != "" && key != nil && !c.SigOK:
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
		if e.Format == Format2 && c.Problem == "" {
			c.Note = strings.TrimSpace(c.Note + " · " + Format2 + ": hashed in the sparse form (a recorded false or zero is not distinguished from an absent one) and its scan_uid is not derivable from the entry")
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
		if !KeyIDIsHashed(e.Format) && c.Signed && c.Problem == "" {
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
