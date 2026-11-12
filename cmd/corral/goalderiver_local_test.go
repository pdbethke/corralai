// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/models"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// ollamaDeriveSeat is a derive seat the resolver placed on a local daemon.
func ollamaDeriveSeat(model, endpoint string) agentbackend.ResolvedSeat {
	return agentbackend.ResolvedSeat{
		Flag: "derive-model", Spec: model, Kind: "api", Provider: "ollama",
		Wire: models.WireOllama, Endpoint: endpoint, DeclaredBy: agentbackend.DeclaredRegistry,
	}
}

// hostedDeriveSeat is a derive seat on a cloud vendor's own endpoint.
func hostedDeriveSeat(model, provider string) agentbackend.ResolvedSeat {
	return agentbackend.ResolvedSeat{
		Flag: "derive-model", Spec: model, Kind: "api", Provider: provider,
		Wire: models.WireHosted, DeclaredBy: agentbackend.DeclaredName,
	}
}

// TestGoalDeriverAcceptsLocalModel pins the fix for `certify --repo` being
// unable to run without a cloud vendor: a seat the resolver placed on a local
// daemon needs no cloud credential.
func TestGoalDeriverAcceptsLocalModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	for _, model := range []string{"qwen3.5:9b-q8_0", "gemma4:12b", "qwen2.5-coder:14b"} {
		d, err := newLLMDeriver(ollamaDeriveSeat(model, "http://127.0.0.1:11434"))
		if err != nil {
			t.Fatalf("newLLMDeriver(%q) = %v, want a local backend and no error", model, err)
		}
		if d == nil {
			t.Fatalf("newLLMDeriver(%q) returned a nil deriver", model)
		}
	}
}

// A cloud model with no credential must still fail closed.
func TestGoalDeriverStillFailsClosedForCloudWithoutKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	if os.Getenv("GEMINI_API_KEY") != "" {
		t.Skip("env not isolated")
	}
	if _, err := newLLMDeriver(hostedDeriveSeat("gemini-3.7-flash", "google")); err == nil {
		t.Fatal("newLLMDeriver(cloud model, no key) = nil error, want a refusal")
	}
}

// TestDeriverUsesResolvedOpenAIChatSeat: the derive seat is built from the
// routing the resolver decided, whatever wire that is. A seat on an
// openai-chat daemon (vllm, lmstudio, ...) used to be unreachable from the
// deriver, which knew only the ollama wire.
func TestDeriverUsesResolvedOpenAIChatSeat(t *testing.T) {
	var path string
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"must never return a negative balance"}}]}`)
	}))
	defer srv.Close()
	rs := agentbackend.ResolvedSeat{
		Flag: "derive-model", Spec: "qwen3-coder", Kind: "api", Provider: "vllm",
		Wire: models.WireOpenAIChat, Endpoint: srv.URL + "/v1", DeclaredBy: agentbackend.DeclaredRegistry,
	}
	d, err := newLLMDeriver(rs)
	if err != nil {
		t.Fatal(err)
	}
	goal, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go", Lang: "go"}, "package a\n")
	if err != nil || !ok {
		t.Fatalf("Derive = %q, %v, %v", goal, ok, err)
	}
	if atomic.LoadInt32(&hits) != 1 || path != "/v1/chat/completions" {
		t.Fatalf("hits=%d path=%q, want one call to /v1/chat/completions", hits, path)
	}
	if !strings.Contains(goal, "negative balance") {
		t.Errorf("goal = %q", goal)
	}
}

// A registry-placed ollama seat talks to ITS daemon, not OLLAMA_URL.
func TestGoalDeriverTalksToTheResolvedDaemonNotOLLAMA_URL(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"must hold"},"done":true}`)
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_URL", "http://wrong.invalid:11434")
	d, err := newLLMDeriver(ollamaDeriveSeat("qwen2.5-coder:14b", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go", Lang: "go"}, "package a\n"); err != nil || !ok {
		t.Fatalf("Derive: ok=%v err=%v", ok, err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("the resolved daemon was called %d times, want 1", hits)
	}
}

// A pinned openrouter gateway, resolved by the resolver, derives through the
// gateway on OPENROUTER_API_KEY alone: the deriver adds no key or URL
// fallback of its own (an OpenAI key must never reach openrouter.ai).
func TestGoalDeriverOnAPinnedGatewayReadsOnlyTheGatewayKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"must hold"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "an-openai-secret")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	rs := agentbackend.ResolvedSeat{
		Flag: "derive-model", Spec: "claude-sonnet-5", Kind: "api", Provider: "openrouter",
		Wire: models.WireHosted, Endpoint: srv.URL, KeyEnv: "OPENROUTER_API_KEY", DeclaredBy: agentbackend.DeclaredPinned,
	}
	d, err := newLLMDeriver(rs)
	if err != nil {
		t.Fatalf("a pinned gateway seat with its key set was refused: %v", err)
	}
	if _, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go", Lang: "go"}, "package a\n"); err != nil || !ok {
		t.Fatalf("Derive: ok=%v err=%v", ok, err)
	}
	if auth != "Bearer or-key" {
		t.Errorf("Authorization = %q, want the gateway's own key", auth)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if _, err := newLLMDeriver(rs); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("without OPENROUTER_API_KEY: err = %v, want a refusal naming it (no OPENAI_API_KEY fallback)", err)
	}
}

// An agent seat is a CLI with its own login: a seat that carries an endpoint
// as well is contradictory and is refused, not silently pointed somewhere.
func TestGoalDeriverRefusesAnAgentSeatWithAnEndpoint(t *testing.T) {
	rs := agentbackend.ResolvedSeat{Flag: "derive-model", Spec: "claude-code", Kind: "agent", Endpoint: "http://gpu2.internal:11434", DeclaredBy: agentbackend.DeclaredAgent}
	_, err := newLLMDeriver(rs)
	if err == nil || !strings.Contains(err.Error(), "agent seat") || !strings.Contains(err.Error(), "gpu2.internal") {
		t.Fatalf("err = %v, want a refusal naming the agent seat and the endpoint", err)
	}
}

// testDeriveSeat is a derive seat for tests that only need the model's name:
// the factory they inject never builds a backend from it.
func testDeriveSeat(model string) agentbackend.ResolvedSeat {
	return agentbackend.ResolvedSeat{Flag: "derive-model", Spec: model, Kind: "api", Wire: models.WireHosted, DeclaredBy: agentbackend.DeclaredName}
}
