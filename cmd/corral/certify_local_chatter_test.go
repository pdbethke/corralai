// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// testShadowModel is a concrete challenger model name for tests that need one.
// It is NOT a product default — corral has no default models; this constant
// exists so these tests keep asserting on a fixed name of their own choosing.
const testShadowModel = "claude-haiku-4-5"

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

// TestResolveAuditRolesDerivesBackendFromAssignedModels is the fence for a
// defect found by the FIRST real CI run of the GitHub Action, 2026-08-03.
//
// Every role was assigned gemini-3.6-flash and GEMINI_API_KEY was present, and
// corral still refused: "no $ANTHROPIC_API_KEY set — export your Claude key".
// The preflight asked the wrong question. It read MODEL_BACKEND, found it
// unset, concluded "default Claude path", and demanded a Claude key — without
// ever looking at the models the run was actually going to use.
//
// The property it should enforce is that EVERY ASSIGNED MODEL HAS A USABLE
// CREDENTIAL. MODEL_BACKEND is one way to say that and not the only one: an
// operator who names gemini-* models for every seat has already said which
// vendor this run uses, and requiring them to ALSO set MODEL_BACKEND is
// requiring them to say it twice, with an error message that names the wrong
// vendor when they don't.
//
// The default path is unchanged: Claude models with no MODEL_BACKEND still
// select anthropic and still require ANTHROPIC_API_KEY.
func TestResolveAuditRolesDerivesBackendFromAssignedModels(t *testing.T) {
	geminiModels := func() localAuditInput {
		return localAuditInput{
			writerModel: "gemini-3.6-flash",
			mutantModel: "gemini-3.6-flash",
			criticModel: "off",
			// The challenger is OFF unless named, so this is belt-and-braces
			// rather than load-bearing: kept explicit so these cases stay
			// about the graded seats even if that default ever moves back.
			shadowModel: "off",
		}
	}

	t.Run("all-gemini models with only a Gemini key", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "gm-test")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		if _, err := resolveAuditRoles(geminiModels(), nil); err != nil {
			t.Fatalf("an all-Gemini run with a Gemini key must be accepted; got: %v", err)
		}
		if got := os.Getenv("MODEL_BACKEND"); got != "gemini" {
			t.Errorf("MODEL_BACKEND = %q, want %q — the backend must be derived from the assigned models so FromEnv builds the right one (unset would default to ollama)", got, "gemini")
		}
	})

	t.Run("all-gemini models with NO key still refuses, naming Google", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		_, err := resolveAuditRoles(geminiModels(), nil)
		if err == nil {
			t.Fatal("a run with no usable credential must refuse before any jail or store is opened")
		}
		if strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
			t.Errorf("the error names the wrong vendor — the run uses Gemini models, so it must ask for a Google key; got: %v", err)
		}
	})

	// THERE IS NO DEFAULT HERD. These two subtests replace a pair that asserted
	// "the stock Claude default keeps working" — the behavior this change
	// removes. A binary that names a vendor's models when the operator named
	// none is not model-agnostic, and it made corral unusable on the first
	// command for anyone holding a different provider's key.
	t.Run("no models named is refused, not defaulted", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
		t.Setenv("GEMINI_API_KEY", "")

		_, err := resolveAuditRoles(localAuditInput{}, nil)
		if err == nil {
			t.Fatal("an unnamed herd must be refused — a present Anthropic key must NOT cause Claude models to be assumed")
		}
		for _, want := range []string{"--writer-model", "--mutant-model"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name the empty seat %s; got: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "claude-") {
			t.Errorf("the refusal must NOT suggest a model name — that reintroduces the default through the error; got: %v", err)
		}
	})

	t.Run("the refusal reports which credentials it can see", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "gm-test")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		_, err := resolveAuditRoles(localAuditInput{}, nil)
		if err == nil {
			t.Fatal("an unnamed herd must be refused")
		}
		// The usual cause is "I have a key, I just don't know what corral
		// wants from me" — so the error has to say what it can see.
		if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
			t.Errorf("the refusal must report the credential that IS present, so the operator knows which catalogue to pick from; got: %v", err)
		}
	})

	// The case that actually broke CI. Graded seats all Gemini, a Gemini key
	// present, and the CHALLENGER seat still carrying its Claude default — so
	// the run really does need an Anthropic key. Refusing is right; refusing
	// with "no $ANTHROPIC_API_KEY set — export your Claude key" is not, because
	// it describes a run the operator did not configure and hides the one seat
	// that is actually the problem. --shadow-model's own help already says the
	// default is a Claude model; the error has to say it too.
	// An EXPLICITLY named cross-vendor challenger still has to be refused when
	// its key is absent — it would fail mid-run otherwise, after the operator
	// had already paid for the graded seats.
	t.Run("an explicitly named cross-vendor challenger is named as the reason", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "gm-test")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		in := geminiModels()
		in.shadowModel = testShadowModel // an Anthropic model, named on purpose
		_, err := resolveAuditRoles(in, nil)
		if err == nil {
			t.Fatal("a challenger seat whose provider key is absent must refuse — it would fail mid-run otherwise")
		}
		msg := err.Error()
		if !strings.Contains(msg, "shadow-model") {
			t.Errorf("the error must name --shadow-model as the way out, since that is the seat that needs the key; got: %v", err)
		}
		if !strings.Contains(msg, testShadowModel) {
			t.Errorf("the error must name the offending model so the operator can see which seat it is; got: %v", err)
		}
	})

	// The counterpart, and the whole reason the shadow default was removed: an
	// UNNAMED challenger must not quietly add a seat from another vendor and
	// then demand that vendor's key. This is the trap an all-Gemini operator
	// used to hit with every graded seat already moved off Anthropic.
	t.Run("an unnamed challenger adds no seat and demands no key", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "gm-test")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		in := geminiModels()
		in.shadowModel = "" // unnamed: the challenger is simply off
		roles, err := resolveAuditRoles(in, nil)
		if err != nil {
			t.Fatalf("an all-Gemini run with a Gemini key must be accepted when no challenger is named; got: %v", err)
		}
		if roles.shadow != "" {
			t.Errorf("shadow = %q, want \"\" — an unnamed challenger seat stays empty", roles.shadow)
		}
		if _, ok := roles.assign[advpool.RoleMutantGeneratorShadow]; ok {
			t.Error("an unnamed challenger must not appear in the role assignment at all")
		}
	})

	// An explicit MODEL_BACKEND is an operator pointing every seat at one
	// endpoint on purpose (a gateway, openrouter, a local ollama). Deriving a
	// backend from model names would silently overrule that.
	t.Run("an explicit MODEL_BACKEND is never overruled", func(t *testing.T) {
		t.Setenv("MODEL_BACKEND", "openrouter")
		t.Setenv("OPENAI_API_KEY", "oa-test")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "")

		if _, err := resolveAuditRoles(geminiModels(), nil); err != nil {
			t.Fatalf("an explicit MODEL_BACKEND must be honoured as-is: %v", err)
		}
		if got := os.Getenv("MODEL_BACKEND"); got != "openrouter" {
			t.Errorf("MODEL_BACKEND = %q, want it left at openrouter", got)
		}
	})
}

