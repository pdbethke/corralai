// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Agentic seats: a coding agent as the reviewer or the verifier. The seat
// is an AGENT DEFINITION — a command line, run in a disposable worktree at
// the commit with the brief on stdin; what it prints (or writes to `{out}`)
// is the reply. It reads the tree itself — no byte cap — and it hands back
// scripts; corral runs them. This is what the five cold reviews of corral
// were, by hand: a coding agent handed a checkout and told to break it.
//
// Three agents are defined here because we have run them; ANY agent is
// defined the same way by the operator, in the environment:
//
//	CORRALAI_AGENT_<NAME>="<command line>"
//	CORRALAI_AGENT_<NAME>_WIRE=claude-json | codex-jsonl | text   (optional; default text)
//
// with NAME the seat name upper-cased (`-` → `_`), and these words
// substituted: `{dir}` the worktree, `{out}` a file the agent may write its
// reply to, `{model}` the pinned model (`<name>:<model>` — required when
// the word is present, refused when it is absent), and `{model:FLAG}`
// which becomes `FLAG <model>` when pinned and nothing otherwise, and
// `{tools:FLAG}`, which becomes `FLAG <a,b,c>` for the tools the caller
// grants (SeatOptions.Tools) and `FLAG ""` — no tools — when it grants none.
// The wire (agentwire.go) is how corral reads the reply, the token usage and
// a response schema; a definition the operator writes is read as plain text
// unless it declares a wire, because corral cannot know what it prints.
// Corral does not confine what the command does; the definition is the
// operator's, and the record carries it.
var builtinAgents = map[string]builtinAgent{
	// The certify form is COLD: no settings sources (the operator's
	// CLAUDE.md, memory, hooks), no MCP servers, no slash commands or skills,
	// no session file, and no tools unless the caller names them. Unflagged,
	// a one-word call carried 108,740 input tokens of the operator's own
	// configuration; this form measured 620 (testdata/agentwire/README.md).
	// `--bare` would be leaner still but refuses subscription (OAuth) auth,
	// which is the point of an agent seat. --system-prompt REPLACES Claude
	// Code's default system prompt (2,972 tokens without it): corral's role
	// prompt arrives as the message, and the seat needs nothing else.
	"claude-code": {
		def:         `claude -p --output-format json --setting-sources "" --strict-mcp-config --disable-slash-commands --no-session-persistence --system-prompt "Follow the instructions in the user's message exactly." {tools:--tools} {model:--model}`,
		wire:        "claude-json",
		maker:       "anthropic",
		binary:      "claude",
		coldCeiling: 5000,
	},
	// codex loaded no operator instructions even unflagged (its own agent
	// prompt is ~14.7k tokens, mostly cached); --ignore-user-config and
	// --ignore-rules make that explicit rather than incidental, and
	// --ephemeral keeps no session. It has no tool switch: --sandbox
	// read-only is its confinement in every verb.
	"codex": {
		def:         "codex exec --json --ignore-user-config --ignore-rules --sandbox read-only --ephemeral --skip-git-repo-check -C {dir} -o {out} {model:-m} -",
		wire:        "codex-jsonl",
		maker:       "openai",
		binary:      "codex",
		coldCeiling: 20000,
	},
	// antigravity (`agy`) takes its prompt as a FLAG VALUE, not on stdin, so
	// the brief is read in by a shell wrapper; the model pin rides through
	// "$@" because {model:FLAG} is only substituted as a whole word.
	//
	// --dangerously-skip-permissions is load-bearing and weaker than the two
	// seats above, which are read-only (--tools Read,Grep,Glob; --sandbox
	// read-only). Headless agy auto-DENIES every tool permission it cannot
	// prompt for, so without it the seat returns an empty reply and reviews
	// nothing at all. It runs in a disposable worktree and nothing it does
	// itself reaches the record — only the scripts it hands back, which
	// corral runs. Stated here rather than left to be discovered.
	//
	// Its JSON mode has not been measured, so it is a text wire (unmetered)
	// and has no cold ceiling yet (spec open question 2).
	"antigravity": {
		def:    `sh -c 'agy --output-format text --sandbox --dangerously-skip-permissions --print-timeout 15m "$@" -p="$(cat)"' sh {model:--model}`,
		wire:   "text",
		maker:  "google",
		binary: "agy",
	},
}

