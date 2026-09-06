// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// `corral ledger` — the verbs for a ledger directory, the signed, hash-linked
// record `certify --repo` writes by default (see defaultLedgerDir).
//
//	corral ledger append <entry.json.gz> <dir>   re-link an entry to <dir>'s current head, re-hash, re-sign, place it
//	corral ledger verify <dir> [--pub hex]       walk the chain (same as `corral verify --ledger <dir>`)
//
// `append` exists because a chain is one writer at a time: an entry written
// on a runner while the branch moved, or on a laptop whose `.corral/ledger`
// worktree is behind, names a head that is no longer the head. A plain git
// rebase moves the commit and not the link. So placement is a verb — fetch,
// append, push — and the retry loop runs the verb, not a file copy.
func runLedger(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Fprint(stdout, ledgerUsage)
		return 0
	}
	switch args[0] {
	case "append":
		return runLedgerAppend(args[1:], stdout, stderr)
	case "retract":
		return runLedgerRetract(args[1:], stdout, stderr)
	case "checkpoint":
		return runLedgerCheckpoint(args[1:], stdout, stderr)
	case "verify":
		fs := flag.NewFlagSet("corral ledger verify", flag.ContinueOnError)
		fs.SetOutput(stderr)
		pub := fs.String("pub", "", "hex-encoded Ed25519 public key to verify signatures against (default: the local certify key)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "corral ledger verify: name one directory")
			return 2
		}
		return runVerifyLedger(fs.Arg(0), *pub, stdout, stderr)
	}
	fmt.Fprintf(stderr, "corral ledger: unknown verb %q\n%s", args[0], ledgerUsage)
	return 2
}

const ledgerUsage = `corral ledger — the signed, hash-linked record, as a directory of entries.

  corral ledger append <entry.json.gz> <dir>   re-link an entry to <dir>'s current head (re-hash, re-sign, place)
  corral ledger retract <dir> <hash> --reason "…"
                                               append an entry retracting an earlier one: the retracted scan STAYS in
                                               the chain (deleting it would break the next link) and stops being the
                                               record — the view, the prior, the verdict cache and scans skip it
  corral ledger checkpoint <dir>               PRUNE: replace every entry with one genesis naming the head it replaced
                                               (hash, count, date); the chain restarts there and the verifier says so
  corral ledger verify <dir> [--pub <hex>]     walk the chain: every hash, link and signature, one line per entry

A certify --repo run writes its entry into the repo's .corral/ledger/ by
default (--ledger <dir> to move it, --no-ledger to skip), and reads earlier
entries there as its prior. On a runner the Action does the same into a
checkout of the corral/ledger branch. To carry a local entry up, or a
runner's entry past a branch that moved: fetch, ` + "`corral ledger append`" + `, push.
`

func runLedgerAppend(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "corral ledger append: usage: corral ledger append <entry.json.gz> <dir>")
		return 2
	}
	src, dir := args[0], strings.TrimRight(args[1], "/")
	e, err := auditpush.ReadLedgerEntry(src)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger append: %v\n", err)
		return 1
	}
	var signer auditpush.LedgerSigner
	if priv, kerr := loadLocalCertifyKeyIfConfigured(); kerr == nil {
		signer = auditpush.Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}
	}
	// Same directory: the entry is already placed; re-linking it in place
	// would be an edit of a placed entry, which is the one thing a ledger
	// refuses. Refuse loudly instead.
	// Both sides absolute: filepath.Rel errors on a relative base against
	// an absolute target, and an error here used to SKIP the check — a
	// relative --dir re-linked an entry in place. Found by `corral review`
	// on this file, the day the verb was written.
	absDir, _ := filepath.Abs(dir)
	if abs, aerr := filepath.Abs(src); aerr == nil {
		if rel, rerr := filepath.Rel(filepath.Join(absDir, auditpush.ScansSubdir), abs); rerr == nil && !strings.HasPrefix(rel, "..") {
			fmt.Fprintf(stderr, "corral ledger append: %s is already an entry of %s — an entry is never re-linked in place; append a copy from elsewhere, or leave the chain as it is\n", src, dir)
			return 2
		}
	}
	name, err := auditpush.AppendLedgerEntry(dir, e, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger append: %v\n", err)
		return 1
	}
	signed := "unsigned"
	if signer != nil {
		signed = "signed by corral-certify"
	}
	fmt.Fprintf(stdout, "appended %s to %s (%s, linked to the current head)\n", name, dir, signed)
	return 0
}

// defaultLedgerDir is where a repo scan writes its entry unless told
// otherwise: .corral/ledger/ under the audited repo — a folder, or the
// corral/ledger branch checked out as a worktree, which is what makes a
// laptop run and an Action run one writer. $CORRAL_LEDGER overrides.
func defaultLedgerDir(repoDir string) string {
	if p := strings.TrimSpace(os.Getenv("CORRAL_LEDGER")); p != "" {
		return p
	}
	return filepath.Join(repoDir, ".corral", "ledger")
}

// ledgerSignerFromLocalKey is the signer every ledger verb writes with:
// the local certify key when one is configured, else nil (unsigned, and
// said to be by every reader).
func ledgerSignerFromLocalKey() (auditpush.LedgerSigner, string) {
	if priv, kerr := loadLocalCertifyKeyIfConfigured(); kerr == nil {
		return auditpush.Ed25519LedgerSigner{KeyID: "corral-certify", Key: priv}, "signed by corral-certify"
	}
	return nil, "unsigned"
}

func runLedgerRetract(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger retract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "", "why, in your words — recorded verbatim on the retraction entry (required)")
	// Go's flag parsing stops at the first positional, and the natural
	// spelling puts --reason last; flags are moved to the front so either
	// order works.
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 2 || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "corral ledger retract: usage: corral ledger retract <dir> <entry hash or prefix> --reason \"…\"")
		return 2
	}
	dir, target := strings.TrimRight(fs.Arg(0), "/"), fs.Arg(1)
	signer, signed := ledgerSignerFromLocalKey()
	name, err := auditpush.WriteRetraction(dir, target, *reason, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger retract: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "retracted %.12s in %s: %s (%s) — the entry stays in the chain and is no longer the record\n", target, name, *reason, signed)
	return 0
}

func runLedgerCheckpoint(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "corral ledger checkpoint: usage: corral ledger checkpoint <dir>")
		return 2
	}
	dir := strings.TrimRight(args[0], "/")
	signer, signed := ledgerSignerFromLocalKey()
	name, pruned, err := auditpush.WriteCheckpoint(dir, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger checkpoint: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "checkpoint %s placed in %s (%s); %d earlier entries pruned — `corral verify --ledger %s` will say the chain begins here\n", name, dir, signed, pruned, dir)
	return 0
}

// flagsFirst reorders args so every flag precedes the positionals, for a
// verb whose flags read naturally last. It asks fs which flags are boolean
// so `--refute <dir>` is not read as --refute=<dir>.
func flagsFirst(fs *flag.FlagSet, args []string) []string {
	isBool := func(name string) bool {
		f := fs.Lookup(strings.TrimLeft(name, "-"))
		if f == nil {
			return false
		}
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		return ok && b.IsBoolFlag()
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positional = append(positional, args[i:]...)
			return append(flags, positional...)
		case strings.HasPrefix(a, "-") && (strings.Contains(a, "=") || isBool(a)):
			flags = append(flags, a)
		case strings.HasPrefix(a, "-") && i+1 < len(args):
			flags = append(flags, a, args[i+1])
			i++
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}
