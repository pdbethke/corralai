// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/ledger/ledgertest"
)

// The keyless pin: a root-signed signer entry that names the workflows and
// refs allowed to sign keylessly from its position on. Every chain here is
// built in memory, in chain order, from the ledgertest CA (a keyless entry)
// and the root's ed25519 key (a signer entry), and read the way every reader
// reads it: Verify, and Derive for the browser.

const (
	pinRepo   = "o/r"
	pinRepoID = "42"
	corralWF  = ".github/workflows/corral.yml"
	mainRef   = "refs/heads/main"
)

type pinChain struct {
	t       *testing.T
	s       *ledgertest.Sigstore
	root    ed25519.PrivateKey
	entries []ledger.Entry
}

func newPinChain(t *testing.T) *pinChain {
	t.Helper()
	return &pinChain{t: t, s: ledgertest.New(t), root: key(0x61)}
}

func (p *pinChain) rootPub() ed25519.PublicKey { return p.root.Public().(ed25519.PublicKey) }

// next links e to the chain's head and gives it a position: a Pushed time,
// a file name and (for a scan) a scan id that are its own.
func (p *pinChain) next(e ledger.Entry) ledger.Entry {
	n := len(p.entries)
	if n > 0 {
		e.Prev = p.entries[n-1].Hash
	}
	e.Pushed = time.Date(2026, 11, 1, 12, 0, n, 0, time.UTC)
	e.File = fmt.Sprintf("%03d-entry.json", n)
	if e.IsScan() {
		e.Bundle = ledger.Bundle{Scan: ledger.ScanRow{Repo: pinRepo, Commit: "abcdef123456", ScanID: int64(n + 1), CorralVersion: "vtest"}}
		e.ScanUID = ledger.ScanUID(e.Bundle.Scan, e.Pushed)
		e.Bundle.Scan.ScanUID = e.ScanUID
	}
	return e
}

// signer appends a signer entry signed by the root's ed25519 key and
// returns its file name.
func (p *pinChain) signer(c ledger.SignerChange) string {
	p.t.Helper()
	return p.signerBy(p.root, c)
}

// signerBy appends a signer entry signed by k, which need not be the root.
func (p *pinChain) signerBy(k ed25519.PrivateKey, c ledger.SignerChange) string {
	p.t.Helper()
	e := p.next(ledger.Entry{Format: ledger.FileFormat, Kind: ledger.KindSigner, KeyID: ledger.KeyFingerprint(k.Public().(ed25519.PublicKey)), Signer: &c})
	h, err := ledger.EntryHash(e)
	if err != nil {
		p.t.Fatal(err)
	}
	e.Hash = h
	raw, _ := hex.DecodeString(h)
	e.Signature = hex.EncodeToString(ed25519.Sign(k, raw))
	e = reparse(p.t, e)
	p.entries = append(p.entries, e)
	return e.File
}

// keylessAs appends e, sealed keylessly with a certificate carrying c.
func (p *pinChain) keylessAs(e ledger.Entry, c ledgertest.CertClaims) {
	p.t.Helper()
	e = p.next(e)
	e.Format, e.KeyID = ledger.Format6, ledger.KeyIDSigstore
	h, err := ledger.EntryHash(e)
	if err != nil {
		p.t.Fatal(err)
	}
	e.Hash = h
	art, err := ledger.KeylessArtifact(e)
	if err != nil {
		p.t.Fatal(err)
	}
	e.Sigstore = p.s.IssueAs(p.t, art, pinRepo, c)
	p.entries = append(p.entries, reparse(p.t, e))
}

// keyless appends a keyless scan entry signed by a run of workflow on ref.
func (p *pinChain) keyless(workflow, ref string) {
	p.t.Helper()
	p.keylessAs(ledger.Entry{}, run(workflow, ref, pinRepoID))
}

