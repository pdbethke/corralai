// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/buildstore"
)

// ledgerRootKeyEnv carries the ledger's ROOT seed (hex Ed25519). The root is
// the one key that may change who else signs, so it is held offline and read
// from the environment only by the verbs that exercise that authority
// (`signer add|revoke`, and `checkpoint` on a chain that has signers).
//
// Deliberately NOT a file corral looks for, and NOT the machine's certify
// key: a root seed found on disk by default is a root seed every process of
// the operator's can use, and a root operation that quietly fell back to the
// machine key would sign with an identity the chain never made its root (the
// "no defaults" rule — an absent seed is a refusal that names the variable).
const ledgerRootKeyEnv = "CORRALAI_LEDGER_ROOT_KEY"

// rootSignerFromEnv builds the root signer from ledgerRootKeyEnv. Errors name
// the variable and where the seed lives, and never carry the seed's bytes:
// buildstore.ParseSigningKeySeed's messages are fixed strings, and its error
// is not wrapped so nothing from the input can ride along.
func rootSignerFromEnv() (auditpush.Ed25519LedgerSigner, ed25519.PublicKey, error) {
	raw := os.Getenv(ledgerRootKeyEnv)
	if strings.TrimSpace(raw) == "" {
		return auditpush.Ed25519LedgerSigner{}, nil, fmt.Errorf("%s is not set — a root operation needs the ledger's root seed (hex Ed25519), which lives offline: `pass show corralai/ledger-root-key`. The machine's certify key is never used as the root", ledgerRootKeyEnv)
	}
	priv, err := buildstore.ParseSigningKeySeed([]byte(raw))
	if err != nil {
		return auditpush.Ed25519LedgerSigner{}, nil, fmt.Errorf("%s is not a hex-encoded 32-byte Ed25519 seed — `pass show corralai/ledger-root-key`", ledgerRootKeyEnv)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return auditpush.Ed25519LedgerSigner{KeyID: auditpush.KeyFingerprint(pub), Key: priv}, pub, nil
}

// rootGate is what every root operation checks before it writes: the seed
// is present, it is the root the operator named (CORRALAI_LEDGER_PUBKEY,
// when set — --pub on `add` names the NEW key, so only the environment can
// name the root there), and the directory verifies cleanly under that root.
// The third check is what proves the seed is THIS chain's root: a chain
// signed by anyone else shows Problems under it. Nothing is written when
// any of them fails.
func rootGate(verb, dir string, stderr io.Writer) (auditpush.Ed25519LedgerSigner, ed25519.PublicKey, bool) {
	signer, rootPub, err := rootSignerFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger %s: %v\n", verb, err)
		return signer, nil, false
	}
	if named, src, perr := auditpush.ResolveLedgerPubKey(""); perr != nil {
		fmt.Fprintf(stderr, "corral ledger %s: %v\n", verb, perr)
		return signer, nil, false
	} else if named != nil && !named.Equal(rootPub) {
		fmt.Fprintf(stderr, "corral ledger %s: %s names root %s but %s derives root %s — refusing, nothing written\n", verb, src, auditpush.KeyFingerprint(named), ledgerRootKeyEnv, signer.KeyID)
		return signer, nil, false
	}
	checks, err := auditpush.VerifyLedgerDir(dir, rootPub)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger %s: %v\n", verb, err)
		return signer, nil, false
	}
	for _, c := range checks {
		if c.Problem != "" {
			fmt.Fprintf(stderr, "corral ledger %s: %s does not verify under root %s (%s: %s) — either the seed is not this chain's root, or the chain is broken; nothing written\n", verb, dir, signer.KeyID, c.File, c.Problem)
			return signer, nil, false
		}
	}
	return signer, rootPub, true
}

// runLedgerSigner is `corral ledger signer add|revoke|list`.
func runLedgerSigner(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, signerUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return runSignerAdd(args[1:], stdout, stderr)
	case "revoke":
		return runSignerRevoke(args[1:], stdout, stderr)
	case "list":
		return runSignerList(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "corral ledger signer: unknown verb %q\n%s", args[0], signerUsage)
	return 2
}

const signerUsage = `usage:
  corral ledger signer add <dir> --name <name> --pub <hex>      register a key (root seed in $CORRALAI_LEDGER_ROOT_KEY)
  corral ledger signer revoke <dir> --keyid <id> --reason "…"   retire a key
  corral ledger signer list <dir> [--pub <root hex>]            active signers, then revoked ones
`

func runSignerAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger signer add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "what this key is (a machine, a runner) — recorded on the registration and shown beside every entry it signs (required)")
	pubHex := fs.String("pub", "", "hex-encoded Ed25519 PUBLIC key to register (required). Note: this names the new signer; the root is named by $CORRALAI_LEDGER_PUBKEY")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(*name) == "" || *pubHex == "" {
		fmt.Fprint(stderr, "corral ledger signer add: ", signerUsage)
		return 2
	}
	raw, err := hex.DecodeString(strings.TrimSpace(*pubHex))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		fmt.Fprintln(stderr, "corral ledger signer add: --pub is not a 32-byte hex-encoded Ed25519 public key")
		return 2
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	root, rootPub, ok := rootGate("signer add", dir, stderr)
	if !ok {
		return 2
	}
	pub := ed25519.PublicKey(raw)
	id := auditpush.KeyFingerprint(pub)
	if pub.Equal(rootPub) {
		fmt.Fprintln(stderr, "corral ledger signer add: that is the root key — the root signs registrations, it is not registered")
		return 2
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer add: %v\n", err)
		return 1
	}
	_, active := auditpush.ActiveSigners(entries)
	for _, k := range active {
		if k.KeyID == id {
			fmt.Fprintf(stderr, "corral ledger signer add: %s is already registered as %q\n", id, k.Name)
			return 2
		}
	}
	file, err := auditpush.WriteSignerChange(dir, auditpush.SignerChange{Action: auditpush.SignerRegister, KeyID: id, PubKey: hex.EncodeToString(pub), Name: strings.TrimSpace(*name)}, root)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer add: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "registered %s as %q in %s (signed by root %s)\n", id, strings.TrimSpace(*name), file, root.KeyID)
	return 0
}

func runSignerRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger signer revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyid := fs.String("keyid", "", "fingerprint of the registered key to retire (required; see `signer list`)")
	reason := fs.String("reason", "", "why, in your words — recorded verbatim (required)")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(*keyid) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprint(stderr, "corral ledger signer revoke: ", signerUsage)
		return 2
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	root, _, ok := rootGate("signer revoke", dir, stderr)
	if !ok {
		return 2
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer revoke: %v\n", err)
		return 1
	}
	// The chain verified under the root a moment ago, so this set is the one
	// the verifier holds, and the registered public key rides on the revoke.
	_, active := auditpush.ActiveSigners(entries)
	for _, k := range active {
		if k.KeyID != strings.TrimSpace(*keyid) {
			continue
		}
		file, err := auditpush.WriteSignerChange(dir, auditpush.SignerChange{Action: auditpush.SignerRevoke, KeyID: k.KeyID, PubKey: k.PubKey, Name: k.Name, Reason: strings.TrimSpace(*reason)}, root)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer revoke: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "revoked %s (%q) in %s: %s (signed by root %s)\n", k.KeyID, k.Name, file, strings.TrimSpace(*reason), root.KeyID)
		return 0
	}
	fmt.Fprintf(stderr, "corral ledger signer revoke: %s is not an active signer of %s — `corral ledger signer list` shows which are\n", *keyid, dir)
	return 2
}

func runSignerList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger signer list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pubFlag := fs.String("pub", "", "hex-encoded Ed25519 ROOT public key to verify the chain under (default: $CORRALAI_LEDGER_PUBKEY; with neither, the list is what the chain CLAIMS and is not verified)")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "corral ledger signer list: ", signerUsage)
		return 2
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	pub, src, err := auditpush.ResolveLedgerPubKey(*pubFlag)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer list: %v\n", err)
		return 2
	}
	checks, err := auditpush.VerifyLedgerDir(dir, pub)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer list: %v\n", err)
		return 2
	}
	for _, c := range checks {
		if c.Problem != "" {
			fmt.Fprintf(stderr, "corral ledger signer list: %s does not verify (%s: %s) — no list is given for a chain that is broken\n", dir, c.File, c.Problem)
			return 1
		}
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer list: %v\n", err)
		return 1
	}
	rootID, active := auditpush.ActiveSigners(entries)
	if pub != nil {
		fmt.Fprintf(stdout, "· verified under root %s (%s)\n", auditpush.KeyFingerprint(pub), src)
	} else {
		fmt.Fprintf(stdout, "· signatures NOT verified (no --pub, no %s): this is what the chain CLAIMS — a signer entry that merely names the root is believed without a key\n", auditpush.LedgerPubKeyEnv)
		if rootID != "" {
			fmt.Fprintf(stdout, "· root, as the chain names it: %s\n", rootID)
		}
	}
	// Which entry file introduced each key, and which revoked it.
	regFile, revoked := map[string]string{}, []auditpush.SignerChange{}
	revFile := map[string]string{}
	for i, e := range entries {
		switch {
		case e.Kind == auditpush.KindSigner && e.Signer != nil && e.Signer.Action == auditpush.SignerRegister:
			if _, seen := regFile[e.Signer.KeyID]; !seen {
				regFile[e.Signer.KeyID] = e.File
			}
		case e.Kind == auditpush.KindSigner && e.Signer != nil && e.Signer.Action == auditpush.SignerRevoke:
			revoked = append(revoked, *e.Signer)
			revFile[e.Signer.KeyID] = e.File
		case i == 0 && e.Kind == auditpush.KindCheckpoint && e.Checkpoint != nil:
			for _, k := range e.Checkpoint.Signers {
				regFile[k.KeyID] = e.File + " (carried by a checkpoint)"
			}
		}
	}
	if len(active) == 0 {
		fmt.Fprintln(stdout, "no active signers")
	}
	for _, k := range active {
		fmt.Fprintf(stdout, "active   %s  %s  registered in %s\n", k.Name, k.KeyID, regFile[k.KeyID])
	}
	for _, r := range revoked {
		fmt.Fprintf(stdout, "revoked  %s  %s  registered in %s, revoked in %s: %s\n", r.Name, r.KeyID, orUnnamed(regFile[r.KeyID]), revFile[r.KeyID], r.Reason)
	}
	return 0
}
