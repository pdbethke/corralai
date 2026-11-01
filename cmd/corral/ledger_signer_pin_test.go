// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/ledger/ledgertest"
	"github.com/pdbethke/corralai/internal/statedir"
)

const (
	pinTestRepoID   = "4242"
	pinTestWorkflow = ".github/workflows/corral.yml"
	pinTestRef      = "refs/heads/main"
)

// pinClaims are the claims of a run the pin under test allows.
func pinClaims() ledgertest.CertClaims {
	return ledgertest.CertClaims{
		Workflow: "https://github.com/" + testKeylessRepo + "/" + pinTestWorkflow + "@" + pinTestRef,
		Ref:      pinTestRef, RepoID: pinTestRepoID, Trigger: "push",
	}
}

// keylessPinEnv is a keyless world with no root anywhere: an in-process
// Sigstore stands in for the public-good one, no root seed or root key is in
// the environment, and the repository id lookup is offline unless a test
// replaces it.
func keylessPinEnv(t *testing.T) *ledgertest.Sigstore {
	t.Helper()
	isolateStateHome(t)
	s := ledgertest.New(t)
	prevRoot := ledgerSigstoreRoot
	ledgerSigstoreRoot = func() (*ledger.SigstoreRoot, error) { return s.Root(t), nil }
	prevFetch := fetchRepoID
	fetchRepoID = func(context.Context, string) (string, error) {
		return "", errors.New("offline in tests")
	}
	t.Cleanup(func() { ledgerSigstoreRoot = prevRoot; fetchRepoID = prevFetch })
	t.Setenv(auditpush.LedgerPubKeyEnv, "")
	t.Setenv(ledgerRootKeyEnv, "")
	return s
}

