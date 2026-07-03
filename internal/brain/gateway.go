// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/gateway"
)

// ── inputs / outputs ───────────────────────────────────────────────────────

type registerEndpointIn struct {
	Name        string `json:"name" jsonschema:"unique name for your upstream MCP server"`
	Endpoint    string `json:"endpoint" jsonschema:"the upstream MCP streamable-HTTP URL"`
	Transport   string `json:"transport,omitempty" jsonschema:"http (default)"`
	Description string `json:"description,omitempty"`
	AuthHeader  string `json:"auth_header,omitempty" jsonschema:"header to inject upstream, e.g. Authorization"`
	AuthToken   string `json:"auth_token,omitempty" jsonschema:"the secret value (held by the brain, never returned); omit on update to keep existing"`
	Enabled     *bool  `json:"enabled,omitempty" jsonschema:"default true"`
}

type promoteIn struct {
	Name              string   `json:"name"`
	Public            bool     `json:"public" jsonschema:"true to make this team-wide, false to make it personal again"`
	AllowedPrincipals []string `json:"allowed_principals,omitempty" jsonschema:"emails permitted when public; empty = all authorized"`
	AuthHeader        string   `json:"auth_header,omitempty" jsonschema:"optional: swap in a team credential header"`
	AuthToken         string   `json:"auth_token,omitempty" jsonschema:"optional: swap in a team credential (replaces the owner's secret)"`
}

type endpointNameIn struct {
	Name string `json:"name"`
}
type setEnabledIn struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}
type okMsg struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}
type endpointsOut struct {
	Endpoints []gateway.Endpoint `json:"endpoints"`
}

type capability struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Public      bool     `json:"public"`
	Tools       []string `json:"tools"`
	Error       string   `json:"error,omitempty"`
}
type capsOut struct {
	Capabilities []capability `json:"capabilities"`
}