// builtinAgent is one agent corral has run and therefore defines itself.
type builtinAgent struct {
	def string // the command line, placeholders intact
	// wire names how the reply, usage and a schema travel (agentWires).
	wire string
	// maker is the vendor of the models the CLI runs when it is not pinned.
	maker string
	// binary is the CLI the definition actually runs. It is data because a
	// definition may wrap the CLI in a shell (antigravity), where the first
	// word of the command line is the shell and says nothing about whether
	// the CLI is installed.
	binary string
	// coldCeiling is the input-token ceiling a one-line prompt must stay
	// under for the seat to count as cold; 0 = not yet measured.
	coldCeiling int
}

// agentEnvPrefix is where the operator's own agent definitions live.
const agentEnvPrefix = "CORRALAI_AGENT_"

// agentWireSuffix marks the variable that declares an operator agent's
// wire. A key ending in it is never itself an agent definition, so an agent
// cannot be named `<anything>-wire`.
const agentWireSuffix = "_WIRE"

// agentEnvKey is the variable that defines the seat called name.
func agentEnvKey(name string) string {
	return agentEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// agentDefinition is the command line for a seat name: a built-in, or the
// operator's, which wins over a built-in of the same name.
func agentDefinition(name string) (def string, builtin, ok bool) {
	key := agentEnvKey(name)
	if strings.HasSuffix(key, agentWireSuffix) {
		return "", false, false
	}
	if name != "" && !strings.ContainsAny(name, " \t/") {
		if v, set := os.LookupEnv(key); set && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), false, true
		}
	}
	if b, isBuiltin := builtinAgents[name]; isBuiltin {
		return b.def, true, true
	}
	return "", false, false
}

// agentWireFor is the wire a defined seat speaks: what its _WIRE variable
// declares, else the built-in's own when the definition IS the built-in's,
// else text. An operator who redefines a built-in's command line gets text,
// because their command need not print what the built-in's CLI prints.
func agentWireFor(name string, builtin bool) (string, agentWire, error) {
	key := AgentWireKey(name)
	wireName := "text"
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		wireName = v
	} else if builtin {
		wireName = builtinAgents[name].wire
	}
	w, ok := agentWires[wireName]
	if !ok {
		return "", nil, fmt.Errorf("agent %s: %s=%q names no wire (want one of %s)", name, key, wireName, strings.Join(wireNames(), ", "))
	}
	return wireName, w, nil
}

// Note: an Ollama tag such as `codex:latest` reads as the codex agent seat
// pinned to the model "latest" — the first word wins, so a local model whose
// name collides with a defined agent must be renamed or the agent undefined.
//
// AgentSeat splits "claude-code:claude-sonnet-5" into agent name and
// model; ok is false for an API model name (a spec whose first word names
// no agent).
func AgentSeat(spec string) (name, model string, ok bool) {
	name, model, _ = strings.Cut(strings.TrimSpace(spec), ":")
	if _, _, defined := agentDefinition(name); !defined {
		return "", "", false
	}
	return name, strings.TrimSpace(model), true
}

// SeatModelOf is the model a seat spec resolves to for the decorrelation
// rule: an API seat is its own name; an agentic seat pinned to a model is
// that model; an unpinned agentic seat is the agent.
func SeatModelOf(spec string) string {
	if name, model, ok := AgentSeat(spec); ok {
		if model == "" {
			return name
		}
		return model
	}
	return strings.TrimSpace(spec)
}

// SeatNamesAModel reports whether spec identifies an actual MODEL, or only
// the tool that ran one. An API seat is its own model name. An agentic seat
// is one only when pinned (`agent:model`); unpinned, the CLI chooses its own
// model and corral is not told which, so the record must not claim to know.
//
// This is deliberately NOT SeatModelOf's question. That one asks "what does
// the decorrelation rule compare", and answering "the agent" is right there —
// two unpinned claude-code seats ARE correlated. This one asks "may a
// per-model analysis use this row", where the same answer is a no.
func SeatNamesAModel(spec string) bool {
	if _, model, ok := AgentSeat(spec); ok {
		return model != ""
	}
	return strings.TrimSpace(spec) != ""
}