// appendKeyless places one keyless scan entry signed as a run with claims c.
func appendKeyless(t *testing.T, s *ledgertest.Sigstore, dir, commit string, c ledgertest.CertClaims) {
	t.Helper()
	prev := newKeylessLedgerSigner
	newKeylessLedgerSigner = func() (auditpush.LedgerSigner, error) {
		return auditpush.NewSigstoreLedgerSigner(func(h []byte) ([]byte, error) { return s.IssueAs(t, h, testKeylessRepo, c), nil }, testKeylessRepo, s.Root(t)), nil
	}
	resetKeylessLedgerSigner()
	defer func() { newKeylessLedgerSigner = prev; resetKeylessLedgerSigner() }()
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	defer t.Setenv(auditpush.LedgerSignerEnv, "")
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: commit, ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}
	if _, err := pushBundle(dir+"/", b, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func pinArgs(dir string, extra ...string) []string {
	return append([]string{"signer", "pin", dir, "--ledger-repo", testKeylessRepo, "--workflow", pinTestWorkflow, "--ref", pinTestRef}, extra...)
}

// The owner of a keyless-only ledger can pin it with a root made on the
// spot: the seed goes only to the path named, 0600, and never to the output;
// LEDGER_PUBKEY is written beside the chain; and a keyless entry the pin
// allows then verifies as keyless-pinned under that root.
func TestPinNewRootOnKeylessOnlyChain(t *testing.T) {
	s := keylessPinEnv(t)
	fetched := ""
	fetchRepoID = func(_ context.Context, repo string) (string, error) { fetched = repo; return pinTestRepoID, nil }
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	seedPath := filepath.Join(t.TempDir(), "root.seed")

	code, out := run(t, pinArgs(dir, "--new-root", "--root-key-out", seedPath)...)
	if code != 0 {
		t.Fatalf("pin --new-root: exit %d\n%s", code, out)
	}
	if fetched != testKeylessRepo || !strings.Contains(out, pinTestRepoID) {
		t.Fatalf("the repository id was looked up for %q and must be printed before it is written:\n%s", fetched, out)
	}
	st, err := os.Stat(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("seed file mode %v, want 0600", st.Mode().Perm())
	}
	seedRaw, err := os.ReadFile(seedPath) // #nosec G304 -- the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	seed := strings.TrimSpace(string(seedRaw))
	if strings.Contains(out, seed) {
		t.Fatal("the root seed's bytes were printed")
	}
	if _, err := buildstore.ParseSigningKeySeed(seedRaw); err != nil {
		t.Fatalf("the seed file is not a hex Ed25519 seed: %v", err)
	}
	pubHex := pubHexOfSeed(t, seed)
	pk, err := os.ReadFile(filepath.Join(dir, "LEDGER_PUBKEY")) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatalf("LEDGER_PUBKEY not written: %v", err)
	}
	if string(pk) != pubHex+"\n" {
		t.Fatalf("LEDGER_PUBKEY is %q, want the new root %s and a newline", pk, pubHex)
	}
	for _, want := range []string{pubHex, "store this seed offline (for example `pass insert`); corral never reads it from a file", "git -C " + dir, "git -C " + dir + " push"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	appendKeyless(t, s, dir, "bbbb2222", pinClaims())
	rawPub, _ := hex.DecodeString(pubHex)
	trust, err := ledgerTrust(rawPub, testKeylessRepo)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := auditpush.VerifyLedgerDirTrust(dir, trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 3 {
		t.Fatalf("want keyless, pin, keyless: %d entries", len(checks))
	}
	for _, c := range checks {
		if c.Problem != "" {
			t.Fatalf("%s: %s", c.File, c.Problem)
		}
	}
	if got := checks[2].Level; got != ledger.LevelKeylessPinned {
		t.Fatalf("the keyless entry after the pin is %q, want %q", got, ledger.LevelKeylessPinned)
	}
	if got := checks[0].Level; got != ledger.LevelKeylessUnpinned {
		t.Fatalf("the keyless entry before the pin is %q, want it to keep %q", got, ledger.LevelKeylessUnpinned)
	}
}

// A chain an ed25519 key has signed already has a root, and a new one would
// fork who vouches for it: refused, and nothing is written — not the seed,
// not LEDGER_PUBKEY, not an entry.
func TestPinNewRootRefusedWhenARootExists(t *testing.T) {
	keylessPinEnv(t)
	dir := t.TempDir()
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	pushAs(t, dir, seedHex(0x11), "aaaa1111")
	before := ledgerFileCount(t, dir)
	seedPath := filepath.Join(t.TempDir(), "root.seed")
	code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", seedPath)...)
	if code == 0 || !strings.Contains(out, "this chain already has a root; set CORRALAI_LEDGER_ROOT_KEY") {
		t.Fatalf("exit %d, want the refusal naming CORRALAI_LEDGER_ROOT_KEY:\n%s", code, out)
	}
	if _, err := os.Stat(seedPath); !os.IsNotExist(err) {
		t.Fatalf("a seed was written on a refusal (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "LEDGER_PUBKEY")); !os.IsNotExist(err) {
		t.Fatalf("LEDGER_PUBKEY was written on a refusal (%v)", err)
	}
	if n := ledgerFileCount(t, dir); n != before {
		t.Fatalf("%d entries after a refusal, want %d", n, before)
	}
}

// The seed goes only where the operator said, and never over a file or into
// the ledger directory, where `git add -A` would publish it.
func TestPinNewRootRefusesASeedPathThatExistsOrSitsInTheLedger(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	existing := filepath.Join(t.TempDir(), "taken")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, filepath.Join(dir, "root.seed"), filepath.Join(dir, "sub", "..", "root.seed")} {
		code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", path)...)
		if code == 0 || !strings.Contains(out, "--root-key-out") {
			t.Fatalf("%s: exit %d, want a refusal naming --root-key-out:\n%s", path, code, out)
		}
	}
	if got, _ := os.ReadFile(existing); string(got) != "keep me\n" { // #nosec G304 -- the test's own temp file
		t.Fatalf("an existing file was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "root.seed")); !os.IsNotExist(err) {
		t.Fatalf("a seed was written into the ledger directory (%v)", err)
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries after refusals, want 1", n)
	}
}

func TestPinRefusesWithoutRefOrWorkflow(t *testing.T) {
	keylessPinEnv(t)
	dir := t.TempDir()
	base := []string{"signer", "pin", dir, "--ledger-repo", testKeylessRepo, "--repo-id", pinTestRepoID}
	for _, tc := range []struct {
		name  string
		args  []string
		wants []string
	}{
		{"no ref", []string{"--workflow", pinTestWorkflow}, []string{"--ref"}},
		{"no workflow", []string{"--ref", pinTestRef}, []string{"--workflow"}},
		{"unpaired", []string{"--workflow", pinTestWorkflow, "--workflow", ".github/workflows/release.yml", "--ref", pinTestRef}, []string{"--workflow", "--ref"}},
		{"no ledger repo", []string{"--workflow", pinTestWorkflow, "--ref", pinTestRef}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, base...), tc.args...)
			wants := tc.wants
			if tc.name == "no ledger repo" {
				args = []string{"signer", "pin", dir, "--repo-id", pinTestRepoID, "--workflow", pinTestWorkflow, "--ref", pinTestRef}
				wants = []string{"--ledger-repo"}
			}
			code, out := run(t, args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			for _, w := range wants {
				if !strings.Contains(out, w) {
					t.Errorf("refusal lacks %q:\n%s", w, out)
				}
			}
			if n := ledgerFileCount(t, dir); n != 0 {
				t.Fatalf("%d entries written on a usage refusal", n)
			}
		})
	}
}

// With no --repo-id and no network the verb cannot learn the id, and says
// which flag supplies it rather than pinning without one.
func TestPinOfflineWithoutRepoIDNamesTheFlag(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	seedPath := filepath.Join(t.TempDir(), "root.seed")
	code, out := run(t, pinArgs(dir, "--new-root", "--root-key-out", seedPath)...)
	if code == 0 || !strings.Contains(out, "--repo-id") {
		t.Fatalf("exit %d, want a refusal naming --repo-id:\n%s", code, out)
	}
	if _, err := os.Stat(seedPath); !os.IsNotExist(err) {
		t.Fatalf("a seed was written though no pin could be (%v)", err)
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries, want the 1 keyless entry only", n)
	}
}

// pin and unpin read keyless entries; add (and revoke, and checkpoint) still
// refuse a keyless ledger before they verify anything.
func TestAddStillRefusedOnKeylessLedger(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	t.Setenv(ledgerRootKeyEnv, seedHex(0x11))
	code, out := run(t, "signer", "add", dir, "--name", "laptop", "--pub", pubHexOfSeed(t, seedHex(0x22)))
	if code == 0 || !strings.Contains(out, "holds keyless entries") {
		t.Fatalf("exit %d, want the keyless refusal:\n%s", code, out)
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
}

func TestUnpinNeedsReason(t *testing.T) {
	f := newSignerFixture(t)
	code, out := run(t, "signer", "unpin", f.dir, "--ledger-repo", testKeylessRepo)
	if code != 2 || !strings.Contains(out, "corral ledger signer unpin: --reason is required") {
		t.Fatalf("exit %d, want a usage refusal naming --reason:\n%s", code, out)
	}
	if n := ledgerFileCount(t, f.dir); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
}

// On a chain that already has a root, pin and unpin go through the root gate
// with the seed in CORRALAI_LEDGER_ROOT_KEY, and the chain verifies after
// each; an unpin with no pin in force is refused before it is written.
func TestPinAndUnpinUnderAnExistingRoot(t *testing.T) {
	keylessPinEnv(t)
	f := newSignerFixture(t)
	code, out := run(t, "signer", "unpin", f.dir, "--ledger-repo", testKeylessRepo, "--reason", "nothing to unpin")
	if code == 0 || !strings.Contains(out, "no keyless pin is in force") {
		t.Fatalf("unpin with no pin: exit %d\n%s", code, out)
	}
	if code, out := run(t, pinArgs(f.dir, "--repo-id", pinTestRepoID)...); code != 0 {
		t.Fatalf("pin: exit %d\n%s", code, out)
	}
	if code, out := run(t, "verify", "--pub", f.rootPub, f.dir); code != 0 {
		t.Fatalf("verify after pin: exit %d\n%s", code, out)
	}
	if code, out := run(t, "signer", "unpin", f.dir, "--ledger-repo", testKeylessRepo, "--reason", "moving to a key"); code != 0 {
		t.Fatalf("unpin: exit %d\n%s", code, out)
	}
	if code, out := run(t, "verify", "--pub", f.rootPub, f.dir); code != 0 {
		t.Fatalf("verify after unpin: exit %d\n%s", code, out)
	}
	if n := ledgerFileCount(t, f.dir); n != 3 {
		t.Fatalf("%d entries, want scan, pin, unpin", n)
	}
	// --new-root is never a way round the existing root.
	t.Setenv(ledgerRootKeyEnv, "")
	if code, out := run(t, pinArgs(f.dir, "--repo-id", pinTestRepoID)...); code == 0 || !strings.Contains(out, ledgerRootKeyEnv) {
		t.Fatalf("pin with no root seed: exit %d\n%s", code, out)
	}
}

// The verb never pushes: it runs no git at all. A `git` on PATH that records
// any invocation stays untouched, the ledger directory is no repository, and
// the push is printed for the owner to run.
func TestPinNeverPushes(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	bin, trace := t.TempDir(), filepath.Join(t.TempDir(), "git-ran")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho \"$@\" >> "+trace+"\n"), 0o700); err != nil { // #nosec G306 -- a test's fake git
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", filepath.Join(t.TempDir(), "root.seed"))...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		got, _ := os.ReadFile(trace) // #nosec G304 -- the test's own temp file
		t.Fatalf("the verb ran git: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the ledger directory became a repository (%v)", err)
	}
	if !strings.Contains(out, "git -C "+dir+" push") {
		t.Fatalf("the push command is not printed:\n%s", out)
	}
}