type callCapabilityIn struct {
	Server    string         `json:"server" jsonschema:"the upstream name (see list_capabilities)"`
	Tool      string         `json:"tool" jsonschema:"the upstream tool to call"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

var (
	errAdminOnly = fmt.Errorf("forbidden: admin only")
	errNotYours  = fmt.Errorf("forbidden: not your endpoint (and you're not an admin)")
)

func registerGateway(s *mcp.Server, opts Options) {
	gw := opts.Gateway

	// ownerOrAdmin: the caller owns `name`, or is an admin.
	ownerOrAdmin := func(req *mcp.CallToolRequest, name string) bool {
		if opts.isAdmin(req) {
			return true
		}
		who, _ := actor(req)
		owner, ok := gw.OwnerOf(name)
		return ok && owner == who
	}

	// register_endpoint — ANY authorized user. Creates a PERSONAL endpoint (usable
	// only by the owner) until an admin promotes it.
	mcp.AddTool(s, &mcp.Tool{Name: "register_endpoint",
		Description: "Register YOUR personal upstream MCP server (usable only by you until an admin promotes it). The auth_token is held by the brain and never returned."},
		func(_ context.Context, req *mcp.CallToolRequest, in registerEndpointIn) (*mcp.CallToolResult, okMsg, error) {
			if in.Name == "" || in.Endpoint == "" {
				return nil, okMsg{}, fmt.Errorf("name and endpoint are required")
			}
			transport := in.Transport
			if transport == "" {
				transport = "http"
			}
			if transport != "http" {
				return nil, okMsg{}, fmt.Errorf("only transport=http is supported in this version")
			}
			enabled := true
			if in.Enabled != nil {
				enabled = *in.Enabled
			}
			who, _ := actor(req)
			err := gw.Register(gateway.Endpoint{
				Name: in.Name, Transport: transport, Endpoint: in.Endpoint,
				Description: in.Description, Enabled: enabled,
			}, gateway.Auth{Header: in.AuthHeader, Token: in.AuthToken}, who)
			if err != nil {
				return nil, okMsg{}, err
			}
			return nil, okMsg{OK: true, Message: "registered personal endpoint " + in.Name}, nil
		})

	// list_my_endpoints — the caller's own endpoints.
	mcp.AddTool(s, &mcp.Tool{Name: "list_my_endpoints", Description: "List the upstream endpoints YOU registered (no secrets)."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, endpointsOut, error) {
			who, _ := actor(req)
			eps, err := gw.ListOwned(who)
			return nil, endpointsOut{Endpoints: eps}, err
		})

	// list_all_endpoints — ADMIN: every endpoint (to review personal ones for promotion).
	mcp.AddTool(s, &mcp.Tool{Name: "list_all_endpoints", Description: "ADMIN: list every registered upstream (owner + public flag, no secrets) — review personal endpoints to promote."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, endpointsOut, error) {
			if !opts.isAdmin(req) {
				return nil, endpointsOut{}, errAdminOnly
			}
			eps, err := gw.ListAll()
			return nil, endpointsOut{Endpoints: eps}, err
		})

	// promote_endpoint — ADMIN: make a personal endpoint public (team-wide / scoped),
	// optionally swapping in a team credential.
	mcp.AddTool(s, &mcp.Tool{Name: "promote_endpoint",
		Description: "ADMIN: promote a personal endpoint to public (team-wide, or scoped to allowed_principals), or set it back to personal. Optionally swap in a team credential — otherwise the owner's secret is shared to permitted users."},
		func(_ context.Context, req *mcp.CallToolRequest, in promoteIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			ok, err := gw.Promote(in.Name, in.Public, in.AllowedPrincipals, gateway.Auth{Header: in.AuthHeader, Token: in.AuthToken})
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no such endpoint"}, err
			}
			vis := "personal"
			if in.Public {
				vis = "public"
			}
			return nil, okMsg{OK: true, Message: in.Name + " is now " + vis}, nil
		})

	// set_endpoint_enabled / remove_endpoint — owner or admin.
	mcp.AddTool(s, &mcp.Tool{Name: "set_endpoint_enabled", Description: "Enable/disable an endpoint you own (or any, as admin)."},
		func(_ context.Context, req *mcp.CallToolRequest, in setEnabledIn) (*mcp.CallToolResult, okMsg, error) {
			if !ownerOrAdmin(req, in.Name) {
				return nil, okMsg{}, errNotYours
			}
			ok, err := gw.SetEnabled(in.Name, in.Enabled)
			return nil, okMsg{OK: ok}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "remove_endpoint", Description: "Delete an endpoint you own (or any, as admin)."},
		func(_ context.Context, req *mcp.CallToolRequest, in endpointNameIn) (*mcp.CallToolResult, okMsg, error) {
			if !ownerOrAdmin(req, in.Name) {
				return nil, okMsg{}, errNotYours
			}
			ok, err := gw.Remove(in.Name)
			return nil, okMsg{OK: ok}, err
		})

	// list_capabilities — discover usable upstreams (own + permitted public) + tools.
	mcp.AddTool(s, &mcp.Tool{Name: "list_capabilities",
		Description: "List the upstream MCP servers you may use (your own + team-public) and their tools. Discover here, then call_capability to use one."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, capsOut, error) {
			who, _ := actor(req)
			eps, err := gw.Usable(who)
			if err != nil {
				return nil, capsOut{}, err
			}
			out := capsOut{Capabilities: []capability{}}
			for _, e := range eps {
				c := capability{Name: e.Name, Description: e.Description, Public: e.Public, Tools: []string{}}
				if _, auth, ok, _ := gw.Resolve(e.Name, who); ok {
					if names, terr := upstreamTools(ctx, e, auth, opts.Egress); terr != nil {
						c.Error = terr.Error()
					} else {
						c.Tools = names
					}
				}
				out.Capabilities = append(out.Capabilities, c)
			}
			return nil, out, nil
		})

	// call_capability — proxy a tool call to a usable upstream; audited.
	mcp.AddTool(s, &mcp.Tool{Name: "call_capability",
		Description: "Call a tool on an upstream MCP server you may use (proxied + audited by the brain)."},
		func(ctx context.Context, req *mcp.CallToolRequest, in callCapabilityIn) (*mcp.CallToolResult, struct{}, error) {
			who, _ := actor(req)
			e, auth, ok, err := gw.Resolve(in.Server, who)
			if err != nil {
				return nil, struct{}{}, err
			}
			if !ok {
				gw.Audit(who, in.Server, in.Tool, truncate(in.Arguments), "denied-or-missing")
				return nil, struct{}{}, fmt.Errorf("upstream %q not available to you", in.Server)
			}
			res, cerr := upstreamCall(ctx, e, auth, in.Tool, in.Arguments, opts.Egress)
			outcome := "ok"
			if cerr != nil {
				outcome = "error: " + cerr.Error()
			}
			gw.Audit(who, in.Server, in.Tool, truncate(in.Arguments), outcome)
			return res, struct{}{}, cerr
		})
}

// ── upstream proxy (go-sdk client per call; auth header injected) ───────────

type headerRT struct {
	header, value string
	base          http.RoundTripper
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.header != "" && h.value != "" {
		r.Header.Set(h.header, h.value)
	}
	return h.base.RoundTrip(r)
}

func upstreamConnect(ctx context.Context, e gateway.Endpoint, auth gateway.Auth, guard *gateway.Guard) (*mcp.ClientSession, error) {
	base := &http.Transport{
		DialContext:           guard.DialContext, // SSRF guard: blocks private/loopback unless allowlisted
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	hc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: headerRT{header: auth.Header, value: auth.Token, base: base},
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "corralai-gateway", Version: "0.1.0"}, nil)
	return c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.Endpoint, HTTPClient: hc}, nil)
}

func upstreamTools(ctx context.Context, e gateway.Endpoint, auth gateway.Auth, guard *gateway.Guard) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	up, err := upstreamConnect(ctx, e, auth, guard)
	if err != nil {
		return nil, err
	}
	defer up.Close()
	lt, err := up.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(lt.Tools))
	for _, t := range lt.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

func upstreamCall(ctx context.Context, e gateway.Endpoint, auth gateway.Auth, tool string, args map[string]any, guard *gateway.Guard) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	up, err := upstreamConnect(ctx, e, auth, guard)
	if err != nil {
		return nil, err
	}
	defer up.Close()
	return up.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

func truncate(args map[string]any) string {
	b, _ := json.Marshal(args)
	if len(b) > 2048 {
		return string(b[:2048]) + "…"
	}
	return string(b)
}
