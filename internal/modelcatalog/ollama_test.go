// SPDX-License-Identifier: Elastic-2.0

package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestListOllamaReadsTags(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ollama.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("requested %q, want /api/tags", r.URL.Path)
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	names, err := ListOllama(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("ListOllama: %v", err)
	}
	for _, want := range []string{"kirby:latest", "qwen3.6:35b-a3b", "nomic-embed-text:latest"} {
		if !slices.Contains(names, want) {
			t.Errorf("names %v missing %q", names, want)
		}
	}
	if len(names) != 13 {
		t.Errorf("got %d names, want 13 (every models[].name in the fixture)", len(names))
	}
}

func TestListOllamaDaemonDownIsLoud(t *testing.T) {
	const base = "http://127.0.0.1:1"
	_, err := ListOllama(context.Background(), http.DefaultClient, base)
	if err == nil {
		t.Fatal("expected an error for an unreachable daemon, got nil")
	}
	if !strings.Contains(err.Error(), "could not list models at "+base) {
		t.Errorf("error %q does not contain %q", err, "could not list models at "+base)
	}
}

func TestListOllamaMalformedIsLoud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	_, err := ListOllama(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a body with no models list, got nil")
	}
	if !strings.Contains(err.Error(), `no "models" list`) {
		t.Errorf("error %q does not contain %q", err, `no "models" list`)
	}
}

func TestListOllamaEmptyBaseRefuses(t *testing.T) {
	names, err := ListOllama(context.Background(), http.DefaultClient, "")
	if err == nil {
		t.Fatalf("empty base must refuse, got names %v and nil error", names)
	}
	if names != nil {
		t.Errorf("empty base returned names %v alongside the error", names)
	}
}
