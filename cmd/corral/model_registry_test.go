// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
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

// seatPtrs is the six seat values a certify run parses, as the resolver sees
// them.
type seatPtrs struct{ derive, mutant, writer, critic, shadow, shadowWriter string }

func (s *seatPtrs) flags() []seatFlag {
	return certifySeats(&s.derive, &s.mutant, &s.writer, &s.critic, &s.shadow, &s.shadowWriter)
}

const registryJSON = `{
  "fast":   {"provider": "google",    "model": "gemini-3.6-flash"},
  "strong": {"provider": "google",    "model": "gemini-3.7-flash"},
  "scribe": {"provider": "anthropic", "model": "claude-sonnet-5"},
  "edge":   {"provider": "ollama",    "model": "qwen3.5:9b-q8_0", "endpoint": "http://127.0.0.1:11434"}
}`

// EVERY seat flag resolves an alias — not just the two anyone remembers. A
// seat missed by the resolver would send the alias itself to a provider as a
// model name, and the 404 would arrive hours into a paid run.
func TestRegistryAliasResolvesAtEverySeatFlag(t *testing.T) {
	t.Setenv(models.EnvInline, registryJSON)
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{derive: "fast", mutant: "fast", writer: "scribe", critic: "strong", shadow: "fast", shadowWriter: "strong"}
	var errb bytes.Buffer
	if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb); err != nil {
		t.Fatalf("resolveSeatRegistry: %v", err)
	}
	want := seatPtrs{
		derive: "gemini-3.6-flash", mutant: "gemini-3.6-flash",
		writer: "claude-sonnet-5", critic: "gemini-3.7-flash",
		shadow: "gemini-3.6-flash", shadowWriter: "gemini-3.7-flash",
	}
	if *s != want {
		t.Errorf("resolved seats = %+v, want %+v", *s, want)
	}
	// The resolution is DISCLOSED: an operator must be able to read which
	// concrete model each alias became without re-deriving it from a file.
	for _, w := range []string{"writer-model", "claude-sonnet-5", "anthropic", "critic-model", "gemini-3.7-flash"} {
		if !strings.Contains(errb.String(), w) {
			t.Errorf("disclosure %q does not name %q", errb.String(), w)
		}
	}
}

// A concrete model name still works EXACTLY as it does today, registry or no
// registry. This is the additive guarantee: nobody's command line changes.
func TestConcreteModelNameUnchangedByRegistry(t *testing.T) {
	for _, tc := range []struct{ name, inline string }{
		{"no registry", ""},
		{"registry declared", registryJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(models.EnvInline, tc.inline)
			t.Setenv(models.EnvFile, "")
			s := &seatPtrs{mutant: "gemini-3.6-flash", writer: "claude-sonnet-5", critic: "off"}
			var errb bytes.Buffer
			if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb); err != nil {
				t.Fatalf("resolveSeatRegistry: %v", err)
			}
			want := seatPtrs{mutant: "gemini-3.6-flash", writer: "claude-sonnet-5", critic: "off"}
			if *s != want {
				t.Errorf("seats = %+v, want them untouched %+v", *s, want)
			}
		})
	}
}

// With no registry at all the resolver is SILENT — a repo that has never heard
// of this feature must behave byte for byte as it did before.
func TestNoRegistryEmitsNothing(t *testing.T) {
	t.Setenv(models.EnvInline, "")
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{mutant: "gemini-3.6-flash", writer: "claude-sonnet-5"}
	var errb bytes.Buffer
	ep, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb)
	if err != nil {
		t.Fatalf("resolveSeatRegistry: %v", err)
	}
	if errb.Len() != 0 {
		t.Errorf("stderr = %q, want silence with no registry", errb.String())
	}
	if len(ep.localEndpoints()) != 0 || len(ep.seatProviders()) != 0 {
		t.Errorf("resolution = %+v, want nothing with no registry", ep)
	}
}

// A seat with NO value stays empty. The registry declares what MAY run; it is
// not a default, and an unnamed seat must still refuse the run downstream.
func TestRegistryNeverFillsAnUnnamedSeat(t *testing.T) {
	t.Setenv(models.EnvInline, registryJSON)
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{}
	if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errDiscard{}); err != nil {
		t.Fatalf("resolveSeatRegistry: %v", err)
	}
	if (*s != seatPtrs{}) {
		t.Fatalf("seats = %+v, want every one still empty — a registry is not a default", *s)
	}
	// And the run still refuses, naming the seats, exactly as before.
	if err := herdNotConfiguredErr("corral certify --repo", s.writer, s.mutant, "off"); err == nil {
		t.Fatal("unnamed seats = nil error, want the no-default-models refusal")
	}
}

