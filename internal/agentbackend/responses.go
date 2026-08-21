// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"strings"
)

// ---- OpenAI Responses API (/v1/responses) ----
//
// A SECOND OpenAI wire format, not a preference. OpenAI serves its Codex models
// — gpt-5-codex, gpt-5.1-codex, gpt-5.1-codex-mini and friends — on the
// Responses API ONLY; they are not available on /chat/completions at all.
//
// Before this existed, VendorOf matched the "gpt-" prefix, routed a Codex model
// to the chat-completions backend, and the call failed at the API boundary with
// whatever OpenAI says about an unknown model on that endpoint. The name
// resolved and then went somewhere that could not serve it — the same shape as
// an unset MODEL_BACKEND resolving to a phantom vendor, and just as confusing to
// the operator, who picked a real model and got told it does not exist.
//
// Three differences from chat completions, each of which is a place to get it
// wrong quietly:
//
//   - Tools are FLAT. Chat completions nests the schema under "function";
//     Responses puts name/description/parameters at the top level of the tool
//     object. Callers here build the chat-completions shape (agentworker.go),
//     so this backend translates rather than making every caller branch.
//   - Output is a LIST of items, not a single choice. Assistant prose arrives as
//     a "message" item whose content parts are "output_text"; a tool call is its
//     own "function_call" item at the top level of that list, carrying
//     "arguments" as a JSON STRING.
//   - Usage is input_tokens/output_tokens, not prompt_tokens/completion_tokens.
//     Reading the wrong pair reports a free call, which is worse than no meter.
//
// Temperature is deliberately NOT sent. The Codex models are reasoning models,
// and reasoning models reject a temperature they do not support rather than
// ignoring it. Omitting a tuning knob costs a little determinism; sending one
// that gets rejected costs the whole run.
type responsesBackend struct{ base, key, model string }

func (b *responsesBackend) Model() string { return b.model }
func (b *responsesBackend) WithModel(model string) Backend {
	c := *b
	c.model = model
	return &c
}

// responsesTools converts chat-completions tool schemas to the Responses shape.
// A tool that is already flat passes through unchanged, so a caller that learns
// the newer shape later does not have to be found and fixed here.
func responsesTools(tools []any) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		fn, nested := m["function"].(map[string]any)
		if !nested {
			out = append(out, t)
			continue
		}
		flat := map[string]any{"type": "function"}
		for _, k := range []string{"name", "description", "parameters", "strict"} {
			if v, present := fn[k]; present {
				flat[k] = v
			}
		}
		out = append(out, flat)
	}
	return out
}

// responsesInput maps the shared Message list onto Responses input items.
//
// "system" becomes "developer", which is the role Responses uses for the
// instruction turn. Tool-result turns are carried as user text: this backend
// drives the same single-shot request/response loop the others do, so it never
// has a call_id to pair a native function_call_output against.
func responsesInput(messages []Message) []map[string]any {
	items := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue // an empty turn is rejected, and carries nothing anyway
		}
		role := m.Role
		switch role {
		case "system":
			role = "developer"
		case "assistant":
			role = "assistant"
		default:
			role = "user"
		}
		items = append(items, map[string]any{"type": "message", "role": role, "content": m.Content})
	}
	return items
}

func (b *responsesBackend) Chat(messages []Message, tools []any) (Message, error) {
	var out struct {
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			// function_call items carry these at the item's top level.
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			CallID    string `json:"call_id"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	hdr := map[string]string{}
	if b.key != "" {
		hdr["Authorization"] = "Bearer " + b.key
	}
	body := map[string]any{
		"model": b.model,
		"input": responsesInput(messages),
	}
	if rt := responsesTools(tools); len(rt) > 0 {
		body["tools"] = rt
	}
	if err := postJSON(b.base+"/responses", hdr, body, &out); err != nil {
		return Message{}, err
	}

	usage := Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	var text strings.Builder
	var calls []ToolCall
	for _, item := range out.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				// "refusal" parts are deliberately not concatenated as prose: a
				// refusal is not an answer, and folding it into Content would
				// hand the caller a plausible-looking empty result.
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "function_call":
			var tc ToolCall
			tc.Function.Name = item.Name
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			tc.Function.Arguments = json.RawMessage(args)
			calls = append(calls, tc)
		}
	}
	// A response with neither prose nor a call still happened and was billed;
	// carry the usage rather than reporting a free call.
	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls, Usage: usage}, nil
}

// needsResponsesAPI reports whether a model is served only by /v1/responses.
//
// Kept as one predicate so the routing table and any future error message
// cannot disagree about which models these are.
func needsResponsesAPI(model string) bool {
	return strings.Contains(strings.ToLower(model), "-codex")
}

// openAIBase is the single resolution of OpenAI's endpoint, shared by both
// wire formats so a gateway override cannot land on one and miss the other.
func openAIBase() string {
	return env("OPENAI_BASE_URL", "https://api.openai.com/v1")
}

// newOpenAIBackend picks the wire format the model is actually served on.
func newOpenAIBackend(key, model string) Backend {
	if needsResponsesAPI(model) {
		return &responsesBackend{base: openAIBase(), key: key, model: model}
	}
	return &openaiBackend{base: openAIBase(), key: key, model: model}
}
