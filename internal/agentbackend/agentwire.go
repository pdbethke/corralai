// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/agentworker"
)

// A wire is how one agent CLI hands back what an API backend gets from its
// HTTP response: the reply, the token usage, and — when the caller asked for
// constrained output — a reply shaped by a schema. Each CLI has its own JSON
// mode for this, so each built-in declares its wire and an operator
// definition declares one in CORRALAI_AGENT_<NAME>_WIRE (default `text`).
//
// Without a wire an agent seat reported `0 tokens` for every call — a
// could-not-measure rendered as a measured zero — and handed a CLI's error
// message on as if the model had said it. The parsers here are tested
// against real captured output (testdata/agentwire), not against what the
// CLIs are assumed to print.

// ErrSeatEnvironment marks a failure of the seat's ENVIRONMENT — a login, a
// usage limit, a model the account cannot use, the CLI itself missing or
// broken — as distinct from anything the model did. A caller that scores
// models must never charge one of these to the model (the fifth review found
// a 429 on the writer path charged to the model).
var ErrSeatEnvironment = errors.New("the agent seat's environment failed (login, usage limit, or the CLI itself) — not a model outcome")

type agentWire interface {
	// args returns extra argv for this call: a schema flag when f != nil.
	// tmp is the call's own scratch directory, removed after the call.
	args(f *agentworker.ResponseFormat, tmp string) ([]string, error)
	// parse reads the reply and usage from the CLI's stdout and the {out}
	// file. metered is false when the wire carries no usage at all, which
	// is not the same as a usage of zero.
	parse(stdout, outFile []byte) (content string, u Usage, metered bool, err error)
	// reportsUsage is whether this wire carries token usage AT ALL — the
	// question Metered asks before a run, when there is no reply to read
	// yet. A metered wire can still fail to report on one call (a CLI that
	// died before its usage record); parse's metered answers for that call,
	// and this answers for the wire.
	reportsUsage() bool
}

// agentWires is the one registry: a definition names its wire, and nothing
// else in corral decides how an agent's output is read.
var agentWires = map[string]agentWire{
	"claude-json": claudeJSONWire{},
	"codex-jsonl": codexJSONLWire{},
	"text":        textWire{},
}

