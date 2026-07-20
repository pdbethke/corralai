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
