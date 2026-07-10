// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// seedGoodRecord builds and saves a fully-valid, signed, anchored build
// record (mirroring what report_build produces and what corral certify
// --out would export), returning its assigned id. It's the "everything
// should verify" fixture for the detail-endpoint tests.
func seedGoodRecord(t *testing.T, store *buildstore.Store, priv ed25519.PrivateKey, w transparency.Witness, repo, commit, branch, actor string) int64 {
	t.Helper()
	steps := []certify.Step{
		{Kind: "context", Actor: actor, Subject: repo + "@" + commit, Detail: map[string]any{"repo": repo, "commit": commit, "branch": branch}},
		{Kind: "execution", Actor: actor, Subject: "go test ./...", Detail: map[string]any{"exit_code": 0.0, "ok": true}},
	}
	built, head := certify.BuildLedger(steps)
	stmt := certify.BuildAttestation(certify.BuildRecord{Repo: repo, Commit: commit, Branch: branch, Actor: actor, Command: "go test ./...", ExitCode: 0}, head)
	envelope, err := certify.SignDSSE(stmt, priv, "brain")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := certify.CanonicalStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}
	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := w.Anchor(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Save(repo, commit, branch, actor, head, string(envelope), string(canonical), string(stepsJSON), string(entryJSON), true,
		"fix the thing", actor+" <a@example.com>", "2026-07-09T12:00:00Z", "", true)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBuildsListEndpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	w := transparency.NewFakeWitness()

	seedGoodRecord(t, store, priv, w, "pdbethke/corralai", "abc123", "main", "peter")
	seedGoodRecord(t, store, priv, w, "pdbethke/other", "def456", "main", "peter")

	srv := httptest.NewServer(Handler(Deps{BuildStore: store, CertifyPub: pub, Witness: w}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/builds")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/builds: %v status=%v", err, res.StatusCode)
	}
	var all []buildstore.Summary
	if err := json.NewDecoder(res.Body).Decode(&all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 summaries, got %d: %+v", len(all), all)
	}

	res2, err := http.Get(srv.URL + "/api/builds?repo=pdbethke/corralai")
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("GET /api/builds?repo=: %v status=%v", err, res2.StatusCode)
	}
	var filtered []buildstore.Summary
	if err := json.NewDecoder(res2.Body).Decode(&filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Repo != "pdbethke/corralai" {
		t.Fatalf("repo filter did not narrow: %+v", filtered)
	}

	// A non-GET must be refused.
	res3, err := http.Post(srv.URL+"/api/builds", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res3.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/builds: status = %d, want 405", res3.StatusCode)
	}
}

func TestBuildsListEndpointNilStore(t *testing.T) {
	srv := httptest.NewServer(Handler(Deps{}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/builds")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/builds with nil store: %v status=%v", err, res.StatusCode)
	}
	var out []buildstore.Summary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty list, got %+v", out)
	}
}

func TestBuildDetailEndpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	w := transparency.NewFakeWitness()
	id := seedGoodRecord(t, store, priv, w, "pdbethke/corralai", "abc123", "main", "peter")

	srv := httptest.NewServer(Handler(Deps{BuildStore: store, CertifyPub: pub, Witness: w}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/builds/" + strconv.FormatInt(id, 10))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/builds/{id}: %v status=%v", err, res.StatusCode)
	}
	var detail struct {
		ID            int64            `json:"id"`
		Repo          string           `json:"repo"`
		Commit        string           `json:"commit"`
		Branch        string           `json:"branch"`
		Actor         string           `json:"actor"`
		Head          string           `json:"head"`
		Anchored      bool             `json:"anchored"`
		CommitMessage string           `json:"commit_message"`
		Checks        []map[string]any `json:"checks"`
		AllOK         bool             `json:"all_ok"`
		VerifyCommand string           `json:"verify_command"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != id {
		t.Fatalf("id mismatch: got %d want %d", detail.ID, id)
	}
	if detail.Repo != "pdbethke/corralai" || detail.Commit != "abc123" || detail.Branch != "main" || detail.Actor != "peter" {
		t.Fatalf("record fields not surfaced: %+v", detail)
	}
	if !detail.Anchored {
		t.Fatalf("expected anchored=true")
	}
	if !detail.AllOK {
		t.Fatalf("expected all_ok=true for a good record: %+v", detail.Checks)
	}
	if len(detail.Checks) != 4 {
		t.Fatalf("expected 4 checks, got %d: %+v", len(detail.Checks), detail.Checks)
	}
	for _, c := range detail.Checks {
		if ok, _ := c["OK"].(bool); !ok {
			t.Fatalf("expected every check OK, got %+v", detail.Checks)
		}
	}
	if detail.VerifyCommand == "" {
		t.Fatalf("expected a non-empty verify_command")
	}
	if detail.CommitMessage != "fix the thing" {
		t.Fatalf("commit_message not surfaced: %+v", detail)
	}

	// Missing id -> 404.
	res2, err := http.Get(srv.URL + "/api/builds/999999")
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/builds/999999: status = %d, want 404", res2.StatusCode)
	}

	// A non-GET must be refused.
	res3, err := http.Post(srv.URL+"/api/builds/"+strconv.FormatInt(id, 10), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res3.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/builds/{id}: status = %d, want 405", res3.StatusCode)
	}
}
