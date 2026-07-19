// SPDX-License-Identifier: Elastic-2.0

// Package agentworker runs ONE advpool role task (mutant-generator,
// test-writer, test-critic) against a single model, in-process — no queue
// claim/complete, no MCP round trip, no brain callback. It exists so both
// cmd/corral-agent's claim loop AND the future `corral certify --local`
// command drive the exact same per-role LLM interaction instead of
// duplicating it: corral-agent wraps RunRole with claiming, retry, and
// brain-side finding/telemetry reporting; --local calls it directly and
// collects the test-critic's findings straight into the in-process run.
//
// The logic here is lifted from cmd/corral-agent/main.go's structured fast
// path (isStructuredRole) and runCriticLoop, kept behaviorally in lockstep —
// see the doc comments on isStructuredRole/isPoolCriticRole/critFreeformSteps
// below, which mirror the originals.
package agentworker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
)

// Message is one turn in a tool-calling chat exchange — the shape every
// cmd/corral-agent Backend already produces/consumes (corral-agent's own
// `omsg`, structurally mirrored here so this package doesn't need to import
// a `main` package). ToolCalls carries a model's function-call request, when
// the model made one instead of (or alongside) replying in Content.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is one function-call request inside a Message: the tool name and
// its raw (not-yet-decoded) argument object.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Chatter is the one capability RunRole needs from a model backend: turn a
// message history (+ optional tool schemas, OpenAI-style function
// descriptors) into the model's next turn. cmd/corral-agent's Backend
// interface already has this exact shape (Chat(messages []omsg, tools []any)
// (omsg, error)); its call sites adapt to Chatter with a thin wrapper that
// converts omsg<->Message, so every existing Backend implementation
// (ollama/openai/anthropic) satisfies RunRole's needs without changing them.
type Chatter interface {
	Chat(messages []Message, tools []any) (Message, error)
}

// isStructuredRole reports whether role produces a typed artifact (mutant
// list / test source) for the brain/validator to re-parse, rather than a
// freeform tool-loop summary. Kept in lockstep with cmd/corral-agent's
// isStructuredRole, which classifies the same roles for the daemon-side
// worker — both must list the shadow seat, since either process can be the
// one that runs it.
//
// The role names are string LITERALS, not advpool constants, deliberately:
// this package mirrors advpool's shapes rather than importing it (see the
// package doc). Importing advpool for one constant pulled internal/buildstore
// — and with it DuckDB — into every binary that links this package, including
// cmd/corral-agent (measured: 0 duckdb packages in `go list -deps
// ./cmd/corral-agent` before that import, 3 after). A worker binary must not
// grow a database driver to name a role.
func isStructuredRole(role string) bool {
	// mutant-generator-shadow (the Task 6 challenger seat) uses the identical
	// structured single-shot path as its primary — it renders the SAME testgen
	// prompt shape, just under a different model/role key, and is never routed
	// through the critic's freeform tool loop.
	return role == "test-writer" || role == "mutant-generator" || role == "mutant-generator-shadow"
}

// isPoolCriticRole mirrors cmd/corral-agent's isPoolCriticRole.
func isPoolCriticRole(role string) bool { return role == "test-critic" }

// critFreeformSteps bounds the critic's tool loop — mirrors
// cmd/corral-agent's constant of the same name (the general path is 15).
const critFreeformSteps = 6

