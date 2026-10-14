// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"sync"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// seatToolProbe answers "what tool and version sat in this seat" ONCE per spec
// for a whole scan. agentbackend.SeatTool runs the agent's --version; asked
// per job, an auto-updating CLI could put one version in the cache key and
// record another on a verdict of the same scan. Resolved once and threaded
// through — the cache key (modelSetKeyWith), every verdict's SeatTools
// (resolveAuditRoles) and the bundle's roster tools — so all three name the
// same version.
//
// A nil probe is valid and probes on every call: a single-file run asks once
// per seat anyway.
type seatToolProbe struct {
	mu    sync.Mutex
	seen  map[string]string
	probe func(spec string) string
}

func newSeatToolProbe() *seatToolProbe {
	return &seatToolProbe{seen: map[string]string{}, probe: agentbackend.SeatTool}
}

// tool is the seat's tool for spec, "" for an API seat.
func (p *seatToolProbe) tool(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "off" {
		return ""
	}
	if p == nil {
		return agentbackend.SeatTool(spec)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.seen[spec]; ok {
		return t
	}
	t := p.probe(spec)
	p.seen[spec] = t
	return t
}

// toolsFor is the record's tool for every agent seat in assign (role ->
// tool), through the one helper review's record uses too. API seats and
// disabled ones have no entry; nil when no seat is an agent.
func (p *seatToolProbe) toolsFor(assign map[string]string) map[string]string {
	var tools map[string]string
	for role, spec := range assign {
		if tool := p.tool(spec); tool != "" {
			if tools == nil {
				tools = map[string]string{}
			}
			tools[role] = tool
		}
	}
	return tools
}

// keyName is a seat as the verdict cache keys it: the spec, plus the agent's
// tool and version (spelled as review prints a seat, toolNote), so a CLI
// upgrade re-measures instead of reusing a verdict a different CLI earned. An
// API seat is its spec exactly, so every existing key is byte-identical.
func (p *seatToolProbe) keyName(spec string) string {
	return spec + toolNote(p.tool(spec))
}
