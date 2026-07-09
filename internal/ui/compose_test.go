// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

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

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
		OK bool  `json:"ok"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK || out.ID == 0 {
		t.Fatalf("bad response: %s", rec.Body.String())
	}

	// Herd persisted.
	h, ok, _ := m.Herd(out.ID)
	if !ok || len(h.Endpoints) != 1 || h.Endpoints[0] != "prod-db" || len(h.LookbookIDs) != 1 {
		t.Fatalf("herd not persisted: %+v ok=%v", h, ok)
	}
	// Context injected into a builder task instruction.
	tasks, _ := q.List(out.ID)
	sawEndpoint, sawLookbook := false, false
	for _, tk := range tasks {
		if strings.Contains(tk.Instruction, "prod-db") {
			sawEndpoint = true
		}
		if tk.Role == "builder" && strings.Contains(tk.Instruction, "neon dashboard") {
			sawLookbook = true
		}
	}
	if !sawEndpoint || !sawLookbook {
		t.Fatalf("injection missing: endpoint=%v lookbook=%v", sawEndpoint, sawLookbook)
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
