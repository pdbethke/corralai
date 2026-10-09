// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/review"
)

// keyless makes writeSignedStatementEnvelope find no local key.
func keyless(t *testing.T) {
	t.Helper()
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	t.Setenv("HOME", t.TempDir()) // no ~/.claude/corralai_certify_key
}

// An envelope signed for a PREVIOUS statement must never sit beside a new
// one: a keyless run after a keyed run in the same place left exactly that.
func TestKeylessAuditStatementRemovesAStaleEnvelope(t *testing.T) {
	keyless(t)
	path := filepath.Join(t.TempDir(), "stmt.json")
	stale := dsseEnvelopePathFor(path)
	if err := os.WriteFile(stale, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := writeStatement(path, map[string]any{"_type": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale envelope survived a keyless write: %v", err)
	}
}

// review --attest into a directory that does not exist yet must create it,
// as the audit path has since the 2026-09-08 incident.
func TestStatementWriterCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".corral", "nested", "stmt.json")
	if _, _, _, err := writeStatement(path, map[string]any{"_type": "x"}); err != nil {
		t.Fatalf("writeStatement into a missing dir: %v", err)
	}
}

// writeAuditStatement goes through the one writer: a stale envelope beside
// its path is gone after a keyless run, and the returned sha and the plain
// bytes are exactly what the pre-refactor inline code produced
// (json.MarshalIndent of the statement, two-space indent, no trailing newline).
func TestWriteAuditStatementUsesTheOneWriterAndKeepsItsBytes(t *testing.T) {
	keyless(t)
	dir := t.TempDir()
	att := filepath.Join(dir, "att.json")
	stale := dsseEnvelopePathFor(att)
	if err := os.WriteFile(stale, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, _, err := writeAuditStatement(att, dir, oneAuditedFileReport(), map[string]string{"writer": "m"}, nil, nil, true, 0, oneAuditedFileBundle(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale envelope survived writeAuditStatement: %v", err)
	}
	got, _ := os.ReadFile(att)
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	old, _ := json.MarshalIndent(m, "", "  ") // the OLD marshaling expression
	if string(old) != string(got) {
		t.Fatalf("audit plain bytes are not MarshalIndent(\"\", \"  \") form")
	}
	sum := sha256.Sum256(got)
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("returned sha %s != sha256 of the plain bytes", sha)
	}
}

// writeReviewStatement into a missing directory succeeds, and its bytes and
// sha are what the old inline MarshalIndent code produced.
func TestWriteReviewStatementUsesTheOneWriterAndKeepsItsBytes(t *testing.T) {
	keyless(t)
	path := filepath.Join(t.TempDir(), ".corral", "review.json")
	r := review.Review{}
	sha, _, signErr, err := writeReviewStatement(path, r)
	if err != nil {
		t.Fatalf("writeReviewStatement into a missing dir: %v", err)
	}
	if signErr == nil {
		t.Error("keyless review must still report why no envelope was written")
	}
	want, _ := json.MarshalIndent(certify.BuildReviewAttestation(r), "", "  ")
	got, _ := os.ReadFile(path)
	if string(got) != string(want) {
		t.Fatalf("review plain bytes changed:\n%s\nwant\n%s", got, want)
	}
	sum := sha256.Sum256(want)
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("returned sha %s != sha256 of old bytes", sha)
	}
}
