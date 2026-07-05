// SPDX-License-Identifier: Elastic-2.0

// Package agentrole parses the AGENT_ROLE env var shared by corral-harness
// and corral-agent into the set of roles a worker serves. A realistic herd
// has fewer workers than a mission plans roles for (up to 9: researcher,
// designer, builder, tester, pentester, perf, integrator, writer, reviewer),
// so a worker that can only ever claim ONE hardcoded role leaves the other
// roles' tasks unclaimed forever — the mission deadlocks on the first
// unstaffed role. AGENT_ROLE now accepts a comma-separated list ("a small
// herd covers all the roles") or "any"/"*"/empty for a pure generalist that
// claims whatever's ready. This also enriches evaluation: attribution stays
// per-TASK (each claim records its own (agent, role)), so a multi-role
// worker fills the model×role performance matrix instead of only the
// diagonal.
package agentrole

import "strings"

// Set is a worker's parsed AGENT_ROLE.
type Set struct {
	// Roles is the explicit list this worker claims against. Empty/nil when
	// Any is true.
	Roles []string
	// Any is true when the worker is a pure generalist — AGENT_ROLE was
	// empty, "any", or "*".
	Any bool
}

// Parse splits a comma-separated AGENT_ROLE value into a Set. Entries are
// trimmed of surrounding whitespace; empty entries are dropped. An empty
// string, "any", or "*" (case-insensitive) means "claim any ready task" —
// the brain's documented claim_task behaviour for an omitted/empty roles
// arg (internal/brain/tasks.go claimTaskIn.Roles: "omit to claim any ready
// task"). A single entry behaves exactly as the old single-role field did.
func Parse(raw string) Set {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "any") || raw == "*" {
		return Set{Any: true}
	}
	var roles []string
	for _, r := range strings.Split(raw, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return Set{Any: true}
	}
	return Set{Roles: roles}
}

// ClaimArg is the value to send as claim_task's "roles" argument: the
// explicit list, or an empty (non-nil) slice for a generalist. The brain
// treats a missing/empty roles arg identically — internal/queue/store.go's
// ClaimNextAs only adds the role filter `if len(roles) > 0` — so sending []
// here reaches the same "claim any ready task" behaviour as omitting the
// key, without every call site needing to conditionally build the args map.
func (s Set) ClaimArg() []string {
	if s.Any {
		return []string{}
	}
	return s.Roles
}

// Display is how this worker's role(s) should read in banners, logs, and
// the bootstrap/report_host free-text role field: the single role
// unchanged, a "+"-joined list for multi-role, or "generalist" for
// any-mode. Single-role output is byte-identical to the pre-multi-role
// behaviour.
func (s Set) Display() string {
	if s.Any {
		return "generalist"
	}
	return strings.Join(s.Roles, "+")
}
