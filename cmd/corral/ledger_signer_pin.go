// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// ledgerPubKeyFile is the file at the root of a ledger directory that names
// its root public key: hex and a newline, the format of the repository's own
// LEDGER_PUBKEY. `signer pin --new-root` writes it beside the chain it roots.
const ledgerPubKeyFile = "LEDGER_PUBKEY"

// fetchRepoID reads a GitHub repository's numeric id, the value a keyless
// certificate carries in its .15 extension and a pin names, from the public
// API. A variable so tests stay offline. It refuses an answer for a
// different repository than the one asked: api.github.com follows a rename
// with a redirect, and pinning the id of wherever the old name now points
// would pin a repository the owner did not name.
var fetchRepoID = func(ctx context.Context, repo string) (string, error) {
	url := "https://api.github.com/repos/" + repo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s answered %s (a private repository needs --repo-id)", url, resp.Status)
	}
	var body struct {
		ID       json.Number `json:"id"`
		FullName string      `json:"full_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	if !strings.EqualFold(body.FullName, repo) {
		return "", fmt.Errorf("GET %s answered for %q, not %q — renamed or moved; name the repository it is now", url, body.FullName, repo)
	}
	return body.ID.String(), nil
}

const pinUsage = `usage: corral ledger signer pin <dir> --ledger-repo <owner/name> --workflow <path> --ref <ref> [--workflow <path> --ref <ref> …]
                                  [--repo-id <digits>] [--new-root --root-key-out <path>]
`

// runSignerPin is `corral ledger signer pin`: the root's statement of which
// GitHub Actions runs may sign this ledger keylessly. It writes one
// root-signed entry (and, with --new-root, the root's seed file and
// LEDGER_PUBKEY), prints the git commands that publish them, and runs none.
func runSignerPin(args []string, stdout, stderr io.Writer) int {
	const verb = "signer pin"
	fs := flag.NewFlagSet("corral ledger signer pin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoFlag := fs.String("ledger-repo", ledgerRepoUsageDefault, ledgerRepoUsage+" (required: the pin is about this repository's runs)")
	var workflows, refs stringSlice
	fs.Var(&workflows, "workflow", "a workflow path in the repository allowed to sign, e.g. .github/workflows/corral.yml (required, repeatable; the Nth --workflow pairs with the Nth --ref)")
	fs.Var(&refs, "ref", "the full ref that workflow must run on, e.g. refs/heads/main (required, repeatable, paired with --workflow by position)")
	repoID := fs.String("repo-id", "", "the repository's numeric GitHub id (`gh api repos/<owner/name> --jq .id`). When absent it is read from api.github.com and printed before anything is written")
	newRoot := fs.Bool("new-root", false, "make a NEW root key for a chain no ed25519 key has signed (keyless or unsigned entries only), and sign the pin with it; needs --root-key-out")
	rootKeyOut := fs.String("root-key-out", "", "with --new-root: the file the new root's seed is written to (mode 0600, never one that exists, never inside the ledger directory). Store it offline; corral never reads it from a file")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	refuse := func(msg string) int {
		fmt.Fprintf(stderr, "corral ledger signer pin: %s\n%s", msg, pinUsage)
		return 2
	}
	repo := strings.TrimSpace(*repoFlag)
	switch {
	case fs.NArg() != 1:
		return refuse("name exactly one ledger directory")
	case repo == "":
		return refuse("--ledger-repo is required: a pin is about one repository's runs, and it is never guessed")
	case len(workflows) == 0:
		return refuse("--workflow is required: at least one workflow may sign, or there is nothing to pin")
	case len(refs) == 0:
		return refuse("--ref is required: each --workflow is pinned to the full ref it runs on")
	case len(workflows) != len(refs):
		return refuse(fmt.Sprintf("--workflow and --ref pair by position, and %d --workflow were given against %d --ref", len(workflows), len(refs)))
	case *newRoot && strings.TrimSpace(*rootKeyOut) == "":
		return refuse("--new-root needs --root-key-out <path>: the new root's seed is written there and nowhere else")
	case !*newRoot && *rootKeyOut != "":
		return refuse("--root-key-out is only for --new-root; an existing root's seed is read from " + ledgerRootKeyEnv)
	}
	if !ledgerRepoRE.MatchString(repo) {
		return refuse(fmt.Sprintf("--ledger-repo %q is not owner/name (letters, digits, - . _ only, one slash)", repo))
	}
	dir := strings.TrimRight(fs.Arg(0), "/")

	// Every check that can refuse runs before anything is written.
	var (
		root auditpush.Ed25519LedgerSigner
		ok   bool
		seed []byte
	)
	if *newRoot {
		if !newRootGate(dir, repo, *rootKeyOut, stderr) {
			return 2
		}
	} else if root, _, ok = rootGate(verb, dir, repo, stderr); !ok {
		return 2
	}
	id := strings.TrimSpace(*repoID)
	if id == "" {
		got, err := fetchRepoID(context.Background(), repo)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer pin: cannot read %s's repository id (%v) — pass --repo-id <digits> (`gh api repos/%s --jq .id`); nothing written\n", repo, err, repo)
			return 2
		}
		id = got
		fmt.Fprintf(stdout, "repository id of %s, read from api.github.com: %s\n", repo, id)
	}
	pin := &auditpush.KeylessPin{RepoID: id}
	for i := range workflows {
		pin.Allow = append(pin.Allow, auditpush.PinnedWorkflow{Workflow: workflows[i], Ref: refs[i]})
	}
	change := auditpush.SignerChange{Action: auditpush.SignerPinKeyless, KeylessPin: pin}
	if p := ledger.SignerShapeProblem(&change); p != "" {
		fmt.Fprintf(stderr, "corral ledger signer pin: %s; nothing written\n", p)
		return 2
	}

	written := []string{}
	if *newRoot {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer pin: generate the root key: %v; nothing written\n", err)
			return 1
		}
		seed = priv.Seed()
		root = auditpush.Ed25519LedgerSigner{KeyID: auditpush.KeyFingerprint(pub), Key: priv}
		created, err := writeNewRoot(dir, *rootKeyOut, seed, pub)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer pin: %v\n", err)
			return 1
		}
		file, err := auditpush.WriteSignerChange(dir, change, root)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer pin: %v — %s\n", err, undoNewRoot(created))
			return 1
		}
		fmt.Fprintf(stdout, "new root %s, public key %s\n", root.KeyID, hex.EncodeToString(pub))
		fmt.Fprintf(stdout, "wrote the root's seed to %s (mode 0600): store this seed offline (for example `pass insert`); corral never reads it from a file. Once it is stored, delete the file; a later root operation reads the seed from %s\n", *rootKeyOut, ledgerRootKeyEnv)
		fmt.Fprintf(stdout, "wrote %s\n", filepath.Join(dir, ledgerPubKeyFile))
		written = append(written, ledgerPubKeyFile, filepath.Join(auditpush.ScansSubdir, file))
		printPinned(stdout, repo, pin, dir, file, root.KeyID)
	} else {
		file, err := auditpush.WriteSignerChange(dir, change, root)
		if err != nil {
			fmt.Fprintf(stderr, "corral ledger signer pin: %v\n", err)
			return 1
		}
		written = append(written, filepath.Join(auditpush.ScansSubdir, file))
		printPinned(stdout, repo, pin, dir, file, root.KeyID)
	}
	printPublish(stdout, dir, written, "Pin keyless signing of the ledger to "+pinPairs(pin))
	return 0
}

// runSignerUnpin is `corral ledger signer unpin`: lift the pin in force, by
// the root, with a reason, from this position on. Entries already judged
// under the pin keep the level they were signed at.
func runSignerUnpin(args []string, stdout, stderr io.Writer) int {
	const verb = "signer unpin"
	fs := flag.NewFlagSet("corral ledger signer unpin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoFlag := fs.String("ledger-repo", ledgerRepoUsageDefault, ledgerRepoUsage+" (required: the chain's keyless entries are checked against it)")
	reason := fs.String("reason", "", "why, in your words — recorded verbatim (required)")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	refuse := func(msg string) int {
		fmt.Fprintf(stderr, "corral ledger signer unpin: %s\nusage: corral ledger signer unpin <dir> --ledger-repo <owner/name> --reason \"…\"\n", msg)
		return 2
	}
	repo := strings.TrimSpace(*repoFlag)
	switch {
	case fs.NArg() != 1:
		return refuse("name exactly one ledger directory")
	case repo == "":
		return refuse("--ledger-repo is required: the chain's keyless entries are checked against it, and it is never guessed")
	case strings.TrimSpace(*reason) == "":
		return refuse("--reason is required: an unpin with no stated cause is a record nobody can weigh")
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	root, _, ok := rootGate(verb, dir, repo, stderr)
	if !ok {
		return 2
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer unpin: %v\n", err)
		return 1
	}
	// The chain verified clean under the root a moment ago, so every signer
	// entry in it was applied by the walk, and the last pin or unpin is the
	// state at the head.
	inForce := false
	for _, e := range entries {
		if e.Kind == auditpush.KindSigner && e.Signer != nil {
			switch e.Signer.Action {
			case auditpush.SignerPinKeyless:
				inForce = true
			case auditpush.SignerUnpinKeyless:
				inForce = false
			}
		}
	}
	if !inForce {
		fmt.Fprintf(stderr, "corral ledger signer unpin: no keyless pin is in force at the head of %s; nothing written\n", dir)
		return 2
	}
	file, err := auditpush.WriteSignerChange(dir, auditpush.SignerChange{Action: auditpush.SignerUnpinKeyless, Reason: strings.TrimSpace(*reason)}, root)
	if err != nil {
		fmt.Fprintf(stderr, "corral ledger signer unpin: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "unpinned keyless signing in %s: %s (signed by root %s)\n", filepath.Join(dir, auditpush.ScansSubdir, file), strings.TrimSpace(*reason), root.KeyID)
	printPublish(stdout, dir, []string{filepath.Join(auditpush.ScansSubdir, file)}, "Unpin keyless signing of the ledger: "+strings.TrimSpace(*reason))
	return 0
}

// newRootGate is rootGate's counterpart for --new-root, and like it refuses
// before anything is written. A new root may start only where no root can
// already be: no entry is ed25519-signed (a signer entry or checkpoint is
// always root-signed, so none of those either), neither root variable is
// set (each names a root this new one would contradict, and which of the two
// to believe is never guessed), no LEDGER_PUBKEY names one, and the chain
// verifies clean against the repository, so a broken chain, or one signed
// from another repository, is never given a root. The seed's destination is
// checked too: it must not exist, and must not sit in the ledger directory,
// where the `git add -A` that publishes a ledger branch would publish it.
func newRootGate(dir, repo, seedOut string, stderr io.Writer) bool {
	no := func(format string, a ...any) bool {
		fmt.Fprintf(stderr, "corral ledger signer pin --new-root: "+format+"; nothing written\n", a...)
		return false
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return no("%s is not a directory — a new root starts on an existing ledger directory (a checkout of its ledger branch)", dir)
	}
	if strings.TrimSpace(os.Getenv(ledgerRootKeyEnv)) != "" {
		return no("%s is set, which names a root already; drop --new-root to pin with that root, or unset it", ledgerRootKeyEnv)
	}
	if strings.TrimSpace(os.Getenv(auditpush.LedgerPubKeyEnv)) != "" {
		return no("%s is set, which names a root this new one would not be; unset it", auditpush.LedgerPubKeyEnv)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return no("%v", err)
	}
	for _, e := range entries {
		if e.Signature != "" {
			return no("this chain already has a root; set CORRALAI_LEDGER_ROOT_KEY to that root's seed and drop --new-root (%s is ed25519-signed by %s)", e.File, e.KeyID)
		}
	}
	trust, err := ledgerTrust(nil, repo)
	if err != nil {
		return no("%v", err)
	}
	checks, err := auditpush.VerifyLedgerDirTrust(dir, trust)
	if err != nil {
		return no("%v", err)
	}
	for _, c := range checks {
		if c.Problem != "" {
			return no("%s does not verify for %s (%s: %s) — a broken chain, or one signed from another repository, is not given a root", dir, repo, c.File, c.Problem)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, ledgerPubKeyFile)); err == nil {
		return no("%s already exists and names a root; a new root would contradict it", filepath.Join(dir, ledgerPubKeyFile))
	}
	if _, err := os.Lstat(seedOut); err == nil {
		return no("--root-key-out %s exists — the seed is never written over a file", seedOut)
	} else if !os.IsNotExist(err) {
		return no("--root-key-out %s: %v", seedOut, err)
	}
	if inside, err := pathWithin(seedOut, dir); err != nil {
		return no("--root-key-out %s: %v", seedOut, err)
	} else if inside {
		return no("--root-key-out %s is inside the ledger directory %s, where publishing the ledger would publish the seed", seedOut, dir)
	}
	return true
}

// pathWithin reports whether path is dir or under it, after both are made
// absolute and their existing directories' symlinks resolved, so a symlink
// or a ".." cannot place the seed in the ledger directory unnoticed.
func pathWithin(path, dir string) (bool, error) {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	if realDir, err = filepath.Abs(realDir); err != nil {
		return false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(abs)
	if realParent, err := filepath.EvalSymlinks(parent); err == nil {
		parent = realParent
	}
	rel, err := filepath.Rel(realDir, filepath.Join(parent, filepath.Base(abs)))
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

// writeNewRoot writes the seed to seedOut (0600, refusing a file that
// exists) and LEDGER_PUBKEY into dir, and returns what it created so a later
// failure can remove it. The seed's bytes appear in no error.
func writeNewRoot(dir, seedOut string, seed []byte, pub ed25519.PublicKey) ([]string, error) {
	var created []string
	if err := writeExclusive(seedOut, []byte(hex.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write the root's seed to --root-key-out %s: %w; nothing written", seedOut, err)
	}
	created = append(created, seedOut)
	pubFile := filepath.Join(dir, ledgerPubKeyFile)
	if err := writeExclusive(pubFile, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil { // #nosec G306 -- a public key the ledger branch publishes
		return created, fmt.Errorf("write %s: %w — %s", pubFile, err, undoNewRoot(created))
	}
	return append(created, pubFile), nil
}

// writeExclusive creates path with mode and writes b, refusing a path that
// exists (O_EXCL), so neither a seed nor a public key replaces a file.
func writeExclusive(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- the path the operator named, created and never read
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// undoNewRoot removes the files a --new-root created when the pin could not
// be written after them, and says what it did: a seed that roots no entry
// must not be mistaken later for this ledger's root.
func undoNewRoot(created []string) string {
	var failed []string
	for _, p := range created {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, p)
		}
	}
	if len(failed) > 0 {
		return "the pin was not written, and these files it created could not be removed and root nothing: " + strings.Join(failed, ", ")
	}
	return "the pin was not written, and the seed and LEDGER_PUBKEY it created were removed"
}

func printPinned(stdout io.Writer, repo string, pin *auditpush.KeylessPin, dir, file, rootID string) {
	fmt.Fprintf(stdout, "pinned keyless signing of %s (repository id %s) to %s in %s (signed by root %s)\n", repo, pin.RepoID, pinPairs(pin), filepath.Join(dir, auditpush.ScansSubdir, file), rootID)
}

func pinPairs(pin *auditpush.KeylessPin) string {
	pairs := make([]string, len(pin.Allow))
	for i, a := range pin.Allow {
		pairs[i] = a.Workflow + "@" + a.Ref
	}
	return strings.Join(pairs, ", ")
}

// printPublish prints the git commands that publish what the verb wrote. The
// verb runs no git itself: publishing the record is the owner's act, from
// the owner's checkout and credentials.
func printPublish(stdout io.Writer, dir string, paths []string, message string) {
	add := append([]string{"git", "-C", dir, "add"}, paths...)
	fmt.Fprintf(stdout, "corral pushes nothing. To publish, run:\n  %s && %s && %s\n",
		shellQuoted(add), shellQuoted([]string{"git", "-C", dir, "commit", "-m", message}), shellQuoted([]string{"git", "-C", dir, "push"}))
}
