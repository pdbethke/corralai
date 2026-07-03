// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/repoindex"
)

type repoSearchIn struct {
	Name  string `json:"name"`
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
}

type repoSearchOut struct {
	Hits []repoindex.Hit `json:"hits"`
}

// errNotRepoMission is the shared error every repo-scoped tool returns when the
// caller holds no claimed task on a repo mission. Defined once and reused by
// repoDirFor (repofiles.go) and repo_search so the message stays identical.
var errNotRepoMission = fmt.Errorf("not on a repo mission")

func registerRepoSearch(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "repo_search",
		Description: "Semantic/hybrid code search over your mission's working copy. Returns path:line ranges ranked by meaning (and keyword). Use it to find where something is handled before editing."},
		func(_ context.Context, req *mcp.CallToolRequest, in repoSearchIn) (*mcp.CallToolResult, repoSearchOut, error) {
			mid, _ := opts.Queue.ClaimedMission(identity(req, in.Name))
			if mid == 0 {
				return nil, repoSearchOut{}, errNotRepoMission
			}
			mi, err := opts.Missions.Mission(mid)
			if err != nil || mi == nil || mi.Repo == "" {
				return nil, repoSearchOut{}, errNotRepoMission
			}
			hits, err := opts.Index.Search(mid, in.Query, in.K)
			if err != nil {
				return nil, repoSearchOut{}, err
			}
			if hits == nil {
				hits = []repoindex.Hit{}
			}
			return nil, repoSearchOut{Hits: hits}, nil
		})
}
