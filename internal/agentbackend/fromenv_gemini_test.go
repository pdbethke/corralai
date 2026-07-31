// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestFromEnvGeminiUsesGoogleKeyAndEndpoint fixes a wiring trap found while
// trying to run a whole audit on Gemini after the Anthropic account hit its
// usage limit: MODEL_BACKEND=gemini shared a switch arm with openai/openrouter,
// so it read OPENAI_API_KEY and defaulted to https://api.openai.com/v1 —
// pointing "the gemini backend" at OpenAI's endpoint with (almost certainly)
// no key at all.
//
// ForModel had the correct Google routing all along, so the two disagreed
// about what "gemini" means depending on which door you came in: a
// cross-vendor CRITIC (ForModel) worked, while pointing the WHOLE run at
// Gemini (FromEnv) silently did not. An operator following the obvious path —
// MODEL_BACKEND=gemini plus GEMINI_API_KEY — got an unauthenticated call to
// the wrong vendor.
func TestFromEnvGeminiUsesGoogleKeyAndEndpoint(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("MODEL_BACKEND", "gemini")
	t.Setenv("AGENT_MODEL", "gemini-3.6-flash")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")

	b := FromEnv()
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("FromEnv() = %T, want *openaiBackend", b)
	}
	if got := ob.key; got != "gm-test" {
		t.Errorf("credential = %q, want the Google one — MODEL_BACKEND=gemini must not read the OpenAI variable", got)
	}
	if !strings.Contains(ob.base, "generativelanguage.googleapis.com") {
		t.Errorf("base = %q, want the Google OpenAI-compatible endpoint, not api.openai.com", ob.base)
	}
	if ob.model != "gemini-3.6-flash" {
		t.Errorf("model = %q, want gemini-3.6-flash", ob.model)
	}
}

// TestFromEnvGeminiFallbackOrder pins back-compat: the ONLY configuration that
// worked for MODEL_BACKEND=gemini before this fix was the OpenAI variable, so
// an operator already doing that must keep working. Preference order is the
// Gemini one, then the Google one, then the OpenAI one.
func TestFromEnvGeminiFallbackOrder(t *testing.T) {
	for _, c := range []struct{ name, gemini, google, openai, want string }{
		{"google when no gemini value", "", "goog", "oai", "goog"},
		{"openai last, for back-compat", "", "", "oai", "oai"},
		{"gemini wins", "gm", "goog", "oai", "gm"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetCredsMemoForTest(t)
			keyring.MockInit()
			t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
			t.Setenv("CREDENTIALS_DIRECTORY", "")
			t.Setenv("MODEL_BACKEND", "gemini")
			t.Setenv("GEMINI_API_KEY", c.gemini)
			t.Setenv("GOOGLE_API_KEY", c.google)
			t.Setenv("OPENAI_API_KEY", c.openai)
			t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
			t.Setenv("OPENAI_BASE_URL", "")

			b := FromEnv()
			ob, ok := b.(*openaiBackend)
			if !ok {
				t.Fatalf("FromEnv() = %T, want *openaiBackend", b)
			}
			if got := ob.key; got != c.want {
				t.Errorf("credential = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFromEnvOpenAIUnchanged guards against over-reach: splitting gemini out of
// the shared switch arm must leave openai/openrouter exactly as they were.
func TestFromEnvOpenAIUnchanged(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("MODEL_BACKEND", "openai")
	t.Setenv("OPENAI_API_KEY", "oai")
	t.Setenv("GEMINI_API_KEY", "gm")
	t.Setenv("OPENAI_BASE_URL", "")

	b := FromEnv()
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("FromEnv() = %T, want *openaiBackend", b)
	}
	if got := ob.key; got != "oai" {
		t.Errorf("openai credential = %q, want oai — must not pick up the Google one", got)
	}
	if !strings.Contains(ob.base, "api.openai.com") {
		t.Errorf("openai base = %q, want api.openai.com", ob.base)
	}
}
