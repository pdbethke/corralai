// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pdbethke/corralai/internal/agentworker"
)

var testFormat = agentworker.ResponseFormat{Name: "verdicts", Schema: map[string]any{
	"type": "object", "additionalProperties": false, "required": []any{"v"},
	"properties": map[string]any{"v": map[string]any{"type": "string", "enum": []any{"a", "b"}}},
}}

// capture serves reply and records the request body.
func capture(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

func schemaOf(t *testing.T, v any) string {
	t.Helper()
	b, _ := json.Marshal(v)
	return string(b)
}

var wantSchema = `{"additionalProperties":false,"properties":{"v":{"enum":["a","b"],"type":"string"}},"required":["v"],"type":"object"}`

// THE WIRE, through the wrapper certify actually uses: a ResponseFormat in
// tools must reach Ollama as its `format` and never as a tool. The schema
// rides in tools precisely so no wrapper can drop it; this holds that.
func TestOllamaSendsTheSchemaAsFormat(t *testing.T) {
	srv, body := capture(t, `{"message":{"role":"assistant","content":"{\"v\":\"a\"}"}}`)
	c := AsChatterBudgeted(&ollamaBackend{url: srv.URL, model: "qwen3.6:35b-a3b"}, &UsageMeter{}, nil)
	m, err := c.Chat([]agentworker.Message{{Role: "user", Content: "x"}}, []any{testFormat})
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, (*body)["format"]); got != wantSchema {
		t.Fatalf("format = %s, want the schema", got)
	}
	if tools, ok := (*body)["tools"]; ok && tools != nil {
		t.Fatalf("a ResponseFormat must not be sent as a tool: %v", tools)
	}
	if m.Content != `{"v":"a"}` {
		t.Fatalf("content = %q", m.Content)
	}
}

func TestOpenAICompatibleSendsTheSchemaAsResponseFormat(t *testing.T) {
	srv, body := capture(t, `{"choices":[{"message":{"content":"{\"v\":\"a\"}"}}]}`)
	b := &openaiBackend{base: srv.URL, key: "k", model: "gemini-3.8-flash"}
	if _, err := AsChatter(b).Chat([]agentworker.Message{{Role: "user", Content: "x"}}, []any{testFormat}); err != nil {
		t.Fatal(err)
	}
	rf, _ := (*body)["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "verdicts" || js["strict"] != true || schemaOf(t, js["schema"]) != wantSchema {
		t.Fatalf("response_format = %v", (*body)["response_format"])
	}
	if tools, ok := (*body)["tools"]; ok && tools != nil {
		t.Fatalf("a ResponseFormat must not be sent as a tool: %v", tools)
	}
}

func TestResponsesAPISendsTheSchemaAsTextFormat(t *testing.T) {
	srv, body := capture(t, `{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"v\":\"a\"}"}]}]}`)
	b := &responsesBackend{base: srv.URL, key: "k", model: "gpt-6-codex"}
	m, err := AsChatter(b).Chat([]agentworker.Message{{Role: "user", Content: "x"}}, []any{testFormat})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := (*body)["text"].(map[string]any)
	f, _ := text["format"].(map[string]any)
	if f["type"] != "json_schema" || f["name"] != "verdicts" || f["strict"] != true || schemaOf(t, f["schema"]) != wantSchema {
		t.Fatalf("text.format = %v", (*body)["text"])
	}
	if _, ok := (*body)["tools"]; ok {
		t.Fatalf("a ResponseFormat must not be sent as a tool: %v", (*body)["tools"])
	}
	if m.Content != `{"v":"a"}` {
		t.Fatalf("content = %q", m.Content)
	}
}

// Anthropic: output_config.format, never a forced tool call, which current
// Claude models refuse with a 400.
func TestAnthropicSendsTheSchemaAsOutputConfigFormat(t *testing.T) {
	srv, body := capture(t, `{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"v\":\"a\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}`)
	b := &anthropicBackend{base: srv.URL, key: "k", model: "claude-haiku-4-5"}
	m, err := AsChatter(b).Chat([]agentworker.Message{{Role: "user", Content: "x"}}, []any{testFormat})
	if err != nil {
		t.Fatal(err)
	}
	oc, _ := (*body)["output_config"].(map[string]any)
	f, _ := oc["format"].(map[string]any)
	if f["type"] != "json_schema" || schemaOf(t, f["schema"]) != wantSchema {
		t.Fatalf("output_config = %v", (*body)["output_config"])
	}
	if _, ok := (*body)["tools"]; ok {
		t.Fatalf("a ResponseFormat must not be sent as a tool: %v", (*body)["tools"])
	}
	if _, ok := (*body)["tool_choice"]; ok {
		t.Fatal("no tool_choice: forced tool use is a 400 on current Claude models")
	}
	if m.Content != `{"v":"a"}` {
		t.Fatalf("content = %q", m.Content)
	}
}

// A provider's 400, 413 or 422 is a rejection of the request's content, and is
// marked so the critic can tell "this provider will not take this schema" from
// a failure the same prompt without its schema would hit again: a bad key
// (401, 403), a timeout (408), a rate limit (429), a missing model (404) or a
// server fault. Answering any of those with a plain call spends a second large
// prompt to learn nothing.
func TestPostJSONMarksARejectedRequest(t *testing.T) {
	for _, c := range []struct {
		code     int
		rejected bool
	}{{400, true}, {413, true}, {422, true}, {401, false}, {403, false}, {408, false}, {409, false}, {429, false}, {500, false}, {503, false}, {404, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(`{"error":"x"}`))
		}))
		var out map[string]any
		err := postJSON(srv.URL, nil, map[string]any{}, &out)
		srv.Close()
		if err == nil {
			t.Fatalf("%d: want an error", c.code)
		}
		if got := errors.Is(err, agentworker.ErrRequestRejected); got != c.rejected {
			t.Errorf("%d: rejected = %v, want %v (%v)", c.code, got, c.rejected, err)
		}
	}
}