// run is the certificate a GitHub Actions run of pinRepo's workflow on ref
// gets from Fulcio when the workflow is not a reusable one.
func run(workflow, ref, repoID string) ledgertest.CertClaims {
	uri := "https://github.com/" + pinRepo + "/" + workflow + "@" + ref
	return ledgertest.CertClaims{Workflow: uri, BuildSigner: uri, Ref: ref, RepoID: repoID, Trigger: "push", Runner: "github-hosted"}
}

func pin(repoID string, allow ...ledger.PinnedWorkflow) ledger.SignerChange {
	return ledger.SignerChange{Action: ledger.SignerPinKeyless, Name: "ci", Reason: "only main signs",
		KeylessPin: &ledger.KeylessPin{RepoID: repoID, Allow: allow}}
}

func allow(workflow, ref string) ledger.PinnedWorkflow {
	return ledger.PinnedWorkflow{Workflow: workflow, Ref: ref}
}

func unpin(reason string) ledger.SignerChange {
	return ledger.SignerChange{Action: ledger.SignerUnpinKeyless, Name: "ci", Reason: reason}
}

func (p *pinChain) trust(pub ed25519.PublicKey) ledger.Trust {
	return ledger.Trust{RootPub: pub, Repo: pinRepo, SigstoreRoot: p.s.Root(p.t)}
}

func (p *pinChain) verify() []ledger.ChainCheck {
	return ledger.Verify(p.entries, p.trust(p.rootPub()))
}

func refusal(signedBy, pinFile string) string {
	return fmt.Sprintf("keyless entry: signed by %s, which the pin registered at %s does not allow", signedBy, pinFile)
}

// wantLevels checks each entry's level, "" meaning "a Problem, and so no
// level"; an entry expected to carry a level must carry no Problem.
func wantLevels(t *testing.T, checks []ledger.ChainCheck, want ...ledger.SigLevel) {
	t.Helper()
	if len(checks) != len(want) {
		t.Fatalf("%d checks, want %d: %+v", len(checks), len(want), checks)
	}
	for i, c := range checks {
		if c.Level != want[i] {
			t.Errorf("entry %d (%s): level %q, want %q (problem %q)", i, c.File, c.Level, want[i], c.Problem)
		}
		if want[i] != "" && c.Problem != "" {
			t.Errorf("entry %d (%s): problem %q, want none", i, c.File, c.Problem)
		}
		if want[i] == "" && c.Problem == "" {
			t.Errorf("entry %d (%s): want a Problem, got none", i, c.File)
		}
	}
}

// The attack the pin exists for: a workflow on a branch the owner never
// approved signs an entry. Under a main pin it is refused, with the one new
// wording.
func TestBranchWorkflowUnderMainPinIsRefused(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, "refs/heads/attacker")
	checks := p.verify()
	if checks[0].Problem != "" || !strings.Contains(checks[0].Note, "pins keyless signing") {
		t.Fatalf("the pin entry must check clean and say what it did: %+v", checks[0])
	}
	if got, want := checks[1].Problem, refusal(corralWF+"@refs/heads/attacker", pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
	if checks[1].Level != "" {
		t.Fatalf("a refused entry has no level: %q", checks[1].Level)
	}
}

// With no pin, the same entry is not refused: it is labelled, and the ref
// it was signed on is there for the reader to see.
func TestUnpinnedBranchEntryIsLabelledNotRefused(t *testing.T) {
	p := newPinChain(t)
	p.keyless(corralWF, "refs/heads/attacker")
	c := only(t, p.verify())
	if c.Problem != "" || c.Level != ledger.LevelKeylessUnpinned {
		t.Fatalf("want keyless-unpinned and no problem: %+v", c)
	}
	if c.Cert == nil || c.Cert.Ref != "refs/heads/attacker" {
		t.Fatalf("the ref must be carried: %+v", c.Cert)
	}
}

// History is not relabelled: an entry written before the pin keeps the
// level it had when it was signed.
func TestEntryBeforePinKeepsItsLevel(t *testing.T) {
	p := newPinChain(t)
	p.keyless(corralWF, mainRef)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, mainRef)
	wantLevels(t, p.verify(), ledger.LevelKeylessUnpinned, ledger.LevelKey, ledger.LevelKeylessPinned)
}

