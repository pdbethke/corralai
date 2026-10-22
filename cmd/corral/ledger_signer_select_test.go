// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/ledger/ledgertest"
)

const testKeylessRepo = "acme/widgets"

// useTestKeylessSigner makes the keyless signer one backed by an in-process
// CA and log, and counts how often it is built.
func useTestKeylessSigner(t *testing.T) (*ledgertest.Sigstore, *int) {
	t.Helper()
	s := ledgertest.New(t)
	built := 0
	prev := newKeylessLedgerSigner
	newKeylessLedgerSigner = func() (auditpush.LedgerSigner, error) {
		built++
		return auditpush.NewSigstoreLedgerSigner(func(h []byte) ([]byte, error) { return s.Issue(t, h, testKeylessRepo), nil }, testKeylessRepo, s.Root(t)), nil
	}
	resetKeylessLedgerSigner()
	t.Cleanup(func() { newKeylessLedgerSigner = prev; resetKeylessLedgerSigner() })
	return s, &built
}

func TestSelectLedgerSignerRefusesUnknown(t *testing.T) {
	isolateStateHome(t)
	t.Setenv(auditpush.LedgerSignerEnv, "gpg")
	s, _, err := selectLedgerSigner(io.Discard)
	if err == nil || s != nil || !errors.Is(err, errSignerUnusable) || !strings.Contains(err.Error(), `"sigstore"`) {
		t.Fatalf("signer %v err %v — want a refusal naming \"sigstore\"", s, err)
	}
}

func TestSelectLedgerSignerKeylessNeedsOIDC(t *testing.T) {
	isolateStateHome(t)
	resetKeylessLedgerSigner()
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	s, _, err := selectLedgerSigner(io.Discard)
	if err == nil || s != nil || !errors.Is(err, errSignerUnusable) || !errors.Is(err, auditpush.ErrNoOIDCToken) {
		t.Fatalf("signer %v err %v — keyless with no OIDC token must refuse, never fall back to the key or to unsigned", s, err)
	}
}

// Keyless selected AND a certify key configured: the key is not used, one
// line says so, and the signer is built once per process — the repo entry
// and its machine-ledger copy share one certificate.
func TestSelectLedgerSignerKeylessOverKey(t *testing.T) {
	isolateStateHome(t)
	_, built := useTestKeylessSigner(t)
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	t.Setenv("CORRALAI_CERTIFY_KEY", seedHex(0x61))
	var errb bytes.Buffer
	for i := 0; i < 3; i++ {
		s, label, err := selectLedgerSigner(&errb)
		if err != nil {
			t.Fatal(err)
		}
		if s.SigningKeyID() != ledger.KeyIDSigstore || !strings.Contains(label, "keyless") {
			t.Fatalf("keyid %q label %q, want the keyless signer", s.SigningKeyID(), label)
		}
	}
	if *built != 1 {
		t.Fatalf("the keyless signer was built %d times in one process, want 1", *built)
	}
	lines := strings.Split(strings.TrimSpace(errb.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "CORRALAI_CERTIFY_KEY") || !strings.Contains(lines[0], "not used") {
		t.Fatalf("want exactly one line saying the certify key is not used, got %q", errb.String())
	}
}

func TestSelectLedgerSignerKeylessNoKeyIsQuiet(t *testing.T) {
	isolateStateHome(t)
	useTestKeylessSigner(t)
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	var errb bytes.Buffer
	if _, _, err := selectLedgerSigner(&errb); err != nil {
		t.Fatal(err)
	}
	if errb.Len() != 0 {
		t.Fatalf("no certify key configured, yet: %q", errb.String())
	}
}

// Unset keeps today's behaviour exactly: the certify key, by fingerprint.
func TestSelectLedgerSignerUnsetKeepsTheKey(t *testing.T) {
	isolateStateHome(t)
	t.Setenv(auditpush.LedgerSignerEnv, "")
	t.Setenv("CORRALAI_CERTIFY_KEY", seedHex(0x62))
	s, label, err := selectLedgerSigner(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := auditpush.KeyFingerprint(pubOfSeedHex(t, seedHex(0x62)))
	if s.SigningKeyID() != want || label != "signed by "+want {
		t.Fatalf("keyid %q label %q, want the certify key %s", s.SigningKeyID(), label, want)
	}
}

// The door is the one every writer uses: a scan pushed to a ledger
// directory (certify --repo's entry and its machine copy, certify --local's
// entry) is keyless and verifies by repository, chain after chain.
func TestPushBundleSignsKeylessly(t *testing.T) {
	isolateStateHome(t)
	s, built := useTestKeylessSigner(t)
	t.Setenv(auditpush.LedgerSignerEnv, "sigstore")
	repoLedger, machine := t.TempDir(), t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "aaaa1111", ScanID: 1}, Files: []auditpush.Row{{Repo: "r", ScanID: 1, Path: "a.py"}}}
	for _, dir := range []string{repoLedger, machine, repoLedger} {
		if _, err := pushBundle(dir+"/", b, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if *built != 1 {
		t.Fatalf("built %d keyless signers for one run", *built)
	}
	trust := ledger.Trust{Repo: testKeylessRepo, SigstoreRoot: s.Root(t)}
	for _, dir := range []string{repoLedger, machine} {
		entries, err := auditpush.ReadLedgerDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range ledger.Verify(entries, trust) {
			if c.Problem != "" || !c.SigOK || !c.Keyless {
				t.Fatalf("%s entry %d: %+v", filepath.Base(dir), i, c)
			}
		}
	}
}

// A verb that writes through the door refuses on an unknown signer before
// writing anything (ledger retract stands in for the rest).
func TestLedgerVerbRefusesUnknownSigner(t *testing.T) {
	isolateStateHome(t)
	dir := t.TempDir()
	t.Setenv(auditpush.LedgerSignerEnv, "")
	if _, err := pushBundle(dir+"/", auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "aaaa1111", ScanID: 1}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, _ := auditpush.ReadLedgerDir(dir)
	before := ledgerFileCount(t, dir)
	t.Setenv(auditpush.LedgerSignerEnv, "gpg")
	var out, errb bytes.Buffer
	if code := runLedger([]string{"retract", dir, entries[0].Hash, "--reason", "test"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), `"sigstore"`) {
		t.Fatalf("exit %d stderr %q — want 1 naming \"sigstore\"", code, errb.String())
	}
	if ledgerFileCount(t, dir) != before {
		t.Fatal("an entry was written with an unknown signer selected")
	}
}

// certify --repo signs its entry at the END of a paid run, so a signer it
// cannot use is refused before anything is spent.
func TestCertifyRepoRefusesUnusableSignerBeforeSpend(t *testing.T) {
	isolateStateHome(t)
	for name, env := range map[string][3]string{
		"unknown":  {"gpg", "", ""},
		"no oidc":  {"sigstore", "", ""},
		"no token": {"sigstore", "https://token.example/x", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(auditpush.LedgerSignerEnv, env[0])
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", env[1])
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", env[2])
			var out, errb bytes.Buffer
			code := runCertifyRepo([]string{"--repo", t.TempDir()}, &out, &errb)
			if code != 2 || !strings.Contains(errb.String(), "the ledger signer cannot be used") || !strings.Contains(errb.String(), "nothing was spent") {
				t.Fatalf("exit %d, want 2 before spend\nstderr: %.600s", code, errb.String())
			}
		})
	}
}

func pubOfSeedHex(t *testing.T, seed string) ed25519.PublicKey {
	t.Helper()
	raw, err := hex.DecodeString(pubHexOfSeed(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
