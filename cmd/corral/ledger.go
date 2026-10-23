// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/statedir"
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
	case "signer":
		return runLedgerSigner(args[1:], stdout, stderr)
	case "push":
		return runLedgerPush(args[1:], stdout, stderr)
	case "verify":
		fs := flag.NewFlagSet("corral ledger verify", flag.ContinueOnError)
		fs.SetOutput(stderr)
		pub := fs.String("pub", "", "hex-encoded Ed25519 public key to verify signatures against (default: $CORRALAI_LEDGER_PUBKEY; with neither, signatures are NOT CHECKED — the local certify key is never used)")
		expectHead := fs.String("expect-head", "", "the hash this chain's newest entry must have, held from OUTSIDE the directory (a git ref, a Rekor receipt, a note). A chain verifies against itself, so removing the newest entries leaves the rest valid and no check inside can see it; this is the anchor that does. A prefix is accepted, but at least 12 hex characters — a short one names too many entries to be an anchor")
		ledgerRepo := fs.String("ledger-repo", ledgerRepoUsageDefault, ledgerRepoUsage)
		if err := fs.Parse(args[1:]); err != nil {
			return flagParseExit(err)
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "corral ledger verify: name one directory")
			return 2
		}
		return runVerifyLedger(fs.Arg(0), *pub, *ledgerRepo, *expectHead, stdout, stderr)
	}
	fmt.Fprintf(stderr, "corral ledger: unknown verb %q\n%s", args[0], ledgerUsage)
	return 2
}