// RunRole executes ONE advpool role task with a single model and returns the
// raw result (for structured roles: mutant-generator / test-writer — the
// caller re-parses) or the filed findings (for test-critic). No queue, no
// MCP — just the one LLM interaction the role needs.
//
// For mutant-generator / test-writer this is exactly cmd/corral-agent's
// structured fast path: one Chat call with instruction as the sole prompt,
// and the model's raw output returned verbatim — RunRole does NOT parse
// mutants/tests itself (the brain-side Validator does; parsing here would
// duplicate and risk diverging from that logic).
//
// For test-critic this replicates runCriticLoop's tool-calling findings
// loop, with one necessary translation: cmd/corral-agent's loop reports
// findings by calling out to a live brain over MCP (which assigns them a
// mission/task scope and a database id); RunRole has no brain to call, so it
// files them directly as queue.Finding values (MissionID/TaskID left at 0 —
// queue.Finding documents 0 as "standalone") and returns them to the caller
// instead of sending them anywhere.
func RunRole(ctx context.Context, model Chatter, role, instruction string) (result string, findings []queue.Finding, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if isStructuredRole(role) {
		m, err := model.Chat([]Message{{Role: "user", Content: instruction}}, nil)
		if err != nil {
			return "", nil, err
		}
		return m.Content, nil, nil
	}
	if isPoolCriticRole(role) {
		summary, findings, err := runCriticLoop(model, instruction)
		if err != nil {
			return "", nil, err
		}
		return summary, findings, nil
	}
	return "", nil, fmt.Errorf("agentworker: role %q has no single-shot runner", role)
}

// criticTools returns the test-critic's restricted toolset: report_finding
// and report_thought only — mirrors cmd/corral-agent's criticTools (the
// schemas are copied verbatim from agentTools' fn("report_finding", ...) /
// fn("report_thought", ...) definitions).
func criticTools() []any {
	fn := func(name, desc string, props map[string]any, req ...string) any {
		if req == nil {
			req = []string{}
		}
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": req},
		}}
	}
	return []any{
		fn("report_finding", "Report a vulnerability, bug, design flaw, missing requirement, or regression you discovered, with a severity. The swarm records it and re-plans around it.",
			map[string]any{
				"type":             map[string]any{"type": "string", "description": "vuln|bug|design-flaw|missing-req|regression|note"},
				"severity":         map[string]any{"type": "string", "description": "low|medium|high|critical"},
				"target":           map[string]any{"type": "string", "description": "the file or area affected"},
				"evidence":         map[string]any{"type": "string", "description": "what you observed"},
				"suggested_action": map[string]any{"type": "string", "description": "how to fix it"},
			}, "type", "severity"),
		fn("report_thought", "Report a short, substantive piece of YOUR OWN reasoning — what you're examining, deciding, or finding right now, in your own words.",
			map[string]any{"text": map[string]any{"type": "string"}}, "text"),
	}
}

// runCriticLoop runs the test-critic to a conclusion: read the dev tests (in
// instruction), file a queue.Finding per vacuous test, then stop. Mirrors
// cmd/corral-agent's runCriticLoop — deliberately NOT the general builder
// loop: a focused prompt + restricted tools (report_finding/report_thought
// only) + a tight step budget (critFreeformSteps) + a report_thought cap
// keep it from burning steps on reflection or a hallucinated tool without
// ever concluding.
func runCriticLoop(model Chatter, instruction string) (string, []queue.Finding, error) {
	sys := `You are a TEST CRITIC in an adversarial audit. Your ONLY job: read the developer's tests provided below and decide whether any are vacuous, tautological, or designed to pass without exercising the goal.

You have EXACTLY two tools: report_finding (file one flaw — name the test and say what it fails to check) and report_thought (optional, at most twice). You CANNOT write files or run commands; do not attempt to.

Procedure: (1) read the tests; (2) call report_finding once per vacuous test — or none if the tests are sound; (3) reply with a one-line summary to finish. Do not narrate. Conclude within a few steps.

Task: ` + instruction
	messages := []Message{{Role: "system", Content: sys}, {Role: "user", Content: "Begin. Read the tests, file findings, then finish."}}
	tools := criticTools()
	last := "reviewed the dev tests"
	thoughts := 0
	var findings []queue.Finding
	for step := 0; step < critFreeformSteps; step++ {
		m, err := model.Chat(messages, tools)
		if err != nil {
			return "", nil, err
		}
		callName, args, ok := extractCall(m)
		if !ok {
			if c := oneline(m.Content); c != "" {
				last = c
			}
			return last, findings, nil
		}
		if args == nil {
			args = map[string]any{}
		}
		var nudge string
		switch callName {
		case "report_finding":
			findings = append(findings, findingFromArgs(args))
			nudge = `{"ok":true}`
		case "report_thought":
			thoughts++
			nudge = `{"ok":true}`
			if thoughts >= 2 {
				nudge = "You have reflected enough. Now call report_finding for each vacuous test (or none if the tests are sound), then reply with a one-line summary to finish."
			}
		default:
			nudge = fmt.Sprintf(`{"error":"unknown tool %q"}`, callName)
		}
		messages = append(messages,
			Message{Role: "assistant", Content: assistantEcho(m.Content, callName, args)},
			Message{Role: "user", Content: fmt.Sprintf("[result of %s] %s", callName, nudge)})
	}
	return last, findings, nil
}

