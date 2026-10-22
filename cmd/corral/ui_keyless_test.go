// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"io"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// A keyless entry checked against --ledger-repo, with no root key given,
// is VERIFIED on every corral ui page. It read "signed, not checked (no key
// given)" while the page decided "checked" from whether a root key was
// passed — the rule in a second place, and wrong for an entry whose trust
// is its repository.
func TestUIShowsAVerifiedKeylessEntryAsVerified(t *testing.T) {
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
	h := uiHandler(fakeSeal{}, dir, testKeylessRepo)
	chain := uiGet(t, h, "/chain-1.html").Body.String()
	if strings.Contains(chain, "signed, not checked") || !strings.Contains(chain, `chip ok">verified`) {
		t.Errorf("the chain page does not show the verified keyless entry as verified:\n%s", chain)
	}
	idx := uiGet(t, h, "/index.html").Body.String()
	if strings.Contains(idx, "not checked") || !strings.Contains(idx, "1/1 verified") {
		t.Errorf("the index banner calls a verified keyless signature unchecked:\n%s", idx)
	}
}
