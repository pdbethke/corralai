// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// signerFixture is a ledger started by a root key, with the root seed in the
// environment the way an operator would hold it for a ceremony.
type signerFixture struct {
	dir      string
	rootSeed string
	rootPub  string
}

func seedHex(b byte) string { return strings.Repeat(hex.EncodeToString([]byte{b}), ed25519.SeedSize) }

func pubHexOfSeed(t *testing.T, seed string) string {
	t.Helper()
	raw, err := hex.DecodeString(seed)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(ed25519.NewKeyFromSeed(raw).Public().(ed25519.PublicKey))
}

func pushAs(t *testing.T, dir, seed, commit string) {
	t.Helper()
	t.Setenv("CORRALAI_CERTIFY_KEY", seed)
	if _, err := pushBundle(dir+"/", auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: commit, ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func newSignerFixture(t *testing.T) signerFixture {
	t.Helper()
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	t.Setenv("CORRALAI_LEDGER_PUBKEY", "")
	f := signerFixture{dir: t.TempDir(), rootSeed: seedHex(0x11)}
	f.rootPub = pubHexOfSeed(t, f.rootSeed)
	pushAs(t, f.dir, f.rootSeed, "aaaa1111")
	t.Setenv(ledgerRootKeyEnv, f.rootSeed)
	return f
}

func ledgerFileCount(t *testing.T, dir string) int {
	t.Helper()
	n, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(n)
}

func run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runLedger(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestSignerAddRegistersAndVerifies(t *testing.T) {
	f := newSignerFixture(t)
	mPub := pubHexOfSeed(t, seedHex(0x22))
	if code, out := run(t, "signer", "add", f.dir, "--name", "laptop", "--pub", mPub); code != 0 {
		t.Fatalf("add: exit %d\n%s", code, out)
	}
	if code, out := run(t, "verify", "--pub", f.rootPub, f.dir); code != 0 {
		t.Fatalf("verify: exit %d\n%s", code, out)
	}
	code, out := run(t, "signer", "list", "--pub", f.rootPub, f.dir)
	if code != 0 || !strings.Contains(out, "laptop") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}
	if code, out := run(t, "signer", "list", f.dir); code != 0 || !strings.Contains(out, "NOT verified") {
		t.Fatalf("keyless list must say it is unverified: exit %d\n%s", code, out)
	}
}

func TestSignerAddRefusesWrongRoot(t *testing.T) {
	f := newSignerFixture(t)
	t.Setenv(ledgerRootKeyEnv, seedHex(0x33))
	before := ledgerFileCount(t, f.dir)
	code, out := run(t, "signer", "add", f.dir, "--name", "x", "--pub", pubHexOfSeed(t, seedHex(0x22)))
	if code != 2 || !strings.Contains(out, "does not verify under root") || ledgerFileCount(t, f.dir) != before {
		t.Fatalf("a seed that is not the root: exit %d, files %d→%d\n%s", code, before, ledgerFileCount(t, f.dir), out)
	}
}

func TestSignerAddRefusesWithoutRootEnv(t *testing.T) {
	f := newSignerFixture(t)
	t.Setenv(ledgerRootKeyEnv, "")
	t.Setenv("CORRALAI_CERTIFY_KEY", f.rootSeed) // must NOT be used as the root
	before := ledgerFileCount(t, f.dir)
	code, out := run(t, "signer", "add", f.dir, "--name", "x", "--pub", pubHexOfSeed(t, seedHex(0x22)))
	if code != 2 || !strings.Contains(out, "CORRALAI_LEDGER_ROOT_KEY") || ledgerFileCount(t, f.dir) != before {
		t.Fatalf("exit %d, files %d→%d\n%s", code, before, ledgerFileCount(t, f.dir), out)
	}
	t.Setenv(ledgerRootKeyEnv, "not-hex")
	if code, out := run(t, "signer", "add", f.dir, "--name", "x", "--pub", pubHexOfSeed(t, seedHex(0x22))); code != 2 || !strings.Contains(out, "CORRALAI_LEDGER_ROOT_KEY") {
		t.Fatalf("malformed seed: exit %d\n%s", code, out)
	}
}

func TestSignerAddRefusesPubMismatch(t *testing.T) {
	f := newSignerFixture(t)
	t.Setenv("CORRALAI_LEDGER_PUBKEY", pubHexOfSeed(t, seedHex(0x44)))
	before := ledgerFileCount(t, f.dir)
	code, out := run(t, "signer", "add", f.dir, "--name", "x", "--pub", pubHexOfSeed(t, seedHex(0x22)))
	if code != 2 || !strings.Contains(out, "CORRALAI_LEDGER_PUBKEY") || ledgerFileCount(t, f.dir) != before {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// The root check must not be vacuous: a directory with nothing the root
// signed would pass "no Problem" under any seed and start a new ledger.
func TestSignerAddRefusesWhereRootHasNotSigned(t *testing.T) {
	newPub := pubHexOfSeed(t, seedHex(0x22))
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify.key"))
	t.Setenv("CORRALAI_LEDGER_PUBKEY", "")
	t.Setenv(ledgerRootKeyEnv, seedHex(0x11))
	missing := filepath.Join(t.TempDir(), "typo-does-not-exist")
	unsigned := t.TempDir()
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	pushAs(t, unsigned, "", "aaaa1111")
	other := t.TempDir()
	pushAs(t, other, seedHex(0x55), "aaaa1111")
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	// missing, empty and unsigned all hit the "root has never signed this
	// chain" refusal (the unsigned one has no Problem to trip, so only that
	// check stops it); "other key" shows a Problem under the root, which
	// refuses first. Either way nothing is written.
	for name, dir := range map[string]string{"missing": missing, "empty": t.TempDir(), "unsigned": unsigned, "other key": other} {
		before := ledgerFileCount(t, dir)
		code, out := run(t, "signer", "add", dir, "--name", "x", "--pub", newPub)
		if code != 2 || !strings.Contains(out, "nothing written") || ledgerFileCount(t, dir) != before {
			t.Errorf("%s: exit %d, files %d→%d\n%s", name, code, before, ledgerFileCount(t, dir), out)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("a refused add created the directory (stat err %v)", err)
	}
}

func TestSignerRevokeThenAppendRefused(t *testing.T) {
	f := newSignerFixture(t)
	mSeed := seedHex(0x22)
	if code, out := run(t, "signer", "add", f.dir, "--name", "laptop", "--pub", pubHexOfSeed(t, mSeed)); code != 0 {
		t.Fatalf("add: %d\n%s", code, out)
	}
	pushAs(t, f.dir, mSeed, "bbbb2222") // registered: accepted
	raw, _ := hex.DecodeString(mSeed)
	id := auditpush.KeyFingerprint(ed25519.NewKeyFromSeed(raw).Public().(ed25519.PublicKey))
	if code, out := run(t, "signer", "revoke", f.dir, "--keyid", id, "--reason", "laptop lost"); code != 0 {
		t.Fatalf("revoke: %d\n%s", code, out)
	}
	staging := t.TempDir()
	pushAs(t, staging, mSeed, "cccc3333")
	names, _ := os.ReadDir(filepath.Join(staging, auditpush.ScansSubdir))
	if code, out := run(t, "append", filepath.Join(staging, auditpush.ScansSubdir, names[0].Name()), f.dir); code != 1 {
		t.Fatalf("append with a revoked key: exit %d\n%s", code, out)
	}
	if code, out := run(t, "signer", "list", "--pub", f.rootPub, f.dir); code != 0 || !strings.Contains(out, "laptop lost") {
		t.Fatalf("list must show the revoked key and why: %d\n%s", code, out)
	}
}

func TestSeedNeverPrinted(t *testing.T) {
	f := newSignerFixture(t)
	mSeed := seedHex(0x22)
	mPub := pubHexOfSeed(t, mSeed)
	raw, _ := hex.DecodeString(mSeed)
	id := auditpush.KeyFingerprint(ed25519.NewKeyFromSeed(raw).Public().(ed25519.PublicKey))
	var all strings.Builder
	for _, args := range [][]string{
		{"signer", "add", f.dir, "--name", "laptop", "--pub", mPub},
		{"signer", "list", "--pub", f.rootPub, f.dir},
		{"signer", "revoke", f.dir, "--keyid", id, "--reason", "r"},
		{"verify", "--pub", f.rootPub, f.dir},
	} {
		_, out := run(t, args...)
		all.WriteString(out)
	}
	// a refusal path too: a wrong root, and a malformed seed
	t.Setenv(ledgerRootKeyEnv, seedHex(0x33))
	_, out := run(t, "signer", "add", f.dir, "--name", "x", "--pub", mPub)
	all.WriteString(out)
	t.Setenv(ledgerRootKeyEnv, f.rootSeed+"zz")
	_, out = run(t, "signer", "add", f.dir, "--name", "x", "--pub", mPub)
	all.WriteString(out)
	for _, s := range []string{f.rootSeed, seedHex(0x33), f.rootSeed[:16]} {
		if strings.Contains(all.String(), s) {
			t.Fatalf("a seed (or a slice of one) was printed:\n%s", all.String())
		}
	}
}

func TestVerifyPrintsSignerName(t *testing.T) {
	f := newSignerFixture(t)
	mSeed := seedHex(0x22)
	if code, out := run(t, "signer", "add", f.dir, "--name", "laptop", "--pub", pubHexOfSeed(t, mSeed)); code != 0 {
		t.Fatalf("add: %d\n%s", code, out)
	}
	pushAs(t, f.dir, mSeed, "bbbb2222")
	code, out := run(t, "verify", "--pub", f.rootPub, f.dir)
	if code != 0 || !strings.Contains(out, "signed by laptop (ed25519:") || !strings.Contains(out, "signed by root (ed25519:") {
		t.Fatalf("verify: exit %d\n%s", code, out)
	}
}

// A checkpoint over a signer chain is signed by the ROOT, and the head must be
// anchored from outside: a root checkpoint over a chain whose trailing revoke
// was deleted would cement the revoked key forever.
func TestCheckpointOnSignerChainNeedsRootAndHeadAnchor(t *testing.T) {
	f := newSignerFixture(t)
	if code, out := run(t, "signer", "add", f.dir, "--name", "laptop", "--pub", pubHexOfSeed(t, seedHex(0x22))); code != 0 {
		t.Fatalf("add: %d\n%s", code, out)
	}
	entries, _ := auditpush.ReadLedgerDir(f.dir)
	head := entries[len(entries)-1].Hash
	before := ledgerFileCount(t, f.dir)

	if code, out := run(t, "checkpoint", f.dir); code != 2 || !strings.Contains(out, "--expect-head") || ledgerFileCount(t, f.dir) != before {
		t.Fatalf("without --expect-head: exit %d\n%s", code, out)
	}
	if code, out := run(t, "checkpoint", "--expect-head", strings.Repeat("0", 12), f.dir); code != 1 || ledgerFileCount(t, f.dir) != before {
		t.Fatalf("wrong head: exit %d\n%s", code, out)
	}
	if code, _ := run(t, "checkpoint", "--expect-head", head[:6], f.dir); code != 2 {
		t.Fatalf("short head must be refused with 2, got %d", code)
	}
	t.Setenv(ledgerRootKeyEnv, "")
	if code, out := run(t, "checkpoint", "--expect-head", head, f.dir); code != 2 || !strings.Contains(out, "CORRALAI_LEDGER_ROOT_KEY") {
		t.Fatalf("without the root env: exit %d\n%s", code, out)
	}
	t.Setenv(ledgerRootKeyEnv, f.rootSeed)
	if code, out := run(t, "checkpoint", "--expect-head", head, f.dir); code != 0 {
		t.Fatalf("root + right head: exit %d\n%s", code, out)
	}
	if code, out := run(t, "verify", "--pub", f.rootPub, f.dir); code != 0 {
		t.Fatalf("verify after checkpoint: exit %d\n%s", code, out)
	}
}

func TestCheckpointOnPlainChainUnchanged(t *testing.T) {
	f := newSignerFixture(t)
	t.Setenv(ledgerRootKeyEnv, "")
	t.Setenv("CORRALAI_CERTIFY_KEY", f.rootSeed)
	if code, out := run(t, "checkpoint", f.dir); code != 0 {
		t.Fatalf("plain chain: exit %d\n%s", code, out)
	}
}
