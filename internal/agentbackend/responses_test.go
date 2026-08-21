// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A Codex model reaching /chat/completions is the bug this file exists to
// prevent: the name resolves, the call goes to an endpoint that does not serve
// it, and the operator is told a model they can see in the docs does not exist.
func TestCodexModelsRouteToTheResponsesAPI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	for _, model := range []string{"gpt-5-codex", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.3-codex"} {
		b, err := ForModel(model)
		if err != nil {
			t.Fatalf("ForModel(%q): %v", model, err)
		}
		if _, ok := b.(*responsesBackend); !ok {
			t.Errorf("model %q routed to %T, want *responsesBackend — Codex is Responses-API only", model, b)
		}
	}
}

// The non-Codex OpenAI models must NOT move: they are served on chat
// completions, and switching them would break every existing caller.
func TestNonCodexOpenAIModelsStayOnChatCompletions(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	for _, model := range []string{"gpt-4o", "o1-preview", "o3-mini"} {
		b, err := ForModel(model)
		if err != nil {
			t.Fatalf("ForModel(%q): %v", model, err)
		}
		if _, ok := b.(*openaiBackend); !ok {
			t.Errorf("model %q routed to %T, want *openaiBackend", model, b)
		}
	}
}

// Responses puts name/description/parameters at the TOP level of a tool object;
// chat completions nests them under "function". Callers build the nested shape,
// so a missed translation sends a tool the API cannot read — and the model
// simply never calls it, which looks like a model that ignores its tools.
func TestToolSchemasAreFlattenedForResponses(t *testing.T) {
	in := []any{map[string]any{"type": "function", "function": map[string]any{
		"name": "report_finding", "description": "file a finding",
		"parameters": map[string]any{"type": "object"},
	}}}

	got := responsesTools(in)

	if len(got) != 1 {
		t.Fatalf("got %d tools, want 1", len(got))
	}
	m := got[0].(map[string]any)
	if m["name"] != "report_finding" {
		t.Errorf("name not lifted to the top level: %#v", m)
	}
	if _, nested := m["function"]; nested {
		t.Errorf("the nested chat-completions wrapper survived: %#v", m)
	}
}

// A tool already in the Responses shape passes through, so a caller that adopts
// the newer shape later does not have to be hunted down here.
func TestAlreadyFlatToolsPassThrough(t *testing.T) {
	in := []any{map[string]any{"type": "function", "name": "already_flat"}}
	got := responsesTools(in)
	if got[0].(map[string]any)["name"] != "already_flat" {
		t.Errorf("a flat tool was mangled: %#v", got[0])
	}
}

// "system" is spelled "developer" on this API, and an empty turn is rejected.
func TestInputRolesAndEmptyTurns(t *testing.T) {
	items := responsesInput([]Message{
		{Role: "system", Content: "you are a critic"},
		{Role: "user", Content: "grade this"},
		{Role: "assistant", Content: "   "}, // whitespace only: carries nothing
	})

	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (the empty turn must be dropped)", len(items))
	}
	if items[0]["role"] != "developer" {
		t.Errorf("system turn became %q, want developer", items[0]["role"])
	}
}

// The end-to-end parse: prose arrives as output_text parts inside a message
// item, a tool call is its own top-level item with arguments as a STRING, and
// usage is input_tokens/output_tokens — reading the chat-completions names
// instead would report a free call.
func TestChatParsesOutputItemsAndUsage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("posted to %q, want /responses", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{
			"output": [
				{"type":"message","role":"assistant","content":[
					{"type":"output_text","text":"planted "},
					{"type":"refusal","text":"NOT PROSE"},
					{"type":"output_text","text":"two mutants"}]},
				{"type":"function_call","name":"report_finding","arguments":"{\"sev\":\"high\"}","call_id":"call_1"}
			],
			"usage": {"input_tokens": 1200, "output_tokens": 34}
		}`))
	}))
	defer srv.Close()

	b := &responsesBackend{base: srv.URL, key: "k", model: "gpt-5.1-codex"}
	msg, err := b.Chat([]Message{{Role: "user", Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if msg.Content != "planted two mutants" {
		t.Errorf("content = %q; output_text parts must join and a refusal must not be folded in as prose", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "report_finding" {
		t.Fatalf("tool call not parsed: %#v", msg.ToolCalls)
	}
	if string(msg.ToolCalls[0].Function.Arguments) != `{"sev":"high"}` {
		t.Errorf("arguments = %s", msg.ToolCalls[0].Function.Arguments)
	}
	if msg.Usage.InputTokens != 1200 || msg.Usage.OutputTokens != 34 {
		t.Errorf("usage = %+v, want 1200/34", msg.Usage)
	}
	// Temperature is deliberately absent: reasoning models reject one they do
	// not support rather than ignoring it, which would fail the whole run.
	if _, sent := gotBody["temperature"]; sent {
		t.Errorf("temperature was sent to a reasoning model: %#v", gotBody)
	}
}
