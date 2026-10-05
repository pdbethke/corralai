// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheVerifyCommandCarriesThePublishedKey: the page told a stranger to
// "pass the ledger's published Ed25519 public key" and never said what it
// was, so the one check that needs the key was a dead end. When the page was
// rendered against a key, the command it prints carries that key; and the
// entry count in it is the record's, not a number typed into the template.
func TestTheVerifyCommandCarriesThePublishedKey(t *testing.T) {
	const key = "909429443700441a9f662c07c5f1b7110333fd7fb94fb21e8991e40f2cb6dc09"
	out := t.TempDir()
	if err := writeSite(out, SiteView{Generated: time.Unix(0, 0), LedgerEntries: 7, SigsChecked: true, PubKeyHex: key}); err != nil {
		t.Fatal(err)
	}
	page := read(t, filepath.Join(out, "index.html"))
	if !strings.Contains(page, "corral ledger verify --pub "+key+" .") {
		t.Fatalf("the verify command does not carry the published key")
	}
	if !strings.Contains(page, "7 gzipped JSON entries") || strings.Contains(page, "121 gzipped") {
		t.Fatalf("the entry count in the instructions is not the record's own")
	}
}

// TestWithoutAKeyThePageSaysSo: rendered without a key, the page must not
// invent one and must keep saying signatures were not checked.
func TestWithoutAKeyThePageSaysSo(t *testing.T) {
	out := t.TempDir()
	if err := writeSite(out, SiteView{Generated: time.Unix(0, 0), LedgerEntries: 7}); err != nil {
		t.Fatal(err)
	}
	page := read(t, filepath.Join(out, "index.html"))
	if strings.Contains(page, "--pub ") && !strings.Contains(page, "--pub &lt;hex&gt;") {
		t.Fatalf("a page rendered without a key printed one")
	}
	if !strings.Contains(page, "not checked") {
		t.Fatalf("a page rendered without a key must say signatures were not checked")
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) // #nosec G304 -- a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
