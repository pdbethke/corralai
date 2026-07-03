// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/memory"
)

type searchIn struct {
	Query string `json:"query" jsonschema:"search terms"`
	Scope string `json:"scope,omitempty" jsonschema:"project tier to restrict to; '*' or empty = all"`
	Type  string `json:"type,omitempty" jsonschema:"filter by type, e.g. 'lesson'"`
	Limit int    `json:"limit,omitempty"`
}
type hitsOut struct {
	Hits []memory.Hit `json:"hits"`
}

type getIn struct {
	Name string `json:"name" jsonschema:"slug or name of the entry"`
}

type listIn struct {
	Scope string `json:"scope,omitempty"`
	Type  string `json:"type,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type addIn struct {
	Name        string `json:"name" jsonschema:"short kebab-case-ish title (becomes the slug; re-adding updates)"`
	Body        string `json:"body" jsonschema:"the fact/guidance (markdown)"`
	Description string `json:"description,omitempty" jsonschema:"one-line summary used for recall relevance"`
	Type        string `json:"type,omitempty" jsonschema:"free-form type; common: lesson | decision | convention | note | reference (use 'lesson' for what broke + the fix)"`
	Project     string `json:"project,omitempty" jsonschema:"project tier — free-form label; defaults to the entry's front-matter, else 'default'"`
	Shared      bool   `json:"shared,omitempty" jsonschema:"team-visible (shared knowledge base); admin only"`
	Author      string `json:"author,omitempty" jsonschema:"the agent recording this (stamped by your client); the brain takes the authoritative identity when authenticated"`
}
type addOut struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Status string `json:"status"`
}
type promoteMemIn struct {
	Name   string `json:"name" jsonschema:"slug or name of the entry"`
	Shared bool   `json:"shared" jsonschema:"true to share team-wide, false to make it private again"`
}

var errNotMemOwner = fmt.Errorf("forbidden: only a memory owner can add private memory")

// auditKnowledge writes a best-effort audit row into the coord store (if
// configured). Errors are silently swallowed — audit failure must not fail the
// tool call.
func auditKnowledge(opts Options, req *mcp.CallToolRequest, action string, detail map[string]any) {
	if opts.Coord == nil {
		return
	}
	opts.Coord.Audit(identity(req, "agent"), action, detail)
}

func registerMemory(s *mcp.Server, mem *memory.Store, opts Options) {
	// Read model: an OWNER sees everything (private + shared); any other authorized
	// caller sees only SHARED entries (the team knowledge base). sharedOnly = !owner.
	mcp.AddTool(s, &mcp.Tool{Name: "search_memory",
		Description: "Full-text (BM25) search across the memory you can see (your own if owner, plus the shared team knowledge base). Scope to a tier or search all."},
		func(_ context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, hitsOut, error) {
			if err := mem.EnsureBuilt(); err != nil {
				return nil, hitsOut{}, err
			}
			h, err := mem.Search(in.Query, in.Scope, in.Type, in.Limit, !opts.isMemoryOwner(req))
			return nil, hitsOut{Hits: h}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_memory", Description: "Fetch one memory entry's full body by slug or name (shared entries, plus your own if you're an owner)."},
		func(_ context.Context, req *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, memory.Entry, error) {
			if err := mem.EnsureBuilt(); err != nil {
				return nil, memory.Entry{}, err
			}
			e, err := mem.Get(in.Name, !opts.isMemoryOwner(req))
			if err != nil {
				return nil, memory.Entry{}, err
			}
			if e == nil {
				return nil, memory.Entry{}, fmt.Errorf("no memory entry %q", in.Name)
			}
			return nil, *e, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_memory", Description: "List memory entries you can see, optionally filtered by tier/type."},
		func(_ context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, hitsOut, error) {
			if err := mem.EnsureBuilt(); err != nil {
				return nil, hitsOut{}, err
			}
			h, err := mem.List(in.Scope, in.Type, in.Limit, !opts.isMemoryOwner(req))
			return nil, hitsOut{Hits: h}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "add_memory",
		Description: "Persist a memory entry (markdown file + reindex). Private entries (default) require a memory owner; shared=true entries (the team knowledge base) require an admin."},
		func(_ context.Context, req *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
			if in.Shared {
				if !opts.isAdmin(req) {
					return nil, addOut{}, errAdminOnly
				}
			} else if !opts.isMemoryOwner(req) {
				return nil, addOut{}, errNotMemOwner
			}
			author := identity(req, in.Author) // authoritative in auth mode; agent-supplied name in dev
			slug, path, status, err := mem.Add(in.Name, in.Body, in.Description, in.Type, in.Project, "", in.Shared, author)
			if err == nil {
				auditKnowledge(opts, req, "add_memory",
					map[string]any{"slug": slug, "shared": in.Shared})
			}
			return nil, addOut{Slug: slug, Path: path, Status: status}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "promote_memory",
		Description: "ADMIN: share an existing memory entry team-wide (shared=true) or make it private again (shared=false). Edits its frontmatter + reindexes."},
		func(_ context.Context, req *mcp.CallToolRequest, in promoteMemIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			ok, err := mem.SetShared(in.Name, in.Shared)
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no such entry"}, err
			}
			auditKnowledge(opts, req, "promote_memory",
				map[string]any{"slug": in.Name, "shared": in.Shared})
			vis := "private"
			if in.Shared {
				vis = "shared team-wide"
			}
			return nil, okMsg{OK: true, Message: in.Name + " is now " + vis}, nil
		})
}
