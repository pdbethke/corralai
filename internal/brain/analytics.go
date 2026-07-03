// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type analyticsIn struct {
	Report string `json:"report,omitempty" jsonschema:"named report: missions|agents|kinds|findings|replans|sprints|model_comparison (default kinds)"`
	SQL    string `json:"sql,omitempty" jsonschema:"ad-hoc read-only SELECT/WITH query over the events table (superuser only)"`
}
type analyticsOut struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// registerAnalytics adds the mission-telemetry analytics tool: named reports over
// the event log, or read-only ad-hoc SQL (superuser). Registered only when a
// telemetry store is configured.
func registerAnalytics(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "mission_analytics",
		Description: "Analyze the mission event log (DuckDB): a named report (missions|agents|kinds|findings|replans|sprints|model_comparison) or an ad-hoc read-only SELECT. Returns columns + rows."},
		func(_ context.Context, req *mcp.CallToolRequest, in analyticsIn) (*mcp.CallToolResult, analyticsOut, error) {
			if in.SQL != "" {
				if !opts.isAdmin(req) {
					return nil, analyticsOut{}, fmt.Errorf("forbidden: ad-hoc SQL is superuser only")
				}
				rep, err := opts.Telemetry.Query(in.SQL)
				if err != nil {
					return nil, analyticsOut{}, err
				}
				return nil, analyticsOut{Columns: rep.Columns, Rows: rep.Rows}, nil
			}
			name := strings.TrimSpace(in.Report)
			if name == "" {
				name = "kinds"
			}
			rep, err := opts.Telemetry.RunReport(name)
			if err != nil {
				return nil, analyticsOut{}, err
			}
			return nil, analyticsOut{Columns: rep.Columns, Rows: rep.Rows}, nil
		})
}
