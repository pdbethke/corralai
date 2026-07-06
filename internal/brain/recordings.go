// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/recordings"
)

type listRecordingsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max rows (default 100)"`
}
type listRecordingsOut struct {
	Recordings []recordings.Summary `json:"recordings"`
}

type queryRecordingsIn struct {
	Report string `json:"report,omitempty" jsonschema:"named report: slugs|event_kinds|findings_by_model (default event_kinds)"`
	SQL    string `json:"sql,omitempty" jsonschema:"ad-hoc read-only SELECT/WITH over recordings_missions/recordings_events (superuser only)"`
	RowCap int    `json:"row_cap,omitempty" jsonschema:"max rows for SQL mode (default 1000)"`
}
type queryRecordingsOut struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type recordingReplayIn struct {
	Slug      string `json:"slug,omitempty" jsonschema:"recording slug"`
	MissionID int64  `json:"mission_id,omitempty" jsonschema:"mission id (latest recording for that mission)"`
}
type recordingReplayOut struct {
	Mission *recordings.MissionMeta `json:"mission,omitempty"`
	Events  []recordings.Event      `json:"events"`
}

type shareRecordingIn struct {
	Slug       string `json:"slug" jsonschema:"recording slug"`
	Visibility string `json:"visibility" jsonschema:"share target: team|public"`
	TeamID     string `json:"team_id,omitempty" jsonschema:"required when visibility=team"`
}
type shareRecordingOut struct {
	OK      bool                    `json:"ok"`
	Mission *recordings.MissionMeta `json:"mission,omitempty"`
}

var recordingsReports = map[string]string{
	"slugs": `SELECT slug, mission_id, task_count, done_task_count, finding_count, duration_seconds, exported_ts
	          FROM recordings_missions ORDER BY exported_ts DESC`,
	"event_kinds": `SELECT kind, count(*) AS n FROM recordings_events GROUP BY kind ORDER BY n DESC`,
	"findings_by_model": `SELECT COALESCE(NULLIF(model,''),'(not recorded)') AS model,
	                             COALESCE(NULLIF(json_extract_string(detail_json,'$.severity'),''),'(none)') AS severity,
	                             count(*) AS findings
	                      FROM recordings_events
	                      WHERE kind='finding_reported'
	                      GROUP BY 1,2 ORDER BY findings DESC, model, severity`,
}

