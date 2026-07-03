// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// rec records a mission event, best-effort: nil store is a no-op and an error
// only logs — telemetry must never block or fail a tool.
func rec(tel *telemetry.Store, missionID int64, kind, actor, subject string, detail map[string]any) {
	if tel == nil {
		return
	}
	if err := tel.Record(telemetry.Event{MissionID: missionID, Kind: kind, Actor: actor, Subject: subject, Detail: detail}); err != nil {
		log.Printf("telemetry %s: %v", kind, err)
	}
}

// recModel is like rec but also stamps a model on the event — used for
// finding_reported so the reporter's model threads to the DuckDB event log.
func recModel(tel *telemetry.Store, missionID int64, kind, actor, subject, model string, detail map[string]any) {
	if tel == nil {
		return
	}
	if err := tel.Record(telemetry.Event{MissionID: missionID, Kind: kind, Actor: actor, Subject: subject, Model: model, Detail: detail}); err != nil {
		log.Printf("telemetry %s: %v", kind, err)
	}
}

// recTask records a task event, resolving the task's mission + key for context.
func recTask(tel *telemetry.Store, q *queue.Store, taskID int64, kind, actor string) {
	if tel == nil {
		return
	}
	var mid int64
	var key string
	if t, _ := q.TaskByID(taskID); t != nil {
		mid, key = t.MissionID, t.Key
	}
	rec(tel, mid, kind, actor, key, nil)
}

// actorOf is the verified principal behind a request, or "lead" for the
// re-planning actions an operator/lead drives in dev.
func actorOf(req *mcp.CallToolRequest) string {
	if p, _ := actor(req); p != "" {
		return p
	}
	return "lead"
}
