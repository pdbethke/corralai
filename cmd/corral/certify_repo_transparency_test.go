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
// this exercises the --attest/--transparency/--ledger wiring with zero real
// audit cost.
func TestCertifyRepoTransparencyStampsLedgerAndBundle(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	fixtureCertifyKey(t) // a configured key must EXIST: --transparency never mints one (e1608f971235#R1)

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	attestPath := filepath.Join(t.TempDir(), "statement.json")

	fake := &transparency.FakeLogger{Entry: transparency.LogEntry{LogIndex: 55, UUID: "uuid-xyz", IntegratedTime: 42}}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--substrate", substrateWorkspace,
		"--ledger", ledgerDir,
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
	// The upload carries the SIGNED ENVELOPE's bytes, not the plain
	// statement's — byte-identity now pins the envelope, per T1b.
	envPath := dsseEnvelopePathFor(attestPath)
	written, rerr := os.ReadFile(envPath)
	if rerr != nil {
		t.Fatalf("reading the envelope %s: %v", envPath, rerr)
	}
	if string(fake.Uploads[0]) != string(written) {
		t.Errorf("uploaded bytes differ from the envelope file on disk")
	}
	// And the plain file is untouched by any of this — still there, still
	// what a plain --attest run has always produced.
	if _, err := os.Stat(attestPath); err != nil {
		t.Errorf("the plain --attest file is missing: %v", err)
	}

	// The entry is written AFTER the upload, so it carries the receipt.
	scans := ledgerScanRows(t, ledgerDir)
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

// ledgerScanRows reads a ledger directory's scan rows, newest first, the
// way `corral scans list` does.
func ledgerScanRows(t *testing.T, dir string) []scanstore.ScanRow {
	t.Helper()
	st, err := openLedgerScans(dir)
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	defer st.Close()
	scans, err := st.Scans(context.Background(), 10)
	if err != nil {
		t.Fatalf("Scans: %v", err)
	}
	return scans
}

// TestCertifyRepoTransparencyFailsOpenOnUploadError pins the top-level
// fail-open contract: an erroring logger prints one stderr line and leaves
// the scan's exit code and ledger receipt columns exactly as an un-uploaded
// scan's — NULL, never a fabricated value.
func TestCertifyRepoTransparencyFailsOpenOnUploadError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	fixtureCertifyKey(t) // a configured key must EXIST: --transparency never mints one (e1608f971235#R1)

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	attestPath := filepath.Join(t.TempDir(), "statement.json")

	fake := &transparency.FakeLogger{Err: errors.New("rekor: connection refused")}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--substrate", substrateWorkspace,
		"--ledger", ledgerDir,
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

	scans := ledgerScanRows(t, ledgerDir)
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

// TestTransparencyWithoutSigningKeyExitsUsageError pins requirement 2 of
// T1b: --transparency REFUSES (exit 2, naming CORRALAI_CERTIFY_KEY_FILE)
// when no usable local signing key is available — checked early, before any
// real work runs, exactly like the --attest guard above. A corrupt
// configured key file is used (rather than "unconfigured") so the test is
// deterministic regardless of what key material happens to exist on the
// host running it.
func TestTransparencyWithoutSigningKeyExitsUsageError(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	corrupt := filepath.Join(t.TempDir(), "corrupt_key")
	if err := os.WriteFile(corrupt, []byte("not a valid seed"), 0o600); err != nil {
		t.Fatalf("seeding a corrupt key file: %v", err)
	}
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", corrupt)

	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--attest", filepath.Join(t.TempDir(), "statement.json"), "--transparency"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "CORRALAI_CERTIFY_KEY_FILE") {
		t.Errorf("stderr = %q, want it to name CORRALAI_CERTIFY_KEY_FILE", errb.String())
	}
}

