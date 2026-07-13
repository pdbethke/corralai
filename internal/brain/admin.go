// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/principals"
)

type whoamiOut struct {
	Principal   string `json:"principal"`    // verified email ("" if unauthenticated/dev)
	IsSuperuser bool   `json:"is_superuser"` // admin rights
	Allowed     bool   `json:"allowed"`      // may use this brain
}

type createSuperuserIn struct {
	Email string `json:"email,omitempty" jsonschema:"email to make a superuser; omit to promote yourself"`
}
type principalOut struct {
	OK        bool   `json:"ok"`
	Email     string `json:"email"`
	Message   string `json:"message,omitempty"`
	Bootstrap bool   `json:"bootstrap,omitempty"` // true when this was the first (free) superuser
}

type emailIn struct {
	Email string `json:"email" jsonschema:"the principal's email"`
}
type setSuperuserIn struct {
	Email       string `json:"email" jsonschema:"the principal's email"`
	IsSuperuser bool   `json:"is_superuser" jsonschema:"true to promote, false to demote"`
}
type listPrincipalsOut struct {
	Principals []principals.Principal `json:"principals"`
}

type mintObserverIn struct {
	Principal  string `json:"principal,omitempty" jsonschema:"who the token authenticates as — must be an allowed principal so it passes the allowlist; omit to use yourself"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"token lifetime in seconds (default 86400 = 24h)"`
}
type observerTokenOut struct {
	OK    bool   `json:"ok"`
	Token string `json:"token"`
	Usage string `json:"usage"`
}

