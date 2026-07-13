// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/gateway"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

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
