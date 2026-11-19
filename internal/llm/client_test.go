// SPDX-License-Identifier: Elastic-2.0

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MODEL_BACKEND", "AGENT_MODEL", "OLLAMA_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL",
		"GEMINI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "CORRAL_CREDS_DIR", "CREDENTIALS_DIRECTORY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
}

// The narrator (and the staffing planner it drives) used to default to ollama
// at 127.0.0.1:11434 running qwen2.5-coder:7b. Each missing piece now leaves it
// off, saying what to set.
func TestFromEnvHasNoDefaults(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, "MODEL_BACKEND is not set"},
		{"no model", map[string]string{"MODEL_BACKEND": "ollama", "OLLAMA_URL": "http://x"}, "AGENT_MODEL is not set"},
		{"ollama without address", map[string]string{"MODEL_BACKEND": "ollama", "AGENT_MODEL": "m"}, "needs OLLAMA_URL"},
		{"unknown backend", map[string]string{"MODEL_BACKEND": "olama", "AGENT_MODEL": "m"}, "is not a backend"},
		// gemini read OPENAI_API_KEY and called api.openai.com.
		{"gemini with only an openai key", map[string]string{"MODEL_BACKEND": "gemini", "AGENT_MODEL": "m", "OPENAI_API_KEY": "k"}, "needs GEMINI_API_KEY"},
		{"openrouter with only an openai key", map[string]string{"MODEL_BACKEND": "openrouter", "AGENT_MODEL": "m", "OPENAI_API_KEY": "k"}, "needs OPENROUTER_API_KEY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearModelEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cl := FromEnv()
			if cl.Available() {
				t.Fatalf("available with %v; want off", c.env)
			}
			if !strings.Contains(cl.Unavailable(), c.want) {
				t.Fatalf("reason %q does not contain %q", cl.Unavailable(), c.want)
			}
			if _, err := cl.Ask(context.Background(), "s", "u"); err == nil {
				t.Fatal("Ask on an unavailable client succeeded")
			}
		})
	}
}

func TestAskReachesTheDeclaredOllama(t *testing.T) {
	clearModelEnv(t)
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "hi"}})
	}))
	defer srv.Close()
	t.Setenv("MODEL_BACKEND", "ollama")
	t.Setenv("AGENT_MODEL", "gpt-oss:20b")
	t.Setenv("OLLAMA_URL", srv.URL)
	cl := FromEnv()
	if !cl.Available() {
		t.Fatalf("unavailable: %s", cl.Unavailable())
	}
	out, err := cl.Ask(context.Background(), "s", "u")
	if err != nil || out != "hi" || gotModel != "gpt-oss:20b" {
		t.Fatalf("Ask = %q, %v (model sent %q)", out, err, gotModel)
	}
}
