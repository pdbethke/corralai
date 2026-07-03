// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/repo"
)

// maxPushBytes caps the total content a single repo_push may carry, symmetric
// with the snapshot's 64 MiB cap. Without it a misbehaving or compromised bee
// could exhaust the brain daemon's memory/disk via ApplyFiles.
const maxPushBytes = 64 << 20

type snapshotIn struct {
	Name string `json:"name"`
}
type snapshotOut struct {
	DataB64  string            `json:"data_b64"`
	Manifest map[string]string `json:"manifest"`
	BaseRev  string            `json:"base_rev"`
}
type pushFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type pushIn struct {
	Name    string     `json:"name"`
	Files   []pushFile `json:"files"`
	BaseRev string     `json:"base_rev"`
}
type pushOut struct {
	Applied []string `json:"applied"`
	Stale   bool     `json:"stale"`
	BaseRev string   `json:"base_rev,omitempty"` // the HEAD after apply; agent uses this to refresh its stale-detection base
}

// registerRepoSync adds repo_snapshot and repo_push tools. The working-copy
// directory is resolved via the existing repoDirFor resolver (DRY with repofiles.go).
func registerRepoSync(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "repo_snapshot",
		Description: "Pull a .git-free snapshot of your mission's working copy (tar.gz, base64) plus a path→sha manifest and the current base_rev.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, snapshotOut, error) {
		dir, err := repoDirFor(req, opts.Queue, opts.Missions, opts.Workspace, in.Name)
		if err != nil {
			return nil, snapshotOut{}, err
		}
		data, manifest, err := opts.Repo.Snapshot(dir)
		if err != nil {
			return nil, snapshotOut{}, err
		}
		rev, _ := opts.Repo.HeadSHA(ctx, dir)
		return nil, snapshotOut{
			DataB64:  base64.StdEncoding.EncodeToString(data),
			Manifest: manifest,
			BaseRev:  rev,
		}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "repo_push",
		Description: "Push changed files back to your mission's working copy. The brain applies them and signals stale when base_rev has advanced.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in pushIn) (*mcp.CallToolResult, pushOut, error) {
		dir, err := repoDirFor(req, opts.Queue, opts.Missions, opts.Workspace, in.Name)
		if err != nil {
			return nil, pushOut{}, err
		}
		var total int
		for _, f := range in.Files {
			total += len(f.Content)
			if total > maxPushBytes {
				return nil, pushOut{}, fmt.Errorf("repo_push payload exceeds %d bytes", int64(maxPushBytes))
			}
		}
		writes := make([]repo.FileWrite, 0, len(in.Files))
		for _, f := range in.Files {
			writes = append(writes, repo.FileWrite{Path: f.Path, Content: f.Content})
		}
		applied, err := opts.Repo.ApplyFiles(dir, writes)
		if err != nil {
			return nil, pushOut{}, err
		}
		cur, _ := opts.Repo.HeadSHA(ctx, dir)
		stale := in.BaseRev != "" && cur != "" && in.BaseRev != cur
		return nil, pushOut{Applied: applied, Stale: stale, BaseRev: cur}, nil
	})
}
