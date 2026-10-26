// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

const fixture = "../../internal/ledger/testdata/two-entries"

func fixturePub(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixture, "rootpub.hex"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// Neither the key nor the repository has a default: a guessed repo checks the
// wrong keyless identity, and no key passes nothing signed. Each refusal names
// what to pass.
func TestLedgerderiveRefusesWhatIsMissing(t *testing.T) {
	pub := fixturePub(t)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no pub", []string{"-repo", "o/r", fixture}, "-pub"},
		{"no repo", []string{"-pub", pub, fixture}, "-repo"},
		{"bad repo", []string{"-pub", pub, "-repo", "nope", fixture}, "-repo"},
		{"bad pub", []string{"-pub", "zz", "-repo", "o/r", fixture}, "Ed25519"},
		{"no dir", []string{"-pub", pub, "-repo", "o/r"}, "directory"},
		{"two dirs", []string{"-pub", pub, "-repo", "o/r", fixture, fixture}, "directory"},
	} {
		var out, errb bytes.Buffer
		if code := run(c.args, &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) || out.Len() != 0 {
			t.Errorf("%s: exit %d, stderr %q, stdout %d bytes; want exit 2 naming %q and no output", c.name, code, errb.String(), out.Len(), c.want)
		}
	}
}

// The output is ledger.Derive's JSON and nothing else — no trailing newline —
// so it can be compared byte for byte with the string the WASM returns.
func TestLedgerderivePrintsDeriveJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-pub", fixturePub(t), "-repo", "o/r", fixture}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var d ledger.Derived
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Format != ledger.DeriveFormat || d.Problem != "" || d.Summary.Totals.Scans != 2 {
		t.Fatalf("format %q problem %q scans %d", d.Format, d.Problem, d.Summary.Totals.Scans)
	}
	again, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), again) {
		t.Fatal("output is not exactly json.Marshal(Derived)")
	}
}