// A workflow must not pin itself: a pin carried by a keyless entry is
// refused by the rule that keeps every signer change with the root key.
func TestKeylessSignerEntryCannotPin(t *testing.T) {
	p := newPinChain(t)
	c := pin(pinRepoID, allow(corralWF, mainRef))
	p.keylessAs(ledger.Entry{Kind: ledger.KindSigner, Signer: &c}, run(corralWF, mainRef, pinRepoID))
	p.keyless(corralWF, "refs/heads/attacker")
	checks := p.verify()
	if !strings.Contains(checks[0].Problem, "must be signed by the ledger's root key") {
		t.Fatalf("a keyless pin must be refused: %+v", checks[0])
	}
	if checks[1].Problem != "" || checks[1].Level != ledger.LevelKeylessUnpinned {
		t.Fatalf("a refused pin must not pin: %+v", checks[1])
	}
}

// A certificate that does not say which workflow, ref or repository id
// signed is refused under a pin, never passed by omission.
func TestMissingBuildConfigUnderPinIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		claims   ledgertest.CertClaims
		signedBy string
		cause    string // the id cause, appended only when the pair matched
	}{
		{"no workflow", ledgertest.CertClaims{Ref: mainRef, RepoID: pinRepoID}, "(absent)@" + mainRef, ""},
		{"no ref", ledgertest.CertClaims{Workflow: "https://github.com/o/r/" + corralWF + "@" + mainRef, RepoID: pinRepoID}, corralWF + "@" + mainRef + "@(absent)", ""},
		{"no repository id", run(corralWF, mainRef, ""), corralWF + "@" + mainRef, " (repository id (absent), pin " + pinRepoID + ")"},
		{"nothing", ledgertest.CertClaims{}, "(absent)@(absent)", ""},
		// The URI names the pinned ref but the certificate's own ref is
		// another: both must match, so this is refused and shows both.
		{"a ref the URI does not name", ledgertest.CertClaims{Workflow: "https://github.com/o/r/" + corralWF + "@" + mainRef, Ref: "refs/heads/attacker", RepoID: pinRepoID},
			corralWF + "@" + mainRef + "@refs/heads/attacker", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPinChain(t)
			pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
			p.keylessAs(ledger.Entry{}, tc.claims)
			if got, want := p.verify()[1].Problem, refusal(tc.signedBy, pinFile)+tc.cause; got != want {
				t.Fatalf("problem\n got %q\nwant %q", got, want)
			}
		})
	}
}

