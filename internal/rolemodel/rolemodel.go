// SPDX-License-Identifier: Elastic-2.0

// Package rolemodel provides policy parsing, pool-availability checks, and
// drift reconciliation for the cross-model attribution feature. It is a pure
// data+logic package with no I/O, no MCP, and no external dependencies.
package rolemodel

import "strings"

// ModelRef identifies a model by optional backend and model name.
type ModelRef struct {
	Backend string // e.g. "anthropic", "ollama", "" means any backend
	Model   string // e.g. "claude-opus", "qwen2.5-coder"
}

// Policy maps a role name to its assigned ModelRef.
type Policy map[string]ModelRef

// Parse parses a comma-separated env string of the form:
//
//	"role=backend:model,role=model,..."
//
// Bare "role=model" (no colon) sets Backend to "". Returns the policy and a
// slice of malformed entries that were skipped (for logging).
func Parse(env string) (Policy, []string) {
	p := Policy{}
	var malformed []string
	if env == "" {
		return p, malformed
	}
	for _, entry := range strings.Split(env, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eqIdx := strings.Index(entry, "=")
		if eqIdx <= 0 {
			// no "=" or "=" at position 0 → no role name
			malformed = append(malformed, entry)
			continue
		}
		role := strings.TrimSpace(entry[:eqIdx])
		value := strings.TrimSpace(entry[eqIdx+1:])
		if role == "" || value == "" {
			malformed = append(malformed, entry)
			continue
		}
		var ref ModelRef
		if colonIdx := strings.Index(value, ":"); colonIdx >= 0 {
			ref.Backend = strings.TrimSpace(value[:colonIdx])
			ref.Model = strings.TrimSpace(value[colonIdx+1:])
		} else {
			ref.Model = value
		}
		if ref.Model == "" {
			malformed = append(malformed, entry)
			continue
		}
		p[role] = ref
	}
	return p, malformed
}

// Lookup returns the ModelRef for the given role and whether it was found.
func (p Policy) Lookup(role string) (ModelRef, bool) {
	ref, ok := p[role]
	return ref, ok
}

// Available returns the role's ModelRef if it is present in pool, otherwise
// returns (ModelRef{}, false). Match requires model equality; if the policy
// entry specifies a non-empty Backend, the backend must also match.
func (p Policy) Available(role string, pool []ModelRef) (ModelRef, bool) {
	ref, ok := p.Lookup(role)
	if !ok {
		return ModelRef{}, false
	}
	for _, candidate := range pool {
		if candidate.Model != ref.Model {
			continue
		}
		if ref.Backend != "" && candidate.Backend != ref.Backend {
			continue
		}
		return ref, true
	}
	return ModelRef{}, false
}

// Reconcile returns the expected model name for the role (per policy) and
// whether drift is detected (reportedModel != expected). If the role is not
// present in the policy, returns ("", false).
func Reconcile(role, reportedModel string, p Policy) (expected string, drift bool) {
	ref, ok := p.Lookup(role)
	if !ok {
		return "", false
	}
	expected = ref.Model
	drift = reportedModel != expected
	return expected, drift
}
