// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// KindSigner is the entry kind that changes WHO may sign this ledger: a
// register adds a key, a revoke retires one, and a keyless pin narrows which
// GitHub Actions workflows may sign keylessly. The entry is itself signed by
// the root key, so the set of signers is part of the record rather than a
// file beside it.
const KindSigner = "signer"

// The four things a signer entry can do, and the key id a format <= 4 entry
// carries when the root key signed it. Before corral-ledger-5 keyid was a
// convention name, not a key; "corral-certify" therefore means "the root"
// on those entries, and a format-5 entry must name its key by fingerprint.
//
// pin-keyless and unpin-keyless are signer actions rather than an entry kind
// of their own because who may sign is already the signer register's job: it
// stands at a position in the chain, is root-signed, and is walked by
// SignerSet. A new kind would be a second door for the same rule.
const (
	SignerRegister     = "register"
	SignerRevoke       = "revoke"
	SignerPinKeyless   = "pin-keyless"
	SignerUnpinKeyless = "unpin-keyless"
	LegacyRootKeyID    = "corral-certify"
)

// SignerChange is the payload of a KindSigner entry. PubKey is carried in
// full (hex) and KeyID must equal its fingerprint, so a reader can check
// the pairing without any outside table; Reason is required on a revoke
// because a retired key with no stated cause is a record nobody can weigh.
//
// A pin or unpin names no key: KeyID and PubKey are empty on it. Their JSON
// tags keep no omitempty, so a pin entry carries two empty strings, because
// changing the tags would change the bytes every existing signer entry is
// re-marshalled to. KeylessPin is omitted when nil, so an entry written
// before it existed hashes exactly as it did.
type SignerChange struct {
	Action     string      `json:"action"`
	KeyID      string      `json:"keyid"`
	PubKey     string      `json:"pubkey"` // hex Ed25519 public key
	Name       string      `json:"name"`
	Reason     string      `json:"reason,omitempty"`
	KeylessPin *KeylessPin `json:"keyless_pin,omitempty"` // pin-keyless only
}

// PinnedWorkflow is one workflow-and-ref pair a pin allows. Workflow is the
// path in the repository (".github/workflows/corral.yml") and Ref the full
// ref ("refs/heads/main"); both are compared as exact strings, never as
// patterns, because a glob is where a pin quietly becomes "any branch" again
// and a project with two release branches lists two pairs.
type PinnedWorkflow struct {
	Workflow string `json:"workflow"`
	Ref      string `json:"ref"`
}

// KeylessPin is the owner's statement of which runs may sign keylessly:
// RepoID is the repository's numeric GitHub id (the certificate's .15
// extension), pinned so a deleted-and-recreated or renamed-and-squatted
// repository, whose URL would still match, does not; Allow is the pairs.
type KeylessPin struct {
	RepoID string           `json:"repo_id"`
	Allow  []PinnedWorkflow `json:"allow"`
}

// PinProblem judges one verified keyless entry's certificate against the pin
// in force at its position: "" when id matches, else the one refusal a pin
// adds. repo is the ledger's repository (owner/name) the bundle already
// verified for, and pinFile the pin entry's file, so the refusal says which
// entry made the rule. The rule: the certificate's repository id is the
// pin's, and some pair has the Build Config URI (.18) exactly
// "https://github.com/<repo>/<workflow>@<ref>" and the Source Repository Ref
// (.14) exactly <ref>. Only .18 is matched, not the Build Signer URI (.9):
// for a reusable workflow .9 names the reusable one and .18 the caller, and
// it is the caller that ran on the owner's ref. An extension the
// certificate lacks never matches, so a pin is never passed by omission.
func PinProblem(pin *KeylessPin, pinFile string, id CertIdentity, repo string) string {
	if id.RepoID != "" && id.RepoID == pin.RepoID {
		for _, a := range pin.Allow {
			if id.Ref != "" && id.Ref == a.Ref && id.Workflow == githubURL(repo)+"/"+a.Workflow+"@"+a.Ref {
				return ""
			}
		}
	}
	return fmt.Sprintf("keyless entry: signed by %s, which the pin registered at %s does not allow", signedByRun(id, repo), pinFile)
}

// signedByRun words the run a certificate names as <workflow>@<ref>: the
// workflow as a path in the repository when its URI is the repository's, a
// missing part as "(absent)". The ref is trimmed off the URI only when it is
// the certificate's own ref, so a URI naming a different ref shows it.
func signedByRun(id CertIdentity, repo string) string {
	const absent = "(absent)"
	wf, ref := id.Workflow, id.Ref
	if ref == "" {
		ref = absent
	} else {
		wf = strings.TrimSuffix(wf, "@"+id.Ref)
	}
	wf = strings.TrimPrefix(wf, githubURL(repo)+"/")
	if wf == "" {
		wf = absent
	}
	return wf + "@" + ref
}

// SignerKey is one registered key as a reader holds it after the walk.
type SignerKey struct {
	KeyID  string `json:"keyid"`
	PubKey string `json:"pubkey"`
	Name   string `json:"name"`
}