func registerRecordings(s *mcp.Server, opts Options) {
	if opts.Recordings == nil {
		return
	}

	mcp.AddTool(s, &mcp.Tool{Name: "list_recordings",
		Description: "List exported replay recordings persisted in the recordings DuckDB. ACL: superusers see all; members see own private + team + public; dev mode is open."},
		func(_ context.Context, req *mcp.CallToolRequest, in listRecordingsIn) (*mcp.CallToolResult, listRecordingsOut, error) {
			rows, err := opts.Recordings.List(in.Limit)
			if err != nil {
				return nil, listRecordingsOut{}, err
			}
			if rows == nil {
				rows = []recordings.Summary{}
			}
			actorPrincipal, _ := actor(req)
			out := make([]recordings.Summary, 0, len(rows))
			for _, row := range rows {
				if recordingVisibleToSummary(row, actorPrincipal, opts.isAdmin(req), opts.Principals == nil) {
					out = append(out, row)
				}
			}
			return nil, listRecordingsOut{Recordings: out}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "query_recordings",
		Description: "Analyze exported recordings in DuckDB: run a named report (slugs|event_kinds|findings_by_model) or superuser-only ad-hoc read-only SQL over recordings_missions and recordings_events."},
		func(_ context.Context, req *mcp.CallToolRequest, in queryRecordingsIn) (*mcp.CallToolResult, queryRecordingsOut, error) {
			if strings.TrimSpace(in.SQL) != "" {
				if !opts.isAdmin(req) {
					return nil, queryRecordingsOut{}, fmt.Errorf("forbidden: ad-hoc SQL is superuser only")
				}
				rep, err := opts.Recordings.Query(in.SQL, in.RowCap)
				if err != nil {
					return nil, queryRecordingsOut{}, err
				}
				return nil, queryRecordingsOut{Columns: rep.Columns, Rows: rep.Rows}, nil
			}
			name := strings.TrimSpace(in.Report)
			if name == "" {
				name = "event_kinds"
			}
			sqlText, ok := recordingsReports[name]
			if !ok {
				return nil, queryRecordingsOut{}, fmt.Errorf("unknown report %q", name)
			}
			rep, err := opts.Recordings.Query(sqlText, 2000)
			if err != nil {
				return nil, queryRecordingsOut{}, err
			}
			return nil, queryRecordingsOut{Columns: rep.Columns, Rows: rep.Rows}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_recording_replay",
		Description: "Return one exported recording's replay event stream by slug (or mission_id). ACL: superusers all; members own private + team + public; dev mode open."},
		func(_ context.Context, req *mcp.CallToolRequest, in recordingReplayIn) (*mcp.CallToolResult, recordingReplayOut, error) {
			if strings.TrimSpace(in.Slug) == "" && in.MissionID == 0 {
				return nil, recordingReplayOut{}, fmt.Errorf("slug or mission_id required")
			}
			actorPrincipal, _ := actor(req)
			isSuper := opts.isAdmin(req)
			devOpen := opts.Principals == nil
			if in.MissionID != 0 {
				meta, err := opts.Recordings.MissionByID(in.MissionID)
				if err != nil {
					return nil, recordingReplayOut{}, err
				}
				if meta == nil {
					return nil, recordingReplayOut{}, fmt.Errorf("no recording for mission_id %d", in.MissionID)
				}
				if !recordingVisibleToMeta(meta, actorPrincipal, isSuper, devOpen) {
					return nil, recordingReplayOut{}, fmt.Errorf("forbidden: recording is not visible to this principal")
				}
				evs, err := opts.Recordings.ReplayBySlug(meta.Slug)
				if err != nil {
					return nil, recordingReplayOut{}, err
				}
				return nil, recordingReplayOut{Mission: meta, Events: evs}, nil
			}
			slug := strings.TrimSpace(in.Slug)
			meta, err := opts.Recordings.MissionBySlug(slug)
			if err != nil {
				return nil, recordingReplayOut{}, err
			}
			if meta == nil {
				return nil, recordingReplayOut{}, fmt.Errorf("no recording for slug %q", slug)
			}
			if !recordingVisibleToMeta(meta, actorPrincipal, isSuper, devOpen) {
				return nil, recordingReplayOut{}, fmt.Errorf("forbidden: recording is not visible to this principal")
			}
			evs, err := opts.Recordings.ReplayBySlug(slug)
			if err != nil {
				return nil, recordingReplayOut{}, err
			}
			return nil, recordingReplayOut{Mission: meta, Events: evs}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "share_recording",
		Description: "Share one recording with a team catalog (visibility=team) or everyone (visibility=public). Human-gated: delegated subagent tokens are refused; auth mode requires superuser or owner."},
		func(_ context.Context, req *mcp.CallToolRequest, in shareRecordingIn) (*mcp.CallToolResult, shareRecordingOut, error) {
			if subagentOf(req) != "" {
				return nil, shareRecordingOut{}, fmt.Errorf("forbidden: superuser only (human gate refuses delegation tokens)")
			}
			slug := strings.TrimSpace(in.Slug)
			meta, err := opts.Recordings.MissionBySlug(slug)
			if err != nil {
				return nil, shareRecordingOut{}, err
			}
			if meta == nil {
				return nil, shareRecordingOut{}, fmt.Errorf("no recording for slug %q", slug)
			}
			p, _ := actor(req)
			owner := p != "" && strings.EqualFold(strings.TrimSpace(meta.SharedBy), p)
			if opts.Principals != nil && !opts.isHumanAdmin(req) && !owner {
				return nil, shareRecordingOut{}, fmt.Errorf("forbidden: superuser or recording owner only")
			}
			if err := opts.Recordings.Share(slug, in.Visibility, in.TeamID, actorOf(req), 0); err != nil {
				return nil, shareRecordingOut{}, err
			}
			updated, err := opts.Recordings.MissionBySlug(slug)
			if err != nil {
				return nil, shareRecordingOut{}, err
			}
			return nil, shareRecordingOut{OK: true, Mission: updated}, nil
		})
}

func recordingVisibleToSummary(s recordings.Summary, principal string, isSuper, devOpen bool) bool {
	if devOpen || isSuper {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(s.Visibility)) {
	case "public", "team":
		return true
	case "", "private":
		return principal != "" && strings.EqualFold(strings.TrimSpace(s.SharedBy), principal)
	default:
		return false
	}
}

func recordingVisibleToMeta(m *recordings.MissionMeta, principal string, isSuper, devOpen bool) bool {
	if m == nil {
		return false
	}
	if devOpen || isSuper {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.Visibility)) {
	case "public", "team":
		return true
	case "", "private":
		return principal != "" && strings.EqualFold(strings.TrimSpace(m.SharedBy), principal)
	default:
		return false
	}
}
