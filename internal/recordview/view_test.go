// SPDX-License-Identifier: Elastic-2.0

package recordview

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
	if err := WriteStatic(out, Site{HasLedger: true, Generated: time.Unix(0, 0), LedgerEntries: 7, SigsChecked: true, PubKeyHex: key}); err != nil {
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
	if err := WriteStatic(out, Site{HasLedger: true, Generated: time.Unix(0, 0), LedgerEntries: 7}); err != nil {
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

// A historical signed entry's vendor must not depend on the environment of
// the process that renders it: codex was OpenAI's CLI when it ran.
func TestVendorOfSeatIgnoresTheRenderersAgentEnvironment(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_CODEX", "/bin/cat")
	t.Setenv("CORRALAI_AGENT_CLAUDE_CODE", "/bin/cat")
	for seat, want := range map[string]string{
		"codex":                  "openai",
		"claude-code":            "anthropic",
		"antigravity":            "google",
		"codex:gpt-6-astra":      "openai",
		"claude-code:gemini-9":   "google",
		"gemini-3.6-flash":       "google",
		"some-operator-agent":    "",
		"mytool:claude-sonnet-5": "anthropic",
	} {
		if got := vendorOfSeat(seat); got != want {
			t.Errorf("vendorOfSeat(%q) = %q, want %q", seat, got, want)
		}
	}
}