// findingFromArgs builds a queue.Finding from a report_finding tool call's
// arguments — the same fields cmd/corral-agent forwards to the brain's
// report_finding, reconstructed locally since RunRole has no brain to call.
// MissionID/TaskID are left at 0 (queue.Finding documents that as
// "standalone"); Status defaults to open, matching AddFinding's behavior.
func findingFromArgs(args map[string]any) queue.Finding {
	str := func(k string) string { s, _ := args[k].(string); return s }
	return queue.Finding{
		Type:            str("type"),
		Severity:        str("severity"),
		Target:          str("target"),
		Evidence:        str("evidence"),
		SuggestedAction: str("suggested_action"),
		Status:          queue.FindingOpen,
		CreatedTS:       float64(time.Now().Unix()),
	}
}

// ---- tool-call extraction (mirrors cmd/corral-agent's extractCall/oneline/
// assistantEcho verbatim, adapted to the Message/ToolCall types above) ----

var fence = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
var callish = regexp.MustCompile(`(?s)\b([a-zA-Z_][a-zA-Z0-9_]*)\s*\(\s*(\{.*\})\s*\)`)

// extractCall returns the tool name + raw args the model wants, handling BOTH
// the structured ToolCalls field AND the common fallback where the model
// emits the call as JSON in content (optionally fenced), or as a textual
// `tool_name({...})` invocation.
func extractCall(m Message) (name string, args map[string]any, ok bool) {
	if len(m.ToolCalls) > 0 {
		tc := m.ToolCalls[0]
		if json.Unmarshal(tc.Arguments, &args) != nil || args == nil {
			// OpenAI-compatible backends return arguments as a JSON-encoded STRING,
			// not an object (Ollama returns the object). Handle both.
			var s string
			if json.Unmarshal(tc.Arguments, &s) == nil {
				_ = json.Unmarshal([]byte(s), &args)
			}
		}
		return tc.Name, args, true
	}
	raw := strings.TrimSpace(m.Content)
	if mt := fence.FindStringSubmatch(raw); mt != nil {
		raw = mt[1]
	}
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		raw = raw[i:]
		if j := strings.LastIndexByte(raw, '}'); j >= 0 {
			raw = raw[:j+1]
		}
	}
	var probe struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Params    map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err == nil && probe.Name != "" {
		a := probe.Arguments
		if a == nil {
			a = probe.Params
		}
		return probe.Name, a, true
	}
	if mt := callish.FindStringSubmatch(m.Content); mt != nil {
		var a map[string]any
		if json.Unmarshal([]byte(mt[2]), &a) == nil {
			return mt[1], a, true
		}
	}
	return "", nil, false
}

// assistantEcho is what goes into the transcript as the assistant's turn
// after a tool call — synthesized when content is empty (native tool-calling
// often leaves Content empty) so an amnesiac model still sees what it did.
func assistantEcho(content, callName string, args map[string]any) string {
	if strings.TrimSpace(content) != "" {
		return content
	}
	return fmt.Sprintf("I called %s(%s) and I am using its result.", callName, oneline(jsons(args)))
}

func jsons(v any) string { b, _ := json.Marshal(v); return string(b) }

func oneline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 110 {
		s = s[:110] + "…"
	}
	return s
}
