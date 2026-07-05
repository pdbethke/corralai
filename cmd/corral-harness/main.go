// SPDX-License-Identifier: Elastic-2.0

// corral-harness loops a HEADLESS CODING AGENT — Claude Code, Gemini CLI,
// Codex, any harness with a non-interactive mode and MCP support — as a swarm
// bee. The bee contract is nothing but MCP tool calls against the brain
// (bootstrap → claim_task → work → complete_task), so the harness brings its
// own model, its own auth (e.g. a Claude Max subscription instead of API
// billing), its own tool loop, and its own sandbox; corralai supplies the
// coordination, the queue, the verification gates, the memory, and the audit
// trail. corral-agent is merely the reference implementation of the same
// contract.
//
// One task per invocation: each harness run gets a fresh context, claims ONE
// task, works it, completes it, and exits. When the queue is empty the bee
// prints IDLE and the launcher backs off. Configuration is all env:
//
//	CORRAL_BRAIN   brain URL (default http://localhost:9019)
//	AGENT_NAME       swarm name (default Harness)
//	AGENT_ROLE       role(s) to serve (default builder): a single role, a
//	               comma-separated list (e.g. "researcher,designer,tester") to
//	               claim any ready task in that set, or "any"/"*"/empty to
//	               claim ANY ready task as a pure generalist
//	AGENT_MODEL      the model driving this harness (e.g. gpt-5.1-codex); adds a
//	               report_host step so findings attribute to it in model_comparison
//	AGENT_BACKEND    the backend/vendor for AGENT_MODEL (e.g. openai, anthropic, gemini)
//	AGENT_WORKSPACE  working directory for the harness (default .)
//	HARNESS_CMD    command template; placeholders: {prompt} {mcp_config} {brain}
//	               e.g. claude -p {prompt} --mcp-config {mcp_config} \
//	                    --allowedTools "mcp__corral__*,Read,Write,Edit,Bash" \
//	                    --permission-mode acceptEdits
//	HARNESS_DESC   how to announce this harness (default derived from HARNESS_CMD)
//	AGENT_ROUNDS     max tasks to run, 0 = forever (default 0)
//	HARNESS_TIMEOUT_SECONDS  per-invocation kill deadline (default 900)
//	HARNESS_IDLE_SECONDS     backoff when the queue is empty (default 30)
//	AGENT_PROMPT_FILE optional file replacing the built-in bee prompt; the same
//	               placeholders are substituted into it
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentrole"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	var n int
	if _, err := fmt.Sscanf(os.Getenv(k), "%d", &n); err == nil && n > 0 {
		return n
	}
	return d
}

func usageText() string {
	return `corral-harness — loops a headless coding agent (Claude Code, Gemini CLI, Codex, ...) as a swarm bee over MCP

Usage:
  corral-harness         claim one task, work it, complete it, exit
  corral-harness -h      print this help and exit

Env:
  CORRAL_BRAIN   brain URL (default http://localhost:9019)
  AGENT_NAME       swarm name (default Harness)
  AGENT_ROLE       role(s) to serve (default builder): a single role, a
                 comma-separated list (e.g. "researcher,designer,tester") to
                 claim any ready task in that set, or "any"/"*"/empty to
                 claim ANY ready task as a pure generalist
  AGENT_MODEL      the model driving this harness (e.g. gpt-5.1-codex); adds a
                 report_host step so findings attribute to it in model_comparison
  AGENT_BACKEND    the backend/vendor for AGENT_MODEL (e.g. openai, anthropic, gemini)
  AGENT_WORKSPACE  working directory for the harness (default .)
  HARNESS_CMD    command template; placeholders: {prompt} {mcp_config} {brain}
                 e.g. claude -p {prompt} --mcp-config {mcp_config} \
                      --allowedTools "mcp__corral__*,Read,Write,Edit,Bash" \
                      --permission-mode acceptEdits
  HARNESS_DESC   how to announce this harness (default derived from HARNESS_CMD)
  AGENT_ROUNDS     max tasks to run, 0 = forever (default 0)
  HARNESS_TIMEOUT_SECONDS  per-invocation kill deadline (default 900)
  HARNESS_IDLE_SECONDS     backoff when the queue is empty (default 30)
  AGENT_PROMPT_FILE optional file replacing the built-in bee prompt; the same
                 placeholders are substituted into it
`
}

// mcpConfig is the de-facto standard .mcp.json shape understood by Claude
// Code, Gemini CLI, Cursor, and friends.
func mcpConfig(brain string) string {
	return fmt.Sprintf(`{"mcpServers":{"corral":{"type":"http","url":%q}}}`, strings.TrimRight(brain, "/")+"/mcp/")
}