// agentBackend runs the agent's command once per Chat.
type agentBackend struct {
	name, model, dir string
	argv             []string // the definition, split; placeholders intact
	wire             agentWire
	tools            []string // what {tools:FLAG} grants; nil = none
	timeout          time.Duration
}

// AgentVersion is what the agent's binary prints for --version, for the
// record: an agent's row in models rank is a tool AND a version AND a
// model. An operator-defined agent's record also carries its definition,
// since that is what sat in the seat.
func AgentVersion(name string) string {
	def, builtin, ok := agentDefinition(name)
	if !ok {
		return ""
	}
	argv, err := splitWords(def)
	if err != nil || len(argv) == 0 {
		return ""
	}
	// argv[0] is the agent's binary — UNLESS the definition wraps it in a
	// shell, which a seat must do when its CLI takes the brief as a flag
	// value rather than on stdin (antigravity). Probing `sh --version` there
	// would put the SHELL's version on the record as the seat's provenance:
	// a signed entry naming the wrong thing as the reviewer. Report no
	// version rather than a wrong one, and let the definition speak.
	version := ""
	if !isShell(argv[0]) {
		if out, err := exec.Command(argv[0], "--version").Output(); err == nil { // #nosec G204 -- the operator's own agent binary, as they defined it
			version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		}
	}
	if builtin && version != "" {
		return version
	}
	return strings.TrimSpace(version + " [" + def + "]")
}

// ErrBinaryNotChecked is AgentBinary's answer for an operator definition that
// wraps its CLI in a shell: the CLI is inside a script corral does not parse,
// so it cannot say whether it is installed, and says so rather than looking
// at the shell and reporting that.
var ErrBinaryNotChecked = errors.New("binary not checked (shell-wrapped definition)")

// AgentBinary checks, without running anything, that the agent's CLI can be
// found: `doctor` needs a free answer to "is it installed" that spends no
// call. A built-in definition names its real binary (builtinAgent.binary); an
// operator's own definition is checked by its first word, unless that word is
// a shell, in which case ErrBinaryNotChecked.
func AgentBinary(name string) error {
	def, builtin, ok := agentDefinition(name)
	if !ok {
		return fmt.Errorf("agent %q is not defined (define it as %s=\"<command line>\")", name, agentEnvKey(name))
	}
	prog := ""
	if builtin {
		prog = builtinAgents[name].binary
	}
	if prog == "" {
		argv, err := splitWords(def)
		if err != nil || len(argv) == 0 {
			return fmt.Errorf("agent %s: its definition %q does not parse", name, def)
		}
		if isShell(argv[0]) {
			return ErrBinaryNotChecked
		}
		prog = argv[0]
	}
	if _, err := exec.LookPath(prog); err != nil {
		return fmt.Errorf("agent %s: binary %q not found: %v", name, prog, err)
	}
	return nil
}

// isShell reports whether a definition's first word is a shell rather than
// the agent itself, so its --version is not mistaken for the seat's.
func isShell(prog string) bool {
	switch filepath.Base(prog) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		return true
	}
	return false
}

// expandAgentArgv substitutes the placeholders. A bare {model} with no
// pinned model, or a pinned model with no {model} word to carry it, is a
// refusal by name — corral never guesses what an agent runs.
func expandAgentArgv(name string, argv []string, model, dir, out string, tools []string) ([]string, error) {
	carries := false
	var expanded []string
	for _, w := range argv {
		switch {
		case w == "{model}":
			carries = true
			if model == "" {
				return nil, fmt.Errorf("agent %s takes a model ({model} in its definition) — pin one as %s:<model>", name, name)
			}
			expanded = append(expanded, model)
		case strings.HasPrefix(w, "{model:") && strings.HasSuffix(w, "}"):
			carries = true
			if model != "" {
				expanded = append(expanded, w[len("{model:"):len(w)-1], model)
			}
		case strings.HasPrefix(w, "{tools:") && strings.HasSuffix(w, "}"):
			// Always the flag, so "no tools" is SAID: a CLI left to its own
			// default would hand the seat every tool it has.
			expanded = append(expanded, w[len("{tools:"):len(w)-1], strings.Join(tools, ","))
		default:
			expanded = append(expanded, strings.NewReplacer("{dir}", dir, "{out}", out).Replace(w))
		}
	}
	if model != "" && !carries {
		return nil, fmt.Errorf("agent %s takes no model (no {model} in its definition) — %s:%s pins one it cannot carry", name, name, model)
	}
	if len(expanded) == 0 {
		return nil, fmt.Errorf("agent %s has an empty definition", name)
	}
	return expanded, nil
}

