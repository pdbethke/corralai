// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
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

	// produced_by must land in the SLSA provenance's resolvedDependencies
	// (materials) — predicate.buildDefinition.resolvedDependencies — so a
	// verifier can see which model produced the change under certification.
	predicate, ok := out.Statement["predicate"].(map[string]any)
	if !ok {
		t.Fatalf("statement.predicate is not an object: %v", out.Statement["predicate"])
	}
	buildDef, ok := predicate["buildDefinition"].(map[string]any)
	if !ok {
		t.Fatalf("predicate.buildDefinition is not an object: %v", predicate["buildDefinition"])
	}
	resolvedDeps, ok := buildDef["resolvedDependencies"].([]any)
	if !ok || len(resolvedDeps) != 1 {
		t.Fatalf("expected exactly one resolvedDependency, got %v", buildDef["resolvedDependencies"])
	}
	dep, ok := resolvedDeps[0].(map[string]any)
	if !ok {
		t.Fatalf("resolvedDependencies[0] is not an object: %v", resolvedDeps[0])
	}
	if dep["uri"] != "model:claude-opus" {
		t.Fatalf("resolvedDependencies[0].uri = %v, want %q", dep["uri"], "model:claude-opus")
	}

	// public_key in the response must be the brain's certify public key.
	wantPub := hex.EncodeToString(pub)
	if out.PublicKey != wantPub {
		t.Fatalf("response public_key = %q, want %q", out.PublicKey, wantPub)
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

	// The stored "statement" column IS the canonical bytes SignStatement
	// signed — re-marshal it exactly as buildstore does (no re-marshal of a
	// decoded map, which could reorder/re-encode and break byte-identity)
	// and confirm it verifies against the returned signature and pubkey.
	storedStmtOnly := map[string]any{}
	for k, v := range stored {
		if k == "signature" || k == "steps" {
			continue
		}
		storedStmtOnly[k] = v
	}
	storedCanonical, err := certify.CanonicalStatement(storedStmtOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !certify.VerifyStatement(storedCanonical, out.Signature, pub) {
		t.Fatal("VerifyStatement must succeed over the stored canonical statement bytes under the brain's public key")
	}

	// Tamper with the stored statement: mutate a byte and confirm
	// VerifyStatement now fails — the persisted artifact is tamper-evident,
	// not just the in-process value.
	tampered := append([]byte(nil), storedCanonical...)
	tampered[0] ^= 0xFF
	if certify.VerifyStatement(tampered, out.Signature, pub) {
		t.Fatal("a tampered stored statement must NOT verify against the original signature")
	}

	// The stored steps must be a valid, independently re-verifiable ledger
	// whose recomputed head matches out.Head and the statement's subject
	// digest — i.e. a verifier working ONLY from what's in buildstore (no
	// trust in the brain's in-process state) can confirm both the ledger
	// integrity and that the statement is bound to that exact ledger.
	rawSteps, ok := stored["steps"].([]any)
	if !ok {
		t.Fatalf("stored steps is not a list: %v (%T)", stored["steps"], stored["steps"])
	}
	// certify.Step tags Hash `json:"-"` (deliberate: a step's own hash must
	// never be part of the input to computing that hash), so a plain
	// json.Unmarshal into certify.Step can't recover Hash. The brain stores
	// it via certify.MarshalSteps (Hash carried explicitly under "hash");
	// reconstruct with certify.UnmarshalSteps, mirroring what an independent
	// verifier (Task 7's CLI) would do reading this record back with no
	// access to brain internals.
	rawStepsJSON, err := json.Marshal(rawSteps)
	if err != nil {
		t.Fatal(err)
	}
	storedLedger, err := certify.UnmarshalSteps(rawStepsJSON)
	if err != nil {
		t.Fatalf("certify.UnmarshalSteps: %v", err)
	}
	ok2, msg := certify.VerifyLedger(storedLedger, out.Head)
	if !ok2 {
		t.Fatalf("VerifyLedger over stored steps failed: %s", msg)
	}

	subjects, ok = stored["subject"].([]any)
	if !ok || len(subjects) != 1 {
		t.Fatalf("stored statement missing subject: %v", stored["subject"])
	}
	subj, ok = subjects[0].(map[string]any)
	if !ok {
		t.Fatalf("stored subject[0] is not an object: %v", subjects[0])
	}
	digest, ok = subj["digest"].(map[string]any)
	if !ok || digest["sha256"] != out.Head {
		t.Fatalf("stored statement.subject[0].digest.sha256 = %v, want ledger head %v", digest["sha256"], out.Head)
	}
}

// TestReportBuildDisabledWithoutValidKey locks the misconfig guard: a brain
// configured with a BuildStore but a missing/invalid CertifyKey must NOT
// register report_build (ed25519.Sign panics on a malformed key — that panic
// must never reach the MCP request goroutine). The tool call must come back
// as a clean "unknown tool" error, not a crash.
func TestReportBuildDisabledWithoutValidKey(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		// Deliberately omit CertifyKey: BuildStore is set, but the signing
		// key is nil/invalid — the guard must skip registration rather than
		// let ed25519.Sign panic on the first call.
		_ = NewServer(nil, nil, Options{
			BuildStore: bs,
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
			"repo":      "pdbethke/corralai",
			"commit":    "abc123",
			"command":   "go test ./...",
			"exit_code": 0,
		},
	})
	// The go-sdk MCP client surfaces an unknown tool either as a returned
	// error or as an IsError result, depending on version — accept either,
	// but a panic (which would abort the goroutine and hang/close the
	// session) must never happen.
	if err == nil && res != nil && !res.IsError {
		t.Fatalf("report_build must be unregistered when CertifyKey is missing/invalid, got a successful result: %+v", res)
	}
}