// registerAdmin adds the Django-style identity/role tools. whoami is open to any
// caller; the rest manage the principal table. create_superuser, add_member,
// set_superuser, remove_principal, and mint_observer gate on isHumanAdmin (not just isAdmin):
// principal writes are a two-hop bypass of the human gate otherwise — a
// delegated subagent under a superuser could set_superuser a standing
// worker's own principal, whose subsequently-clean token would then pass
// isHumanAdmin everywhere else. EXCEPT the very first create_superuser, which
// is open when no superuser exists yet regardless of caller (exactly like
// `manage.py createsuperuser` on a fresh database) — the bootstrap branch
// never consults isHumanAdmin, so dev's first-run flow is unaffected.
func registerAdmin(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "whoami",
		Description: "Report who the brain sees you as: your verified principal (email), whether you're a superuser, and whether you're allowed to use this brain."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOut, error) {
			p, _ := actor(req)
			out := whoamiOut{Principal: p, IsSuperuser: opts.isAdmin(req), Allowed: true}
			if opts.Principals != nil {
				out.Allowed = opts.Principals.Allowed(p)
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "create_superuser",
		Description: "Create or promote a superuser (admin). The FIRST superuser is free to create when none exists yet (bootstrap); after that, only an existing superuser may. Omit email to promote yourself."},
		func(_ context.Context, req *mcp.CallToolRequest, in createSuperuserIn) (*mcp.CallToolResult, principalOut, error) {
			if opts.Principals == nil {
				return nil, principalOut{}, fmt.Errorf("role store unavailable (dev mode)")
			}
			caller, _ := actor(req)
			email := in.Email
			if email == "" {
				email = caller // promote yourself
			}
			if email == "" {
				return nil, principalOut{}, fmt.Errorf("no email: provide one or authenticate so I can promote you")
			}
			bootstrap := opts.Principals.SuperuserCount() == 0
			if !bootstrap && !opts.isHumanAdmin(req) {
				return nil, principalOut{Email: email}, fmt.Errorf("forbidden: only a superuser can create another (a superuser already exists)")
			}
			if err := opts.Principals.CreateSuperuser(email, caller); err != nil {
				return nil, principalOut{Email: email}, err
			}
			msg := "promoted to superuser"
			if bootstrap {
				msg = "bootstrap superuser created — you now own this brain"
			}
			return nil, principalOut{OK: true, Email: email, Message: msg, Bootstrap: bootstrap}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_principals",
		Description: "List everyone allowed to use the brain and who is a superuser. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listPrincipalsOut, error) {
			if opts.Principals == nil {
				return nil, listPrincipalsOut{Principals: []principals.Principal{}}, nil
			}
			if !opts.isAdmin(req) {
				return nil, listPrincipalsOut{}, fmt.Errorf("forbidden: superuser only")
			}
			ps, err := opts.Principals.List()
			if ps == nil {
				ps = []principals.Principal{}
			}
			return nil, listPrincipalsOut{Principals: ps}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "add_member",
		Description: "Allow a (non-superuser) principal to use the brain. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in emailIn) (*mcp.CallToolResult, principalOut, error) {
			if opts.Principals == nil {
				return nil, principalOut{}, fmt.Errorf("role store unavailable (dev mode)")
			}
			if !opts.isHumanAdmin(req) {
				return nil, principalOut{Email: in.Email}, fmt.Errorf("forbidden: superuser only")
			}
			caller, _ := actor(req)
			if err := opts.Principals.AddMember(in.Email, caller); err != nil {
				return nil, principalOut{Email: in.Email}, err
			}
			return nil, principalOut{OK: true, Email: in.Email, Message: "added as member"}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "set_superuser",
		Description: "Promote or demote an EXISTING principal's superuser flag. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in setSuperuserIn) (*mcp.CallToolResult, principalOut, error) {
			if opts.Principals == nil {
				return nil, principalOut{}, fmt.Errorf("role store unavailable (dev mode)")
			}
			if !opts.isHumanAdmin(req) {
				return nil, principalOut{Email: in.Email}, fmt.Errorf("forbidden: superuser only")
			}
			ok, err := opts.Principals.SetSuperuser(in.Email, in.IsSuperuser)
			if err != nil {
				return nil, principalOut{Email: in.Email}, err
			}
			if !ok {
				return nil, principalOut{Email: in.Email}, fmt.Errorf("no such principal %q (add_member first)", in.Email)
			}
			return nil, principalOut{OK: true, Email: in.Email, Message: "updated"}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "mint_observer",
		Description: "Mint a READ-ONLY observer token. The holder can WATCH the swarm (the live UI, /api/state, /events) but CANNOT act (no claims, no instructions, no MCP tool calls). Hand it to a dashboard, an ops integration, or a demo viewer. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in mintObserverIn) (*mcp.CallToolResult, observerTokenOut, error) {
			if opts.MintObserver == nil {
				return nil, observerTokenOut{}, fmt.Errorf("observer tokens unavailable: delegation is not enabled on this brain")
			}
			// isHumanAdmin, not isAdmin: minting a standing token is a privileged
			// write, so a delegated subagent / worker session rolled up to a
			// superuser must not self-authorize it — same gate as every other
			// principal-table write (see registerAdmin's isHumanAdmin note).
			if !opts.isHumanAdmin(req) {
				return nil, observerTokenOut{}, fmt.Errorf("forbidden: superuser only")
			}
			principal := in.Principal
			if principal == "" {
				principal, _ = actor(req) // mint for yourself
			}
			if principal == "" {
				return nil, observerTokenOut{}, fmt.Errorf("no principal: provide one or authenticate so I can scope the token")
			}
			ttl := 24 * time.Hour
			if in.TTLSeconds > 0 {
				ttl = time.Duration(in.TTLSeconds) * time.Second
			}
			tok, err := opts.MintObserver(principal, ttl)
			if err != nil {
				return nil, observerTokenOut{}, err
			}
			return nil, observerTokenOut{OK: true, Token: tok,
				Usage: "watch read-only with:  corral-observe --brain <brain-url> --token <token>"}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "remove_principal",
		Description: "Revoke a principal's access to the brain entirely. Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in emailIn) (*mcp.CallToolResult, principalOut, error) {
			if opts.Principals == nil {
				return nil, principalOut{}, fmt.Errorf("role store unavailable (dev mode)")
			}
			if !opts.isHumanAdmin(req) {
				return nil, principalOut{Email: in.Email}, fmt.Errorf("forbidden: superuser only")
			}
			ok, err := opts.Principals.Remove(in.Email)
			if err != nil {
				return nil, principalOut{Email: in.Email}, err
			}
			if !ok {
				return nil, principalOut{Email: in.Email}, fmt.Errorf("no such principal %q", in.Email)
			}
			return nil, principalOut{OK: true, Email: in.Email, Message: "removed"}, nil
		})
}
