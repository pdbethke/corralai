// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

func registerTaskArtifacts(s *mcp.Server, q *queue.Store, artStore *taskartifacts.Store) {
	if q == nil || artStore == nil {
		return
	}

	type saveArtifactIn struct {
		Name     string `json:"name" jsonschema:"the agent's fallback name"`
		TaskID   int64  `json:"task_id" jsonschema:"the active task ID"`
		Filename string `json:"filename" jsonschema:"name of the artifact file, e.g. screenshot.png"`
		MimeType string `json:"mime_type" jsonschema:"MIME type of the artifact, e.g. image/png"`
		Data     string `json:"data" jsonschema:"base64-encoded string payload of the artifact"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "save_task_artifact",
		Description: "Save a task execution artifact (like a screenshot or file) directly to the database.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in saveArtifactIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)

		// 1. Claim Authorization Check: Verify task is claimed by the requesting agent
		t, err := q.TaskByID(in.TaskID)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("lookup task %d: %w", in.TaskID, err)
		}
		if t == nil {
			return nil, okOut{OK: false}, fmt.Errorf("task %d not found", in.TaskID)
		}
		if t.Status != "claimed" || t.ClaimedBy != agent {
			return nil, okOut{OK: false}, fmt.Errorf("forbidden: task %d is not claimed by agent %q", in.TaskID, agent)
		}

		// 2. Decode payload and check size limit (5MB)
		dataBytes, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("decode base64 artifact data: %w", err)
		}
		if len(dataBytes) > 5*1024*1024 {
			return nil, okOut{OK: false}, fmt.Errorf("payload exceeds 5MB size limit")
		}

		// 3. MIME Verification and Type Allowlist
		detectedType := http.DetectContentType(dataBytes)
		allowedMime := false
		safePrefixes := []string{"image/png", "image/jpeg", "text/plain", "application/json"}
		for _, p := range safePrefixes {
			if strings.HasPrefix(detectedType, p) {
				allowedMime = true
				break
			}
		}
		if !allowedMime {
			return nil, okOut{OK: false}, fmt.Errorf("dangerous or invalid content type: %s", detectedType)
		}
		// Match declared MIME type prefix (e.g. image/png matches image/png; charset=binary)
		if !strings.HasPrefix(detectedType, in.MimeType) {
			return nil, okOut{OK: false}, fmt.Errorf("MIME type mismatch: declared %q, detected %q", in.MimeType, detectedType)
		}

		// 4. Filename Sanitization
		safeFilename := filepath.Base(in.Filename)

		// 5. Save in dedicated SQLite database
		id, err := artStore.SaveArtifact(t.MissionID, in.TaskID, agent, safeFilename, in.MimeType, dataBytes)
		if err != nil {
			log.Printf("save_task_artifact: failed: %v", err)
			return nil, okOut{OK: false}, err
		}

		log.Printf("save_task_artifact: saved artifact %d (%s) for task %d in dedicated db", id, safeFilename, in.TaskID)
		return nil, okOut{OK: true}, nil
	})

	type listArtifactsIn struct {
		TaskID    int64 `json:"task_id,omitempty" jsonschema:"filter by task ID"`
		MissionID int64 `json:"mission_id,omitempty" jsonschema:"filter by mission ID"`
	}

	type artifactMetaOut struct {
		ID        int64   `json:"id"`
		MissionID int64   `json:"mission_id"`
		TaskID    int64   `json:"task_id"`
		Agent     string  `json:"agent"`
		Name      string  `json:"name"`
		MimeType  string  `json:"mime_type"`
		Size      int     `json:"size"`
		CreatedTS float64 `json:"created_ts"`
	}

	type listArtifactsOut struct {
		Artifacts []artifactMetaOut `json:"artifacts"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_task_artifacts",
		Description: "List metadata of saved task/mission artifacts from the database.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in listArtifactsIn) (*mcp.CallToolResult, listArtifactsOut, error) {
		var list []taskartifacts.TaskArtifact
		var err error

		if in.TaskID > 0 {
			list, err = artStore.GetArtifactsForTask(in.TaskID)
		} else if in.MissionID > 0 {
			list, err = artStore.GetArtifactsForMission(in.MissionID)
		} else {
			return nil, listArtifactsOut{}, fmt.Errorf("either task_id or mission_id must be provided")
		}

		if err != nil {
			return nil, listArtifactsOut{}, err
		}

		out := listArtifactsOut{Artifacts: []artifactMetaOut{}}
		for _, a := range list {
			out.Artifacts = append(out.Artifacts, artifactMetaOut{
				ID:        a.ID,
				MissionID: a.MissionID,
				TaskID:    a.TaskID,
				Agent:     a.Agent,
				Name:      a.Name,
				MimeType:  a.MimeType,
				Size:      len(a.Data),
				CreatedTS: a.CreatedTS,
			})
		}
		return nil, out, nil
	})
}
