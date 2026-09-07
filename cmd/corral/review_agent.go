// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// Agentic seats: a coding agent as the reviewer or the verifier. The seat
// is an AGENT DEFINITION — a command line, run in a disposable worktree at
// the commit with the brief on stdin; what it prints (or writes to `{out}`)
// is the reply. It reads the tree itself — no byte cap — and it hands back
// scripts; corral runs them. This is what the five cold reviews of corral
// were, by hand: a coding agent handed a checkout and told to break it.
//
// Two agents are defined here because we have run them; ANY agent is
// defined the same way by the operator, in the environment:
//
//	CORRALAI_AGENT_<NAME>="<command line>"
//
// with NAME the seat name upper-cased (`-` → `_`), and these words
// substituted: `{dir}` the worktree, `{out}` a file the agent may write its
// reply to, `{model}` the pinned model (`<name>:<model>` — required when
// the word is present, refused when it is absent), and `{model:FLAG}`
// which becomes `FLAG <model>` when pinned and nothing otherwise. Corral
// does not confine what the command does; the definition is the operator's,
// and the record carries it.
var builtinAgents = map[string]string{
	"claude-code": "claude -p --output-format text --tools Read,Grep,Glob {model:--model}",
	"codex":       "codex exec --sandbox read-only --skip-git-repo-check -C {dir} -o {out} {model:-m} -",
}

// agentEnvPrefix is where the operator's own agent definitions live.
const agentEnvPrefix = "CORRALAI_AGENT_"

// agentDefinition is the command line for a seat name: a built-in, or the
// operator's, which wins over a built-in of the same name.
func agentDefinition(name string) (def string, builtin, ok bool) {
	key := agentEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if name != "" && !strings.ContainsAny(name, " \t/") {
		if v, set := os.LookupEnv(key); set && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), false, true
		}
	}
	if d, isBuiltin := builtinAgents[name]; isBuiltin {
		return d, true, true
	}
	return "", false, false
}

// agentSeat splits "claude-code:claude-sonnet-5" into agent name and
// model; ok is false for an API model name (a spec whose first word names
// no agent).
func agentSeat(spec string) (name, model string, ok bool) {
	name, model, _ = strings.Cut(strings.TrimSpace(spec), ":")
	if _, _, defined := agentDefinition(name); !defined {
		return "", "", false
	}
	return name, strings.TrimSpace(model), true
}

// seatModelOf is the model a seat spec resolves to for the decorrelation
// rule: an API seat is its own name; an agentic seat pinned to a model is
// that model; an unpinned agentic seat is the agent.
func seatModelOf(spec string) string {
	if name, model, ok := agentSeat(spec); ok {
		if model == "" {
			return name
		}
		return model
	}
	return strings.TrimSpace(spec)
}

// agentBackend runs the agent's command once per Chat.
type agentBackend struct {
	name, model, dir string
	argv             []string // the definition, split; placeholders intact
	timeout          time.Duration
}

// agentVersion is what the agent's binary prints for --version, for the
// record: an agent's row in models rank is a tool AND a version AND a
// model. An operator-defined agent's record also carries its definition,
// since that is what sat in the seat.
func agentVersion(name string) string {
	def, builtin, ok := agentDefinition(name)
	if !ok {
		return ""
	}
	argv, err := splitWords(def)
	if err != nil || len(argv) == 0 {
		return ""
	}
	version := ""
	if out, err := exec.Command(argv[0], "--version").Output(); err == nil { // #nosec G204 -- the operator's own agent binary, as they defined it
		version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	if builtin {
		return version
	}
	return strings.TrimSpace(version + " [" + def + "]")
}

// expandAgentArgv substitutes the placeholders. A bare {model} with no
// pinned model, or a pinned model with no {model} word to carry it, is a
// refusal by name — corral never guesses what an agent runs.
func expandAgentArgv(name string, argv []string, model, dir, out string) ([]string, error) {
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

func (a agentBackend) Chat(messages []agentbackend.Message, _ []any) (agentbackend.Message, error) {
	var prompt strings.Builder
	for _, m := range messages {
		if prompt.Len() > 0 {
			prompt.WriteString("\n\n")
		}
		prompt.WriteString(m.Content)
	}
	outFile := ""
	for _, w := range a.argv {
		if strings.Contains(w, "{out}") {
			f, err := os.CreateTemp("", "corral-agent-*.txt")
			if err != nil {
				return agentbackend.Message{}, err
			}
			outFile = f.Name()
			f.Close()
			defer os.Remove(outFile)
			break
		}
	}
	argv, err := expandAgentArgv(a.name, a.argv, a.model, a.dir, outFile)
	if err != nil {
		return agentbackend.Message{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- the operator's own agent definition, run in a disposable worktree
	cmd.Dir = a.dir
	cmd.Stdin = strings.NewReader(prompt.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return agentbackend.Message{}, fmt.Errorf("%s: %v: %s", a.name, err, tail(strings.TrimSpace(stderr.String()), 800))
	}
	content := stdout.String()
	if outFile != "" {
		if b, err := os.ReadFile(outFile); err == nil && len(bytes.TrimSpace(b)) > 0 { // #nosec G304 -- our own temp file
			content = string(b)
		}
	}
	return agentbackend.Message{Role: "assistant", Content: content}, nil
}

// newAgentSeat is the backend for an agentic seat in dir (a disposable
// worktree), or nil, false for an API seat. A definition that does not
// split, or that cannot carry the pin, is an error by name.
func newAgentSeat(spec, dir string, timeout time.Duration) (agentbackend.Backend, bool, error) {
	name, model, ok := agentSeat(spec)
	if !ok {
		return nil, false, nil
	}
	def, _, _ := agentDefinition(name)
	argv, err := splitWords(def)
	if err != nil {
		return nil, true, fmt.Errorf("agent %s: %v in its definition %q", name, err, def)
	}
	if _, err := expandAgentArgv(name, argv, model, dir, "out"); err != nil {
		return nil, true, err
	}
	return agentBackend{name: name, model: model, dir: filepath.Clean(dir), argv: argv, timeout: timeout}, true, nil
}

// definedAgents lists the seat names an operator can use right now — the
// built-ins and everything CORRALAI_AGENT_* defines — for help and errors.
func definedAgents() []string {
	seen := map[string]bool{}
	for n := range builtinAgents {
		seen[n] = true
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, agentEnvPrefix) && strings.TrimSpace(v) != "" {
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