// The numeric repository id is pinned so a renamed-and-squatted name,
// whose URL matches, is still refused.
func TestRepoIDMismatchIsRefused(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keylessAs(ledger.Entry{}, run(corralWF, mainRef, "43"))
	want := refusal(corralWF+"@"+mainRef, pinFile) + " (repository id 43, pin " + pinRepoID + ")"
	if got := p.verify()[1].Problem; got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// A key the root registered is a signer of scans, not of the rules: a pin or
// an unpin it signs is refused by the root-only authority rule, and the pin
// in force (or its absence) is unchanged by it.
func TestRegisteredNonRootKeyCannotPinOrUnpin(t *testing.T) {
	op := key(0x64)
	opPub := op.Public().(ed25519.PublicKey)
	register := ledger.SignerChange{Action: ledger.SignerRegister, KeyID: ledger.KeyFingerprint(opPub), PubKey: hex.EncodeToString(opPub), Name: "laptop"}

	p := newPinChain(t)
	p.signer(register)
	p.signerBy(op, pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, "refs/heads/attacker")
	checks := p.verify()
	if !strings.Contains(checks[1].Problem, "must be signed by the ledger's root key") {
		t.Fatalf("a pin signed by a registered non-root key must be refused: %+v", checks[1])
	}
	if checks[2].Problem != "" || checks[2].Level != ledger.LevelKeylessUnpinned {
		t.Fatalf("a refused pin must not pin: %+v", checks[2])
	}

	p = newPinChain(t)
	p.signer(register)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.signerBy(op, unpin("the laptop wants out"))
	p.keyless(corralWF, "refs/heads/attacker")
	checks = p.verify()
	if !strings.Contains(checks[2].Problem, "must be signed by the ledger's root key") {
		t.Fatalf("an unpin signed by a registered non-root key must be refused: %+v", checks[2])
	}
	if got, want := checks[3].Problem, refusal(corralWF+"@refs/heads/attacker", pinFile); got != want {
		t.Fatalf("the pin must still be in force after a refused unpin\n got %q\nwant %q", got, want)
	}
}

// A glob is where a pin quietly becomes "any branch" again: it is a shape
// Problem, and the pin it is in is never applied.
func TestGlobInPinIsAShapeProblem(t *testing.T) {
	p := newPinChain(t)
	p.signer(pin(pinRepoID, allow(corralWF, "refs/heads/*")))
	p.keyless(corralWF, "refs/heads/attacker")
	checks := p.verify()
	if checks[0].Problem == "" {
		t.Fatalf("a glob in a pin must be a Problem: %+v", checks[0])
	}
	if checks[1].Problem != "" || checks[1].Level != ledger.LevelKeylessUnpinned {
		t.Fatalf("a pin with a Problem must not pin: %+v", checks[1])
	}
}

// Every shape rule for the two new actions, and the one they add to the
// old two, judged where the writer and the verifier both judge it.
func TestPinChangeShape(t *testing.T) {
	op := key(0x62).Public().(ed25519.PublicKey)
	good := func(mut func(*ledger.SignerChange)) *ledger.SignerChange {
		c := pin(pinRepoID, allow(corralWF, mainRef), allow(".github/workflows/release.yaml", "refs/tags/v1.0.0"))
		if mut != nil {
			mut(&c)
		}
		return &c
	}
	setAllow := func(wf, ref string) func(*ledger.SignerChange) {
		return func(c *ledger.SignerChange) { c.KeylessPin.Allow = []ledger.PinnedWorkflow{allow(wf, ref)} }
	}
	cases := []struct {
		name string
		c    *ledger.SignerChange
		ok   bool
	}{
		{"a good pin", good(nil), true},
		{"a pin with a keyid", good(func(c *ledger.SignerChange) { c.KeyID = ledger.KeyFingerprint(op) }), false},
		{"a pin with a pubkey", good(func(c *ledger.SignerChange) { c.PubKey = hex.EncodeToString(op) }), false},
		{"a pin action with no pin", good(func(c *ledger.SignerChange) { c.KeylessPin = nil }), false},
		{"a pin that allows nothing", good(func(c *ledger.SignerChange) { c.KeylessPin.Allow = nil }), false},
		{"no repository id", good(func(c *ledger.SignerChange) { c.KeylessPin.RepoID = "" }), false},
		{"a repository id that is a name", good(func(c *ledger.SignerChange) { c.KeylessPin.RepoID = "o/r" }), false},
		{"a signed repository id", good(func(c *ledger.SignerChange) { c.KeylessPin.RepoID = "-42" }), false},
		{"a workflow outside .github/workflows", good(setAllow("ci/corral.yml", mainRef)), false},
		{"a workflow that is not yaml", good(setAllow(".github/workflows/corral.json", mainRef)), false},
		{"a ref that is not a ref", good(setAllow(corralWF, "main")), false},
		{"a star in the ref", good(setAllow(corralWF, "refs/heads/*")), false},
		{"a question mark in the ref", good(setAllow(corralWF, "refs/heads/ma?n")), false},
		{"brackets in the ref", good(setAllow(corralWF, "refs/heads/[m]ain")), false},
		{"braces in the workflow", good(setAllow(".github/workflows/{a,b}.yml", mainRef)), false},
		{"a star in the workflow", good(setAllow(".github/workflows/*.yml", mainRef)), false},
		{"whitespace in the ref", good(setAllow(corralWF, "refs/heads/main ")), false},
		{"whitespace in the workflow", good(setAllow(".github/workflows/a b.yml", mainRef)), false},
		{"an empty ref", good(setAllow(corralWF, "")), false},
		{"an empty workflow", good(setAllow("", mainRef)), false},
		{"a good unpin", &ledger.SignerChange{Action: ledger.SignerUnpinKeyless, Reason: "moving to a key"}, true},
		{"an unpin with no reason", &ledger.SignerChange{Action: ledger.SignerUnpinKeyless, Reason: "  "}, false},
		{"an unpin that carries a pin", &ledger.SignerChange{Action: ledger.SignerUnpinKeyless, Reason: "x", KeylessPin: good(nil).KeylessPin}, false},
		{"an unpin with a keyid", &ledger.SignerChange{Action: ledger.SignerUnpinKeyless, Reason: "x", KeyID: ledger.KeyFingerprint(op)}, false},
		{"a register that carries a pin", &ledger.SignerChange{Action: ledger.SignerRegister, KeyID: ledger.KeyFingerprint(op), PubKey: hex.EncodeToString(op), Name: "laptop", KeylessPin: good(nil).KeylessPin}, false},
		{"a revoke that carries a pin", &ledger.SignerChange{Action: ledger.SignerRevoke, KeyID: ledger.KeyFingerprint(op), PubKey: hex.EncodeToString(op), Reason: "lost", KeylessPin: good(nil).KeylessPin}, false},
		{"a good register", &ledger.SignerChange{Action: ledger.SignerRegister, KeyID: ledger.KeyFingerprint(op), PubKey: hex.EncodeToString(op), Name: "laptop"}, true},
	}
	for _, tc := range cases {
		p := ledger.SignerShapeProblem(tc.c)
		if (p == "") != tc.ok {
			t.Errorf("%s: problem %q, want ok=%v", tc.name, p, tc.ok)
		}
	}
}

// Review Focus 1: a reusable workflow. The Build Signer URI (.9) names the
// reusable workflow, the Build Config URI (.18) the caller, and the pin
// matches .18 only.
func TestReusableWorkflowMatchesTheCaller(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	// The pinned workflow is the reusable one; the caller is another.
	caller := run(".github/workflows/caller.yml", mainRef, pinRepoID)
	caller.BuildSigner = "https://github.com/" + pinRepo + "/" + corralWF + "@" + mainRef
	p.keylessAs(ledger.Entry{}, caller)
	// The pinned workflow is the caller; it calls a reusable workflow elsewhere.
	pinned := run(corralWF, mainRef, pinRepoID)
	pinned.BuildSigner = "https://github.com/other/lib/.github/workflows/reuse.yml@refs/tags/v1"
	p.keylessAs(ledger.Entry{}, pinned)
	checks := p.verify()
	if got, want := checks[1].Problem, refusal(".github/workflows/caller.yml@"+mainRef, pinFile); got != want {
		t.Fatalf("a caller outside the pin must be refused whatever .9 says\n got %q\nwant %q", got, want)
	}
	if checks[2].Problem != "" || checks[2].Level != ledger.LevelKeylessPinned {
		t.Fatalf("the pinned caller must be pinned whatever it calls: %+v", checks[2])
	}
}

// Review Focus 2: GitHub refs are case-sensitive and a trailing slash is
// another ref, so neither matches.
func TestRefIsCaseAndSlashExact(t *testing.T) {
	for _, ref := range []string{"refs/heads/Main", "refs/heads/main/", "refs/heads/mai", "refs/heads/main2", "refs/tags/main"} {
		t.Run(ref, func(t *testing.T) {
			p := newPinChain(t)
			pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
			p.keyless(corralWF, ref)
			if got, want := p.verify()[1].Problem, refusal(corralWF+"@"+ref, pinFile); got != want {
				t.Fatalf("problem\n got %q\nwant %q", got, want)
			}
		})
	}
	// The same rule for the workflow's own case.
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(".github/workflows/Corral.yml", mainRef)
	if got, want := p.verify()[1].Problem, refusal(".github/workflows/Corral.yml@"+mainRef, pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// Review Focus 3: a reader holding the wrong root key. The pin entry is a
// Problem, the set does not move, and a later keyless entry reads
// keyless-unpinned, never pinned and never refused by a pin it did not
// accept.
func TestWrongRootLeavesPinUnapplied(t *testing.T) {
	p := newPinChain(t)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, mainRef)
	p.keyless(corralWF, "refs/heads/attacker")
	wrong := key(0x63).Public().(ed25519.PublicKey)
	checks := ledger.Verify(p.entries, p.trust(wrong))
	wantLevels(t, checks, "", ledger.LevelKeylessUnpinned, ledger.LevelKeylessUnpinned)
}

// Review Focus 4: pin, unpin, pin again. Each entry takes the level in
// force at its position, the re-pin replaces the pin rather than adding to
// it, and nothing is relabelled.
func TestPinUnpinRepin(t *testing.T) {
	p := newPinChain(t)
	release := "refs/heads/release"
	p.keyless(corralWF, mainRef)                       // 0 unpinned
	p.signer(pin(pinRepoID, allow(corralWF, mainRef))) // 1
	p.keyless(corralWF, mainRef)                       // 2 pinned
	p.signer(unpin("moving the release branch"))       // 3
	p.keyless(corralWF, "refs/heads/attacker")         // 4 unpinned: no pin in force
	p.keyless(corralWF, mainRef)                       // 5 unpinned
	p2 := p.signer(pin(pinRepoID, allow(corralWF, release)))
	p.keyless(corralWF, release) // 7 pinned under the new pin
	p.keyless(corralWF, mainRef) // 8 refused: the re-pin replaced main
	checks := p.verify()
	wantLevels(t, checks, ledger.LevelKeylessUnpinned, ledger.LevelKey, ledger.LevelKeylessPinned, ledger.LevelKey,
		ledger.LevelKeylessUnpinned, ledger.LevelKeylessUnpinned, ledger.LevelKey, ledger.LevelKeylessPinned, "")
	if got, want := checks[8].Problem, refusal(corralWF+"@"+mainRef, p2); got != want {
		t.Fatalf("the refusal names the pin in force\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(checks[3].Note, "moving the release branch") {
		t.Fatalf("an unpin says why: %+v", checks[3])
	}
}

// Unpinning when nothing is pinned is a Problem, not a no-op.
func TestUnpinWithNoPinIsAProblem(t *testing.T) {
	p := newPinChain(t)
	p.signer(unpin("nothing to undo"))
	c := only(t, p.verify())
	if c.Problem != "unpins, but no keyless pin is in force at this point in the chain" {
		t.Fatalf("problem %q", c.Problem)
	}
	// And a second unpin after a real one is the same Problem.
	p = newPinChain(t)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.signer(unpin("once"))
	p.signer(unpin("twice"))
	if got := p.verify()[2].Problem; got != "unpins, but no keyless pin is in force at this point in the chain" {
		t.Fatalf("problem %q", got)
	}
}

// Review Focus 5: a tag ref pinned exactly, accepted for that tag and
// refused for the next one.
func TestTagRefPinnedExactly(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, "refs/tags/v1.0.0")))
	p.keyless(corralWF, "refs/tags/v1.0.0")
	p.keyless(corralWF, "refs/tags/v1.0.1")
	checks := p.verify()
	wantLevels(t, checks, ledger.LevelKey, ledger.LevelKeylessPinned, "")
	if got, want := checks[2].Problem, refusal(corralWF+"@refs/tags/v1.0.1", pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// Two pairs allow two release branches; each matches only its own ref.
func TestPinAllowsEachListedPair(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef), allow(".github/workflows/release.yaml", "refs/heads/release")))
	p.keyless(corralWF, mainRef)
	p.keyless(".github/workflows/release.yaml", "refs/heads/release")
	p.keyless(corralWF, "refs/heads/release") // a listed workflow on another pair's ref
	checks := p.verify()
	wantLevels(t, checks, ledger.LevelKey, ledger.LevelKeylessPinned, ledger.LevelKeylessPinned, "")
	if got, want := checks[3].Problem, refusal(corralWF+"@refs/heads/release", pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// With no root key the pin is read by name, as every signer entry is, and
// the level says the pin was not checked. A refusal under it still stands.
func TestPinWithoutPubIsUnchecked(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, mainRef)
	p.keyless(corralWF, "refs/heads/attacker")
	checks := ledger.Verify(p.entries, p.trust(nil))
	if checks[0].Problem != "" || checks[1].Problem != "" || checks[1].Level != ledger.LevelKeylessPinnedUnchecked {
		t.Fatalf("want keyless-pinned-unchecked: %+v", checks[:2])
	}
	if got, want := checks[2].Problem, refusal(corralWF+"@refs/heads/attacker", pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// A bundle that fails keeps its own keyless Problem; the pin never words a
// signature that did not verify.
func TestFailedBundleUnderPinKeepsItsProblem(t *testing.T) {
	p := newPinChain(t)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, "refs/heads/attacker")
	tr := p.trust(p.rootPub())
	tr.Repo = "o/other"
	c := ledger.Verify(p.entries, tr)[1]
	if c.Problem == "" || strings.Contains(c.Problem, "pin") {
		t.Fatalf("want the bundle's own problem, not a pin's: %q", c.Problem)
	}
}

// The pin survives JSON: the field name is keyless_pin, and an entry that
// carries one hashes over it, so editing the allow list breaks the hash.
func TestPinIsCoveredByTheHash(t *testing.T) {
	p := newPinChain(t)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	e := p.entries[0]
	if !strings.Contains(string(e.Raw), `"keyless_pin":{"repo_id":"42","allow":[{"workflow":".github/workflows/corral.yml","ref":"refs/heads/main"}]}`) {
		t.Fatalf("raw = %s", e.Raw)
	}
	edited := strings.Replace(string(e.Raw), "refs/heads/main", "refs/heads/evil", 1)
	e2, err := ledger.ParseEntry(e.File, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if c := only(t, ledger.Verify([]ledger.Entry{e2}, p.trust(p.rootPub()))); !strings.Contains(c.Problem, "edited after it was written") {
		t.Fatalf("an edited pin must fail its hash: %+v", c)
	}
}

// TestBrowserDeriveMatchesCLIForPinCases is the parity check: the browser's
// whole copy of the rules is ledger.Derive, compiled to WASM by
// internal/ledger/wasm, which builds only for GOOS=js. So it runs here, where
// plain `go test` reaches it: every pin case goes through both Verify (the
// CLI's walk) and Derive (the browser's), and Level and Problem must agree
// entry by entry.
func TestBrowserDeriveMatchesCLIForPinCases(t *testing.T) {
	type fixture struct {
		name string
		p    *pinChain
		pub  bool
	}
	var fixtures []fixture
	build := func(name string, pub bool, f func(p *pinChain)) {
		p := newPinChain(t)
		f(p)
		fixtures = append(fixtures, fixture{name, p, pub})
	}
	build("branch under main pin", true, func(p *pinChain) {
		p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
		p.keyless(corralWF, "refs/heads/attacker")
	})
	build("entry before pin", true, func(p *pinChain) {
		p.keyless(corralWF, mainRef)
		p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
		p.keyless(corralWF, mainRef)
	})
	build("pin, unpin, re-pin", true, func(p *pinChain) {
		p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
		p.keyless(corralWF, mainRef)
		p.signer(unpin("x"))
		p.keyless(corralWF, "refs/heads/attacker")
		p.signer(pin(pinRepoID, allow(corralWF, "refs/tags/v1.0.0")))
		p.keyless(corralWF, "refs/tags/v1.0.0")
	})
	build("repo id mismatch", true, func(p *pinChain) {
		p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
		p.keylessAs(ledger.Entry{}, run(corralWF, mainRef, "43"))
	})
	build("no root key", false, func(p *pinChain) {
		p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
		p.keyless(corralWF, mainRef)
	})
	build("glob pin", true, func(p *pinChain) {
		p.signer(pin(pinRepoID, allow(corralWF, "refs/heads/*")))
		p.keyless(corralWF, mainRef)
	})
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			var pub ed25519.PublicKey
			if fx.pub {
				pub = fx.p.rootPub()
			}
			tr := fx.p.trust(pub)
			cli := ledger.Verify(fx.p.entries, tr)
			var raw []ledger.RawEntry
			for _, e := range fx.p.entries {
				raw = append(raw, ledger.RawEntry{Name: e.File, Bytes: e.Raw})
			}
			d, err := ledger.Derive(raw, tr)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Entries) != len(cli) {
				t.Fatalf("derive has %d entries, the walk %d", len(d.Entries), len(cli))
			}
			for i, c := range cli {
				st := d.Entries[i]
				// Derive adds one Problem of its own to a clean check: a
				// signature nobody checked (statusOf), which is the root-signed
				// pin entry read with no root key. It is not a pin rule, and
				// the walk leaves that entry's Problem empty.
				wantProblem := c.Problem
				if wantProblem == "" && c.Signed && !c.SigChecked {
					wantProblem = "signed, but no root key was given to check the signature against"
				}
				if st.File != c.File || st.Level != c.Level || st.Problem != wantProblem {
					t.Errorf("entry %d: derive {%s %q %q}, walk {%s %q %q}", i, st.File, st.Level, st.Problem, c.File, c.Level, wantProblem)
				}
			}
		})
	}
}