// newRootPinned is a keyless ledger the owner just rooted and pinned with
// --new-root: one keyless entry, then the pin. It returns the seed written.
func newRootPinned(t *testing.T, s *ledgertest.Sigstore, dir string) string {
	t.Helper()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	seedPath := filepath.Join(t.TempDir(), "root.seed")
	if code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", seedPath)...); code != 0 {
		t.Fatalf("pin --new-root: exit %d\n%s", code, out)
	}
	raw, err := os.ReadFile(seedPath) // #nosec G304 -- the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// The seed is never written where corral reads a signing key of its own:
// the certify key file (wherever CORRALAI_CERTIFY_KEY_FILE moves it) holds
// exactly this format, and every later certify would sign as the root.
func TestPinNewRootRefusesASeedPathCorralReadsAsAKey(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	certifyKey := filepath.Join(t.TempDir(), "certify.key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", certifyKey)
	state, err := statedir.Dir(statedir.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{certifyKey, "CORRALAI_CERTIFY_KEY_FILE"},
		{filepath.Join(state, "root.seed"), "state directory"},
		{filepath.Join(state, "corralai_certify_key"), "state directory"},
	} {
		code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", tc.path)...)
		if code == 0 || !strings.Contains(out, tc.want) || !strings.Contains(out, "never written where corral looks for keys") {
			t.Fatalf("%s: exit %d, want a refusal naming %q:\n%s", tc.path, code, tc.want, out)
		}
		if _, err := os.Stat(tc.path); !os.IsNotExist(err) {
			t.Fatalf("%s was written (%v)", tc.path, err)
		}
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries after refusals, want 1", n)
	}
}

