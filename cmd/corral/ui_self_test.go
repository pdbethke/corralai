// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// uiSelfLedger writes one signed qualifying go run and points the UI's
// explicit-key lookup at the key that signed it.
func uiSelfLedger(t *testing.T) string {
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
	t.Setenv(auditpush.LedgerPubKeyEnv, hex.EncodeToString(pub))
	return dir
}

// Empty --self is legal in the UI but never means "exclude nothing": the
// page says it needs the flag. With it, the run on disk is on the page.
func TestUISelfFlagRendersLanguages(t *testing.T) {
	dir := uiSelfLedger(t)

	without := uiGet(t, uiHandler(fakeSeal{}, dir, "", nil), "/languages.html")
	if without.Code != http.StatusOK {
		t.Fatalf("status %d: %s", without.Code, without.Body.String())
	}
	if !strings.Contains(without.Body.String(), "--self") {
		t.Fatalf("no --self: the page must name the flag it needs:\n%s", without.Body.String())
	}

	with := uiGet(t, uiHandler(fakeSeal{}, dir, "", []string{"github.com/pdbethke/corralai"}), "/languages.html")
	page := with.Body.String()
	i := strings.Index(page, `<td class="mono">go</td>`)
	if i < 0 {
		t.Fatalf("no go row:\n%s", page)
	}
	if row := page[i : i+strings.Index(page[i:], "</tr>")]; strings.Contains(row, "no run on this record") {
		t.Fatalf("the signed qualifying run is not shown: %s", row)
	}
}

// The flag must reach the handler from the command line, repeated.
func TestUISelfFlagIsRepeatableAndParsed(t *testing.T) {
	var out, errb bytes.Buffer
	code := runUI([]string{"--print-url", "--self", "a/b", "--self", "c/d"}, func(string) (sealReader, error) { return fakeSeal{}, nil }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
