// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func appendOne(t *testing.T, dir string) {
	t.Helper()
	if _, err := auditpush.AppendLedgerEntry(dir, auditpush.LedgerEntry{Bundle: auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "abcdef1234567890", CorralVersion: "vtest"}}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsVerifiesAndPairs(t *testing.T) {
	dir := t.TempDir()
	appendOne(t, dir)
	appendOne(t, dir)
	s, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasLedger || s.LedgerEntries != 2 || len(s.Chain) != 2 || s.ChainProblems != 0 {
		t.Fatalf("got HasLedger=%v entries=%d chain=%d problems=%d", s.HasLedger, s.LedgerEntries, len(s.Chain), s.ChainProblems)
	}
	if s.SigsChecked {
		t.Fatal("no key was given, so signatures were not checked")
	}
}

// Review Focus 1: in live mode, each request loads afresh.
func TestLiveModeReadsTheLedgerFreshOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	appendOne(t, dir)
	first, _ := Load(dir, nil)
	appendOne(t, dir)
	second, _ := Load(dir, nil)
	if first.LedgerEntries != 1 || second.LedgerEntries != 2 {
		t.Fatalf("Load is not reading afresh: %d then %d", first.LedgerEntries, second.LedgerEntries)
	}
}