// Chat runs the agent once. The wire supplies the schema flag (when the
// caller asked for constrained output — found by splitFormat, exactly as the
// API backends find it) and reads the reply and usage back. A failure the
// CLI REPORTS (an error record, a missing binary) is ErrSeatEnvironment; any
// other failure is a plain error naming the seat. Neither is ever content.
func (a agentBackend) Chat(messages []Message, tools []any) (Message, error) {
	var prompt strings.Builder
	for _, m := range messages {
		if prompt.Len() > 0 {
			prompt.WriteString("\n\n")
		}
		prompt.WriteString(m.Content)
	}
	_, format := splitFormat(tools)
	// The call's own scratch — {out} and any schema file — kept apart from
	// the seat's directory, which a certify seat sees empty.
	scratch, err := os.MkdirTemp("", "corral-agent-*")
	if err != nil {
		return Message{}, err
	}
	defer os.RemoveAll(scratch)
	outFile := filepath.Join(scratch, "out.txt")
	if err := os.WriteFile(outFile, nil, 0o600); err != nil {
		return Message{}, err
	}
	dir := a.dir
	if dir == "" {
		d, err := os.MkdirTemp("", "corral-seat-*")
		if err != nil {
			return Message{}, err
		}
		defer os.RemoveAll(d)
		dir = d
	}
	argv, err := expandAgentArgv(a.name, a.argv, a.model, dir, outFile, a.tools)
	if err != nil {
		return Message{}, err
	}
	extra, err := a.wire.args(format, scratch)
	if err != nil {
		return Message{}, fmt.Errorf("%s: %w", a.name, err)
	}
	argv = withWireArgs(argv, extra)
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- the operator's own agent definition, run in a disposable worktree
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return Message{}, fmt.Errorf("%s: %w: %v", a.name, ErrSeatEnvironment, runErr)
	}
	out, _ := os.ReadFile(outFile) // #nosec G304 -- our own temp file
	content, u, metered, perr := a.wire.parse(stdout.Bytes(), out)
	// Carried on the reply, success or failure: a call the wire could not
	// meter ran all the same, and a meter that saw zeros without this flag
	// would record a measured free call.
	u.Unmetered = !metered
	if runErr != nil {
		tail := tailOf(strings.TrimSpace(stderr.String()), 800)
		if errors.Is(perr, ErrSeatEnvironment) {
			return Message{Usage: u}, fmt.Errorf("%s: %w (%v): %s", a.name, perr, runErr, tail)
		}
		return Message{Usage: u}, fmt.Errorf("%s: %v: %s", a.name, runErr, tail)
	}
	if perr != nil {
		return Message{Usage: u}, fmt.Errorf("%s: %w", a.name, perr)
	}
	return Message{Role: "assistant", Content: content, Usage: u}, nil
}

// withWireArgs places a wire's extra flags: before a trailing `-` (the
// stdin marker, which must stay last), else at the end.
func withWireArgs(argv, extra []string) []string {
	if len(extra) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(extra))
	if n := len(argv); n > 1 && argv[n-1] == "-" {
		out = append(append(append(out, argv[:n-1]...), extra...), "-")
		return out
	}
	return append(append(out, argv...), extra...)
}

