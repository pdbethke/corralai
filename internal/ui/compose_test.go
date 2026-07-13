// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

// TestCreateMissionPersistsAndInjectsHerd used to prove the HTTP composer
// persisted a herd and injected it into the default (ScaledPlan) build's
// tasks. The build-plan sizer is retired and this endpoint doesn't (yet)
// accept an explicit plan, so create_mission over HTTP now has nothing to
// build a mission from and must fail closed instead of silently
// synthesizing a build arc. Phase 3 reworks this handler; herd persist+inject
// is still covered end to end over MCP (missions_test.go's
// TestCreateMissionMCPPersistsAndInjectsHerd), which does accept an explicit
// plan.
func TestCreateMissionPersistsAndInjectsHerd(t *testing.T) {
	dir := t.TempDir()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	// An endpoint usable by the anonymous (dev) principal "".
	if err := gw.Register(gateway.Endpoint{Name: "prod-db", Transport: "stdio", Endpoint: "x", Enabled: true}, gateway.Auth{}, ""); err != nil {
		t.Fatal(err)
	}
	lbID, err := ta.SaveLookbookItem("neon", "Emulate the neon dashboard mock.", "image/png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	mux := Handler(Deps{Missions: m, Queue: q, Gateway: gw, TaskArtifacts: ta, RoleModels: rolemodel.New()})

	body, _ := json.Marshal(map[string]any{
		"directive":     "build a dashboard",
		"role_models":   map[string]rolemodel.ModelRef{"builder": {Backend: "anthropic", Model: "claude-opus"}},
		"mcp_endpoints": []string{"prod-db"},
		"lookbook_ids":  []int64{lbID},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (build missions are retired): %s", rec.Code, rec.Body.String())
	}
}

func TestComposeOptions(t *testing.T) {
	dir := t.TempDir()
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	gw.Register(gateway.Endpoint{Name: "prod-db", Transport: "stdio", Endpoint: "x", Enabled: true}, gateway.Auth{}, "")
	ta.SaveLookbookItem("neon", "neon mock", "image/png", []byte("x"))
	h := Handler(Deps{Gateway: gw, TaskArtifacts: ta})

	req := httptest.NewRequest(http.MethodGet, "/api/mission/compose-options", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Endpoints []struct {
			Name string `json:"name"`
		} `json:"endpoints"`
		Lookbook []struct {
			Name string `json:"name"`
		} `json:"lookbook"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Endpoints) != 1 || out.Endpoints[0].Name != "prod-db" {
		t.Fatalf("endpoints wrong: %+v", out.Endpoints)
	}
	if len(out.Lookbook) != 1 || out.Lookbook[0].Name != "neon" {
		t.Fatalf("lookbook wrong: %+v", out.Lookbook)
	}
}

func TestCreateMissionRejectsUnknownEndpoint(t *testing.T) {
	dir := t.TempDir()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	gw, _ := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	ta, _ := taskartifacts.Open(filepath.Join(dir, "ta.sqlite3"))
	h := Handler(Deps{Missions: m, Queue: q, Gateway: gw, TaskArtifacts: ta, RoleModels: rolemodel.New()})

	body, _ := json.Marshal(map[string]any{"directive": "x", "mcp_endpoints": []string{"nope"}})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown endpoint must be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}
