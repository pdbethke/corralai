// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// TestMain lets a test run the real main() in a child process: main exits the
// process on every refusal, so the exit status is part of what the tests
// assert (the empty-record render, the -self refusal), and only a separate
// process can show it. Two variable names, one per helper that re-executes.
func TestMain(m *testing.M) {
	if os.Getenv("LEDGERSITE_TEST_RUN_MAIN") == "1" || os.Getenv("LEDGERSITE_AS_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A fresh corral deployment's record is a single checkpoint entry (that is also
// the shape of the live ledger branch after its 2026-11-09 restart). The site
// must render it and exit 0; a deploy that fails on an empty record cannot be
// the first deploy of anything.
func TestSiteRendersACheckpointOnlyRecord(t *testing.T) {
	seed := bytes.Repeat([]byte{0x44}, ed25519.SeedSize)
	k := ed25519.NewKeyFromSeed(seed)
	pub := k.Public().(ed25519.PublicKey)
	signer := auditpush.Ed25519LedgerSigner{KeyID: ledger.KeyFingerprint(pub), Key: k}
	dir := t.TempDir()
	auditpush.SetLedgerSigner(signer)
	t.Cleanup(func() { auditpush.SetLedgerSigner(nil) })
	b := ledger.Bundle{Scan: ledger.ScanRow{Repo: "o/r", Commit: "abcdef123456", ScanID: 1, CorralVersion: "vtest"}}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auditpush.WriteCheckpoint(dir, signer, pub); err != nil {
		t.Fatal(err)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Kind != ledger.KindCheckpoint {
		t.Fatalf("setup: want exactly one checkpoint entry, got %d (err %v)", len(entries), err)
	}

	out := filepath.Join(t.TempDir(), "ledger")
	cmd := exec.Command(os.Args[0], "-ledger", dir, "-out", out, "-repo", "o/r", "-pubkey", hex.EncodeToString(pub), "-self", "github.com/o/r", "-site-chrome")
	cmd.Env = append(os.Environ(), "LEDGERSITE_TEST_RUN_MAIN=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ledgersite on a checkpoint-only record: %v\n%s", err, output)
	}
	for _, f := range []string{"index.html", "chain-1.html", "audits-1.html", "reviews-1.html", "verdicts.html", "languages.html"} {
		st, err := os.Stat(filepath.Join(out, f))
		if err != nil || st.Size() == 0 {
			t.Errorf("%s was not written for a checkpoint-only record (err %v)", f, err)
		}
	}
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

func runLedgersite(t *testing.T, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "LEDGERSITE_AS_MAIN=1", "CORRALAI_LEDGER_PUBKEY=")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// qualifyingLedger writes one signed, qualifying go run and returns the
// directory and the hex public key that signed it.
func qualifyingLedger(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	d := false
	e := auditpush.LedgerEntry{Bundle: auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "https://github.com/acme/widgets", RepoIdentity: "git", Commit: "c1",
			Runner: "go-test", VerdictGeneration: "40", TreeDirty: &d},
		Files: []auditpush.Row{{Lang: "go", Path: "a.go", Audited: 1, MutantsGraded: 5}},
	}}
	signer := auditpush.Ed25519LedgerSigner{KeyID: auditpush.KeyFingerprint(pub), Key: priv}
	if _, err := auditpush.AppendLedgerEntry(dir, e, signer); err != nil {
		t.Fatal(err)
	}
	return dir, hex.EncodeToString(pub)
}

// -self names the repositories whose runs are self-audit and never count as
// proof. Without it the page would silently exclude nothing, so the program
// refuses instead.
func TestLedgersiteRefusesWithoutSelf(t *testing.T) {
	dir, pub := qualifyingLedger(t)
	out, code := runLedgersite(t, "-ledger", dir, "-out", t.TempDir(), "-repo", "acme/widgets", "-pubkey", pub)
	if code != 2 {
		t.Fatalf("exit %d, want 2; output:\n%s", code, out)
	}
	if !strings.Contains(out, "ledgersite: -self is required") {
		t.Fatalf("the refusal does not name -self:\n%s", out)
	}
}

func TestLedgersiteWritesLanguagesPage(t *testing.T) {
	dir, pub := qualifyingLedger(t)
	site := t.TempDir()
	out, code := runLedgersite(t, "-ledger", dir, "-out", site, "-repo", "acme/widgets", "-pubkey", pub, "-self", "github.com/pdbethke/corralai")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(site, "languages.html"))
	if err != nil {
		t.Fatalf("no languages.html: %v", err)
	}
	page := string(b)
	i := strings.Index(page, `<td class="mono">go</td>`)
	if i < 0 {
		t.Fatalf("no go row:\n%s", page)
	}
	row := page[i : i+strings.Index(page[i:], "</tr>")]
	if strings.Contains(row, "no qualifying run on this record") {
		t.Fatalf("the signed qualifying go run is not shown: %s", row)
	}
	idx, _ := os.ReadFile(filepath.Join(site, "index.html"))
	if !strings.Contains(string(idx), "languages.html") {
		t.Fatalf("the index does not link the Languages page")
	}
}
