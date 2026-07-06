// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/recordings"
)

type replayFile struct {
	Events []recordings.Event `json:"events"`
}

type metaFile struct {
	Directive       string         `json:"directive"`
	TaskCount       int            `json:"task_count"`
	DoneTaskCount   int            `json:"done_task_count"`
	FindingCount    int            `json:"finding_count"`
	DurationSeconds float64        `json:"duration_seconds"`
	Models          []string       `json:"models"`
	Platform        map[string]any `json:"platform"`
}

func mustReadJSON(path string, out any) error {
	b, err := os.ReadFile(path) // #nosec G304 -- operator-controlled export helper inputs
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func main() {
	dbPath := flag.String("db", "", "DuckDB path (default: CORRALAI_RECORDINGS_DB or ~/.claude/corralai_recordings.duckdb)")
	slug := flag.String("slug", "", "recording slug")
	missionID := flag.Int64("mission-id", 0, "mission id")
	replayPath := flag.String("replay", "", "path to scrubbed replay json (with events[])")
	metaPath := flag.String("meta", "", "path to metadata json")
	flag.Parse()

	if strings.TrimSpace(*slug) == "" {
		fmt.Fprintln(os.Stderr, "slug is required")
		os.Exit(2)
	}
	if strings.TrimSpace(*replayPath) == "" || strings.TrimSpace(*metaPath) == "" {
		fmt.Fprintln(os.Stderr, "replay and meta are required")
		os.Exit(2)
	}
	path := strings.TrimSpace(*dbPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("CORRALAI_RECORDINGS_DB"))
	}
	if path == "" {
		path = recordings.DefaultDB
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir db dir: %v\n", err)
		os.Exit(1)
	}
	store, err := recordings.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open recordings db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	var replay replayFile
	if err := mustReadJSON(*replayPath, &replay); err != nil {
		fmt.Fprintf(os.Stderr, "read replay: %v\n", err)
		os.Exit(1)
	}
	var meta metaFile
	if err := mustReadJSON(*metaPath, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "read meta: %v\n", err)
		os.Exit(1)
	}

	row := recordings.MissionMeta{
		Slug:            *slug,
		MissionID:       *missionID,
		Directive:       meta.Directive,
		TaskCount:       meta.TaskCount,
		DoneTaskCount:   meta.DoneTaskCount,
		FindingCount:    meta.FindingCount,
		DurationSeconds: meta.DurationSeconds,
		Models:          meta.Models,
		Platform:        meta.Platform,
	}
	if err := store.Upsert(row, replay.Events); err != nil {
		fmt.Fprintf(os.Stderr, "upsert recording: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("recordings db updated: %s (slug=%s mission=%d events=%d)\n", path, *slug, *missionID, len(replay.Events))
}
