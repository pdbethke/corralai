// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
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
// entries included, and chain (Pushed) order — never file-name order. Names
// carry seconds and Pushed microseconds, so two entries inside one second
// sorted differently by each (ed079ca08965#R5). The fixture makes that
// disagreement total: the files are renamed so name order is the REVERSE of
// push order, and the expected order comes from how the entries were
// appended, not from asking the reader under test.
func TestSiteLoadsWhatTheLedgerReaderLoads(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "abcdef1234567890", ScanID: int64(i), CorralVersion: "vtest"}}
		// A nil signer writes an unsigned entry, which still chains.
		if _, err := auditpush.AppendLedgerEntry(dir, auditpush.LedgerEntry{Bundle: b}, nil); err != nil {
			t.Fatal(err)
		}
	}
	scans := filepath.Join(dir, auditpush.ScansSubdir)
	gz, err := filepath.Glob(filepath.Join(scans, "*.json.gz"))
	if err != nil || len(gz) != 3 {
		t.Fatalf("setup: want 3 gzipped entries, got %d (err %v)", len(gz), err)
	}
	sort.Strings(gz) // names are push-ordered as written: gz[0] is ScanID 1
	// Reverse the names (entry 1 sorts last) and leave the middle entry
	// uncompressed, as an older writer could have.
	renamed := []string{"c.json.gz", "b.json", "a.json.gz"}
	for k, old := range gz {
		raw := mustGunzip(t, old)
		if err := os.Remove(old); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(scans, renamed[k])
		if strings.HasSuffix(dst, ".gz") {
			writeGzip(t, dst, raw)
		} else if err := os.WriteFile(dst, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := loadEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("site loaded %d entries, want 3 — the site is dropping entries", len(got))
	}
	for i, e := range got {
		if e.Bundle.Scan.ScanID != int64(i+1) {
			t.Fatalf("position %d holds scan %d (file %s), want scan %d — not chain order", i, e.Bundle.Scan.ScanID, e.File, i+1)
		}
		if i > 0 && e.Pushed.Before(got[i-1].Pushed) {
			t.Fatalf("entry %d was pushed before entry %d", i, i-1)
		}
	}
}

func writeGzip(t *testing.T, path string, raw []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- a path the test itself builds
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
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
