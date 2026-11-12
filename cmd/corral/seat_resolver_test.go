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

// declareLocalSeats gives the bare seat names a test types a declared source,
// as every seat now needs one: OLLAMA_URL naming a daemon whose listing serves
// exactly these models. The URL is never dialled (listOllama is replaced).
func declareLocalSeats(t *testing.T, names ...string) {
	t.Helper()
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://ollama.test.invalid:11434")
	fakeOllama(t, names...)
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

// A VENDOR pin (MODEL_BACKEND=anthropic, claude, gemini, or openai without
// OPENAI_BASE_URL) is not a gateway: it names one vendor, not an endpoint that
// fronts many, so a seat for another vendor still goes to ITS vendor by name
// (its vendor/model form) rather than being forced onto the pinned one. Only a gateway pin is never
// re-routed. (Ported from the TestBaseVendorMapsBackendLabels table, which
// pinned this behaviour through baseVendor, a helper that no longer exists.)
func TestResolveVendorPinIsNotAGateway(t *testing.T) {
	for _, pin := range []string{"anthropic", "claude", "gemini", "openai"} {
		t.Run(pin, func(t *testing.T) {
			clearSeatEnv(t)
			t.Setenv("MODEL_BACKEND", pin)
			s := &seatPtrs{writer: "anthropic/claude-sonnet-5", mutant: "google/gemini-3.6-flash"}
			set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
			if err != nil {
				t.Fatalf("resolveSeats: %v", err)
			}
			w, _ := set.byRole(advpool.RoleTestWriter)
			m, _ := set.byRole(advpool.RoleMutantGenerator)
			if w.Provider != "anthropic" || m.Provider != "google" || w.DeclaredBy != agentbackend.DeclaredVendor || m.DeclaredBy != agentbackend.DeclaredVendor {
				t.Errorf("under MODEL_BACKEND=%s: writer %+v, mutant %+v, want each routed to the vendor its vendor/model form names", pin, w, m)
			}
		})
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

// A name carrying an ollama-style tag (gpt-oss:20b) is a local model's name,
// not an OpenAI API model, even though it starts with gpt-. With no declared
// source it must be refused naming the fixes, never routed to OpenAI by its
// prefix — the acceptance run on 2026-11-12 found it asking for OPENAI_API_KEY.
func TestResolveTaggedGptOssWithNoSourceRefuses(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	s := &seatPtrs{writer: "gpt-oss:20b"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil {
		t.Fatalf("resolved %+v; a tagged gpt-oss name with no declared source must refuse", set)
	}
	for _, w := range []string{".corral/models.json", "vendor/model", "OLLAMA_URL", "MODEL_BACKEND"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

// No vendor is guessed from a model's name (founder ruling 2026-11-12). A bare
// claude-sonnet-5 with the Anthropic key exported is still refused: the guess
// would decide where the audited source and a credential go.
func TestResolveBareCloudNameRefuses(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	s := &seatPtrs{writer: "claude-sonnet-5"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil {
		t.Fatalf("resolved %+v; a bare cloud name must refuse, never be routed by its prefix", set)
	}
	for _, w := range []string{".corral/models.json", "vendor/model", "OLLAMA_URL", "MODEL_BACKEND"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

// The explicit vendor/model form names the vendor, and the seat value is
// rewritten to the bare model (as an alias is) so cache keys, --prior matching
// and the ledger's model fields keep today's spelling.
func TestResolveExplicitVendorIsHosted(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "anthropic/claude-sonnet-5", critic: "google/gemini-3.8-flash", mutant: "openai/gpt-6"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	for _, c := range []struct{ role, provider, model, val string }{
		{advpool.RoleTestWriter, "anthropic", "claude-sonnet-5", s.writer},
		{advpool.RoleTestCritic, "google", "gemini-3.8-flash", s.critic},
		{advpool.RoleMutantGenerator, "openai", "gpt-6", s.mutant},
	} {
		rs, _ := set.byRole(c.role)
		if rs.Provider != c.provider || rs.Spec != c.model || rs.Wire != models.WireHosted || rs.Endpoint != "" || rs.DeclaredBy != agentbackend.DeclaredVendor {
			t.Errorf("%s resolved = %+v, want %s hosted, spec %s, declared by vendor/model", c.role, rs, c.provider, c.model)
		}
		if c.val != c.model {
			t.Errorf("%s seat value = %q, want it rewritten to %q", c.role, c.val, c.model)
		}
	}
}

// The vendor set is closed: a slash in an ollama name (hf.co/…, library/…) is
// not a vendor, so such a name is served by the daemon that lists it.
func TestResolveSlashedLocalNameIsDiscovered(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("OLLAMA_URL", "http://daemon:11434")
	fakeOllama(t, "hf.co/x/y:q4")
	s := &seatPtrs{writer: "hf.co/x/y:q4"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Provider != "ollama" || rs.DeclaredBy != agentbackend.DeclaredDiscovered || rs.Spec != "hf.co/x/y:q4" {
		t.Errorf("resolved = %+v, want ollama, discovered, spec unchanged", rs)
	}
}

// A pinned gateway receives the seat verbatim, vendor prefix included: the
// gateway, not corral, decides what anthropic/claude-sonnet-5 means.
func TestResolvePinnedGatewayTakesTheVendorFormVerbatim(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openrouter")
	s := &seatPtrs{writer: "anthropic/claude-sonnet-5"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.Provider != "openrouter" || rs.DeclaredBy != agentbackend.DeclaredPinned || rs.Spec != "anthropic/claude-sonnet-5" {
		t.Errorf("resolved = %+v, want openrouter, pinned, spec verbatim", rs)
	}
	if s.writer != "anthropic/claude-sonnet-5" {
		t.Errorf("seat value = %q, want it unchanged under a gateway", s.writer)
	}
}

// --local-endpoint places any name that is not the explicit vendor form. An
// explicit vendor seat stays hosted, so a placement on it is refused as a
// placement on a cloud seat rather than quietly sending it to the daemon.
func TestResolvePlacementDoesNotTakeAnExplicitVendor(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "openai/gpt-6"}
	set, err := resolveSeatsPlaced(context.Background(), "test", t.TempDir(), s.flags(),
		map[string]string{advpool.RoleTestWriter: "http://localhost:11436"}, nil)
	if err == nil {
		t.Fatalf("resolved %+v; an explicit vendor seat must not be placed on a local daemon", set)
	}
	if !strings.Contains(err.Error(), "CLOUD model") || !strings.Contains(err.Error(), "openai") {
		t.Errorf("err = %v, want the cloud-seat placement refusal naming openai", err)
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

func TestResolveVendorFormNeedsNoListing(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "google/gemini-3.8-flash"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.Provider != "google" || rs.DeclaredBy != agentbackend.DeclaredVendor || rs.Wire != models.WireHosted {
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
// by the later steps (its vendor/model form here), per the T4 ruling.
func TestResolvePinnedOpenAIIsGatewayOnlyWithBaseURL(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openai")
	s := &seatPtrs{writer: "google/gemini-3.8-flash"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Provider != "google" || rs.DeclaredBy != agentbackend.DeclaredVendor {
		t.Errorf("without OPENAI_BASE_URL: %+v, want google from its vendor/model form", rs)
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

// A pinned openrouter gateway reads OpenRouter's key and nothing else.
// Substituting OPENAI_API_KEY would send an OpenAI secret to openrouter.ai.
func TestPinnedOpenRouterNeverSendsTheOpenAIKey(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	s := &seatPtrs{writer: "claude-sonnet-5"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	rs, _ := set.byRole(advpool.RoleTestWriter)
	if rs.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q, want OPENROUTER_API_KEY only", rs.KeyEnv)
	}
	if _, err := agentbackend.Build(rs, agentbackend.SeatOptions{}); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("Build with only OPENAI_API_KEY set: err = %v, want a refusal naming OPENROUTER_API_KEY", err)
	}
}

// Where the command takes --local-endpoint, the no-source refusal names it as
// the fourth way to declare where a bare local name runs.
func TestNoSourceRefusalNamesLocalEndpointWhereItExists(t *testing.T) {
	clearSeatEnv(t)
	s := &seatPtrs{writer: "qwen3.6:35b-a3b"}
	_, err := resolveSeatsPlaced(context.Background(), "test", t.TempDir(), s.flags(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "--local-endpoint") {
		t.Errorf("certify-style refusal = %v, want it to name --local-endpoint", err)
	}
	_, err = resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err == nil || strings.Contains(err.Error(), "--local-endpoint") {
		t.Errorf("refusal for a command without --local-endpoint = %v, must not offer it", err)
	}
}

// OPENAI_BASE_URL names an OpenAI-compatible endpoint (often a company proxy);
// it is never the OpenRouter gateway's URL. Reading it there would send the
// OpenRouter key to that proxy.
func TestPinnedOpenRouterIgnoresOpenAIBaseURL(t *testing.T) {
	clearSeatEnv(t)
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("OPENAI_BASE_URL", "http://company-proxy:8080/v1")
	s := &seatPtrs{writer: "claude-sonnet-5"}
	set, err := resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Endpoint != "https://openrouter.ai/api/v1" {
		t.Errorf("Endpoint = %q, want OpenRouter's own URL, never OPENAI_BASE_URL", rs.Endpoint)
	}

	t.Setenv("OPENROUTER_BASE_URL", "http://or-mirror:9000/api/v1")
	set, err = resolveSeats(context.Background(), "test", t.TempDir(), s.flags(), nil)
	if err != nil {
		t.Fatalf("resolveSeats: %v", err)
	}
	if rs, _ := set.byRole(advpool.RoleTestWriter); rs.Endpoint != "http://or-mirror:9000/api/v1" {
		t.Errorf("Endpoint = %q, want OPENROUTER_BASE_URL", rs.Endpoint)
	}
}
