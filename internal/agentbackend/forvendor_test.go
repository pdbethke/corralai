// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestForVendorAnthropic verifies ForVendor builds an anthropicBackend for the
// DECLARED anthropic vendor, reading ANTHROPIC_API_KEY, without making any
// network call.
func TestForVendorAnthropic(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	b, err := ForVendor("anthropic", "claude-sonnet-5")
	if err != nil {
		t.Fatalf("ForVendor(anthropic, claude-sonnet-5) error: %v", err)
	}
	ab, ok := b.(*anthropicBackend)
	if !ok {
		t.Fatalf("ForVendor(anthropic, claude-sonnet-5) = %T, want *anthropicBackend", b)
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

// TestForVendorGoogle verifies ForVendor sends a google seat to the
// OpenAI-compatible Google endpoint, reading GEMINI_API_KEY.
func TestForVendorGoogle(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")

	b, err := ForVendor("google", "gemini-3.5-flash")
	if err != nil {
		t.Fatalf("ForVendor(google, gemini-3.5-flash) error: %v", err)
	}
	ob, ok := b.(*openaiBackend)
	if !ok {
		t.Fatalf("ForVendor(google, gemini-3.5-flash) = %T, want *openaiBackend", b)
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

// TestForVendorGoogleFailsClosedWithoutKey verifies ForVendor refuses to build
// a Gemini backend (and returns an actionable error) when GEMINI_API_KEY is
// unset (GOOGLE_API_KEY is no longer read: TestGeminiReadsOnlyItsOwnKeyAndURL) — the fail-closed contract that
// keeps a cross-vendor critic from silently falling back to an unauthenticated
// or wrong backend.
func TestForVendorGoogleFailsClosedWithoutKey(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	_, err := ForVendor("google", "gemini-3.5-flash")
	if err == nil {
		t.Fatal("ForVendor(google, gemini-3.5-flash) with no key: want error, got nil")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("error %q should name the missing env var GEMINI_API_KEY", err.Error())
	}
}

// TestForVendorRefusesAnUndeclaredVendor: ForVendor serves only a vendor the
// caller names, and names it from the closed set. There is no "infer it from
// the model" arm left to fall into.
func TestForVendorRefusesAnUndeclaredVendor(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	for _, vendor := range []string{"", "ollama", "Anthropic", "hf.co"} {
		b, err := ForVendor(vendor, "claude-sonnet-5")
		if err == nil {
			t.Errorf("ForVendor(%q, claude-sonnet-5) = %T; want a refusal", vendor, b)
			continue
		}
		if !strings.Contains(err.Error(), "anthropic, google, openai") {
			t.Errorf("ForVendor(%q) error %q should name the vendors it serves", vendor, err)
		}
	}
	if b, err := ForVendor("openai", ""); err == nil {
		t.Errorf("ForVendor(openai, \"\") = %T; want a refusal for an empty model", b)
	}
}

// A model name is never a vendor: a gpt- name ForVendor is told is anthropic
// goes to Anthropic, and a local-looking name told openai goes to OpenAI. The
// declaration decides, so no spelling can move a seat.
func TestForVendorFollowsTheDeclarationNotTheName(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_BASE_URL", "")
	if b, err := ForVendor("anthropic", "gpt-oss:20b"); err != nil {
		t.Fatal(err)
	} else if _, ok := b.(*anthropicBackend); !ok {
		t.Errorf("ForVendor(anthropic, gpt-oss:20b) = %T, want *anthropicBackend", b)
	}
	if b, err := ForVendor("openai", "my-local-thing"); err != nil {
		t.Fatal(err)
	} else if _, ok := b.(*openaiBackend); !ok {
		t.Errorf("ForVendor(openai, my-local-thing) = %T, want *openaiBackend", b)
	}
}

// SplitVendorModel accepts exactly anthropic/, google/ and openai/: a closed
// set, so an ollama name with a slash (hf.co/…, library/…) is never a vendor.
func TestSplitVendorModel(t *testing.T) {
	for spec, want := range map[string][2]string{
		"anthropic/claude-sonnet-5": {"anthropic", "claude-sonnet-5"},
		"google/gemini-3.8-flash":   {"google", "gemini-3.8-flash"},
		"openai/gpt-6":              {"openai", "gpt-6"},
		"openai/org/model":          {"openai", "org/model"},
		"hf.co/x/y:q4":              {"", ""},
		"library/llama3:8b":         {"", ""},
		"Anthropic/claude-sonnet-5": {"", ""},
		"anthropic/":                {"", ""},
		"claude-sonnet-5":           {"", ""},
		"gpt-oss:20b":               {"", ""},
		"":                          {"", ""},
	} {
		v, m, ok := SplitVendorModel(spec)
		if v != want[0] || m != want[1] || ok != (want[0] != "") {
			t.Errorf("SplitVendorModel(%q) = %q, %q, %v; want %q, %q", spec, v, m, ok, want[0], want[1])
		}
	}
}

// TestForVendorGoogleIgnoresTheOpenAIBaseURL: a cross-vendor google seat
// followed OPENAI_BASE_URL, so an unpinned run whose gpt seats went through
// a gateway sent its gemini seat — carrying GEMINI_API_KEY — to that
// gateway too. The OpenAI variable names the OpenAI endpoint; Gemini's is
// CORRALAI_GEMINI_BASE_URL (and, on the pinned MODEL_BACKEND=gemini door
// only, the back-compat OPENAI_BASE_URL — see TestFromEnvGeminiFallbackOrder).
func TestForVendorGoogleIgnoresTheOpenAIBaseURL(t *testing.T) {
	resetCredsMemoForTest(t)
	keyring.MockInit()
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("GEMINI_API_KEY", "gm-test")
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:18082/v1")
	t.Setenv("MODEL_BACKEND", "")

	b, err := ForVendor("google", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	ob := b.(*openaiBackend)
	if strings.Contains(ob.base, "18082") {
		t.Errorf("base = %q — the gemini seat followed OPENAI_BASE_URL, carrying the Google key with it", ob.base)
	}
	t.Setenv("CORRALAI_GEMINI_BASE_URL", "http://127.0.0.1:18083/v1")
	b, _ = ForVendor("google", "gemini-3.6-flash")
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

	t.Setenv("AGENT_MODEL", "some-model")
	b, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
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
	// The older OPENAI_* configuration no longer stands in for it: that sent an
	// OpenAI secret to openrouter.ai and the OpenRouter key to an OpenAI proxy.
	// TestFromEnvOpenRouterNeverBorrowsOpenAIConfiguration pins the refusal.
}

// VendorOf answers for agent seats only, from the pinned model or the maker
// declared on the built-in definition. A model name that is not an agent seat
// answers "": no vendor is guessed from a model's name (founder ruling
// 2026-11-12) — the seat resolver's declaration is the only source for one.
func TestVendorOfAgentSeats(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_MYTOOL", "/bin/cat")
	t.Setenv("CORRALAI_AGENT_CODEX", "") // not set: the built-in applies
	for spec, want := range map[string]string{
		"codex":                  "openai",
		"claude-code":            "anthropic",
		"antigravity":            "google",
		"codex:gpt-6-astra":      "openai",
		"claude-code:gemini-9":   "google", // the pinned model decides, not the maker
		"codex:astra-unprefixed": "openai", // no prefix: fall back to the maker
		"mytool":                 "",       // operator agent, unpinned: unknown
		"mytool:claude-sonnet-5": "anthropic",
		"mytool:local-model":     "",
		// A bare model name is not an agent seat, and its spelling is not a
		// vendor: each answers "", including the shape that leaked.
		"claude-sonnet-5":           "",
		"gemini-3.6-pro":            "",
		"gpt-6":                     "",
		"gpt-oss:20b":               "",
		"anthropic/claude-sonnet-5": "",
		"llama3:8b":                 "",
		"":                          "",
	} {
		if got := VendorOf(spec); got != want {
			t.Errorf("VendorOf(%q) = %q, want %q", spec, got, want)
		}
	}
	// A redefined built-in has no declared maker: corral cannot know what it runs.
	t.Setenv("CORRALAI_AGENT_CODEX", "/bin/cat")
	if got := VendorOf("codex"); got != "" {
		t.Errorf("VendorOf(redefined codex) = %q, want empty", got)
	}
}

// RecordedVendorOf answers from the tables alone: the environment cannot
// change what a recorded seat was.
func TestRecordedVendorOfIsEnvFree(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_CODEX", "/bin/cat")
	t.Setenv("CORRALAI_AGENT_MYTOOL", "/bin/cat")
	for spec, want := range map[string]string{
		"codex": "openai", "codex:gpt-6": "openai", "claude-code": "anthropic",
		"antigravity": "google", "mytool": "", "mytool:claude-sonnet-5": "anthropic",
		"gpt-6": "openai", "llama3:8b": "",
	} {
		if got := RecordedVendorOf(spec); got != want {
			t.Errorf("RecordedVendorOf(%q) = %q, want %q", spec, got, want)
		}
	}
}
