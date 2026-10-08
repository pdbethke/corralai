// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
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

// The site must see exactly what the ledger's own reader sees: plain .json
// entries included, chain (Pushed) order — never a walker of its own, which
// is how the copy this replaced came to drop every uncompressed entry.
func TestSiteLoadsWhatTheLedgerReaderLoads(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "abcdef1234567890", ScanID: int64(i), CorralVersion: "vtest"}}
		// A nil signer writes an unsigned entry, which still chains.
		if _, err := auditpush.AppendLedgerEntry(dir, auditpush.LedgerEntry{Bundle: b}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Rewrite one entry uncompressed, as an older writer could have left it.
	gz, _ := filepath.Glob(filepath.Join(dir, auditpush.ScansSubdir, "*.json.gz"))
	if len(gz) != 3 {
		t.Fatalf("setup: want 3 gzipped entries, got %d", len(gz))
	}
	raw := mustGunzip(t, gz[1])
	if err := os.WriteFile(strings.TrimSuffix(gz[1], ".gz"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gz[1]); err != nil {
		t.Fatal(err)
	}

	want, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("site loaded %d entries, the ledger reader %d — the site is dropping entries", len(got), len(want))
	}
	for i := range want {
		if got[i].File != want[i].File {
			t.Fatalf("entry %d: site %s, ledger %s — order differs", i, got[i].File, want[i].File)
		}
	}
}

func mustGunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- a path the test itself just wrote
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
