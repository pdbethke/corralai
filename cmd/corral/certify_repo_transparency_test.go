// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
	"github.com/pdbethke/corralai/internal/transparency"
)

// TestUploadToTransparencyLogIsByteIdentical pins the load-bearing property:
// the bytes handed to the logger are the EXACT bytes written to the
// --attest path — read back off disk, never re-serialized from the
// in-memory statement. A re-marshal (different key order, different
// whitespace) would make the uploaded bytes and the file on disk two
// different artifacts sharing one hash claim.
func TestUploadToTransparencyLogIsByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statement.json")
	// Deliberately odd formatting a re-marshal would normalize away, so a
	// test that passed on re-serialized bytes would still fail here.
	written := []byte("{\n  \"predicateType\":   \"https://corralai.dev/certify/audit/v1\",\n\t\"weird\":true\n}\n")
	if err := os.WriteFile(path, written, 0o600); err != nil {
		t.Fatalf("seed the attest file: %v", err)
	}

	fake := &transparency.FakeLogger{Entry: transparency.LogEntry{LogIndex: 7, UUID: "u-1", IntegratedTime: 123}}
	var out, errb bytes.Buffer
	entry, ok := uploadToTransparencyLog(context.Background(), fake, path, []byte("pubkey"), &out, &errb)
	if !ok {
		t.Fatalf("uploadToTransparencyLog: ok=false, stderr=%q", errb.String())
	}
	if entry != fake.Entry {
		t.Fatalf("entry = %+v, want %+v", entry, fake.Entry)
	}
	if len(fake.Uploads) != 1 || string(fake.Uploads[0]) != string(written) {
		t.Fatalf("uploaded bytes = %q, want the exact file bytes %q", fake.Uploads, written)
	}
	if got := out.String(); !strings.Contains(got, "attestation logged: rekor index 7 (uuid u-1)") {
		t.Fatalf("stdout = %q, want the rekor receipt line", got)
	}
}

// TestUploadToTransparencyLogFailsOpen is the fail-open contract: an
// erroring logger produces one stderr line and ok=false — the caller's exit
// code is untouched by this function; runCertifyRepo's own wiring is what
// keeps the scan's verdict exit code independent of this outcome.
func TestUploadToTransparencyLogFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statement.json")
	if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("seed the attest file: %v", err)
	}

	fake := &transparency.FakeLogger{Err: errors.New("rekor: 503 service unavailable")}
	var out, errb bytes.Buffer
	entry, ok := uploadToTransparencyLog(context.Background(), fake, path, nil, &out, &errb)
	if ok {
		t.Fatalf("ok = true, want false on an upload error")
	}
	if entry != (transparency.LogEntry{}) {
		t.Fatalf("entry = %+v, want the zero value on failure", entry)
	}
	if !strings.Contains(errb.String(), "rekor: 503 service unavailable") {
		t.Fatalf("stderr = %q, want the upload error surfaced", errb.String())
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing printed on failure", out.String())
	}
}

// TestUploadToTransparencyLogMissingFileFailsOpen: the --attest write can
// itself have failed (writeAuditStatement returns an error and the caller
// never gets here in practice), but this function's own contract must hold
// regardless — a missing file is reported, not panicked on.
func TestUploadToTransparencyLogMissingFileFailsOpen(t *testing.T) {
	fake := &transparency.FakeLogger{}
	var out, errb bytes.Buffer
	_, ok := uploadToTransparencyLog(context.Background(), fake, filepath.Join(t.TempDir(), "nope.json"), nil, &out, &errb)
	if ok {
		t.Fatal("ok = true, want false when the attest file cannot be read")
	}
	if len(fake.Uploads) != 0 {
		t.Fatalf("the logger must not be called when the file cannot be read, got %d call(s)", len(fake.Uploads))
	}
	if errb.String() == "" {
		t.Fatal("stderr is empty, want a message naming the read failure")
	}
}

