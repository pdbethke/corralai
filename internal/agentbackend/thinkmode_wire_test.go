// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The predicate having the right answer is not the same as the REQUEST carrying
// it. This asserts on the actual wire body: Qwen 3+ must send "think": false,
// and every other model must omit the field entirely (it is Ollama-specific and
// we do not send speculative fields to models we have not probed).
func TestOllamaSendsThinkFalseOnlyForQwen3Plus(t *testing.T) {
	for _, c := range []struct {
		model    string
		wantSent bool
	}{
		{"qwen3.5:9b-q8_0", true},
		{"qwen3:14b", true},
		{"qwen3-coder:30b", true},
		{"deepseek-r1:14b", true},
		{"qwen2.5-coder:14b", false},
		{"llama3.3:70b", false},
		{"mistral:7b", false},
		{"deepseek-v3:latest", false},
	} {
		t.Run(c.model, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("request body was not JSON: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"message":{"role":"assistant","content":"ok"}}`)
			}))
			defer srv.Close()

			b := &ollamaBackend{url: srv.URL, model: c.model}
			if _, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			// num_ctx must reach the wire for EVERY model: without it ollama
			// silently uses its own small default and a normal-sized prompt
			// fails with "exceeds the available context size" on a model
			// trained for 30x that.
			if o, ok := body["options"].(map[string]any); !ok {
				t.Errorf("no options map on the wire — num_ctx cannot be set")
			} else if o["num_ctx"] == nil {
				t.Errorf("num_ctx absent from the wire body: %v", o)
			}

			v, present := body["think"]
			if present != c.wantSent {
				t.Fatalf("think field present = %v, want %v (body: %v)", present, c.wantSent, body)
			}
			if c.wantSent && v != false {
				t.Errorf("think = %v, want false", v)
			}
		})
	}
}

// THE WIRE, again: a hint that never reaches a caller is a hint nobody reads.
// This branch has now shipped three features whose value died at an unwired
// boundary, so the guidance gets a test that it survives the backend's error
// path rather than only existing in ollamareq.
func TestOllamaBackendWrapsContextOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"request (15802 tokens) exceeds the available context size (4096)"}`)
	}))
	defer srv.Close()

	b := &ollamaBackend{url: srv.URL, model: "deepseek-r1:14b"}
	_, err := b.Chat([]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "CORRALAI_OLLAMA_NUM_CTX") {
		t.Errorf("the context-overflow hint did not survive the backend's error path: %v", err)
	}
}

// TestAnthropicReportsAReplyThatWasAllThinking: a Claude 5 model can spend
// the entire max_tokens budget in a thinking block and return no text with
// stop_reason max_tokens. That used to come back as an empty message and
// no error — the seat "said nothing" — which is how a sonnet reviewer sat
// a whole review in silence. It is an error, by name.
func TestAnthropicReportsAReplyThatWasAllThinking(t *testing.T) {
	var sentMax float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentMax, _ = body["max_tokens"].(float64)
		_, _ = w.Write([]byte(`{"stop_reason":"max_tokens","content":[{"type":"thinking","thinking":""}],"usage":{"input_tokens":12000,"output_tokens":4096}}`))
	}))
	defer srv.Close()
	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-sonnet-5"}
	_, err := b.Chat([]Message{{Role: "user", Content: "review this"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "produced no text") || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("an all-thinking reply must be an error by name, got %v", err)
	}
	if sentMax < 16000 {
		t.Errorf("max_tokens sent = %v — too small to hold a reasoning model's thinking plus its answer", sentMax)
	}
	// And a reply that DID say something under max_tokens is not an error:
	// truncated text is the caller's to judge.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason":"max_tokens","content":[{"type":"text","text":"{\"partial\":"}],"usage":{"output_tokens":9}}`))
	}))
	defer srv2.Close()
	b2 := &anthropicBackend{base: srv2.URL, key: "k", model: "claude-sonnet-5"}
	if m, err := b2.Chat([]Message{{Role: "user", Content: "x"}}, nil); err != nil || m.Content == "" {
		t.Errorf("a truncated reply with text must not be an error: %v %q", err, m.Content)
	}
}
