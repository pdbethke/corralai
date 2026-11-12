// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
	"github.com/pdbethke/corralai/internal/models"
)

// TestHerdPreflightAcceptsRegistryOllamaGptOss is the doctor failure of
// 2026-11-11: a registry entry declares gpt-oss:120b on an ollama daemon, and
// the herd preflight still re-guessed the vendor from the NAME, called it an
// OpenAI model and refused for want of OPENAI_API_KEY. The declaration must
// outrank the name, with no OpenAI credential anywhere.
func TestHerdPreflightAcceptsRegistryOllamaGptOss(t *testing.T) {
	noKeys(t)
	clearSeatEnv(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv(models.EnvInline, `{"big": {"provider":"ollama","model":"gpt-oss:120b","endpoint":"http://127.0.0.1:11434"}}`)

	s := &seatPtrs{mutant: "big", writer: "big", critic: "off", shadow: "off"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), io.Discard)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	roles, err := resolveAuditRoles(localAuditInput{
		cmdName: "test", mutantModel: s.mutant, writerModel: s.writer, criticModel: s.critic, shadowModel: s.shadow,
		seats: set,
	}, io.Discard)
	if err != nil {
		t.Fatalf("a registry-declared local gpt-oss herd must be accepted with no OpenAI key; got: %v", err)
	}
	if roles.chatterFor(advpool.RoleTestWriter) == nil {
		t.Fatal("the writer seat has no chatter")
	}
	// The same herd through doctor's own entry, which asks the same preflight.
	for _, r := range checkHerd(s.mutant, s.writer, s.critic, s.shadow, "", set) {
		if !r.ok {
			t.Errorf("doctor refused what certify accepts: %s :: %s", r.name, r.detail)
		}
	}
}

// TestChatterRoutesEachRoleByItsResolvedSeat: one run, two wires. The writer is
// declared on an ollama daemon and the critic on an OpenAI-compatible server
// (vllm); each fake must receive exactly its own role's call, over its own wire.
func TestChatterRoutesEachRoleByItsResolvedSeat(t *testing.T) {
	noKeys(t)
	clearSeatEnv(t)
	var ollamaHits, chatHits int32
	var chatPath string
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ollamaHits, 1)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
	defer ollamaSrv.Close()
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&chatHits, 1)
		chatPath = r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer chatSrv.Close()
	t.Setenv(models.EnvInline, `{
	  "scribe": {"provider":"ollama","model":"gpt-oss:120b","endpoint":"`+ollamaSrv.URL+`"},
	  "judge":  {"provider":"vllm","model":"qwen3-coder","endpoint":"`+chatSrv.URL+`/v1"}
	}`)

	s := &seatPtrs{writer: "scribe", critic: "judge"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	chatterFor, err := localChatterFor(set, nil, nil)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}
	msg := []agentworker.Message{{Role: "user", Content: "hi"}}
	if _, err := chatterFor(advpool.RoleTestWriter).Chat(msg, nil); err != nil {
		t.Fatalf("writer chat: %v", err)
	}
	if got := atomic.LoadInt32(&ollamaHits); got != 1 || atomic.LoadInt32(&chatHits) != 0 {
		t.Fatalf("after the writer's call: ollama=%d openai-chat=%d, want 1 and 0", got, atomic.LoadInt32(&chatHits))
	}
	if _, err := chatterFor(advpool.RoleTestCritic).Chat(msg, nil); err != nil {
		t.Fatalf("critic chat: %v", err)
	}
	if atomic.LoadInt32(&ollamaHits) != 1 || atomic.LoadInt32(&chatHits) != 1 {
		t.Fatalf("after the critic's call: ollama=%d openai-chat=%d, want 1 and 1", atomic.LoadInt32(&ollamaHits), atomic.LoadInt32(&chatHits))
	}
	if chatPath != "/v1/chat/completions" {
		t.Errorf("the openai-chat endpoint is used verbatim, so the path is %q, want /v1/chat/completions", chatPath)
	}
	if chatterFor(advpool.RoleMutantGenerator) != nil {
		t.Error("a role with no seat must get no chatter, not a fallback backend")
	}
}

// A role with a model but no resolved seat is a caller that skipped the
// resolver. It is refused up front, naming the role: the alternative is a
// chatter-less task failing mid-run.
func TestResolveAuditRolesRefusesAnUnresolvedSeat(t *testing.T) {
	noKeys(t)
	_, err := resolveAuditRoles(localAuditInput{
		writerModel: "gemini-3.6-flash", mutantModel: "gemini-3.6-flash", criticModel: "off", shadowModel: "off",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "never resolved") || !strings.Contains(err.Error(), advpool.RoleMutantGenerator) {
		t.Fatalf("want a refusal naming the unresolved seat, got %v", err)
	}
}

// A --local-endpoint placement is a declared source for a bare local name: an
// operator with no OLLAMA_URL and two daemons, one per GPU, names each seat's
// daemon on the command line and nothing else.
func TestLocalEndpointDeclaresASourceForABareName(t *testing.T) {
	noKeys(t)
	clearSeatEnv(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
	defer srv.Close()
	s := &seatPtrs{writer: "qwen3.5:9b"}
	var errb bytes.Buffer
	set, err := resolveSeatsPlaced(context.Background(), "test", t.TempDir(), s.flags(),
		map[string]string{advpool.RoleTestWriter: srv.URL}, &errb)
	if err != nil {
		t.Fatalf("a placed bare name must resolve with no OLLAMA_URL: %v", err)
	}
	if !strings.Contains(errb.String(), "writer-model:") || !strings.Contains(errb.String(), srv.URL) {
		t.Errorf("the disclosure must show the placed daemon:\n%s", errb.String())
	}
	chatterFor, err := localChatterFor(set, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chatterFor(advpool.RoleTestWriter).Chat([]agentworker.Message{{Role: "user", Content: "hi"}}, nil); err != nil || hits != 1 {
		t.Fatalf("chat err=%v hits=%d, want the placed daemon to be called once", err, hits)
	}
	// Without the placement the same name is refused, naming the sources.
	if _, err := resolveSeats(context.Background(), "test", t.TempDir(), (&seatPtrs{writer: "qwen3.5:9b"}).flags(), io.Discard); err == nil {
		t.Fatal("a bare name with no declared source and no placement must be refused")
	}
}

// A placement on an agent seat or a cloud seat is refused, never ignored.
func TestLocalEndpointRefusalsNameTheSeat(t *testing.T) {
	noKeys(t)
	clearSeatEnv(t)
	t.Setenv("GEMINI_API_KEY", "k")
	_, err := resolveSeatsOf(t, localAuditInput{writerModel: "google/gemini-3.7-flash"},
		map[string]string{advpool.RoleTestWriter: "http://localhost:11436"})
	if err == nil || !strings.Contains(err.Error(), "CLOUD model") || !strings.Contains(err.Error(), "gemini-3.7-flash") {
		t.Fatalf("want the cloud refusal naming the model, got %v", err)
	}
}