// RENAMING AN ALIAS MUST NOT CHANGE A CACHE KEY. The key records the herd; if
// an alias reached it, renaming "fast" to "quick" would invalidate every
// cached verdict for a change that altered nothing, and two projects using the
// same alias for different models would collide.
func TestAliasRenameDoesNotChangeTheCacheKey(t *testing.T) {
	keyFor := func(t *testing.T, inline, writerAlias, mutantAlias string) string {
		t.Helper()
		t.Setenv(models.EnvInline, inline)
		t.Setenv(models.EnvFile, "")
		s := &seatPtrs{writer: writerAlias, mutant: mutantAlias, critic: "off"}
		if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errDiscard{}); err != nil {
			t.Fatalf("resolveSeatRegistry: %v", err)
		}
		w, m, c, sh, sw := resolveRoleModels(localAuditInput{
			writerModel: s.writer, mutantModel: s.mutant, criticModel: s.critic,
			shadowModel: s.shadow, shadowWriterModel: s.shadowWriter,
		})
		return modelSetKey(w, m, c, sh, sw)
	}
	a := keyFor(t, `{"scribe": {"provider":"anthropic","model":"claude-sonnet-5"}, "fast": {"provider":"google","model":"gemini-3.6-flash"}}`, "scribe", "fast")
	b := keyFor(t, `{"penman": {"provider":"anthropic","model":"claude-sonnet-5"}, "quick": {"provider":"google","model":"gemini-3.6-flash"}}`, "penman", "quick")
	concrete := keyFor(t, "", "claude-sonnet-5", "gemini-3.6-flash")
	if a != b {
		t.Errorf("renaming aliases changed the cache key:\n  %s\n  %s", a, b)
	}
	if a != concrete {
		t.Errorf("an alias keyed differently from the concrete model it names:\n  alias:    %s\n  concrete: %s", a, concrete)
	}
	if strings.Contains(a, "scribe") || strings.Contains(a, "fast") {
		t.Errorf("cache key carries an alias, not the resolved model: %s", a)
	}
}

// A run that names an alias RECORDS the concrete model. The alias is a label;
// the verdict, ledger and signed statement must name what actually ran.
func TestAliasRunRecordsTheConcreteModel(t *testing.T) {
	t.Setenv(models.EnvInline, registryJSON)
	t.Setenv(models.EnvFile, "")
	var out, errb bytes.Buffer
	// Two aliases for models that are DIFFERENT names but resolve to the same
	// concrete model would be the interesting case; here the point is simpler:
	// the decorrelation refusal must quote the CONCRETE model, proving the
	// resolved value — not the alias — is what reached the run.
	code := runCertifyRepo([]string{
		"--repo", t.TempDir(),
		"--writer-model", "strong",
		"--critic-model", "strong",
		"--mutant-model", "fast",
		"--substrate", "workspace",
	}, &out, &errb)
	if code == 0 {
		t.Fatalf("exit = 0, want a refusal (critic == writer once both aliases resolve)\nstderr=%s", errb.String())
	}
	if !strings.Contains(errb.String(), "gemini-3.7-flash") {
		t.Errorf("refusal names the alias rather than the resolved model: %s", errb.String())
	}
}

// A malformed registry refuses the run with exit 2, before anything is spent.
func TestBadRegistryRefusesTheRun(t *testing.T) {
	t.Setenv(models.EnvInline, `{"default": {"provider":"google","model":"gemini-3.6-flash"}}`)
	t.Setenv(models.EnvFile, "")
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--writer-model", "gemini-3.6-flash",
		"--mutant-model", "gemini-3.6-flash", "--critic-model", "off", "--dry-run",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for an alias named \"default\"\nstderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no default models") {
		t.Errorf("refusal does not explain the rule: %s", errb.String())
	}
}

