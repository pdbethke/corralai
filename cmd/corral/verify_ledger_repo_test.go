// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// A keyless entry is checked against the repository that signed it, and the
// reader is TOLD that repository: with no --ledger-repo the chain fails with
// the ledger package's own Problem (no guess from Scan.Repo, which is "r"
// here and not the signing repository), with it the entry verifies.
func TestVerifyLedgerRepoFlag(t *testing.T) {
	isolateStateHome(t)
	s, _ := useTestKeylessSigner(t)
	prev := ledgerSigstoreRoot
	ledgerSigstoreRoot = func() (*ledger.SigstoreRoot, error) { return s.Root(t), nil }
	t.Cleanup(func() { ledgerSigstoreRoot = prev })
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	t.Setenv(auditpush.LedgerPubKeyEnv, "")
	dir := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "aaaa1111", ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}
	if _, err := pushBundle(dir+"/", b, io.Discard); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := runVerifyLedger(dir, "", "", "", &out, &errb); code != 1 || !strings.Contains(out.String(), ledger.ProblemNoLedgerRepo) {
		t.Fatalf("without the flag: exit %d, want 1 with %q\n%s%s", code, ledger.ProblemNoLedgerRepo, out.String(), errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := runVerifyLedger(dir, "", testKeylessRepo, "", &out, &errb); code != 0 || !strings.Contains(out.String(), "keyless") {
		t.Fatalf("with the flag: exit %d, want 0 naming keyless\n%s%s", code, out.String(), errb.String())
	}

	// The wrong repository is a failure, not a pass.
	out.Reset()
	if code := runVerifyLedger(dir, "", "evil/widgets", "", &out, &errb); code != 1 {
		t.Fatalf("wrong repo: exit %d, want 1\n%s", code, out.String())
	}

	// Malformed values are refused, naming the flag.
	for _, bad := range []string{"widgets", "a/b/c", "a b/c", "https://github.com/a/b", "/b", "a/"} {
		out.Reset()
		errb.Reset()
		if code := runVerifyLedger(dir, "", bad, "", &out, &errb); code != 2 || !strings.Contains(errb.String(), "--ledger-repo") {
			t.Errorf("%q: exit %d, want 2 naming --ledger-repo: %s", bad, code, errb.String())
		}
	}
}

// corral ui reads the same trust as corral verify: told the signing
// repository it renders a keyless chain, not told it the chain page carries
// the Problem.
func TestUILedgerRepoFlagReachesTheChainPage(t *testing.T) {
	isolateStateHome(t)
	s, _ := useTestKeylessSigner(t)
	prev := ledgerSigstoreRoot
	ledgerSigstoreRoot = func() (*ledger.SigstoreRoot, error) { return s.Root(t), nil }
	t.Cleanup(func() { ledgerSigstoreRoot = prev })
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	t.Setenv(auditpush.LedgerPubKeyEnv, "")
	dir := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "aaaa1111", ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}
	if _, err := pushBundle(dir+"/", b, io.Discard); err != nil {
		t.Fatal(err)
	}
	without := uiGet(t, uiHandler(fakeSeal{}, dir, ""), "/chain-1.html")
	if !strings.Contains(without.Body.String(), "repository is not named") {
		t.Fatalf("without --ledger-repo the chain page must carry the Problem:\n%s", without.Body.String())
	}
	with := uiGet(t, uiHandler(fakeSeal{}, dir, testKeylessRepo), "/chain-1.html")
	if with.Code != 200 || strings.Contains(with.Body.String(), "repository is not named") {
		t.Fatalf("with --ledger-repo the page must not carry it (code %d)", with.Code)
	}
}