// TestBaseVendorMapsBackendLabels: the base backend's vendor is what a per-role
// model must differ from. A pinned gateway (openrouter/ollama) returns "" —
// those front many vendors behind one endpoint, so a "claude-" name there is
// NOT an Anthropic call and must never be re-routed to Anthropic behind the
// operator's back.
func TestBaseVendorMapsBackendLabels(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", "anthropic"},
		{"anthropic", "anthropic"},
		{"claude", "anthropic"},
		{"gemini", "google"},
		{"openai", "openai"},
		{"openrouter", ""},
		{"ollama", ""},
	} {
		t.Setenv("MODEL_BACKEND", tc.env)
		if got := baseVendor(); got != tc.want {
			t.Errorf("MODEL_BACKEND=%q: baseVendor()=%q want %q", tc.env, got, tc.want)
		}
	}
}

// TestLocalChatterFailsClosedOnAnyRoleNotJustTheCritic is the regression that
// matters. The router used to cross-route ONLY the test-critic, which made the
// product's central claim unreachable: the generator and the writer were pinned
// to whichever single backend the run started on, so "a different model marks
// the exam" could only mean two models from the same vendor. Worse, it did not
// fail loudly — a Gemini generator name was sent to Anthropic and came back 404
// mid-run.
//
// With no Gemini credential present, asking for a Gemini GENERATOR must now
// refuse up front and name that role.
func TestLocalChatterFailsClosedOnAnyRoleNotJustTheCritic(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	_, err := localChatterFor(advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "gemini-3.6-flash",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "claude-haiku-4-5",
	})
	if err == nil {
		t.Fatal("a Gemini generator with no Gemini key must refuse the run, not 404 mid-run")
	}
	if !strings.Contains(err.Error(), advpool.RoleMutantGenerator) {
		t.Fatalf("the error must name the offending role, got: %v", err)
	}
}

// TestLocalChatterRoutesThreeVendorsAtOnce: the herd the product actually
// argues for — the fault-planter, the test-writer and the critic each from a
// different vendor. Before this, only one of the three could leave the base.
func TestLocalChatterRoutesThreeVendorsAtOnce(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("OPENAI_API_KEY", "sk-oai-test")

	pick, err := localChatterFor(advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "gemini-3.6-flash",
		advpool.RoleTestWriter:      "claude-sonnet-5",
		advpool.RoleTestCritic:      "gpt-5",
	})
	if err != nil {
		t.Fatalf("all three credentials present, want no error: %v", err)
	}
	gen, writer, critic := pick(advpool.RoleMutantGenerator), pick(advpool.RoleTestWriter), pick(advpool.RoleTestCritic)
	if gen == nil || writer == nil || critic == nil {
		t.Fatal("every role must resolve to a chatter")
	}
	// The generator and the critic are off-base vendors, so each must have been
	// given its OWN backend rather than the shared base-with-model.
	if gen == writer {
		t.Fatal("a Gemini generator must not share the Anthropic base backend with a Claude writer")
	}
	if critic == writer {
		t.Fatal("an OpenAI critic must not share the Anthropic base backend with a Claude writer")
	}
}

// TestLocalChatterLeavesAPinnedGatewayAlone: MODEL_BACKEND=openrouter means the
// operator pointed every seat at one endpoint on purpose. Re-routing a
// "claude-" name there to Anthropic would overrule them and spend on a vendor
// they did not choose.
func TestLocalChatterLeavesAPinnedGatewayAlone(t *testing.T) {
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := localChatterFor(advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "anthropic/claude-sonnet-5",
		advpool.RoleTestCritic:      "google/gemini-3.6-flash",
	}); err != nil {
		t.Fatalf("a pinned gateway must not be cross-routed or key-checked: %v", err)
	}
}