// A local registry entry must feed the SAME mechanism --local-endpoint uses —
// not a second path. Proven end to end: the seat's request arrives at the
// daemon the registry named, not at the process-wide OLLAMA_URL.
func TestLocalRegistryEntryPlacesItsSeatOnItsDaemon(t *testing.T) {
	var declaredHits, baseHits int32
	reply := `{"message":{"role":"assistant","content":"ok"},"done":true}`
	declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&declaredHits, 1)
		_, _ = io.WriteString(w, reply)
	}))
	defer declared.Close()
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		_, _ = io.WriteString(w, reply)
	}))
	defer base.Close()

	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("OLLAMA_URL", base.URL)
	t.Setenv(models.EnvInline, `{"edge": {"provider":"ollama","model":"qwen3.5:9b-q8_0","endpoint":"`+declared.URL+`"}}`)
	t.Setenv(models.EnvFile, "")

	s := &seatPtrs{writer: "edge", mutant: "gemma4:12b"}
	res, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errDiscard{})
	if err != nil {
		t.Fatalf("resolveSeatRegistry: %v", err)
	}
	endpoints := res.localEndpoints()
	if got := endpoints[advpool.RoleTestWriter]; got != declared.URL {
		t.Fatalf("registry endpoint for the writer = %q, want %q", got, declared.URL)
	}
	chatterFor, err := localChatterFor(
		advpool.RoleAssignment{advpool.RoleTestWriter: s.writer, advpool.RoleMutantGenerator: s.mutant},
		nil,
		mergeLocalEndpoints(nil, endpoints),
	)
	if err != nil {
		t.Fatalf("localChatterFor: %v", err)
	}
	msg := []agentworker.Message{{Role: "user", Content: "hi"}}
	if _, err := chatterFor(advpool.RoleTestWriter).Chat(msg, nil); err != nil {
		t.Fatalf("writer chat: %v", err)
	}
	if _, err := chatterFor(advpool.RoleMutantGenerator).Chat(msg, nil); err != nil {
		t.Fatalf("generator chat: %v", err)
	}
	if got := atomic.LoadInt32(&declaredHits); got != 1 {
		t.Errorf("declared daemon got %d request(s), want 1", got)
	}
	if got := atomic.LoadInt32(&baseHits); got != 1 {
		t.Errorf("OLLAMA_URL got %d request(s), want 1 (the undeclared seat)", got)
	}
}

// An explicit --local-endpoint beats the declaration: the flag names one
// seat's daemon for THIS run against a declaration covering every run.
func TestExplicitLocalEndpointBeatsTheRegistry(t *testing.T) {
	got := mergeLocalEndpoints(
		map[string]string{advpool.RoleTestWriter: "http://flag:1"},
		map[string]string{advpool.RoleTestWriter: "http://registry:2", advpool.RoleMutantGenerator: "http://registry:3"},
	)
	if got[advpool.RoleTestWriter] != "http://flag:1" {
		t.Errorf("writer endpoint = %q, want the flag to win", got[advpool.RoleTestWriter])
	}
	if got[advpool.RoleMutantGenerator] != "http://registry:3" {
		t.Errorf("mutant endpoint = %q, want the declared one", got[advpool.RoleMutantGenerator])
	}
}

// errDiscard is an io.Writer that swallows disclosure output in tests that
// assert on values rather than on words.
type errDiscard struct{}

func (errDiscard) Write(p []byte) (int, error) { return len(p), nil }

// The decorrelation guard is UNCHANGED — two distinct model names from one
// vendor still pass, exactly as they did before providers were data. What is
// new is that the run SAYS so. Behaviour: none. Disclosure: better.
func TestSameVendorSeatsAreDisclosedNotRefused(t *testing.T) {
	t.Setenv(models.EnvInline, "")
	t.Setenv(models.EnvFile, "")
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("GEMINI_API_KEY", "k")
	var errb bytes.Buffer
	if _, err := resolveAuditRoles(localAuditInput{
		cmdName:     "corral certify --repo",
		writerModel: "gemini-3.7-flash", mutantModel: "gemini-3.6-flash",
		criticModel: "gemini-3.6-flash",
	}, &errb); err != nil {
		t.Fatalf("two distinct models from one vendor must still be ACCEPTED: %v", err)
	}
	for _, w := range []string{"decorrelation", "SAME provider", "google"} {
		if !strings.Contains(errb.String(), w) {
			t.Errorf("disclosure %q does not name %q", errb.String(), w)
		}
	}
}

// The DECLARED provider beats the name-based guess: that is what having
// provider as a field buys. Two models whose names imply nothing still share a
// vendor when the registry says they do.
func TestDeclaredProviderDrivesTheVendorDisclosure(t *testing.T) {
	providers := map[string]string{
		advpool.RoleTestWriter: "openrouter",
		advpool.RoleTestCritic: "openrouter",
	}
	if got := sharedSeatProvider(providers, "some/writer", "some/critic"); got != "openrouter" {
		t.Errorf("sharedSeatProvider = %q, want openrouter", got)
	}
	// Different providers, and an unknowable one, are both silent.
	providers[advpool.RoleTestCritic] = "google"
	if got := sharedSeatProvider(providers, "some/writer", "some/critic"); got != "" {
		t.Errorf("sharedSeatProvider across vendors = %q, want \"\"", got)
	}
	if got := sharedSeatProvider(nil, "qwen3.5:9b", "gemma4:12b"); got != "" {
		t.Errorf("two unplaceable local names = %q, want \"\" (never a guessed shared vendor)", got)
	}
	if got := sharedSeatProvider(nil, "gemini-3.6-flash", "off"); got != "" {
		t.Errorf("a disabled critic = %q, want \"\"", got)
	}
}

