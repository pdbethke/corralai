// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fromEnv is the one seam the refusal tests call FromEnv through.
func fromEnv() (Backend, error) { return FromEnv() }

// cleanBackendEnv gives a test an environment in which nothing is declared, so
// a refusal can only be the code's, not the host's.
func cleanBackendEnv(t *testing.T) {
	t.Helper()
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	for _, k := range []string{
		"MODEL_BACKEND", "AGENT_MODEL", "OLLAMA_URL",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "CORRALAI_GEMINI_BASE_URL", "ANTHROPIC_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// An unset MODEL_BACKEND used to mean "ollama" at 127.0.0.1:11434. That is a
// decision nobody made: a run on a box with no daemon, or with a different
// one, silently talked to whatever answered there.
func TestFromEnvRefusesUnsetBackend(t *testing.T) {
	cleanBackendEnv(t)
	t.Setenv("AGENT_MODEL", "some-model")
	b, err := fromEnv()
	if err == nil {
		t.Fatalf("FromEnv with MODEL_BACKEND unset built %T, want a refusal", b)
	}
	if !strings.Contains(err.Error(), "MODEL_BACKEND") {
		t.Errorf("the refusal must name MODEL_BACKEND, got: %v", err)
	}
}

// AGENT_MODEL used to default to qwen2.5-coder:7b for every backend, including
// a hosted one, where that name is a 404 at best.
func TestFromEnvRefusesUnsetAgentModel(t *testing.T) {
	for _, backend := range []string{"ollama", "openai", "openrouter", "gemini", "anthropic"} {
		t.Run(backend, func(t *testing.T) {
			cleanBackendEnv(t)
			t.Setenv("MODEL_BACKEND", backend)
			t.Setenv("OLLAMA_URL", "http://127.0.0.1:1")
			t.Setenv("OPENROUTER_API_KEY", "k")
			b, err := fromEnv()
			if err == nil {
				t.Fatalf("FromEnv with AGENT_MODEL unset built %T, want a refusal", b)
			}
			if !strings.Contains(err.Error(), "AGENT_MODEL") {
				t.Errorf("the refusal must name AGENT_MODEL, got: %v", err)
			}
		})
	}
}

func TestFromEnvOllamaRefusesUnsetURL(t *testing.T) {
	cleanBackendEnv(t)
	t.Setenv("MODEL_BACKEND", "ollama")
	t.Setenv("AGENT_MODEL", "some-model")
	b, err := fromEnv()
	if err == nil {
		t.Fatalf("FromEnv for ollama with OLLAMA_URL unset built %T, want a refusal", b)
	}
	if !strings.Contains(err.Error(), "OLLAMA_URL") {
		t.Errorf("the refusal must name OLLAMA_URL, got: %v", err)
	}
}

// Any MODEL_BACKEND value that was not recognised used to fall into the
// ollama arm, so a typo (`MODEL_BACKEND=opnai`) silently ran locally.
func TestFromEnvRefusesAnUnknownBackend(t *testing.T) {
	cleanBackendEnv(t)
	t.Setenv("MODEL_BACKEND", "opnai")
	t.Setenv("AGENT_MODEL", "some-model")
	t.Setenv("OLLAMA_URL", "http://127.0.0.1:1")
	b, err := fromEnv()
	if err == nil {
		t.Fatalf("FromEnv with MODEL_BACKEND=opnai built %T, want a refusal", b)
	}
	if !strings.Contains(err.Error(), "opnai") || !strings.Contains(err.Error(), "MODEL_BACKEND") {
		t.Errorf("the refusal must name the variable and the bad value, got: %v", err)
	}
}

// The pinned openrouter arm used to fall back to OPENAI_API_KEY (sending an
// OpenAI secret to openrouter.ai) and OPENAI_BASE_URL (sending the OpenRouter
// key to a company's OpenAI proxy). The resolver's pinned-gateway step already
// refuses both (5cec2670); this is the same rule at corral-agent's door.
func TestFromEnvOpenRouterNeverBorrowsOpenAIConfiguration(t *testing.T) {
	cleanBackendEnv(t)
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("AGENT_MODEL", "some-model")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	t.Setenv("OPENAI_BASE_URL", "http://proxy.invalid/v1")
	b, err := fromEnv()
	if err == nil {
		t.Fatalf("FromEnv for openrouter with only OpenAI configuration built %T, want a refusal naming OPENROUTER_API_KEY", b)
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("the refusal must name OPENROUTER_API_KEY, got: %v", err)
	}

	t.Setenv("OPENROUTER_API_KEY", "sk-or")
	resetCredsMemoForTest(t)
	b, err = fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("FromEnv = %T, want *openaiBackend", b)
	}
	if ob.key != "sk-or" {
		t.Errorf("key = %q, want OPENROUTER_API_KEY", ob.key)
	}
	if ob.base != "https://openrouter.ai/api/v1" {
		t.Errorf("base = %q, want OpenRouter's own endpoint, never OPENAI_BASE_URL", ob.base)
	}
}

// Local model names reach a backend only through the resolver, which knows
// whether a registry, an OLLAMA_URL or a pinned gateway declared them.
func TestForSeatNoLongerFallsBackToLocal(t *testing.T) {
	cleanBackendEnv(t)
	t.Setenv("OLLAMA_URL", "http://127.0.0.1:1")
	b, err := ForSeat("qwen3.6:35b-a3b", SeatOptions{})
	if err == nil {
		t.Fatalf("ForSeat of a bare local name built %T, want a refusal", b)
	}
}