// A first-time keyless owner who leaves out --new-root is told it is the way.
func TestPinWithoutNewRootOnARootlessLedgerSaysHowToMakeOne(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	t.Setenv(ledgerRootKeyEnv, seedHex(0x11))
	code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID)...)
	if code != 2 || !strings.Contains(out, "has no root yet") || !strings.Contains(out, "--new-root --root-key-out <path>") {
		t.Fatalf("exit %d, want the refusal pointing at --new-root:\n%s", code, out)
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
}

// After the first pin the owner holds the root seed in the environment, and
// unpin and a second pin go through the root gate on the keyless chain; each
// keyless entry is judged by the pin in force where it stands.
func TestUnpinThenRepinUnderTheNewRoot(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	seed := newRootPinned(t, s, dir)
	appendKeyless(t, s, dir, "bbbb2222", pinClaims())
	t.Setenv(ledgerRootKeyEnv, seed)
	if code, out := run(t, "signer", "unpin", dir, "--ledger-repo", testKeylessRepo, "--reason", "moving the workflow"); code != 0 {
		t.Fatalf("unpin: exit %d\n%s", code, out)
	}
	appendKeyless(t, s, dir, "cccc3333", pinClaims())
	if code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID)...); code != 0 {
		t.Fatalf("second pin: exit %d\n%s", code, out)
	}
	appendKeyless(t, s, dir, "dddd4444", pinClaims())
	rawPub, _ := hex.DecodeString(pubHexOfSeed(t, seed))
	trust, err := ledgerTrust(rawPub, testKeylessRepo)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := auditpush.VerifyLedgerDirTrust(dir, trust)
	if err != nil {
		t.Fatal(err)
	}
	want := []ledger.SigLevel{ledger.LevelKeylessUnpinned, "", ledger.LevelKeylessPinned, "", ledger.LevelKeylessUnpinned, "", ledger.LevelKeylessPinned}
	if len(checks) != len(want) {
		t.Fatalf("%d entries, want %d", len(checks), len(want))
	}
	for i, c := range checks {
		if c.Problem != "" {
			t.Fatalf("%s: %s", c.File, c.Problem)
		}
		if want[i] != "" && c.Level != want[i] {
			t.Errorf("entry %d (%s): level %q, want %q", i, c.File, c.Level, want[i])
		}
		if want[i] == "" && c.Level != ledger.LevelKey {
			t.Errorf("entry %d (%s), a root-signed signer entry: level %q, want %q", i, c.File, c.Level, ledger.LevelKey)
		}
	}
}