const ledgerUsage = `corral ledger — the hash-linked record (signed when a key is present), as a directory of entries.

  corral ledger append <entry.json.gz> <dir>   re-link an entry to <dir>'s current head (re-hash, re-sign, place)
  corral ledger retract <dir> <hash> --reason "…"
                                               append an entry retracting an earlier one: the retracted scan STAYS in
                                               the chain (deleting it would break the next link) and stops being the
                                               record — the view, the prior, the verdict cache and scans skip it
  corral ledger checkpoint [--expect-head <hash>] <dir>
                                               PRUNE: replace every entry with one genesis naming the head it replaced
                                               (hash, count, date); the chain restarts there and the verifier says so.
                                               On a chain that registers signers it is signed by the ROOT
                                               ($CORRALAI_LEDGER_ROOT_KEY) and --expect-head is REQUIRED: a root
                                               checkpoint over a chain whose newest entries were removed would make
                                               a revoked key permanent
  corral ledger signer add <dir> --name <n> --pub <hex>
  corral ledger signer revoke <dir> --keyid <id> --reason "…"
  corral ledger signer list <dir> [--pub <root hex>]
                                               who may sign this ledger besides the root. add/revoke are signed by the
                                               ROOT key, read from $CORRALAI_LEDGER_ROOT_KEY and nowhere else (the
                                               machine key is never a fallback); the entry is part of the chain, so
                                               verify checks each signature against the key the chain registered
  corral ledger push <dir> <dsn> [--push-source] [--dry-run]
                                               append the directory's record to a warehouse you own (a DuckDB path, or
                                               md:<db>): every scan, review and adjudication entry the target does not
                                               already hold (by entry hash), retracted scans left out. Source — the
                                               authored tests, verdict JSON, scripts and their output — travels only
                                               with --push-source. A run's own --push already does this as it goes;
                                               this is for a directory that ran without one, or a branch pulled later
  corral ledger verify [--pub <hex>] [--expect-head <hash>] <dir>
                                               walk the chain: every hash, link and signature, one line per entry.
                                               A chain verifies against ITSELF, so removing the newest entries leaves
                                               the rest valid — --expect-head is the anchor from outside that catches it

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
	signer, signed, kerr := selectLedgerSigner(stderr)
	if kerr != nil {
		fmt.Fprintf(stderr, "corral ledger append: %v\n", kerr)
		return 1
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

// selectLedgerSigner is THE door every ledger writer goes through — certify
// --repo's entry and its machine copy, certify --local's entries (both via
// pushBundle), review and review adjudicate, and the ledger verbs append,
// retract and checkpoint — so how an entry is signed is decided in one place
// and cannot differ between verbs.
//
// CORRALAI_LEDGER_SIGNER chooses (auditpush.KeylessSignerSelected):
//
//   - "sigstore": the keyless signer, the GitHub Actions run's own identity
//     through Sigstore (auditpush.NewGitHubSigstoreSigner). A certify key,
//     if one is configured, is NOT used, and one line on stderr says so —
//     once per process, as the signer itself is built once per process, so
//     a run's entries share one certificate. A keyless signer that cannot
//     be built (no OIDC token) is refused: it never falls back to the key or
//     to unsigned, because the operator chose a signer and an entry made any
//     other way is a permanent record they did not ask for.
//   - unset or blank: the local certify key when one is configured, else
//     nil (unsigned, and said to be by every reader). "Not configured"
//     (errNoLocalKey) is the only case that may write unsigned. Any other
//     key error — the legacy-location refusal above all — is returned, and
//     the caller writes nothing: an unsigned entry is permanent in a
//     hash-linked chain, and one written because the key was merely in the
//     old place would sit forever in a ledger whose every other entry
//     verifies against LEDGER_PUBKEY.
//   - anything else: refused, naming the accepted value.
//
// Every refusal wraps errSignerUnusable.
func selectLedgerSigner(stderr io.Writer) (auditpush.LedgerSigner, string, error) {
	keyless, err := auditpush.KeylessSignerSelected()
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w, so nothing was written", errSignerUnusable, err)
	}
	if keyless {
		s, err := processKeylessLedgerSigner(stderr)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s=%s: %w, so nothing was written (it never falls back to a key or to unsigned)", errSignerUnusable, auditpush.LedgerSignerEnv, auditpush.LedgerSignerSigstore, err)
		}
		return s, "signed keylessly (Sigstore, this run's GitHub identity)", nil
	}
	priv, kerr := loadLocalCertifyKeyIfConfigured()
	switch {
	case kerr == nil:
		// The key is named by its fingerprint (corral-ledger-5): the
		// verifier resolves a keyid to the root or a registered signer, and
		// a format-5 entry keyed "corral-certify" is a Problem there.
		id := auditpush.KeyFingerprint(priv.Public().(ed25519.PublicKey))
		return auditpush.Ed25519LedgerSigner{KeyID: id, Key: priv}, "signed by " + id, nil
	case errors.Is(kerr, errNoLocalKey):
		return nil, "unsigned", nil
	}
	return nil, "", fmt.Errorf("%w: the local certify key: %w, so nothing was written (an unsigned entry would be permanent)", errSignerUnusable, kerr)
}

// errSignerUnusable marks selectLedgerSigner's refusal, so a caller whose
// other write errors are fail-open (a full disk never changes a verdict) can
// tell this one apart.
var errSignerUnusable = errors.New("the ledger signer cannot be used")

// newKeylessLedgerSigner builds the keyless signer. A variable only so a
// test can supply one backed by an in-process Sigstore (ledgertest).
var newKeylessLedgerSigner = func() (auditpush.LedgerSigner, error) {
	return auditpush.NewGitHubSigstoreSigner()
}

// keylessLedgerSigner is the process's keyless signer, built on first use
// and kept: every entry the run writes then shares one Fulcio certificate
// (one OIDC token, one certificate, a log entry per ledger entry). A build
// that fails is not kept, so the error is returned to every caller.
var keylessLedgerSigner struct {
	mu sync.Mutex
	s  auditpush.LedgerSigner
}

func processKeylessLedgerSigner(stderr io.Writer) (auditpush.LedgerSigner, error) {
	keylessLedgerSigner.mu.Lock()
	defer keylessLedgerSigner.mu.Unlock()
	if keylessLedgerSigner.s != nil {
		return keylessLedgerSigner.s, nil
	}
	s, err := newKeylessLedgerSigner()
	if err != nil {
		return nil, err
	}
	if v := certifyKeyConfigured(); v != "" {
		fmt.Fprintf(stderr, "corral: %s=%s — ledger entries are signed keylessly; the certify key (%s) is not used for them\n", auditpush.LedgerSignerEnv, auditpush.LedgerSignerSigstore, v)
	}
	keylessLedgerSigner.s = s
	return s, nil
}

// resetKeylessLedgerSigner forgets the process's keyless signer (tests).
func resetKeylessLedgerSigner() {
	keylessLedgerSigner.mu.Lock()
	keylessLedgerSigner.s = nil
	keylessLedgerSigner.mu.Unlock()
}

// certifyKeyConfigured names where a certify key would have come from —
// the variable that is set, or the key file at the default path — or ""
// when none is. It only looks: nothing is read, and nothing is created.
func certifyKeyConfigured() string {
	for _, v := range []string{"CORRALAI_CERTIFY_KEY", "CORRALAI_CERTIFY_KEY_FILE"} {
		if strings.TrimSpace(os.Getenv(v)) != "" {
			return v
		}
	}
	if p, err := statedir.Path(statedir.CertifyKey); err == nil {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// preflightLedgerSigner asks, before a paid run, what selectLedgerSigner
// would refuse at its end and can be known without the network: an unknown
// CORRALAI_LEDGER_SIGNER, or keyless signing where this process has no OIDC
// token or repository.
func preflightLedgerSigner() error {
	keyless, err := auditpush.KeylessSignerSelected()
	if err != nil {
		return err
	}
	if keyless {
		return auditpush.CheckKeylessSigning()
	}
	return nil
}

func runLedgerRetract(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger retract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "", "why, in your words — recorded verbatim on the retraction entry (required)")
	// Go's flag parsing stops at the first positional, and the natural
	// spelling puts --reason last; flags are moved to the front so either
	// order works.
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 2 || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "corral ledger retract: usage: corral ledger retract <dir> <entry hash or prefix> --reason \"…\"")
		return 2
	}
	dir, target := strings.TrimRight(fs.Arg(0), "/"), fs.Arg(1)
	signer, signed, kerr := selectLedgerSigner(stderr)
	if kerr != nil {
		fmt.Fprintf(stderr, "corral ledger retract: %v\n", kerr)
		return 1
	}
	name, err := auditpush.WriteRetraction(dir, target, *reason, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger retract: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "retracted %.12s in %s: %s (%s) — the entry stays in the chain and is no longer the record\n", target, name, *reason, signed)
	return 0
}

func runLedgerCheckpoint(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger checkpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	expectHead := fs.String("expect-head", "", "the hash this chain's newest entry must have, held from OUTSIDE the directory. REQUIRED on a chain that registers signers (at least 12 hex characters): the root's signature on a checkpoint makes the chain as it stands permanent, and a chain whose newest entries — a revocation, say — were deleted would be cemented without them")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "corral ledger checkpoint: usage: corral ledger checkpoint [--expect-head <hash>] <dir>")
		return 2
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger checkpoint: %v\n", err)
		return 1
	}
	var (
		signer  auditpush.LedgerSigner
		signed  string
		rootPub ed25519.PublicKey
	)
	if auditpush.ChainHasSigners(entries) {
		// The root vouches for the carried set (see WriteCheckpoint), so the
		// root signs, and the chain must be the one the operator expects.
		// The anchor is checked FIRST and against the head the root key
		// verifies, so a refusal precedes every write and delete.
		root, rpub, ok := rootGate("checkpoint", dir, stderr)
		if !ok {
			return 2
		}
		if strings.TrimSpace(*expectHead) == "" {
			fmt.Fprintf(stderr, "corral ledger checkpoint: %s registers signers, so a checkpoint needs --expect-head <hash> from OUTSIDE the directory (a git ref, a note) — a root checkpoint over a chain whose newest entries were removed would make a revoked key permanent. Nothing written\n", dir)
			return 2
		}
		checks, err := auditpush.VerifyLedgerDir(dir, rpub)
		if err != nil || len(checks) == 0 {
			fmt.Fprintf(stderr, "corral ledger checkpoint: cannot read %s's head: %v\n", dir, err)
			return 1
		}
		short, match := matchExpectHead(*expectHead, checks[len(checks)-1].Hash)
		if short {
			fmt.Fprintf(stderr, "corral: --expect-head %s is too short to be an anchor — give at least 12 hex characters\n", strings.TrimSpace(*expectHead))
			return 2
		}
		if !match {
			fmt.Fprintf(stderr, "corral ledger checkpoint: head is %.12s, expected %s — entries were removed from the END of this chain, which the chain itself cannot detect. Nothing written\n", checks[len(checks)-1].Hash, strings.TrimSpace(*expectHead))
			return 1
		}
		signer, rootPub, signed = root, rpub, "signed by root "+root.KeyID
	} else {
		// A checkpoint is a genesis: where the chain begins stays with the
		// ledger's root key, and the verifier refuses a keyless one. Said
		// here, before any signer is built or anything written.
		if keyless, _ := auditpush.KeylessSignerSelected(); keyless {
			fmt.Fprintf(stderr, "corral ledger checkpoint: %s=%s signs keylessly, and a checkpoint must be signed by the ledger's root key or the local certify key — unset %s to checkpoint. Nothing written\n", auditpush.LedgerSignerEnv, auditpush.LedgerSignerSigstore, auditpush.LedgerSignerEnv)
			return 2
		}
		var kerr error
		signer, signed, kerr = selectLedgerSigner(stderr)
		if kerr != nil {
			fmt.Fprintf(stderr, "corral ledger checkpoint: %v\n", kerr)
			return 1
		}
	}
	name, pruned, err := auditpush.WriteCheckpoint(dir, signer, rootPub)
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

func runLedgerPush(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral ledger push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	withSource := fs.Bool("push-source", false, "also send the source bytes the entries hold — authored tests, verdict JSON, reproduction scripts and their output; off by default, they quote the audited code")
	dryRun := fs.Bool("dry-run", false, "say what would be pushed and write nothing")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "corral ledger push: usage: corral ledger push <dir> <dsn> [--push-source] [--dry-run]")
		return 2
	}
	dir, target := strings.TrimRight(fs.Arg(0), "/"), strings.TrimSpace(fs.Arg(1))
	if auditpush.IsLedgerDir(target) {
		fmt.Fprintln(stderr, "corral ledger push: the target is a directory — to move entries between directories, `corral ledger append` re-links them one at a time")
		return 2
	}
	plan, err := auditpush.PushLedgerDir(dir, target, *withSource, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger push: %v\n", err)
		return 1
	}
	verb := "pushed"
	if *dryRun {
		verb = "would push"
	}
	fmt.Fprintf(stdout, "%s %d scan(s) (%d file rows), %d review(s) (%d findings), %d adjudication(s) from %s to %s\n", verb, plan.Scans, plan.Files, plan.Reviews, plan.Findings, plan.Adjudications, dir, target)
	if plan.Skipped > 0 {
		fmt.Fprintf(stdout, "  %d entr%s already there, skipped\n", plan.Skipped, map[bool]string{true: "y", false: "ies"}[plan.Skipped == 1])
	}
	if plan.Retracted > 0 {
		// "entries", not "scans": PushLedgerDir counts every retracted KIND —
		// scans, reviews and adjudications alike — so calling them scans
		// described the number as something narrower than it is. (Round
		// four, R7.)
		fmt.Fprintf(stdout, "  %d retracted entr%s left out — not the record\n", plan.Retracted, map[bool]string{true: "y", false: "ies"}[plan.Retracted == 1])
		for _, h := range plan.RetractedButPushed {
			fmt.Fprintf(stdout, "  ! %.12s was retracted AFTER the warehouse received it — the rows are still there and a push cannot remove them; delete them yourself, or the view keeps serving what the record disowns\n", h)
		}
	}
	if plan.NotPushable > 0 {
		fmt.Fprintf(stdout, "  %d retraction/checkpoint/signer entr%s are chain facts, not rows\n", plan.NotPushable, map[bool]string{true: "y", false: "ies"}[plan.NotPushable == 1])
	}
	source := "source withheld (hashes only)"
	if *withSource {
		source = "source included (--push-source)"
	}
	fmt.Fprintf(stdout, "  %s; a repeat push skips what the warehouse holds by entry hash (rows an older corral pushed carry none and would repeat)\n", source)
	return 0
}
