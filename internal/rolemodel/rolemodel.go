// SPDX-License-Identifier: Elastic-2.0

// Package rolemodel provides policy parsing, pool-availability checks, and
// drift reconciliation for the cross-model attribution feature. It is a pure
// data+logic package with no I/O, no MCP, and no external dependencies.
package rolemodel

import (
	"strings"
	"sync"
)

// ModelRef identifies a model by optional backend and model name.
type ModelRef struct {
	Backend string // e.g. "anthropic", "ollama", "" means any backend
	Model   string // e.g. "claude-opus", "qwen2.5-coder"
}

// Policy maps a role name to its assigned ModelRef.
//
// A single Policy instance is shared across goroutines: the staffing engine
// writes it as it dynamically staffs a mission (Set), while the UI topology
// loop and subagent spawn read it (Lookup / Available / Snapshot). It is
// therefore guarded by an RWMutex — pass it by POINTER (never copy it, or the
// mutex is copied). A nil *Policy is a valid empty policy (all reads degrade to
// "not found", never panic).
type Policy struct {
	mu sync.RWMutex
	m  map[string]ModelRef
}

// New returns an empty, ready-to-use policy.
func New() *Policy { return &Policy{m: map[string]ModelRef{}} }

// Set assigns a model to a role. Safe for concurrent use.
func (p *Policy) Set(role string, ref ModelRef) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]ModelRef{}
	}
	p.m[role] = ref
}

// Len reports how many roles the policy assigns.
func (p *Policy) Len() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.m)
}

// Snapshot returns a plain-map copy of the policy — safe to serialize or range
// without holding the lock.
func (p *Policy) Snapshot() map[string]ModelRef {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]ModelRef, len(p.m))
	for k, v := range p.m {
		out[k] = v
	}
	return out
}

// Parse parses a comma-separated env string of the form:
//
//	"role=backend:model,role=model,..."
//
// Bare "role=model" (no colon) sets Backend to "". Returns the policy and a
// slice of malformed entries that were skipped (for logging).
func Parse(env string) (*Policy, []string) {
	p := New()
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
		p.m[role] = ref // single-threaded build; no lock needed before New() escapes
	}
	return p, malformed
}

// Lookup returns the ModelRef for the given role and whether it was found.
func (p *Policy) Lookup(role string) (ModelRef, bool) {
	if p == nil {
		return ModelRef{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	ref, ok := p.m[role]
	return ref, ok
}

// Available returns the role's ModelRef if it is present in pool, otherwise
// returns (ModelRef{}, false). Match requires model equality; if the policy
// entry specifies a non-empty Backend, the backend must also match.
func (p *Policy) Available(role string, pool []ModelRef) (ModelRef, bool) {
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
func Reconcile(role, reportedModel string, p *Policy) (expected string, drift bool) {
	ref, ok := p.Lookup(role)
	if !ok {
		return "", false
	}
	expected = ref.Model
	drift = reportedModel != expected
	return expected, drift
}
