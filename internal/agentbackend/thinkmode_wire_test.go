// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