// newAgentSeat is the backend for an agentic seat in dir ("" = a fresh empty
// directory per call), or nil, false for an API seat. A definition that does
// not split, or that cannot carry the pin, is an error by name.
func newAgentSeat(spec string, opt SeatOptions) (Backend, bool, error) {
	dir, timeout := opt.Dir, opt.Timeout
	name, model, ok := AgentSeat(spec)
	if !ok {
		return nil, false, nil
	}
	def, builtin, _ := agentDefinition(name)
	argv, err := splitWords(def)
	if err != nil {
		return nil, true, fmt.Errorf("agent %s: %v in its definition %q", name, err, def)
	}
	if _, err := expandAgentArgv(name, argv, model, dir, "out", opt.Tools); err != nil {
		return nil, true, err
	}
	_, wire, err := agentWireFor(name, builtin)
	if err != nil {
		return nil, true, err
	}
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	if timeout <= 0 {
		timeout = defaultSeatTimeout
	}
	return agentBackend{name: name, model: model, dir: dir, argv: argv, wire: wire, tools: opt.Tools, timeout: timeout}, true, nil
}

// Metered reports whether b reports the token usage of its calls, which is
// what a --max-tokens cap needs before it can bound a seat. Every API backend
// does. An agent seat does exactly when its WIRE carries usage: the same
// agent name is metered on claude-json or codex-jsonl and not on text, so
// the wire answers, never the agent's name.
func Metered(b Backend) bool {
	if a, ok := b.(agentBackend); ok {
		return a.wire.reportsUsage()
	}
	return true
}

// AgentWireKey is the variable that declares the agent NAME's wire, for an
// error that tells the operator where to change it.
func AgentWireKey(name string) string {
	return agentEnvKey(name) + agentWireSuffix
}

// defaultSeatTimeout bounds one agent call when the caller names none.
const defaultSeatTimeout = 15 * time.Minute

// SeatOptions are how a caller places an agent seat. They mean nothing to an
// API seat.
type SeatOptions struct {
	Dir     string        // the agent's working tree; "" = a fresh empty temp dir per call, removed after
	Timeout time.Duration // per call; 0 = 15m
	// Tools are what a definition's {tools:FLAG} grants the agent; nil =
	// none. A certify seat's prompt carries the code and needs none;
	// review grants read-only tools because its seat reads the tree.
	Tools []string
}

// ForSeat is the single resolver for a seat: spec is either an agent seat
// (`codex`, `claude-code:claude-sonnet-5`, any CORRALAI_AGENT_<NAME>) or a
// model name. An agent seat is driven as a CLI; anything else defers to
// ForModelOrLocal, so a name with no cloud-vendor prefix is still served by
// the local daemon, as review's seats always were.
//
// The deferral keeps ForModel's fail-closed guarantee for every CLOUD name: a
// claude-/gemini-/gpt- model whose key is missing returns the actionable
// credential error and is NEVER quietly re-served by a local model (the
// local fallback applies only to names VendorOf does not recognise). certify's
// up-front credential check relies on that. An empty spec returns the same
// error ForModel("") does.
//
// An agent seat does not implement ModelSwitcher: it is one tool and one
// model, and WithModel would silently change what sat in the seat.
func ForSeat(spec string, opt SeatOptions) (Backend, error) {
	if b, isAgent, err := newAgentSeat(spec, opt); isAgent {
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	if strings.TrimSpace(spec) == "" {
		return ForModel(spec)
	}
	return ForModelOrLocal(spec)
}

// DefinedAgents lists the seat names an operator can use right now — the
// built-ins and everything CORRALAI_AGENT_* defines — for help and errors.
func DefinedAgents() []string {
	seen := map[string]bool{}
	for n := range builtinAgents {
		seen[n] = true
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, agentEnvPrefix) && !strings.HasSuffix(k, agentWireSuffix) && strings.TrimSpace(v) != "" {
			seen[strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(k, agentEnvPrefix), "_", "-"))] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// splitWords splits a command line the way a POSIX shell would split
// words: whitespace separates, single quotes are literal, double quotes
// and backslashes escape. No expansion — the definition is argv, not a
// script; an agent that needs a pipeline gets a wrapper script.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord, single, double := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\\' && !single:
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing backslash")
			}
			i++
			cur.WriteByte(s[i])
			inWord = true
		case double:
			if c == '"' {
				double = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			single, inWord = true, true
		case c == '"':
			double, inWord = true, true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if single || double {
		return nil, fmt.Errorf("unterminated quote")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// tailOf keeps the last n bytes of s, for error text. oneline's head-truncating
// shape does not fit: the end of an agent's stderr is where it says why it died.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