// A --repo-id the chain's own keyless entries contradict is a mistake, not
// a pin: refused, naming both ids.
func TestPinRefusesARepoIDTheChainContradicts(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	for _, args := range [][]string{
		pinArgs(dir, "--repo-id", "9999", "--new-root", "--root-key-out", filepath.Join(t.TempDir(), "root.seed")),
	} {
		code, out := run(t, args...)
		if code != 2 || !strings.Contains(out, "9999") || !strings.Contains(out, pinTestRepoID) {
			t.Fatalf("exit %d, want a refusal naming 9999 and %s:\n%s", code, pinTestRepoID, out)
		}
	}
	// A fetched id is held to the same rule.
	fetchRepoID = func(context.Context, string) (string, error) { return "9999", nil }
	seedPath := filepath.Join(t.TempDir(), "root.seed")
	if code, out := run(t, pinArgs(dir, "--new-root", "--root-key-out", seedPath)...); code != 2 || !strings.Contains(out, "9999") {
		t.Fatalf("fetched id: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(seedPath); !os.IsNotExist(err) {
		t.Fatalf("a seed was written for a refused pin (%v)", err)
	}
	if n := ledgerFileCount(t, dir); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
}

// A reader with only the repository name, no root key, still verifies a
// pinned keyless ledger: the barrier to reading is never raised by a pin.
// The pinned entry reads as pinned-but-unchecked, never as checked.
func TestVerifyWithoutPubStillPassesAfterAPin(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	newRootPinned(t, s, dir)
	appendKeyless(t, s, dir, "bbbb2222", pinClaims())
	var out, errb bytes.Buffer
	if code := runVerifyLedger(dir, "", testKeylessRepo, "", &out, &errb); code != 0 {
		t.Fatalf("verify --ledger-repo without --pub: exit %d\n%s%s", code, out.String(), errb.String())
	}
	t.Logf("verify output:\n%s%s", out.String(), errb.String())
	if !strings.Contains(out.String(), ledger.LevelSentence(ledger.LevelKeylessPinnedUnchecked)) {
		t.Fatalf("the pinned entry does not read as pinned-unchecked:\n%s", out.String())
	}
	if strings.Contains(out.String(), ledger.LevelSentence(ledger.LevelKeylessPinned)) {
		t.Fatalf("with no root key a pinned entry read as checked:\n%s", out.String())
	}
}

// The pin lives on the ledger branch, so anyone who can rewrite that branch
// can drop it: the verb says so on every successful pin, names the outside
// anchor that detects the rewrite, and tells verifiers to take the root key
// from outside the branch — on both the --new-root path and the existing-root
// path, which print through one helper.
func TestPinPrintsWhatThePinDependsOn(t *testing.T) {
	want := []string{
		"holds only while the ledger branch cannot be force-pushed or deleted",
		"branch ruleset",
		"--expect-head",
		"take the root public key from outside the ledger branch",
		"--pub",
		"CORRALAI_LEDGER_PUBKEY",
	}
	check := func(name, out string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, out)
			}
		}
	}
	s := keylessPinEnv(t)
	dir := t.TempDir()
	appendKeyless(t, s, dir, "aaaa1111", pinClaims())
	code, out := run(t, pinArgs(dir, "--repo-id", pinTestRepoID, "--new-root", "--root-key-out", filepath.Join(t.TempDir(), "root.seed"))...)
	if code != 0 {
		t.Fatalf("pin --new-root: exit %d\n%s", code, out)
	}
	check("--new-root", out)

	f := newSignerFixture(t)
	code, out = run(t, pinArgs(f.dir, "--repo-id", pinTestRepoID)...)
	if code != 0 {
		t.Fatalf("pin under a root: exit %d\n%s", code, out)
	}
	check("existing root", out)
}