// TestCertifyRepoSourcePushedIsTheSinksOwnFact: source_pushed is a
// custody fact about ONE sink. The ledger entry always carries the source
// it holds (the verdict cache reads it back) and says so; a warehouse row
// says whether source reached IT — true only when --push-source was given
// AND the push succeeded, and a failed push leaves no row at all, never a
// row swearing the source had left the box. On a runner the failed push
// is also raised as a workflow annotation — the exit code stays the
// verdict's, but a green job with a silent stderr line is how an operator
// learns from a query that their warehouse is empty.
func TestCertifyRepoSourcePushedIsTheSinksOwnFact(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	t.Setenv("GITHUB_ACTIONS", "true")

	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "README.md"), "# x\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base := gitRevParseHead(t, root)

	run := func(pushTarget string, extra ...string) (scanstore.ScanRow, string) {
		ledgerDir := filepath.Join(t.TempDir(), "ledger")
		var out, errb bytes.Buffer
		code := runCertifyRepo(append([]string{
			"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
			"--diff-base", base, "--substrate", substrateWorkspace,
			"--ledger", ledgerDir,
			"--push", pushTarget,
		}, extra...), &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d: stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		scans := ledgerScanRows(t, ledgerDir)
		if len(scans) != 1 {
			t.Fatalf("ledger: %d entries, want 1", len(scans))
		}
		return scans[0], errb.String()
	}
	warehouseSourcePushed := func(target string) bool {
		t.Helper()
		db, err := attachWarehouse(target, true)
		if err != nil {
			t.Fatalf("open the warehouse: %v", err)
		}
		defer db.Close()
		var v bool
		if err := db.QueryRow(`SELECT source_pushed FROM corral_scans`).Scan(&v); err != nil {
			t.Fatalf("read source_pushed: %v", err)
		}
		return v
	}

	// A push that cannot succeed: the target's directory does not exist.
	// The entry is still written, and still carries its own source.
	entry, errb := run(filepath.Join(t.TempDir(), "no", "such", "dir", "wh.duckdb"), "--push-source")
	if !entry.SourcePushed {
		t.Errorf("the ledger entry must say it carries source; it does")
	}
	if !strings.Contains(errb, "::warning title=corral push failed::") {
		t.Errorf("stderr = %q, want a workflow annotation for the failed push", errb)
	}

	withheld := filepath.Join(t.TempDir(), "wh.duckdb")
	if _, errb := run(withheld); strings.Contains(errb, "::warning") {
		t.Errorf("stderr = %q, an annotation on a push that succeeded", errb)
	}
	if warehouseSourcePushed(withheld) {
		t.Errorf("pushed without --push-source, yet the warehouse row says source_pushed=true")
	}

	shipped := filepath.Join(t.TempDir(), "wh.duckdb")
	run(shipped, "--push-source")
	if !warehouseSourcePushed(shipped) {
		t.Errorf("pushed with --push-source, yet the warehouse row says source_pushed=false")
	}
}

// Review e1608f971235#R3: when the stale envelope cannot be removed, nothing
// may be left for the Action to attest — neither the new plain statement
// (it used to be written first) nor an old one at the same path.
func TestWriteStatementLeavesNothingToAttestWhenTheStaleEnvelopeStays(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "statement.json")
	mustWrite(t, path, `{"old":"statement"}`)
	// A non-empty directory where the envelope goes: os.Remove cannot take it.
	mustWrite(t, filepath.Join(dsseEnvelopePathFor(path), "keep"), "x")

	sha, _, _, err := writeStatement(path, map[string]any{"new": "statement"})
	if err == nil || sha != "" {
		t.Fatalf("want an error and no hash, got sha=%q err=%v", sha, err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		b, _ := os.ReadFile(path)
		t.Fatalf("a statement was left for the Action to attest after the write failed: %s", b)
	}
}

// Review e1608f971235#R4: an audit's --attest stays quiet about a missing
// key, but the sign error is returned to the caller rather than dropped, so
// --transparency can say why there is no envelope.
func TestWriteAuditStatementReturnsTheSignError(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	corrupt := filepath.Join(t.TempDir(), "corrupt_key")
	mustWrite(t, corrupt, "not a valid seed")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", corrupt)
	dir := t.TempDir()
	att := filepath.Join(t.TempDir(), "statement.json")

	sha, signErr, err := writeAuditStatement(att, dir, oneAuditedFileReport(), map[string]string{"writer": "m"}, nil, nil, true, 0, oneAuditedFileBundle(0))
	if err != nil || sha == "" {
		t.Fatalf("the plain statement must still be written: sha=%q err=%v", sha, err)
	}
	if signErr == nil {
		t.Fatal("the sign error was dropped")
	}
}

// Reviews e1608f971235#R2 and #R4 at the --transparency door: a sign error
// is reported as itself (not as "no such file" on the envelope), and every
// way the requested public log entry fails is raised as a workflow
// annotation on a runner — as a failed statement write and a failed push
// already were — because a green job with one stderr line is how an operator
// learns from a NULL column that nothing was logged.
func TestTransparencyFailuresAreNamedAndRaisedOnARunner(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	var out, errb bytes.Buffer
	if _, ok := logStatementToTransparency(filepath.Join(t.TempDir(), "s.json"), errors.New("disk full writing the envelope"), &out, &errb); ok {
		t.Fatal("a sign failure reported a log entry")
	}
	if !strings.Contains(errb.String(), "disk full writing the envelope") || !strings.Contains(errb.String(), "::warning title=corral transparency failed::") {
		t.Fatalf("the sign error must be named and raised: %q", errb.String())
	}

	fixtureCertifyKey(t)
	path := filepath.Join(t.TempDir(), "s.json")
	mustWrite(t, dsseEnvelopePathFor(path), `{"payload":"x"}`)
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger {
		return &transparency.FakeLogger{Err: errors.New("rekor: connection refused")}
	}
	errb.Reset()
	if _, ok := logStatementToTransparency(path, nil, &out, &errb); ok {
		t.Fatal("a failed upload reported a log entry")
	}
	if !strings.Contains(errb.String(), "::warning title=corral transparency failed::") || !strings.Contains(errb.String(), "connection refused") {
		t.Fatalf("a failed upload must be raised with its cause: %q", errb.String())
	}
}
