// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// Host is one bee's runtime facts — WHERE it runs and what powers it — so the
// swarm UI can draw a topology of the ecosystem: which host each agent sits on,
// the model driving it, and the jail confining its commands. Observability only.
type Host struct {
	Agent       string   `json:"agent"`
	Role        string   `json:"role"`
	Host        string   `json:"host"`    // hostname / container id
	Model       string   `json:"model"`   // e.g. gemini-3.1-pro-preview
	Backend     string   `json:"backend"` // model backend: gemini|anthropic|ollama|…
	Jail        string   `json:"jail"`    // exec isolation: bwrap|container|none
	Net         bool     `json:"net"`     // is the jail allowed network egress
	OS          string   `json:"os"`      // goos/goarch
	Pid         int      `json:"pid"`
	TS          int64    `json:"ts"` // last announce, Unix seconds
	LocalAgents []string `json:"local_agents,omitempty"`
}

// hostTTL drops a bee from the topology this long after its last announce, so a
// departed agent fades out rather than lingering forever.
const hostTTL = 120

// HostBook is the latest runtime facts per agent (keyed by name, latest-wins).
//
// It is also the debounce state for host_seen telemetry (see recordHostSeen),
// and it lives only in memory: a brain restart forgets every prior sighting, so
// the first announce round after a restart re-emits host_seen for each agent.
// That burst is bounded by fleet size and intended — not a debounce bug.
type HostBook struct {
	mu               sync.RWMutex
	items            map[string]Host
	interceptPending map[string]bool
}

// NewHostBook returns an initialised HostBook. State is in-memory only — see
// the HostBook doc for the restart / host_seen re-emission consequence.
func NewHostBook() *HostBook {
	return &HostBook{
		items:            map[string]Host{},
		interceptPending: map[string]bool{},
	}
}

// Set records (or refreshes) one agent's facts.
func (b *HostBook) Set(h Host) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[h.Agent] = h
}

// Get returns the most-recently-reported Host for an agent, and whether it was
// found. It does NOT enforce the TTL — a departed agent's last-known model is
// still useful for attributing findings filed before the TTL elapsed.
func (b *HostBook) Get(agent string) (Host, bool) {
	b.mu.RLock()
	h, ok := b.items[agent]
	b.mu.RUnlock()
	return h, ok
}

