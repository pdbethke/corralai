// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
)

// TestReportBuild drives report_build over the in-memory MCP transport (mirrors
// missions_test.go's harness): a passing build must come back as a signed,
// stored, tamper-evident record — the accountability wedge Task 4 (`corral
// certify`) builds on.
func TestReportBuild(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(nil, nil, Options{
			BuildStore: bs,
			CertifyKey: priv,
		}).Run(ctx, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "report_build",
		Arguments: map[string]any{
			"repo":          "pdbethke/corralai",
			"commit":        "abc123",
			"branch":        "feat/corral-certify",
			"command":       "go test ./...",
			"exit_code":     0,
			"duration_s":    12.5,
			"output_digest": "deadbeef",
			"produced_by":   []string{"claude-opus"},
		},
	})
	if err != nil {
		t.Fatalf("report_build: %v", err)
	}
	if res.IsError {
		t.Fatalf("report_build returned a tool error: %q", toolErrText(res))
	}

	var out reportBuildOut
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode reportBuildOut: %v", err)
	}
	if out.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if out.Head == "" {
		t.Fatal("expected a non-empty ledger head")
	}

	subjects, ok := out.Statement["subject"].([]any)
	if !ok || len(subjects) != 1 {
		t.Fatalf("expected exactly one subject in the statement, got %v", out.Statement["subject"])
	}
	subj, ok := subjects[0].(map[string]any)
	if !ok {
		t.Fatalf("subject[0] is not an object: %v", subjects[0])
	}
	digest, ok := subj["digest"].(map[string]any)
	if !ok {
		t.Fatalf("subject[0].digest is not an object: %v", subj["digest"])
	}
	if digest["sha256"] != out.Head {
		t.Fatalf("statement.subject[0].digest.sha256 = %v, want ledger head %v", digest["sha256"], out.Head)
	}

	if !certify.VerifySig(out.Head, out.Signature, pub) {
		t.Fatal("signature does not verify against the ledger head under the brain's public key")
	}

	stored, found, err := bs.Get(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("record %d not found in the build store", out.ID)
	}
	if stored["signature"] != out.Signature {
		t.Fatalf("stored signature %v != returned signature %v", stored["signature"], out.Signature)
	}

	// Tamper with the statement: flip the digest, VerifyLedger-equivalent
	// check via re-signing should no longer match — a corrupted statement
	// must not silently verify.
	tamperedHead := out.Head[:len(out.Head)-1] + "0"
	if out.Head[len(out.Head)-1] == '0' {
		tamperedHead = out.Head[:len(out.Head)-1] + "1"
	}
	if certify.VerifySig(tamperedHead, out.Signature, pub) {
		t.Fatal("a tampered head must NOT verify against the original signature")
	}
}