// KeyFingerprint names a public key: "ed25519:" + the first 16 hex chars
// of sha256(pubkey). Derived from the key, never chosen, so a keyid cannot
// claim a key it is not. 64 bits is a label that is hard to confuse by
// accident, not a collision-resistant identity: the full PubKey travels in
// the register entry and is what signatures are checked against.
func KeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// SignerShapeProblem is the KindSigner arm of EntryShapeProblem.
func SignerShapeProblem(c *SignerChange) string {
	if c == nil {
		return "a signer entry that carries no signer change"
	}
	switch c.Action {
	case SignerRegister, SignerRevoke:
	case SignerPinKeyless, SignerUnpinKeyless:
		return pinShapeProblem(c)
	default:
		return fmt.Sprintf("a signer change's action is %s, %s, %s or %s, not %q", SignerRegister, SignerRevoke, SignerPinKeyless, SignerUnpinKeyless, c.Action)
	}
	if c.KeylessPin != nil {
		return fmt.Sprintf("a signer %s carries no keyless pin", c.Action)
	}
	raw, err := hex.DecodeString(c.PubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "a signer change's pubkey is a 32-byte Ed25519 key in hex"
	}
	if want := KeyFingerprint(ed25519.PublicKey(raw)); c.KeyID != want {
		return fmt.Sprintf("a signer change's keyid is its pubkey's fingerprint (%s), not %q", want, c.KeyID)
	}
	if c.Action == SignerRegister && strings.TrimSpace(c.Name) == "" {
		return "a signer registration names the key"
	}
	if c.Action == SignerRevoke && strings.TrimSpace(c.Reason) == "" {
		return "a signer revocation states a reason"
	}
	return ""
}

// pinShapeProblem is SignerShapeProblem for a pin or unpin: it names no key,
// a pin names a numeric repository id and at least one exact pair, and an
// unpin carries no pin and says why.
func pinShapeProblem(c *SignerChange) string {
	if c.KeyID != "" || c.PubKey != "" {
		return fmt.Sprintf("a signer %s names no key: its keyid and pubkey are empty", c.Action)
	}
	if c.Action == SignerUnpinKeyless {
		if c.KeylessPin != nil {
			return "a keyless unpin carries no keyless pin"
		}
		if strings.TrimSpace(c.Reason) == "" {
			return "a keyless unpin states a reason"
		}
		return ""
	}
	p := c.KeylessPin
	if p == nil || len(p.Allow) == 0 {
		return "a keyless pin allows at least one workflow and ref"
	}
	if p.RepoID == "" || strings.Trim(p.RepoID, "0123456789") != "" {
		return fmt.Sprintf("a keyless pin's repo_id is the repository's numeric GitHub id, not %q", p.RepoID)
	}
	for _, a := range p.Allow {
		switch {
		case strings.ContainsAny(a.Workflow, pinGlobChars) || strings.ContainsAny(a.Ref, pinGlobChars):
			return fmt.Sprintf("a keyless pin is exact strings only: %q@%q holds a glob character (%s)", a.Workflow, a.Ref, pinGlobChars)
		case strings.IndexFunc(a.Workflow, unicode.IsSpace) >= 0 || strings.IndexFunc(a.Ref, unicode.IsSpace) >= 0:
			return fmt.Sprintf("a keyless pin's workflow and ref hold no whitespace: %q@%q", a.Workflow, a.Ref)
		case !strings.HasPrefix(a.Workflow, ".github/workflows/") || !(strings.HasSuffix(a.Workflow, ".yml") || strings.HasSuffix(a.Workflow, ".yaml")):
			return fmt.Sprintf("a keyless pin's workflow is a path under .github/workflows/ ending .yml or .yaml, not %q", a.Workflow)
		case !strings.HasPrefix(a.Ref, "refs/"):
			return fmt.Sprintf("a keyless pin's ref is a full ref (refs/heads/..., refs/tags/...), not %q", a.Ref)
		}
	}
	return ""
}

// pinGlobChars are refused in a pin's workflow and ref: none of them is a
// pattern to the matcher, but a reader of the pin could take one for it.
const pinGlobChars = "*?[]{}"

// CheckpointSignersProblem is the shape rule for a checkpoint's carried
// set. Each key must pass the very rule a registration does
// (SignerShapeProblem) — a wrong-length key seeded here would otherwise
// reach ed25519.Verify, which panics on one — and a key may appear once.
func CheckpointSignersProblem(keys []SignerKey) string {
	seen := map[string]bool{}
	for _, k := range keys {
		reg := SignerChange{Action: SignerRegister, KeyID: k.KeyID, PubKey: k.PubKey, Name: k.Name}
		if p := SignerShapeProblem(&reg); p != "" {
			return "a checkpoint's carried signer: " + p
		}
		if seen[k.KeyID] {
			return fmt.Sprintf("a checkpoint carries signer %s twice", k.KeyID)
		}
		seen[k.KeyID] = true
	}
	return ""
}
