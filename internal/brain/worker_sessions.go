// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WorkerSessions tracks, per MCP session, whether the session has identified
// itself as a corral-agent worker — either by ClientInfo.Name at the MCP
// handshake, or by calling bootstrap/report_host (the two calls every
// shipped corral-agent makes and corral-admin/the UI never do).
//
// This is a TRUTHFULNESS GUARDRAIL, not a security boundary: dev mode has no
// cryptographic identity at all (anyone on the port is anonymous), so a
// hostile caller could simply not announce itself and this mark would never
// be set. It exists so a worker acting HONESTLY — as every shipped
// corral-agent does — cannot accidentally pass the human gate in dev mode,
// matching the rule isHumanAdmin already enforces when auth is on.
//
// Known limitation: an IN-PROCESS subagent (spawn_subagent without an
// out-of-process delegation token) shares its parent's MCP session, so
// marking the session marks the parent too — a spawned worker and the human
// operator that spawned it are indistinguishable by this signal alone in
// that configuration. Out-of-process subagents (their own MCP connection)
// are unaffected.
type WorkerSessions struct {
	mu  sync.Mutex
	ids map[string]bool
}

// NewWorkerSessions returns an empty tracker.
func NewWorkerSessions() *WorkerSessions {
	return &WorkerSessions{ids: map[string]bool{}}
}

// Mark records req's MCP session as a worker session. Nil-safe: a nil
// receiver, nil req, or nil req.Session is a no-op — dev-mode marking is
// opportunistic instrumentation on the bootstrap/report_host handlers, never
// a hard dependency for those tools to function.
func (w *WorkerSessions) Mark(req *mcp.CallToolRequest) {
	if w == nil || req == nil || req.Session == nil {
		return
	}
	if id := req.Session.ID(); id != "" {
		w.mu.Lock()
		w.ids[id] = true
		w.mu.Unlock()
	}
}

// Is reports whether req's session is a worker: either it named itself
// "corral-agent" at the MCP handshake (checked live, needs no prior Mark
// call), or an earlier call in the same session marked it. The ClientInfo
// check works even with a nil receiver (Options.WorkerSessions unset);
// only the behavioral (marked) signal requires a non-nil tracker.
func (w *WorkerSessions) Is(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	if ip := req.Session.InitializeParams(); ip != nil && ip.ClientInfo != nil && ip.ClientInfo.Name == "corral-agent" {
		return true
	}
	if w == nil {
		return false
	}
	id := req.Session.ID()
	if id == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ids[id]
}
