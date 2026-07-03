// SPDX-License-Identifier: Elastic-2.0

package reference

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedderCallsEndpoint(t *testing.T) {
	var gotModel string
	var gotInput []string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotModel, gotInput = req.Model, req.Input
		// echo one 3-dim embedding per input
		var out struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		for range req.Input {
			out.Data = append(out.Data, struct {
				Embedding []float64 `json:"embedding"`
			}{Embedding: []float64{0.1, 0.2, 0.3}})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e := NewEmbedderFor(srv.URL, "test-model", "sk-abc")
	vecs, err := e.Embed([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "test-model" || len(gotInput) != 2 || gotInput[0] != "alpha" {
		t.Fatalf("request shape wrong: model=%q input=%v", gotModel, gotInput)
	}
	if gotAuth != "Bearer sk-abc" {
		t.Fatalf("auth header = %q, want bearer", gotAuth)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 || vecs[0][0] != float32(0.1) {
		t.Fatalf("vectors wrong: %v", vecs)
	}
}

func TestNewEmbedderDisabledWithoutURL(t *testing.T) {
	t.Setenv("CORRALAI_EMBED_URL", "")
	if NewEmbedder() != nil {
		t.Fatal("embedder should be nil (disabled) when CORRALAI_EMBED_URL is unset")
	}
	t.Setenv("CORRALAI_EMBED_URL", "https://x/v1/embeddings")
	if e := NewEmbedder(); e == nil || e.Model() == "" {
		t.Fatal("embedder should be configured when URL is set (with a default model)")
	}
}

func TestChunkWindowsWithOverlap(t *testing.T) {
	if chunk("", 100, 10) != nil {
		t.Fatal("empty text yields no chunks")
	}
	text := ""
	for i := 0; i < 250; i++ {
		text += "x"
	}
	cs := chunk(text, 100, 20)
	if len(cs) < 3 {
		t.Fatalf("250 chars / (100-20 step) should yield >=3 chunks, got %d", len(cs))
	}
	if len([]rune(cs[0])) != 100 {
		t.Fatalf("first chunk should be 100 runes, got %d", len([]rune(cs[0])))
	}
}
