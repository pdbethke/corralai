// SPDX-License-Identifier: Elastic-2.0

package ledger_test

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// An unmeasured file carries no CoveringUnits key at all: omitempty keeps
// every row written without unit evidence (every Python row, every row from
// before the field existed) marshalling to the bytes it always did.
func TestCoveringUnitsOmittedWhenUnmeasured(t *testing.T) {
	js, err := json.Marshal(ledger.Row{Path: "pkg/a.py"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "CoveringUnits") {
		t.Errorf("an unmeasured row carries the CoveringUnits key: %s", js)
	}
	three := 3
	js, err = json.Marshal(ledger.Row{Path: "src/a.ts", CoveringUnits: &three})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `"CoveringUnits":3`) {
		t.Errorf("a measured row lost its count: %s", js)
	}
}

// A row as every binary before this field wrote it parses, with
// CoveringUnits nil (unmeasured), never a fabricated 0, and CoveringTests
// keeps its own value.
func TestOlderLedgerRowsStillParse(t *testing.T) {
	old := `{"Repo":"o/r","Commit":"c","Path":"pkg/a.py","Lang":"python","Disposition":"audited","CoveringTests":2,"ImportOnly":null}`
	var r ledger.Row
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatal(err)
	}
	if r.CoveringUnits != nil {
		t.Errorf("CoveringUnits = %d on a row that never carried it, want nil", *r.CoveringUnits)
	}
	if r.CoveringTests == nil || *r.CoveringTests != 2 {
		t.Errorf("CoveringTests = %v, want 2", r.CoveringTests)
	}
}

// An entry carrying CoveringUnits verifies under the existing Verify, with
// no new format: from corral-ledger-3 the hash is over the entry's bytes as
// written (Entry.Raw), not over this binary's struct. The second half is the
// older-binary case: a reader whose Row has no CoveringUnits field decodes
// the entry with the key ignored (json.Unmarshal drops unknown keys), and
// the hash it recomputes from Raw still matches.
func TestAnEntryCarryingCoveringUnitsStillVerifies(t *testing.T) {
	root := key(0x41)
	dir := t.TempDir()
	auditpush.SetLedgerSigner(signerFor(root))
	t.Cleanup(func() { auditpush.SetLedgerSigner(nil) })
	three := 3
	b := ledger.Bundle{
		Scan:  ledger.ScanRow{Repo: "o/r", Commit: "abcdef123456", ScanID: 1, CorralVersion: "vtest"},
		Files: []ledger.Row{{Repo: "o/r", Commit: "abcdef123456", Path: "src/a.ts", Lang: "typescript", CoveringUnits: &three}},
	}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	es := readBytes(t, dir)
	if len(es) != 1 {
		t.Fatalf("want one entry, got %d", len(es))
	}
	if es[0].Format != ledger.FileFormat {
		t.Errorf("format = %q, want the existing %q: an additive field needs no new format", es[0].Format, ledger.FileFormat)
	}
	if !strings.Contains(string(es[0].Raw), `"CoveringUnits": 3`) && !strings.Contains(string(es[0].Raw), `"CoveringUnits":3`) {
		t.Fatalf("the written entry does not carry the count:\n%s", es[0].Raw)
	}
	if got := es[0].Bundle.Files[0].CoveringUnits; got == nil || *got != 3 {
		t.Errorf("parsed CoveringUnits = %v, want 3", got)
	}
	for _, c := range ledger.Verify(es, ledger.Trust{RootPub: root.Public().(ed25519.PublicKey)}) {
		if c.Problem != "" || !c.HashOK || !c.SigOK {
			t.Fatalf("an entry carrying CoveringUnits must verify clean: %+v", c)
		}
	}

	older := es[0]
	older.Bundle.Files[0].CoveringUnits = nil // what a Row without the field holds
	h, err := ledger.EntryHash(older)
	if err != nil {
		t.Fatal(err)
	}
	if h != es[0].Hash {
		t.Errorf("the hash moved when the reader's struct does not know CoveringUnits: %s, want %s", h, es[0].Hash)
	}
}
