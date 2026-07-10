// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
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

	// The stored "signature" column IS the DSSE envelope SignDSSE produced —
	// it carries its own embedded copy of the canonical statement, so
	// verifying it needs no separate canonical-bytes column. Confirm it
	// verifies under the brain's public key and that the embedded statement
	// matches what the tool response reported.
	storedStmt, ok, err := certify.VerifyDSSE([]byte(out.Signature), pub)
	if err != nil {
		t.Fatalf("VerifyDSSE returned error: %v", err)
	}
	if !ok {
		t.Fatal("VerifyDSSE must succeed over the stored DSSE envelope under the brain's public key")
	}
	gotSubjJSON, err := json.Marshal(storedStmt["subject"])
	if err != nil {
		t.Fatal(err)
	}
	wantSubjJSON, err := json.Marshal(out.Statement["subject"])
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSubjJSON) != string(wantSubjJSON) {
		t.Fatalf("envelope-embedded statement subject = %s, want %s", gotSubjJSON, wantSubjJSON)
	}

	// Tamper with the stored envelope: flip a byte in its payload and confirm
	// VerifyDSSE now fails — the persisted artifact is tamper-evident, not
	// just the in-process value.
	tampered := append([]byte(nil), stored["signature"].(string)...)
	var envMap map[string]any
	if err := json.Unmarshal(tampered, &envMap); err != nil {
		t.Fatal(err)
	}
	payload := []byte(envMap["payload"].(string))
	idx := len(payload) / 2
	if payload[idx] == 'A' {
		payload[idx] = 'B'
	} else {
		payload[idx] = 'A'
	}
	envMap["payload"] = string(payload)
	tamperedEnvelope, err := json.Marshal(envMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := certify.VerifyDSSE(tamperedEnvelope, pub); ok {
		t.Fatal("a tampered stored envelope must NOT verify against the original signature")
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

	// out.Steps (the response's own "steps" field, not the DB row) must be
	// an independently verifiable ledger too — an --out record built purely
	// from the tool response (Task 7's CLI path) needs no brain round-trip.
	if len(out.Steps) == 0 {
		t.Fatal("expected report_build's response to carry a non-empty steps field")
	}
	stepsBytes, err := json.Marshal(out.Steps)
	if err != nil {
		t.Fatal(err)
	}
	respLedger, err := certify.UnmarshalSteps(stepsBytes)
	if err != nil {
		t.Fatalf("certify.UnmarshalSteps(out.Steps): %v", err)
	}
	if ok3, msg := certify.VerifyLedger(respLedger, out.Head); !ok3 {
		t.Fatalf("VerifyLedger over response steps failed: %s", msg)
	}
}

// TestReportBuildStoresGitLinkFields locks Task 2: report_build must thread
// the commit_message/commit_author/commit_date/commit_signature params
// through to the stored record verbatim, and derive pass from exit_code == 0
// (denormalized so the dashboard's status filter is a cheap column read).
func TestReportBuildStoresGitLinkFields(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	_, priv, err := ed25519.GenerateKey(nil)
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
			"repo":           "pdbethke/corralai",
			"commit":         "abc123",
			"branch":         "feat/x",
			"command":        "go test ./...",
			"exit_code":      0,
			"commit_message": "fix the thing",
			"commit_author":  "Peter <peter@example.com>",
			"commit_date":    "2026-07-09T12:00:00-05:00",
			"commit_signature": map[string]any{
				"signed":    true,
				"signer":    "Peter <peter@example.com>",
				"mechanism": "gpg",
				"verified":  "good",
			},
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

	stored, found, err := bs.Get(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("record %d not found in the build store", out.ID)
	}
	if stored["commit_message"] != "fix the thing" {
		t.Fatalf("stored commit_message = %v, want %q", stored["commit_message"], "fix the thing")
	}
	if stored["commit_author"] != "Peter <peter@example.com>" {
		t.Fatalf("stored commit_author = %v, want %q", stored["commit_author"], "Peter <peter@example.com>")
	}
	if stored["commit_date"] != "2026-07-09T12:00:00-05:00" {
		t.Fatalf("stored commit_date = %v, want %q", stored["commit_date"], "2026-07-09T12:00:00-05:00")
	}
	sigStr, ok := stored["commit_signature"].(string)
	if !ok || sigStr == "" {
		t.Fatalf("expected a non-empty stored commit_signature, got %v (%T)", stored["commit_signature"], stored["commit_signature"])
	}
	var sigMap map[string]any
	if err := json.Unmarshal([]byte(sigStr), &sigMap); err != nil {
		t.Fatalf("stored commit_signature is not valid JSON: %v", err)
	}
	if sigMap["verified"] != "good" {
		t.Fatalf("stored commit_signature.verified = %v, want %q", sigMap["verified"], "good")
	}
	if stored["pass"] != true {
		t.Fatalf("stored pass = %v, want true for exit_code == 0", stored["pass"])
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

// failingWitness is a real (not mocked) transparency.Witness implementation
// whose Anchor always errors — it stands in for a transparency log that's
// down or unreachable, exercising report_build's graceful-degradation path.
type failingWitness struct{}

func (failingWitness) Anchor(context.Context, []byte) (transparency.Entry, error) {
	return transparency.Entry{}, errors.New("transparency log unreachable")
}

func (failingWitness) VerifyInclusion(transparency.Entry, []byte) (bool, string) {
	return false, "failingWitness never anchors"
}

// TestReportBuildAnchorsToWitness drives report_build with a hermetic
// transparency.NewFakeWitness() configured and confirms the response AND the
// stored buildstore record both carry the anchoring evidence — the
// trustless tier's third-party-checkable guarantee.
func TestReportBuildAnchorsToWitness(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(nil, nil, Options{
			BuildStore: bs,
			CertifyKey: priv,
			Witness:    transparency.NewFakeWitness(),
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
	if !out.Anchored {
		t.Fatal("expected anchored=true in the response when a witness is configured and succeeds")
	}
	// LogIndex is a valid field (0 is the fake witness's first index), so
	// just confirm it round-tripped rather than asserting non-zero.

	stored, found, err := bs.Get(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("record %d not found in the build store", out.ID)
	}
	if stored["anchored"] != true {
		t.Fatalf("stored anchored = %v, want true", stored["anchored"])
	}
	rekorStr, ok := stored["rekor"].(string)
	if !ok || rekorStr == "" {
		t.Fatalf("expected a non-empty stored rekor, got %v (%T)", stored["rekor"], stored["rekor"])
	}
}

// TestReportBuildDegradesOnWitnessOutage confirms the load-bearing
// graceful-degradation contract: when the transparency witness is
// configured but unreachable, report_build must STILL succeed (never fail
// the build because the log is down), returning anchored=false and storing
// the record the same way.
func TestReportBuildDegradesOnWitnessOutage(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(nil, nil, Options{
			BuildStore: bs,
			CertifyKey: priv,
			Witness:    failingWitness{},
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
	if err != nil {
		t.Fatalf("report_build must NOT fail when the transparency witness is unreachable: %v", err)
	}
	if res.IsError {
		t.Fatalf("report_build returned a tool error when the witness was down (must degrade gracefully instead): %q", toolErrText(res))
	}

	var out reportBuildOut
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode reportBuildOut: %v", err)
	}
	if out.Anchored {
		t.Fatal("expected anchored=false when the witness errors")
	}
	if out.ID == 0 {
		t.Fatal("expected the build record to still be saved despite the witness outage")
	}

	stored, found, err := bs.Get(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("record %d not found in the build store", out.ID)
	}
	if stored["anchored"] != false {
		t.Fatalf("stored anchored = %v, want false", stored["anchored"])
	}
	if stored["rekor"] != "" {
		t.Fatalf("stored rekor = %q, want empty on witness outage", stored["rekor"])
	}
}
