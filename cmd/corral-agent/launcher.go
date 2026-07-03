// SPDX-License-Identifier: Elastic-2.0

// cmd/corral-agent/launcher.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/admission"
)

type childSpec struct {
	Role string
	N    int
}

// parseSpawnSpec parses AGENT_SPAWN_CHILDREN: "tester:2,builder:1". Entries without
// a positive count are ignored. Spawning is deterministic + operator-configured —
// never an LLM-callable tool.
func parseSpawnSpec(s string) []childSpec {
	var out []childSpec
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		role := strings.TrimSpace(kv[0])
		n, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if role == "" || err != nil || n <= 0 {
			continue
		}
		out = append(out, childSpec{Role: role, N: n})
	}
	return out
}

// spawnConfiguredChildren reads AGENT_SPAWN_CHILDREN and launches the requested
// children, each gated by the host admission controller. No-op when unset.
func spawnConfiguredChildren(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string) {
	specs := parseSpawnSpec(os.Getenv("AGENT_SPAWN_CHILDREN"))
	if len(specs) == 0 {
		return
	}
	spawnConfiguredChildrenN(ctrl, brain, parent, brainURL, launchProcess, specs)
}

// spawnConfiguredChildrenN is the testable core: for each requested child, ask the
// brain to register it + mint a token (brain enforces the budget), then Acquire a
// host slot (host enforces capacity/load); only on BOTH grants does it launch.
func spawnConfiguredChildrenN(ctrl admission.Controller, brain func(string, map[string]any) string, parent, brainURL string, launch func(env []string, lease admission.Lease) error, specs []childSpec) {
	seq := map[string]int{}
	for _, spec := range specs {
		for i := 0; i < spec.N; i++ {
			seq[spec.Role]++
			name := fmt.Sprintf("%s-%d", spec.Role, seq[spec.Role])
			raw := brain("spawn_subagent", map[string]any{
				"name": name, "role": spec.Role, "parent": parent, "out_of_process": true,
			})
			var resp struct {
				Name    string `json:"name"`
				Token   string `json:"token"`
				Error   string `json:"error"`
				Model   string `json:"model"`   // policy-assigned model (empty = inherit default)
				Backend string `json:"backend"` // policy-assigned backend (empty = inherit default)
			}
			_ = json.Unmarshal([]byte(raw), &resp)
			if resp.Error != "" || resp.Token == "" {
				fmt.Printf("[launcher] brain refused %s/%s: %s\n", parent, name, oneline(raw))
				continue // brain budget refusal — do not launch
			}
			lease, err := ctrl.Acquire(spec.Role)
			if err != nil {
				fmt.Printf("[launcher] %s\n", err.Error()) // host admission refusal — do not launch
				continue
			}
			childEnv := append(os.Environ(),
				"CORRAL_TOKEN="+resp.Token,
				"AGENT_NAME="+resp.Name,
				"AGENT_ROLE="+spec.Role,
				"AGENT_MODE=queue",
				"CORRAL_BRAIN="+brainURL,
				"AGENT_SPAWN_CHILDREN=", // children never recursively spawn (defense in depth)
			)
			// Best-effort model injection: if the brain resolved a policy-assigned model
			// for this role, inject it so the child uses the right model. Empty = the
			// child inherits the parent/default. Degrade-never-block.
			if resp.Model != "" {
				childEnv = append(childEnv, "AGENT_MODEL="+resp.Model)
			}
			if resp.Backend != "" {
				childEnv = append(childEnv, "MODEL_BACKEND="+resp.Backend)
			}
			if err := launch(childEnv, lease); err != nil {
				fmt.Printf("[launcher] launch %s failed: %v\n", resp.Name, err)
				lease.Release()
				continue
			}
			fmt.Printf("[launcher] launched %s (role %s)\n", resp.Name, spec.Role)
			// lease is released when the child process exits (launchProcess wires the wait).
		}
	}
}

// launchProcess starts a child corral-agent with the given env and returns once it
// has started (not waited). The lease is released when the child process exits.
func launchProcess(env []string, lease admission.Lease) error {
	cmd := exec.Command(os.Args[0]) // #nosec G702,G204 -- re-execs this agent's OWN binary (os.Args[0]) to spawn a child worker; env-driven, no attacker input; unrelated to the run_command sandbox
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait(); lease.Release() }() // free the host slot when the child exits
	return nil
}
