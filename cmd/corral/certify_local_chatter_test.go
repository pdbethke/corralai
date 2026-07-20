// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// captureServer records every request it receives (path + decoded JSON body)
// and answers with whichever shape resShape produces — "anthropic" or
// "openai" — so a test can prove which vendor's endpoint + wire shape a
// Chatter actually spoke to, without any real network call.
func captureServer(t *testing.T, resShape string) (*httptest.Server, *[]capturedReq) {
	t.Helper()
	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, capturedReq{
			path:   r.URL.Path,
			model:  fmt.Sprint(body["model"]),
			authz:  r.Header.Get("Authorization"),
			apikey: r.Header.Get("x-api-key"),
		})
		w.Header().Set("Content-Type", "application/json")
		switch resShape {
		case "anthropic":
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

type capturedReq struct {
	path, model, authz, apikey string
}

// TestLocalChatterForSameVendorCriticSharesBaseBackend verifies that when the
// critic model is on the SAME vendor as the writer/mutant-generator (the
// stock default: two Claude models), all three roles route through the base
// backend's WithModel — every role's Chat call hits the SAME endpoint.
func TestLocalChatterForSameVendorCriticSharesBaseBackend(t *testing.T) {
	srv, reqs := captureServer(t, "anthropic")
	t.Setenv("MODEL_BACKEND", "anthropic")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "claude-haiku-4-5",
	}
	chatterFor, err := localChatterFor(assign)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}
	writer := chatterFor(advpool.RoleTestWriter)
	critic := chatterFor(advpool.RoleTestCritic)

	if _, err := writer.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("writer.Chat: %v", err)
	}
	if _, err := critic.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("critic.Chat: %v", err)
	}

	if len(*reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (both hit the shared anthropic server)", len(*reqs))
	}
	if (*reqs)[0].model != "claude-sonnet-5" || (*reqs)[1].model != "claude-haiku-4-5" {
		t.Fatalf("models = %q, %q; want claude-sonnet-5 then claude-haiku-4-5", (*reqs)[0].model, (*reqs)[1].model)
	}
}

// TestLocalChatterForCrossVendorCriticRoutesToGemini verifies that when the
// critic model resolves to a DIFFERENT vendor than the base (Claude writer +
// mutant-generator, Gemini critic) on the default direct-Claude path, the
// critic's Chatter hits the Gemini (OpenAI-compatible) endpoint while writer
// and mutant-generator keep hitting the Anthropic endpoint — real
// cross-vendor decorrelation, not just a different Claude model.
func TestLocalChatterForCrossVendorCriticRoutesToGemini(t *testing.T) {
	anthropicSrv, anthropicReqs := captureServer(t, "anthropic")
	geminiSrv, geminiReqs := captureServer(t, "openai")

	t.Setenv("MODEL_BACKEND", "anthropic")
	t.Setenv("ANTHROPIC_BASE_URL", anthropicSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", geminiSrv.URL)
	t.Setenv("GEMINI_API_KEY", "gm-test")

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "gemini-3.5-flash",
	}
	chatterFor, err := localChatterFor(assign)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}
	writer := chatterFor(advpool.RoleTestWriter)
	mutant := chatterFor(advpool.RoleMutantGenerator)
	critic := chatterFor(advpool.RoleTestCritic)

	if _, err := writer.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("writer.Chat: %v", err)
	}
	if _, err := mutant.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("mutant.Chat: %v", err)
	}
	if _, err := critic.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("critic.Chat: %v", err)
	}

	if len(*anthropicReqs) != 2 {
		t.Fatalf("anthropic server got %d requests, want 2 (writer + mutant-generator)", len(*anthropicReqs))
	}
	if len(*geminiReqs) != 1 {
		t.Fatalf("gemini server got %d requests, want 1 (critic only)", len(*geminiReqs))
	}
	if (*geminiReqs)[0].model != "gemini-3.5-flash" {
		t.Errorf("gemini request model = %q, want gemini-3.5-flash", (*geminiReqs)[0].model)
	}
	if !strings.Contains((*geminiReqs)[0].authz, "gm-test") {
		t.Errorf("gemini request Authorization = %q, want to carry the Gemini key", (*geminiReqs)[0].authz)
	}
}

// TestLocalChatterForCrossVendorCriticFailsClosedWithoutKey verifies that a
// cross-vendor --critic-model request refuses to build a router (returns an
// error) when the target vendor's key is missing, rather than silently
// falling back to the base Claude backend.
func TestLocalChatterForCrossVendorCriticFailsClosedWithoutKey(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "claude-sonnet-5",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "gemini-3.5-flash",
	}
	_, err := localChatterFor(assign)
	if err == nil {
		t.Fatal("localChatterFor with missing GEMINI_API_KEY: want error, got nil")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("error %q should name the missing env var", err.Error())
	}
}

// TestLocalChatterForExplicitBackendNeverCrossVendorRoutes verifies that an
// operator-pinned MODEL_BACKEND (e.g. openai, pointing every role at one
// endpoint that may itself serve any model) is never disturbed by the
// cross-vendor critic logic, even when the critic's model name looks like a
// different vendor's (gemini-*) — the explicit single-backend WithModel
// behavior must be unchanged.
func TestLocalChatterForExplicitBackendNeverCrossVendorRoutes(t *testing.T) {
	srv, reqs := captureServer(t, "openai")
	t.Setenv("MODEL_BACKEND", "openai")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	// Deliberately absent — proves no cross-vendor path was taken (it would
	// error if ForModel were invoked for the critic here).
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "some-router-model",
		advpool.RoleTestWriter:      "some-router-model",
		advpool.RoleTestCritic:      "gemini-3.5-flash",
	}
	chatterFor, err := localChatterFor(assign)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}
	critic := chatterFor(advpool.RoleTestCritic)
	if _, err := critic.Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("critic.Chat: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("got %d requests to the explicit openai backend, want 1", len(*reqs))
	}
	if (*reqs)[0].model != "gemini-3.5-flash" {
		t.Errorf("model = %q, want gemini-3.5-flash (WithModel on the SAME explicit backend, not cross-vendor routed)", (*reqs)[0].model)
	}
}