// beePrompt is the one-task bee contract, phrased for a harness that has its
// own file/exec tools and sees the brain as the MCP server named "corral".
// When the operator declares the harness's model (AGENT_MODEL / AGENT_BACKEND),
// the prompt gains a report_host step: finding attribution is stamped from
// the HostBook, which ONLY report_host feeds — without it a harness bee's
// findings show as "(not recorded)" in model_comparison.
//
// rs is the parsed AGENT_ROLE (internal/agentrole): a single role, a list, or
// a generalist ("any"). Its ClaimArg() feeds claim_task's "roles" — an empty
// array for a generalist, which the brain treats the same as an omitted
// roles arg (claim any ready task; internal/queue/store.go ClaimNextAs only
// filters `if len(roles) > 0`) — so a small herd of multi-role bees can cover
// every planned role instead of deadlocking on the first one nobody staffs.
func beePrompt(name string, rs agentrole.Set, instance, desc, model, backend string) string {
	roleDisplay := rs.Display()
	rolesJSON, _ := json.Marshal(rs.ClaimArg())
	announce := ""
	if model != "" {
		announce = fmt.Sprintf("\n   Then call report_host with {\"name\":%q,\"role\":%q,\"host\":%q,\"model\":%q,\"backend\":%q,\"jail\":\"none\"} so topology and finding attribution know what drives you.",
			name, roleDisplay, instance, model, backend)
	}
	return fmt.Sprintf(`You are %q, a %s bee in a corralai swarm. The MCP server "corral" is the swarm's brain. Work EXACTLY ONE task, then stop.

1. Call bootstrap with {"name":%q,"role":%q,"program":%q} to enter the swarm.%s
2. Call claim_task with {"name":%q,"roles":%s,"instance":%q}.
   - If it returns task:null, print exactly IDLE and stop. Do not invent work.
3. Do the task in the current directory with YOUR OWN tools (write real files,
   run real commands). Before writing a file, call claim_paths on it; if a peer
   holds it, back off. Call search_memory first — a peer may have solved this —
   and record decisions/lessons with add_memory as you go.
4. If the task mentions a verify command (e.g. "go build", "go test"), RUN it
   and make it pass; the brain's completion gate checks that a passing run was
   recorded, so also call report_execution with the command and exit code.
5. Report any vulnerability/bug/design flaw/missing requirement via
   report_finding (with severity).
6. As you work, call report_thought a few times with {"name":%q,"mission_id":<the
   claimed task's mission_id>,"text":"<a short, substantive sentence>"} — your
   ACTUAL reasoning at real decision/finding points (what you're examining,
   deciding, or finding — e.g. "the ratelimit test fails because the bucket
   refills too slowly; checking the interval"). This is your own reasoning in
   your own words, verbatim, never fabricated or dramatized for effect — it's a
   silent no-op unless this mission opted in, so call it freely, but skip status
   filler like "working on it" and skip steps with nothing substantive to say.
7. Call complete_task with {"id":<the task id>,"result":"<one-line summary>"}.
   If it refuses, satisfy the stated reason and try once more.
8. Print a one-line summary of what you did.`,
		name, roleDisplay, name, roleDisplay, desc, announce, name, string(rolesJSON), instance, name)
}

func expand(tmpl string, sub map[string]string) string {
	out := tmpl
	for k, v := range sub {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// splitCmd is a minimal shell-less splitter: fields, honoring single and
// double quotes. Harness command lines are operator-authored config, not
// untrusted input — this exists so no shell interprets the substituted prompt.
func splitCmd(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Print(usageText())
			return
		}
	}
	brain := env("CORRAL_BRAIN", "http://localhost:9019")
	rs := agentrole.Parse(env("AGENT_ROLE", "builder"))
	role := rs.Display() // e.g. "builder", "researcher+designer", or "generalist"
	name := env("AGENT_NAME", "Harness")
	ws := env("AGENT_WORKSPACE", ".")
	tmpl := os.Getenv("HARNESS_CMD")
	if tmpl == "" {
		fmt.Fprintln(os.Stderr, `HARNESS_CMD required — the headless-agent command template, e.g.:
  HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral__*,Read,Write,Edit,Bash" --permission-mode acceptEdits'`)
		os.Exit(2)
	}
	desc := env("HARNESS_DESC", splitCmd(tmpl)[0]+" (headless harness)")
	rounds := envInt("AGENT_ROUNDS", 0)
	timeout := time.Duration(envInt("HARNESS_TIMEOUT_SECONDS", 900)) * time.Second
	idle := time.Duration(envInt("HARNESS_IDLE_SECONDS", 30)) * time.Second

	// The generated MCP config file: the standard shape every harness reads.
	cfgPath := filepath.Join(os.TempDir(), fmt.Sprintf("corral-harness-%d.mcp.json", os.Getpid()))
	if err := os.WriteFile(cfgPath, []byte(mcpConfig(brain)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write mcp config:", err)
		os.Exit(1)
	}
	defer os.Remove(cfgPath)

	instance, _ := os.Hostname()
	prompt := beePrompt(name, rs, instance, desc, os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_BACKEND"))
	if pf := os.Getenv("AGENT_PROMPT_FILE"); pf != "" {
		b, err := os.ReadFile(pf) // #nosec G304 G703 -- AGENT_PROMPT_FILE is the operator's own prompt-override path (launcher config, same trust domain as HARNESS_CMD itself), not tainted input
		if err != nil {
			fmt.Fprintln(os.Stderr, "read AGENT_PROMPT_FILE:", err)
			os.Exit(1)
		}
		prompt = string(b)
	}
	sub := map[string]string{"prompt": prompt, "mcp_config": cfgPath, "brain": brain}

	fmt.Printf("[%s/%s] harness bee online — %s → %s\n", name, role, desc, brain)
	for round := 1; rounds == 0 || round <= rounds; round++ {
		argv := make([]string, 0, 8)
		for _, tok := range splitCmd(tmpl) {
			argv = append(argv, expand(tok, sub))
		}
		if len(argv) == 0 {
			fmt.Fprintln(os.Stderr, "HARNESS_CMD expanded to nothing")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- operator-authored harness template, by design
		cmd.Dir = ws
		cmd.Stderr = os.Stderr
		start := time.Now()
		out, err := cmd.Output()
		cancel()
		text := strings.TrimSpace(string(out))
		fmt.Printf("[%s/%s] r%d (%s): %s\n", name, role, round, time.Since(start).Round(time.Second), tail(text, 400))
		if err != nil {
			fmt.Printf("[%s/%s] harness exit: %v — backing off %s\n", name, role, err, idle)
			time.Sleep(idle)
			continue
		}
		if strings.Contains(text, "IDLE") {
			time.Sleep(idle)
		}
	}
	fmt.Printf("[%s/%s] done.\n", name, role)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