const strictRegistryJSON = `{
  "strict": true,
  "fast":   {"provider": "google",    "model": "gemini-3.6-flash"},
  "scribe": {"provider": "anthropic", "model": "claude-sonnet-5"}
}`

// THE TYPO. Under a strict registry a value that is not a declared alias is
// refused, exit 2, before anything is spent. Without this, `--writer-model
// nope` fell through as a "concrete model name", was inferred as a local
// daemon, and died at the seat hours into a paid run — the exact stale- or
// mistyped-reference failure this registry exists to remove.
func TestStrictRegistryRefusesAnUnknownAlias(t *testing.T) {
	t.Setenv(models.EnvInline, strictRegistryJSON)
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{writer: "nope", mutant: "fast"}
	var errb bytes.Buffer
	_, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb)
	if err == nil {
		t.Fatal("strict registry accepted an unknown value, want a refusal")
	}
	for _, w := range []string{"nope", "fast", "scribe", "--writer-model"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not name %q", err, w)
		}
	}

	// End to end: exit 2, not 1, and not a run.
	var out, cmdErr bytes.Buffer
	code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--writer-model", "nope", "--mutant-model", "fast",
		"--critic-model", "off", "--dry-run",
	}, &out, &cmdErr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr=%s", code, cmdErr.String())
	}
	if !strings.Contains(cmdErr.String(), "scribe") {
		t.Errorf("refusal does not list the known aliases: %s", cmdErr.String())
	}
}

// Strict changes only what a NAMED seat may say. A declared alias resolves, a
// deliberately disabled critic is still "off", and an unnamed seat still hits
// the no-default-models refusal rather than being filled.
func TestStrictRegistryAllowsAliasesAndOff(t *testing.T) {
	t.Setenv(models.EnvInline, strictRegistryJSON)
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{writer: "scribe", mutant: "fast", critic: "off"}
	var errb bytes.Buffer
	if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb); err != nil {
		t.Fatalf("strict registry refused declared aliases: %v", err)
	}
	want := seatPtrs{writer: "claude-sonnet-5", mutant: "gemini-3.6-flash", critic: "off"}
	if *s != want {
		t.Errorf("seats = %+v, want %+v", *s, want)
	}
	if !strings.Contains(errb.String(), "strict") {
		t.Errorf("disclosure does not say which mode is in force: %q", errb.String())
	}

	empty := &seatPtrs{}
	if _, err := resolveSeatRegistry("test", t.TempDir(), empty.flags(), &errDiscard{}); err != nil {
		t.Fatalf("strict registry refused an unnamed seat at resolution: %v", err)
	}
	if (*empty != seatPtrs{}) {
		t.Fatalf("seats = %+v, want them still empty — strict is not a default", *empty)
	}
	if err := herdNotConfiguredErr("corral certify --repo", empty.writer, empty.mutant, "off"); err == nil {
		t.Fatal("unnamed seats under a strict registry = nil error, want the no-default-models refusal")
	}
}

// NON-strict is unchanged: an unknown value is still the concrete model name
// the flags have always accepted, and it is still disclosed as an inference.
func TestNonStrictRegistryStillAcceptsAConcreteName(t *testing.T) {
	t.Setenv(models.EnvInline, registryJSON)
	t.Setenv(models.EnvFile, "")
	s := &seatPtrs{writer: "claude-sonnet-5", mutant: "gemini-3.6-flash"}
	var errb bytes.Buffer
	if _, err := resolveSeatRegistry("test", t.TempDir(), s.flags(), &errb); err != nil {
		t.Fatalf("non-strict registry refused a concrete model name: %v", err)
	}
	want := seatPtrs{writer: "claude-sonnet-5", mutant: "gemini-3.6-flash"}
	if *s != want {
		t.Errorf("seats = %+v, want them untouched %+v", *s, want)
	}
	if !strings.Contains(errb.String(), "concrete name") {
		t.Errorf("disclosure %q does not disclose the inference", errb.String())
	}
	if strings.Contains(errb.String(), "strict") {
		t.Errorf("a non-strict registry announced strict mode: %q", errb.String())
	}
}