// The Build Config URI is matched whole, not by its tail: a workflow of the
// same path in another repository is not the pinned one, and the refusal
// prints its full URI because it is not this repository's.
func TestWorkflowOfAnotherRepositoryIsRefused(t *testing.T) {
	p := newPinChain(t)
	pinFile := p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	other := "https://github.com/other/r/" + corralWF + "@" + mainRef
	p.keylessAs(ledger.Entry{}, ledgertest.CertClaims{Workflow: other, BuildSigner: other, Ref: mainRef, RepoID: pinRepoID})
	if got, want := p.verify()[1].Problem, refusal(other, pinFile); got != want {
		t.Fatalf("problem\n got %q\nwant %q", got, want)
	}
}

// Derive counts the verified entries by level, so a page words its summary
// from them instead of tallying the list: an unpinned keyless entry, the
// root-signed pin (a key) and a pinned keyless entry are one of each.
func TestDeriveCountsVerifiedEntriesByLevel(t *testing.T) {
	p := newPinChain(t)
	p.keyless(corralWF, mainRef)
	p.signer(pin(pinRepoID, allow(corralWF, mainRef)))
	p.keyless(corralWF, mainRef)
	p.keyless(corralWF, mainRef)
	var raw []ledger.RawEntry
	for _, e := range p.entries {
		raw = append(raw, ledger.RawEntry{Name: e.File, Bytes: e.Raw})
	}
	d, err := ledger.Derive(raw, p.trust(p.rootPub()))
	if err != nil {
		t.Fatal(err)
	}
	tot := d.Summary.Totals
	if tot.EntriesVerified != 4 || tot.EntriesKey != 1 || tot.EntriesKeylessUnpinned != 1 || tot.EntriesKeylessPinned != 2 || tot.EntriesKeylessPinnedUnchecked != 0 {
		t.Fatalf("by level: verified %d key %d unpinned %d pinned %d unchecked %d", tot.EntriesVerified, tot.EntriesKey, tot.EntriesKeylessUnpinned, tot.EntriesKeylessPinned, tot.EntriesKeylessPinnedUnchecked)
	}
	j := summaryJSON(t, d.Summary)
	for _, k := range []string{`"entries_key":1`, `"entries_keyless_unpinned":1`, `"entries_keyless_pinned":2`, `"entries_keyless_pinned_unchecked":0`} {
		if !strings.Contains(j, k) {
			t.Errorf("want %s in %s", k, j)
		}
	}
}