func wireNames() []string {
	names := make([]string, 0, len(agentWires))
	for n := range agentWires {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// claudeJSONWire reads `claude -p --output-format json`: one result document.
type claudeJSONWire struct{}

func (claudeJSONWire) reportsUsage() bool { return true }

func (claudeJSONWire) args(f *agentworker.ResponseFormat, _ string) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	schema, err := json.Marshal(f.Schema)
	if err != nil {
		return nil, fmt.Errorf("response schema %s: %v", f.Name, err)
	}
	return []string{"--json-schema", string(schema)}, nil
}

func (claudeJSONWire) parse(stdout, _ []byte) (string, Usage, bool, error) {
	var doc struct {
		Type             string          `json:"type"`
		IsError          bool            `json:"is_error"`
		TerminalReason   string          `json:"terminal_reason"`
		APIErrorStatus   *int            `json:"api_error_status"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		Usage            struct {
			InputTokens              int    `json:"input_tokens"`
			OutputTokens             int    `json:"output_tokens"`
			CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &doc); err != nil {
		return "", Usage{}, false, fmt.Errorf("not a claude-json result document (%v): %s", err, oneline(string(stdout)))
	}
	if doc.Type != "result" {
		// Valid JSON that is not the result document — a CLI that ignored
		// --output-format, or a definition on the wrong wire. Reading its
		// absent .result as an empty reply would hand on nothing as the
		// model's answer.
		return "", Usage{}, false, fmt.Errorf("not a claude-json result document (type %q): %s", doc.Type, oneline(string(stdout)))
	}
	u := anthropicUsage(doc.Usage.InputTokens, doc.Usage.OutputTokens, doc.Usage.CacheReadInputTokens, doc.Usage.CacheCreationInputTokens)
	// `subtype` is NOT a success signal: the captured error document says
	// "success" beside is_error true. is_error and terminal_reason are.
	if doc.IsError || (doc.TerminalReason != "" && doc.TerminalReason != "completed") {
		status := ""
		if doc.APIErrorStatus != nil {
			status = fmt.Sprintf(", status %d", *doc.APIErrorStatus)
		}
		return "", u, true, fmt.Errorf("%w: claude reported an error (terminal_reason %q%s): %s", ErrSeatEnvironment, doc.TerminalReason, status, clipLine(doc.Result, seatErrorChars))
	}
	if s := bytes.TrimSpace(doc.StructuredOutput); len(s) > 0 && !bytes.Equal(s, []byte("null")) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, s); err == nil {
			return compact.String(), u, true, nil
		}
	}
	return doc.Result, u, true, nil
}

// codexJSONLWire reads `codex exec --json -o {out}`: an event stream on
// stdout, the final message in the {out} file.
type codexJSONLWire struct{}

func (codexJSONLWire) reportsUsage() bool { return true }

func (codexJSONLWire) args(f *agentworker.ResponseFormat, tmp string) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	schema, err := json.Marshal(f.Schema)
	if err != nil {
		return nil, fmt.Errorf("response schema %s: %v", f.Name, err)
	}
	path := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(path, schema, 0o600); err != nil {
		return nil, err
	}
	return []string{"--output-schema", path}, nil
}

func (codexJSONLWire) parse(stdout, outFile []byte) (string, Usage, bool, error) {
	var (
		u        Usage
		metered  bool
		lastText string
		failure  string
		events   int
	)
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens           int    `json:"input_tokens"`
				CachedInputTokens     *int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
				OutputTokens          int    `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type == "" {
			continue
		}
		events++
		switch ev.Type {
		case "turn.completed":
			// input_tokens already contains the cached half (the OpenAI
			// convention), so it is copied, not normalised. Summed, so a
			// stream of several turns reports all of them.
			metered = true
			u.InputTokens += ev.Usage.InputTokens
			u.OutputTokens += ev.Usage.OutputTokens
			u.CachedInputTokens = addPtr(u.CachedInputTokens, ev.Usage.CachedInputTokens)
			u.CacheWriteInputTokens = addPtr(u.CacheWriteInputTokens, ev.Usage.CacheWriteInputTokens)
		case "error":
			failure = ev.Message
		case "turn.failed":
			failure = ev.Error.Message
		case "item.completed":
			// An item of type "error" is a WARNING — the captured stream
			// carries one ("Defaulting to fallback metadata") before a turn
			// that goes on. Only the top-level events above end the call.
			if ev.Item.Type == "agent_message" {
				lastText = ev.Item.Text
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", u, metered, fmt.Errorf("reading the codex event stream: %v", err)
	}
	if failure != "" {
		return "", u, metered, fmt.Errorf("%w: codex reported an error: %s", ErrSeatEnvironment, codexErrorText(failure))
	}
	if events == 0 {
		return "", u, false, fmt.Errorf("not a codex-jsonl event stream: %s", oneline(string(stdout)))
	}
	content := lastText
	if len(bytes.TrimSpace(outFile)) > 0 {
		content = string(outFile)
	}
	if strings.TrimSpace(content) == "" {
		return "", u, metered, errors.New("codex returned no reply (no -o file and no agent message)")
	}
	return content, u, metered, nil
}

// codexErrorText is the readable part of a codex error message. The captured
// one is itself a JSON document — the provider's error body, with the words
// at .error.message and the HTTP status at .status — so it is unwrapped when
// it parses that way and passed through as it is when it does not.
func codexErrorText(msg string) string {
	var body struct {
		Status int `json:"status"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(msg), &body) == nil && body.Error.Message != "" {
		if body.Status != 0 {
			return clipLine(fmt.Sprintf("status %d: %s", body.Status, body.Error.Message), seatErrorChars)
		}
		return clipLine(body.Error.Message, seatErrorChars)
	}
	return clipLine(msg, seatErrorChars)
}

// seatErrorChars bounds a CLI's error text in an error message: long enough
// for a usage-limit notice with its reset time, short enough for one line.
const seatErrorChars = 400

// clipLine folds s onto one line and keeps its first n bytes.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// addPtr sums two nullable counters: nil + nil stays nil (nothing was
// reported), and a reported value is never lost to a nil beside it.
func addPtr(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	sum := *b
	if a != nil {
		sum += *a
	}
	return &sum
}

// textWire is any CLI corral cannot read structure from: the reply is the
// {out} file when the agent wrote one, else stdout. It carries no usage, so
// it is unmetered, and no schema flag — a schema reaches the model only as
// the prompt describes it.
type textWire struct{}

func (textWire) reportsUsage() bool { return false }

func (textWire) args(*agentworker.ResponseFormat, string) ([]string, error) { return nil, nil }

func (textWire) parse(stdout, outFile []byte) (string, Usage, bool, error) {
	content := string(stdout)
	if len(bytes.TrimSpace(outFile)) > 0 {
		content = string(outFile)
	}
	if strings.TrimSpace(content) == "" {
		return "", Usage{}, false, errors.New("returned an empty reply (nothing on stdout, nothing in {out})")
	}
	return content, Usage{}, false, nil
}
