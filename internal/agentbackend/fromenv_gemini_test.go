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

	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
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

// TestGeminiReadsOnlyItsOwnKeyAndURL: a Gemini seat reads GEMINI_API_KEY and
// nothing else. It used to fall back to GOOGLE_API_KEY and then to
// OPENAI_API_KEY, so with only an OpenAI key set that OpenAI secret was sent
// to Google; and a pinned gemini backend followed OPENAI_BASE_URL (often a
// company's OpenAI proxy). Unset, the seat refuses naming GEMINI_API_KEY.
func TestGeminiReadsOnlyItsOwnKeyAndURL(t *testing.T) {
	for _, c := range []struct{ name, gemini, google, openai string }{
		{"only an OpenAI key", "", "", "sk-openai"},
		{"only GOOGLE_API_KEY", "", "goog", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetCredsMemoForTest(t)
			keyring.MockInit()
			t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
			t.Setenv("CREDENTIALS_DIRECTORY", "")
			t.Setenv("MODEL_BACKEND", "gemini")
			t.Setenv("AGENT_MODEL", "gemini-3.6-flash")
			t.Setenv("GEMINI_API_KEY", c.gemini)
			t.Setenv("GOOGLE_API_KEY", c.google)
			t.Setenv("OPENAI_API_KEY", c.openai)
			t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
			t.Setenv("OPENAI_BASE_URL", "")
			if b, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
				t.Errorf("FromEnv = %T, %v; want a refusal naming GEMINI_API_KEY", b, err)
			}
			if b, err := ForModel("gemini-3.6-flash"); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
				t.Errorf("ForModel = %T, %v; want a refusal naming GEMINI_API_KEY", b, err)
			}
		})
	}

	t.Run("pinned gemini ignores OPENAI_BASE_URL", func(t *testing.T) {
		resetCredsMemoForTest(t)
		keyring.MockInit()
		t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
		t.Setenv("CREDENTIALS_DIRECTORY", "")
		t.Setenv("MODEL_BACKEND", "gemini")
		t.Setenv("AGENT_MODEL", "gemini-3.6-flash")
		t.Setenv("GEMINI_API_KEY", "gm")
		t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
		t.Setenv("OPENAI_BASE_URL", "http://company-proxy:8080/v1")
		b, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		ob, ok := b.(*openaiBackend)
		if !ok {
			t.Fatalf("FromEnv() = %T, want *openaiBackend", b)
		}
		if ob.key != "gm" || ob.base != "https://generativelanguage.googleapis.com/v1beta/openai" {
			t.Errorf("key %q base %q; want GEMINI_API_KEY at Google's own endpoint", ob.key, ob.base)
		}
	})
}

// TestFromEnvOpenAIUnchanged guards against over-reach: splitting gemini out of
// the shared switch arm must leave openai/openrouter exactly as they were.
func TestFromEnvOpenAIUnchanged(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("MODEL_BACKEND", "openai")
	t.Setenv("AGENT_MODEL", "some-model")
	t.Setenv("OPENAI_API_KEY", "oai")
	t.Setenv("GEMINI_API_KEY", "gm")
	t.Setenv("OPENAI_BASE_URL", "")

	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
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
