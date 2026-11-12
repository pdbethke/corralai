// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/models"
)

// clearSeatEnv removes every ambient source a seat could resolve from, so a
// test names exactly the sources it is about.
func clearSeatEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MODEL_BACKEND", "OLLAMA_URL", "OPENAI_BASE_URL", "OPENROUTER_BASE_URL", models.EnvInline, models.EnvFile} {
		t.Setenv(k, "")
	}
}

func fakeOllama(t *testing.T, served ...string) *int32 {
	t.Helper()
	var calls int32
	prev := listOllama
	listOllama = func(ctx context.Context, hc *http.Client, base string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return served, nil
	}
	t.Cleanup(func() { listOllama = prev })
	return &calls
}

func TestResolveBareGptOssWithOllamaListingIsLocal(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://daemon:11434")
	fakeOllama(t, "gpt-oss:120b")
	s := &seatPtrs{writer: "gpt-oss:120b"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, ok := set.byRole(advpool.RoleTestWriter)
	if !ok {
		t.Fatal("writer seat not resolved")
	}
	if rs.Provider != "ollama" || rs.Wire != models.WireOllama || rs.Endpoint != "http://daemon:11434" || rs.DeclaredBy != agentbackend.DeclaredDiscovered {
		t.Errorf("resolved = %+v, want local ollama, discovered, at the daemon", rs)
	}
}

func TestResolvePinnedGatewayIsNeverRerouted(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openrouter")
	s := &seatPtrs{writer: "claude-sonnet-5"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.Provider != "openrouter" || rs.DeclaredBy != agentbackend.DeclaredPinned {
		t.Errorf("resolved = %+v, want openrouter, pinned", rs)
	}
}

func TestResolveRegistryAliasNeedsNoEnv(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv(models.EnvInline, `{"oss":{"provider":"ollama","model":"gpt-oss:120b","endpoint":"http://h:11434"}}`)
	s := &seatPtrs{writer: "oss"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.Endpoint != "http://h:11434" || rs.DeclaredBy != agentbackend.DeclaredRegistry {
		t.Errorf("resolved = %+v", rs)
	}
	if s.writer != "gpt-oss:120b" {
		t.Errorf("seat value = %q, want the concrete model", s.writer)
	}
}

func TestResolveBareLocalWithNoSourceRefuses(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "qwen3.6:35b-a3b"}
	_, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil {
		t.Fatal("a bare local name with no declared source must refuse")
	}
	for _, w := range []string{".corral/models.json", "OLLAMA_URL", "MODEL_BACKEND"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

func TestResolveRegistryVllmIsOpenAIChat(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv(models.EnvInline, `{"v":{"provider":"vllm","model":"m","endpoint":"http://h:8000/v1"}}`)
	s := &seatPtrs{critic: "v"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestCritic)
	if rs.Wire != models.WireOpenAIChat || rs.Endpoint != "http://h:8000/v1" {
		t.Errorf("resolved = %+v", rs)
	}
}

func TestResolveCloudNameWithoutListingIsByName(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "gemini-3.8-flash"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.Provider != "google" || rs.DeclaredBy != agentbackend.DeclaredName || rs.Wire != models.WireHosted {
		t.Errorf("resolved = %+v", rs)
	}
}

func TestResolveStrictMissStillRefuses(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv(models.EnvInline, `{"strict":true,"fast":{"provider":"google","model":"gemini-3.6-flash"}}`)
	s := &seatPtrs{writer: "fats"}
	_, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil || !strings.Contains(err.Error(), "fats") {
		t.Fatalf("err = %v, want the strict unknown-alias refusal", err)
	}
}

func TestResolveListsOllamaOncePerInvocation(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://daemon:11434")
	calls := fakeOllama(t, "a", "b", "c")
	s := &seatPtrs{mutant: "a", writer: "b", critic: "c"}
	var errb bytes.Buffer
	if _, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), &errb); err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("ollama listed %d times, want 1", n)
	}
	if strings.Count(errb.String(), "discovered") != 3 {
		t.Errorf("disclosure = %q, want one line per seat", errb.String())
	}
}

// stripModelsDisclosure removes the seat-resolution disclosure block (a
// "  models:" header and its indented seat lines) so a test that asserts "no
// other stderr output" is not broken by the disclosure, which now prints
// whenever any seat resolved.
func stripModelsDisclosure(s string) string {
	var out []string
	in := false
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "  models:"):
			in = true
		case in && strings.HasPrefix(l, "    "):
		default:
			in = false
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// A declared daemon that cannot be listed refuses the seat loudly. Falling
// through would let a bare local name like gpt-oss:120b be routed by what its
// name resembles (OpenAI, and spend), and the refusal would wrongly tell the
// operator to set the OLLAMA_URL they already set.
func TestResolveUnlistableOllamaRefusesLoudly(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://down:11434")
	prev := listOllama
	listOllama = func(context.Context, *http.Client, string) ([]string, error) {
		return nil, errors.New("connection refused")
	}
	t.Cleanup(func() { listOllama = prev })
	s := &seatPtrs{writer: "gpt-oss:120b"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil {
		t.Fatalf("resolved %+v; want a refusal naming the unlistable daemon", set)
	}
	for _, want := range []string{"--writer-model", "could not list models at http://down:11434", "connection refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// The pinned ollama gateway needs its daemon named: no default endpoint.
func TestResolvePinnedOllamaWithoutURLRefuses(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "ollama")
	s := &seatPtrs{writer: "qwen3.6:35b-a3b"}
	if _, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil); err == nil || !strings.Contains(err.Error(), "OLLAMA_URL is not set") {
		t.Errorf("err = %v; want a refusal saying OLLAMA_URL is not set", err)
	}
}

// openai is a gateway only with OPENAI_BASE_URL; without it a seat is routed
// as today (discovery/name), per the T4 ruling.
func TestResolvePinnedOpenAIIsGatewayOnlyWithBaseURL(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openai")
	s := &seatPtrs{writer: "gemini-3.8-flash"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Provider != "google" || rs.DeclaredBy != agentbackend.DeclaredName {
		t.Errorf("without OPENAI_BASE_URL: %+v, want google by name", rs)
	}

	t.Setenv("OPENAI_BASE_URL", "http://proxy:8080/v1")
	set, err = resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Provider != "openai" || rs.Endpoint != "http://proxy:8080/v1" || rs.KeyEnv != "OPENAI_API_KEY" {
		t.Errorf("with OPENAI_BASE_URL: %+v, want the openai gateway at the proxy", rs)
	}
}
