// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// The browser reads entries as bytes, gzipped or not; the CLI reads the same
// bytes from disk. Both must parse to the same entry, and the entry's hash,
// recomputed from the bytes alone, must be the one the writer stored: that
// equality is what lets a page verify a ledger it fetched.
func TestParseEntryGzipAndPlain(t *testing.T) {
	dir := t.TempDir()
	b := ledger.Bundle{Scan: ledger.ScanRow{Repo: "r", Commit: "abcdef1234567890", ScanID: 1, CorralVersion: "vtest"}}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	names, err := os.ReadDir(filepath.Join(dir, auditpush.ScansSubdir))
	if err != nil || len(names) != 1 {
		t.Fatalf("want one entry file, got %d (%v)", len(names), err)
	}
	name := names[0].Name()
	gz, err := os.ReadFile(filepath.Join(dir, auditpush.ScansSubdir, name))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	fromGz, err := ledger.ParseEntry(name, gz)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	fromPlain, err := ledger.ParseEntry(strings.TrimSuffix(name, ".gz"), plain)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if fromGz.Hash == "" || fromGz.Hash != fromPlain.Hash || fromGz.ScanUID != fromPlain.ScanUID ||
		fromGz.Bundle.Scan.Repo != "r" || fromPlain.Bundle.Scan.Repo != "r" {
		t.Fatalf("gzip and plain disagree: %+v vs %+v", fromGz, fromPlain)
	}
	if fromGz.File != name || fromGz.Raw == nil {
		t.Errorf("ParseEntry must set File and Raw, got %q / %d bytes", fromGz.File, len(fromGz.Raw))
	}
	for label, e := range map[string]ledger.Entry{"gzip": fromGz, "plain": fromPlain} {
		got, err := ledger.EntryHash(e)
		if err != nil || got != e.Hash {
			t.Errorf("%s: EntryHash = %q (%v), stored Hash = %q", label, got, err, e.Hash)
		}
	}
}

func TestParseEntryRefusesGarbage(t *testing.T) {
	_, err := ledger.ParseEntry("x.json.gz", []byte{0x1f, 0x8b, 0})
	if err == nil || !strings.Contains(err.Error(), "x.json.gz") {
		t.Fatalf("a truncated gzip must be refused naming the file, got %v", err)
	}
	if _, err := ledger.ParseEntry("y.json", []byte(`{"format":"nope"}`)); err == nil || !strings.Contains(err.Error(), "y.json") {
		t.Fatalf("an unknown format must be refused naming the file, got %v", err)
	}
}