// List returns the live entries (announced within hostTTL), sorted by host then
// agent so the topology groups cleanly.
func (b *HostBook) List() []Host {
	now := time.Now().Unix()
	b.mu.Lock()
	out := make([]Host, 0, len(b.items))
	for k, h := range b.items {
		if now-h.TS > hostTTL {
			delete(b.items, k)
			continue
		}
		out = append(out, h)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// AvailableModels returns distinct {Backend, Model} pairs among hosts whose TS
// is within the presence window (TS >= now-windowSecs). Uses RLock so it does
// not evict stale entries (eviction is List's job).
func (b *HostBook) AvailableModels(windowSecs int64, now int64) []rolemodel.ModelRef {
	cutoff := now - windowSecs
	b.mu.RLock()
	defer b.mu.RUnlock()
	seen := map[rolemodel.ModelRef]struct{}{}
	for _, h := range b.items {
		if h.TS < cutoff {
			continue
		}
		seen[rolemodel.ModelRef{Backend: h.Backend, Model: h.Model}] = struct{}{}
	}
	out := make([]rolemodel.ModelRef, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	return out
}

// AnnotatedHost extends Host's fields with the policy's expected model and
// whether this host's actual model drifts from that expectation.
// Fields are flat (not embedding Host) to avoid a JSON-schema shadowing issue:
// Host.Host (the hostname field) would be shadowed by the embedded type name,
// causing a mismatch between the generated schema and the marshalled JSON.
// SYNC: AnnotatedHost mirrors Host field-for-field (embedding shadows Host.Host
// under the MCP schema walk). If you add a field to Host, mirror it here AND in
// AnnotateHosts, and TestAnnotatedHostMirrorsHost keeps the counts honest.
type AnnotatedHost struct {
	Agent       string   `json:"agent"`
	Role        string   `json:"role"`
	Host        string   `json:"host"`    // hostname / container id
	Model       string   `json:"model"`   // e.g. gemini-3.1-pro-preview
	Backend     string   `json:"backend"` // model backend: gemini|anthropic|ollama|…
	Jail        string   `json:"jail"`    // exec isolation: bwrap|container|none
	Net         bool     `json:"net"`     // is the jail allowed network egress
	OS          string   `json:"os"`      // goos/goarch
	Pid         int      `json:"pid"`
	TS          int64    `json:"ts"` // last announce, Unix seconds
	LocalAgents []string `json:"local_agents,omitempty"`
	Expected    string   `json:"expected,omitempty"` // policy's model for this role; "" when role not in policy
	Drift       bool     `json:"drift,omitempty"`    // reportedModel != expected (false when no policy)
}

// AnnotateHosts decorates each Host with Expected and Drift fields derived from
// the given role-model policy. A nil or empty policy produces no annotations
// (Expected="", Drift=false — degrade-never-block).
func AnnotateHosts(hosts []Host, p rolemodel.Policy) []AnnotatedHost {
	out := make([]AnnotatedHost, len(hosts))
	for i, h := range hosts {
		expected, drift := rolemodel.Reconcile(h.Role, h.Model, p)
		out[i] = AnnotatedHost{
			Agent: h.Agent, Role: h.Role, Host: h.Host,
			Model: h.Model, Backend: h.Backend, Jail: h.Jail,
			Net: h.Net, OS: h.OS, Pid: h.Pid, TS: h.TS,
			LocalAgents: h.LocalAgents,
			Expected:    expected, Drift: drift,
		}
	}
	return out
}

// topologyOut is the response shape for swarm_topology.
type topologyOut struct {
	Hosts  []AnnotatedHost  `json:"hosts"`
	Policy rolemodel.Policy `json:"policy"` // the declared policy (may be nil/empty)
}

// registerHost registers the report_host and swarm_topology MCP tools against
// s. When book is nil the function is a no-op.
func registerHost(s *mcp.Server, book *HostBook, opts Options) {
	if book == nil {
		return
	}
	type reportHostIn struct {
		Name        string   `json:"name"`
		Role        string   `json:"role"`
		Host        string   `json:"host"`
		Model       string   `json:"model"`
		Backend     string   `json:"backend"`
		Jail        string   `json:"jail"`
		Net         bool     `json:"net"`
		OS          string   `json:"os"`
		Pid         int      `json:"pid"`
		LocalAgents []string `json:"local_agents,omitempty"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_host",
		Description: "Announce your runtime facts (host, model, jail) so the swarm's topology view can map where every agent runs. Re-announce periodically; observability only.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportHostIn) (*mcp.CallToolResult, okOut, error) {
		opts.WorkerSessions.Mark(req)
		recordHostSeen(opts.Telemetry, book, Host{
			Agent: identity(req, in.Name), Role: in.Role, Host: in.Host,
			Model: in.Model, Backend: in.Backend, Jail: in.Jail, Net: in.Net,
			OS: in.OS, Pid: in.Pid, TS: time.Now().Unix(),
			LocalAgents: in.LocalAgents,
		})
		return nil, okOut{OK: true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "swarm_topology",
		Description: "Return the live topology: each active bee with its host, model, jail, and (when a role-model policy is configured) whether the actual model drifts from the expected one.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, topologyOut, error) {
		hosts := book.List()
		annotated := AnnotateHosts(hosts, opts.RoleModels)
		return nil, topologyOut{Hosts: annotated, Policy: opts.RoleModels}, nil
	})
}

func (b *HostBook) SetInterceptPending(agent string, enable bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.interceptPending == nil {
		b.interceptPending = make(map[string]bool)
	}
	b.interceptPending[agent] = enable
}

func (b *HostBook) IsInterceptPending(agent string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.interceptPending == nil {
		return false
	}
	return b.interceptPending[agent]
}

