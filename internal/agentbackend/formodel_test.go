// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestForModelAnthropic verifies ForModel infers the anthropic vendor from a
// claude-* model name and builds an anthropicBackend reading
// ANTHROPIC_API_KEY, without making any network call.
func TestForModelAnthropic(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	b, err := ForModel("claude-sonnet-5")
	if err != nil {
		t.Fatalf("ForModel(claude-sonnet-5) error: %v", err)
	}
	ab, ok := b.(*anthropicBackend)
	if !ok {
		t.Fatalf("ForModel(claude-sonnet-5) = %T, want *anthropicBackend", b)
	}
	if ab.model != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", ab.model)
	}
	if ab.key != "sk-ant-test" {
		t.Errorf("key = %q, want sk-ant-test", ab.key)
	}
	if ab.base != "https://api.anthropic.com" {
		t.Errorf("base = %q, want default anthropic base", ab.base)
	}
}

// TestForModelGemini verifies ForModel routes a gemini-* model to the
// OpenAI-compatible Google endpoint, reading GEMINI_API_KEY.
func TestForModelGemini(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")

	b, err := ForModel("gemini-3.5-flash")
	if err != nil {
		t.Fatalf("ForModel(gemini-3.5-flash) error: %v", err)
	}
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("ForModel(gemini-3.5-flash) = %T, want *openaiBackend", b)
	}
	if ob.model != "gemini-3.5-flash" {
		t.Errorf("model = %q, want gemini-3.5-flash", ob.model)
	}
	if ob.key != "gm-test" {
		t.Errorf("key = %q, want gm-test", ob.key)
	}
	if !strings.Contains(ob.base, "generativelanguage.googleapis.com") {
		t.Errorf("base = %q, want the Google OpenAI-compatible endpoint", ob.base)
	}
}

// TestForModelGeminiFallsBackToGoogleAPIKey verifies GOOGLE_API_KEY is used
// when GEMINI_API_KEY is absent.
func TestForModelGeminiFallsBackToGoogleAPIKey(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "goog-test")

	b, err := ForModel("gemini-3.5-flash")
	if err != nil {
		t.Fatalf("ForModel(gemini-3.5-flash) error: %v", err)
	}
	ob := b.(*openaiBackend)
	if ob.key != "goog-test" {
		t.Errorf("key = %q, want goog-test", ob.key)
	}
}

// TestForModelGeminiFailsClosedWithoutKey verifies ForModel refuses to build
// a Gemini backend (and returns an actionable error) when neither
// GEMINI_API_KEY nor GOOGLE_API_KEY is set — the fail-closed contract that
// keeps a cross-vendor critic from silently falling back to an unauthenticated
// or wrong backend.
func TestForModelGeminiFailsClosedWithoutKey(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	_, err := ForModel("gemini-3.5-flash")
	if err == nil {
		t.Fatal("ForModel(gemini-3.5-flash) with no key: want error, got nil")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("error %q should name the missing env var GEMINI_API_KEY", err.Error())
	}
}

// TestForModelUnknownVendor verifies ForModel refuses local/unrecognized
// model names (e.g. an ollama model) rather than guessing a vendor.
func TestForModelUnknownVendor(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	_, err := ForModel("qwen2.5-coder:7b")
	if err == nil {
		t.Fatal("ForModel(qwen2.5-coder:7b): want error, got nil")
	}
	if !strings.Contains(err.Error(), "qwen2.5-coder:7b") {
		t.Errorf("error %q should name the model", err.Error())
	}
}

// TestForModelGeminiIgnoresTheOpenAIBaseURL: a cross-vendor gemini-* seat
// followed OPENAI_BASE_URL, so an unpinned run whose gpt seats went through
// a gateway sent its gemini seat — carrying GEMINI_API_KEY — to that
// gateway too. The OpenAI variable names the OpenAI endpoint; Gemini's is
// CORRALAI_GEMINI_BASE_URL (and, on the pinned MODEL_BACKEND=gemini door
// only, the back-compat OPENAI_BASE_URL — see TestFromEnvGeminiFallbackOrder).
func TestForModelGeminiIgnoresTheOpenAIBaseURL(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:18082/v1")
	t.Setenv("MODEL_BACKEND", "")

	b, err := ForModel("gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	ob := b.(*openaiBackend)
	if strings.Contains(ob.base, "18082") {
		t.Errorf("base = %q — the gemini seat followed OPENAI_BASE_URL, carrying the Google key with it", ob.base)
	}
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "http://127.0.0.1:18083/v1")
	b, _ = ForModel("gemini-3.6-flash")
	if ob := b.(*openaiBackend); !strings.Contains(ob.base, "18083") {
		t.Errorf("base = %q, want CORRALAI_GEMINI_BASE_URL honoured", ob.base)
	}
}

// TestOpenRouterReadsItsOwnKey: the seventh (claims) review found
// OPENROUTER_API_KEY documented in three places and read by nothing — the
// openrouter arm read OPENAI_API_KEY and defaulted to api.openai.com, so the
// documented setup sent an unauthenticated request to the wrong host.
func TestOpenRouterReadsItsOwnKey(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("MODEL_BACKEND", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENROUTER_BASE_URL", "")

	b := FromEnv()
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("FromEnv() = %T, want *openaiBackend", b)
	}
	if ob.key != "sk-or-test" {
		t.Errorf("key = %q, want OPENROUTER_API_KEY", ob.key)
	}
	if !strings.Contains(ob.base, "openrouter.ai") {
		t.Errorf("base = %q, want OpenRouter's endpoint by default", ob.base)
	}
	// The older configuration keeps working.
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-oai")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1")
	resetCredsMemoForTest(t)
	if ob := FromEnv().(*openaiBackend); ob.key != "sk-oai" || ob.base != "http://127.0.0.1:1/v1" {
		t.Errorf("fallback = %q @ %q, want the OPENAI_* configuration honoured", ob.key, ob.base)
	}
}
