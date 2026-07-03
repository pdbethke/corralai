// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/artifacts"
)

type wireArtifact struct {
	Path       string  `json:"path"`
	Kind       string  `json:"kind"`
	Sha256     string  `json:"sha256"`
	Rev        int64   `json:"rev"`
	UpdatedTS  float64 `json:"updated_ts"`
	UpdatedBy  string  `json:"updated_by"`
	Deleted    bool    `json:"deleted"`
	ContentB64 string  `json:"content_b64,omitempty"`
}

func toWire(a artifacts.Artifact) wireArtifact {
	w := wireArtifact{
		Path: a.Path, Kind: a.Kind, Sha256: a.Sha256, Rev: a.Rev,
		UpdatedTS: a.UpdatedTS, UpdatedBy: a.UpdatedBy, Deleted: a.Deleted,
	}
	if !a.Deleted && a.Content != nil {
		w.ContentB64 = base64.StdEncoding.EncodeToString(a.Content)
	}
	return w
}

type syncHeadOut struct {
	HeadRev int64 `json:"head_rev"`
	Count   int   `json:"count"`
}
type syncPullIn struct {
	SinceRev int64  `json:"since_rev" jsonschema:"return artifacts changed after this rev (0 = everything)"`
	Prefix   string `json:"prefix,omitempty" jsonschema:"restrict to paths under this prefix, e.g. skills/roles/tester/ (equip one role)"`
}
type syncPullOut struct {
	HeadRev int64          `json:"head_rev"`
	Changes []wireArtifact `json:"changes"`
}
type syncPutIn struct {
	Path       string  `json:"path" jsonschema:"e.g. skills/deploy/SKILL.md or hooks/branch-guard.sh"`
	ContentB64 string  `json:"content_b64" jsonschema:"base64-encoded file content"`
	Mtime      float64 `json:"mtime,omitempty" jsonschema:"client file mtime (unix seconds) — used for conflict last-write-wins"`
}
type syncPutOut struct {
	Path   string `json:"path"`
	Rev    int64  `json:"rev"`
	Sha256 string `json:"sha256"`
}
type syncDeleteIn struct {
	Path string `json:"path"`
}
type syncDeleteOut struct {
	Path    string `json:"path"`
	Rev     int64  `json:"rev"`
	Deleted bool   `json:"deleted"`
}

// registerArtifacts adds the fleet skill/hook sync tools. Pull is open to any
// allowed member (the HTTP authz gate already ensures that); push (put/delete)
// edits the canonical fleet set that fans out to everyone — publishing or
// tombstoning an EXECUTABLE skill is strictly more behavior-shaping than
// approving a proposal, so it gates on isHumanAdmin (not just isAdmin): a
// delegation token rolled up to a superuser must not publish into the fleet's
// canonical set any more than it may vet its own proposals.
func registerArtifacts(s *mcp.Server, store *artifacts.Store, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "sync_head",
		Description: "Current head revision + live artifact count of the fleet's shared skills/hooks. Cheap poll to see if a `corral sync` would pull anything."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, syncHeadOut, error) {
			return nil, syncHeadOut{HeadRev: store.HeadRev(), Count: store.Count()}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sync_pull",
		Description: "Pull every shared skill/hook changed after since_rev (tombstones included), with content. The client writes skills to ~/.claude/skills and stages hooks for review."},
		func(_ context.Context, _ *mcp.CallToolRequest, in syncPullIn) (*mcp.CallToolResult, syncPullOut, error) {
			ch, err := store.ChangesPrefix(in.SinceRev, in.Prefix)
			if err != nil {
				return nil, syncPullOut{}, err
			}
			out := syncPullOut{HeadRev: store.HeadRev(), Changes: make([]wireArtifact, 0, len(ch))}
			for _, a := range ch {
				out.Changes = append(out.Changes, toWire(a))
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sync_put",
		Description: "Publish/replace a shared skill or hook into the fleet's canonical set (it fans out to every machine on their next sync). Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in syncPutIn) (*mcp.CallToolResult, syncPutOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, syncPutOut{Path: in.Path}, fmt.Errorf("forbidden: superuser only (publishing changes the whole fleet)")
			}
			content, err := base64.StdEncoding.DecodeString(in.ContentB64)
			if err != nil {
				return nil, syncPutOut{Path: in.Path}, fmt.Errorf("bad content_b64: %w", err)
			}
			rev, sha, err := store.Put(in.Path, content, identity(req, "operator"), in.Mtime)
			return nil, syncPutOut{Path: in.Path, Rev: rev, Sha256: sha}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "sync_delete",
		Description: "Remove a shared skill or hook from the fleet's canonical set (tombstoned so the deletion propagates). Superuser only."},
		func(_ context.Context, req *mcp.CallToolRequest, in syncDeleteIn) (*mcp.CallToolResult, syncDeleteOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, syncDeleteOut{Path: in.Path}, fmt.Errorf("forbidden: superuser only")
			}
			rev, ok, err := store.Delete(in.Path, identity(req, "operator"))
			return nil, syncDeleteOut{Path: in.Path, Rev: rev, Deleted: ok}, err
		})
}