// What the docs and the pin's own output say about a rewrite, executed: cut
// the chain before the pin and append a keyless entry from another branch,
// and the shorter chain still verifies under the right root key, the entry
// reading keyless-unpinned with no problem. Only a head hash held from
// outside the directory (`corral ledger verify --expect-head`) catches it.
func TestTruncatingThePinIsCaughtOnlyByAnOutsideHead(t *testing.T) {
	s := keylessPinEnv(t)
	dir := t.TempDir()
	seed := newRootPinned(t, s, dir)
	pub := pubHexOfSeed(t, seed)
	appendKeyless(t, s, dir, "bbbb2222", pinClaims())
	rawPub, _ := hex.DecodeString(pub)
	trust, err := ledgerTrust(rawPub, testKeylessRepo)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := auditpush.VerifyLedgerDirTrust(dir, trust)
	if err != nil || len(checks) != 3 {
		t.Fatalf("pinned chain: %d checks, %v", len(checks), err)
	}
	head := checks[2].Hash

	// The rewrite: everything from the pin on is removed.
	for _, c := range checks[1:] {
		if err := os.Remove(filepath.Join(dir, auditpush.ScansSubdir, c.File)); err != nil {
			t.Fatal(err)
		}
	}
	attacker := pinClaims()
	attacker.Workflow = "https://github.com/" + testKeylessRepo + "/" + pinTestWorkflow + "@refs/heads/attacker"
	attacker.Ref = "refs/heads/attacker"
	appendKeyless(t, s, dir, "cccc3333", attacker)

	var out, errb bytes.Buffer
	if code := runVerifyLedger(dir, pub, testKeylessRepo, "", &out, &errb); code != 0 {
		t.Fatalf("the truncated chain no longer verifies, so the docs overstate the gap: exit %d\n%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), ledger.LevelSentence(ledger.LevelKeylessUnpinned)) {
		t.Fatalf("the attacker's entry does not read keyless-unpinned:\n%s", out.String())
	}
	out.Reset()
	errb.Reset()
	if code := runVerifyLedger(dir, pub, testKeylessRepo, head, &out, &errb); code == 0 {
		t.Fatalf("--expect-head with the pinned chain's head did not catch the rewrite:\n%s%s", out.String(), errb.String())
	}
}
