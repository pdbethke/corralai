// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// workerSessionTTL is how long a worker mark survives without being touched.
// Both Mark and an Is hit refresh a mark, so a worker that keeps calling
// tools stays marked for as long as it lives plus one TTL; only sessions
// that have gone silent expire. 24h is deliberately generous — expiry exists
// to shed DEAD sessions (the go-sdk exposes no per-session close hook the
// brain could wire instead, and every reconnect mints a fresh session ID),
// not to un-mark live ones.
const workerSessionTTL = 24 * time.Hour

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
// Marks are keyed by MCP session ID and expire workerSessionTTL after their
// last touch, swept lazily during Mark — the map cannot grow without bound
// on a long-lived brain whose fleet reconnects (each reconnect is a fresh
// session ID, and the SDK never tells us when the old one died).
//
// Known limitation: an IN-PROCESS subagent (spawn_subagent without an
// out-of-process delegation token) shares its parent's MCP session, so
// marking the session marks the parent too — a spawned worker and the human
// operator that spawned it are indistinguishable by this signal alone in
// that configuration. Out-of-process subagents (their own MCP connection)
// are unaffected.
type WorkerSessions struct {
	mu  sync.Mutex
	ids map[string]time.Time // session ID → last touched (Mark or Is hit)
	now func() time.Time     // clock seam; tests override
}

// NewWorkerSessions returns an empty tracker.
func NewWorkerSessions() *WorkerSessions {
	return &WorkerSessions{ids: map[string]time.Time{}, now: time.Now}
}

// initLocked lazily fills the zero-value fields (nil ids map, nil now clock)
// so a bare WorkerSessions{} — e.g. a struct literal in a config that forgot
// NewWorkerSessions — never panics (a nil map write or a nil func call both
// would). Callers must hold w.mu.
func (w *WorkerSessions) initLocked() {
	if w.ids == nil {
		w.ids = map[string]time.Time{}
	}
	if w.now == nil {
		w.now = time.Now
	}
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
		w.mark(id)
	}
}

// mark records id's touch time and lazily sweeps every expired entry — the
// amortized cleanup that keeps the map bounded without a sweeper goroutine.
// O(live sessions) per call, on the two rare handlers (bootstrap/report_host).
func (w *WorkerSessions) mark(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.initLocked()
	t := w.now()
	for k, touched := range w.ids {
		if t.Sub(touched) > workerSessionTTL {
			delete(w.ids, k)
		}
	}
	w.ids[id] = t
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
	return w.isMarked(id)
}

// isMarked reports whether id carries a live mark. A hit refreshes the mark
// (a worker still calling tools never expires while alive); an expired entry
// is evicted on the spot and reported unmarked.
func (w *WorkerSessions) isMarked(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.initLocked()
	t := w.now()
	touched, ok := w.ids[id]
	if !ok {
		return false
	}
	if t.Sub(touched) > workerSessionTTL {
		delete(w.ids, id)
		return false
	}
	w.ids[id] = t
	return true
}
