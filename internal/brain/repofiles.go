// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repo"
)

// repoDirFor resolves the working-copy directory for the caller's currently
// claimed repo mission. Returns an error when the caller holds no claimed task
// or when the claimed mission has no associated repo (workspace-only mission).
func repoDirFor(req *mcp.CallToolRequest, q *queue.Store, m *mission.Store, workspace, name string) (string, error) {
	mid, _ := q.ClaimedMission(identity(req, name))
	if mid == 0 {
		return "", errNotRepoMission
	}
	mi, err := m.Mission(mid)
	if err != nil || mi == nil || mi.Repo == "" {
		return "", errNotRepoMission
	}
	return mission.MissionDir(workspace, mid), nil
}

type readRepoIn struct {
	Name string `json:"name" jsonschema:"your agent name"`
	Path string `json:"path" jsonschema:"relative path to read (no .. escapes)"`
}

type readRepoOut struct {
	Content string `json:"content"`
}

type repoTreeIn struct {
	Name   string `json:"name" jsonschema:"your agent name"`
	Subdir string `json:"subdir,omitempty" jsonschema:"subdirectory to list (default: root)"`
}

type repoTreeOut struct {
	Files []string `json:"files"`
}

type repoGrepIn struct {
	Name  string `json:"name" jsonschema:"your agent name"`
	Query string `json:"query" jsonschema:"literal string to search for"`
	K     int    `json:"k,omitempty" jsonschema:"max hits (default 20)"`
}

type repoGrepOut struct {
	Hits []string `json:"hits"`
}

// registerRepoFiles adds read_repo, repo_tree, and repo_grep tools. Each tool
// resolves the working-copy directory from the caller's claimed mission via
// repoDirFor, so a bee can only read the repo it is actively working on.
func registerRepoFiles(s *mcp.Server, q *queue.Store, m *mission.Store, eng *repo.Engine, workspace string) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_repo",
		Description: "Read a file from the current repo mission's working copy. The path must be relative and must not escape the repo root.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in readRepoIn) (*mcp.CallToolResult, readRepoOut, error) {
		dir, err := repoDirFor(req, q, m, workspace, in.Name)
		if err != nil {
			return nil, readRepoOut{}, err
		}
		content, err := eng.ReadFile(dir, in.Path)
		if err != nil {
			return nil, readRepoOut{}, err
		}
		return nil, readRepoOut{Content: content}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "repo_tree",
		Description: "List files in the current repo mission's working copy, optionally scoped to a subdirectory.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in repoTreeIn) (*mcp.CallToolResult, repoTreeOut, error) {
		dir, err := repoDirFor(req, q, m, workspace, in.Name)
		if err != nil {
			return nil, repoTreeOut{}, err
		}
		files, err := eng.Tree(dir, in.Subdir)
		if err != nil {
			return nil, repoTreeOut{}, err
		}
		if files == nil {
			files = []string{}
		}
		return nil, repoTreeOut{Files: files}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "repo_grep",
		Description: "Search for a literal string across all files in the current repo mission's working copy.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in repoGrepIn) (*mcp.CallToolResult, repoGrepOut, error) {
		dir, err := repoDirFor(req, q, m, workspace, in.Name)
		if err != nil {
			return nil, repoGrepOut{}, err
		}
		hits, err := eng.Grep(dir, in.Query, in.K)
		if err != nil {
			return nil, repoGrepOut{}, err
		}
		if hits == nil {
			hits = []string{}
		}
		return nil, repoGrepOut{Hits: hits}, nil
	})
}