// TestTransparencyWithoutAttestExitsUsageError pins the binding constraint:
// --transparency names an upload with nothing to upload without --attest,
// and that is a usage error (exit 2), caught before any real work runs.
func TestTransparencyWithoutAttestExitsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--transparency"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "transparency logs an attestation; there is none") {
		t.Fatalf("stderr = %q, want the transparency-without-attest message", errb.String())
	}
}

// TestCertifyRepoTransparencyStampsLedgerAndBundle drives --attest
// --transparency through the full runCertifyRepo wiring — flag, statement
// write, upload, ledger stamp, and the receipt threaded into the bundle —
// with a FakeLogger substituted for newTransparencyLogger, so no network is
// touched. The fixture uses an empty diff scope (base == HEAD, nothing
// changed since), the same trick TestCertifyRepoRecordRoundTripsReportedFiles
// uses: every candidate is excluded before any model call or jail run, so
// this exercises the --attest/--transparency/--record wiring with zero real
// audit cost.
func TestCertifyRepoTransparencyStampsLedgerAndBundle(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	certKeyPath := filepath.Join(t.TempDir(), "certify_key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", certKeyPath)

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	attestPath := filepath.Join(t.TempDir(), "statement.json")

	fake := &transparency.FakeLogger{Entry: transparency.LogEntry{LogIndex: 55, UUID: "uuid-xyz", IntegratedTime: 42}}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--substrate", substrateWorkspace,
		"--record", "--record-db", dsn,
		"--attest", attestPath, "--transparency",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "attestation logged: rekor index 55 (uuid uuid-xyz)") {
		t.Errorf("stdout = %q, want the rekor receipt line", out.String())
	}
	if len(fake.Uploads) != 1 {
		t.Fatalf("logger received %d upload(s), want 1", len(fake.Uploads))
	}
	written, rerr := os.ReadFile(attestPath)
	if rerr != nil {
		t.Fatalf("reading %s: %v", attestPath, rerr)
	}
	if string(fake.Uploads[0]) != string(written) {
		t.Errorf("uploaded bytes differ from the file on disk")
	}

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	scans, err := st.Scans(context.Background(), 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("got %d scan(s), want 1", len(scans))
	}
	if scans[0].RekorLogIndex == nil || *scans[0].RekorLogIndex != 55 {
		t.Errorf("ledger RekorLogIndex = %v, want 55", scans[0].RekorLogIndex)
	}
	if scans[0].RekorUUID != "uuid-xyz" {
		t.Errorf("ledger RekorUUID = %q, want uuid-xyz", scans[0].RekorUUID)
	}
}

// TestCertifyRepoTransparencyFailsOpenOnUploadError pins the top-level
// fail-open contract: an erroring logger prints one stderr line and leaves
// the scan's exit code and ledger receipt columns exactly as an un-uploaded
// scan's — NULL, never a fabricated value.
func TestCertifyRepoTransparencyFailsOpenOnUploadError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	certKeyPath := filepath.Join(t.TempDir(), "certify_key")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", certKeyPath)

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	attestPath := filepath.Join(t.TempDir(), "statement.json")

	fake := &transparency.FakeLogger{Err: errors.New("rekor: connection refused")}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--substrate", substrateWorkspace,
		"--record", "--record-db", dsn,
		"--attest", attestPath, "--transparency",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("a failed --transparency upload changed the exit code: got %d, want 0; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "rekor: connection refused") {
		t.Errorf("stderr = %q, want the upload error surfaced", errb.String())
	}
	if strings.Contains(out.String(), "attestation logged:") {
		t.Errorf("stdout = %q, must not claim a receipt on a failed upload", out.String())
	}

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	scans, err := st.Scans(context.Background(), 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("got %d scan(s), want 1", len(scans))
	}
	if scans[0].RekorLogIndex != nil {
		t.Errorf("ledger RekorLogIndex = %v, want nil after a failed upload", *scans[0].RekorLogIndex)
	}
	if scans[0].RekorUUID != "" {
		t.Errorf("ledger RekorUUID = %q, want empty after a failed upload", scans[0].RekorUUID)
	}
}
