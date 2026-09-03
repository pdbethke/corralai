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

// importOpts is this binary's flag set, bound to a struct.
//
// A PRIVATE FlagSet, for the reason corral-observe uses one: the package-level
// flag.CommandLine is shared with every dependency that registers on it at
// init, and one of corral's already does — go-rod's lib/defaults adds a `-rod`
// flag, which reached corral-observe's shipped -h until it was moved off the
// global set. A binary should advertise its own interface and nothing else.
type importOpts struct {
	db, slug, replay, meta string
	missionID              int64
}

func importFlags(o *importOpts) *flag.FlagSet {
	fs := flag.NewFlagSet("corral-recordings-import", flag.ExitOnError)
	fs.StringVar(&o.db, "db", "", "DuckDB path (default: CORRALAI_RECORDINGS_DB or ~/.claude/corralai_recordings.duckdb)")
	fs.StringVar(&o.slug, "slug", "", "recording slug")
	fs.Int64Var(&o.missionID, "mission-id", 0, "mission id")
	fs.StringVar(&o.replay, "replay", "", "path to scrubbed replay json (with events[])")
	fs.StringVar(&o.meta, "meta", "", "path to metadata json")
	return fs
}

// requiredMissing names the required flags the operator did not supply, in flag
// order, or nil when the invocation is complete.
//
// Returns the WHOLE list rather than stopping at the first: an operator running
// an import by hand should learn everything they still need in one go, not one
// flag per attempt.
func requiredMissing(o importOpts) []string {
	var missing []string
	for _, f := range []struct {
		name, val string
	}{
		{"slug", o.slug}, {"replay", o.replay}, {"meta", o.meta},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, "--"+f.name)
		}
	}
	return missing
}

func main() {
	var o importOpts
	// #nosec G104 -- ExitOnError exits on a bad flag; the error is nil here.
	_ = importFlags(&o).Parse(os.Args[1:])
	dbPath, slug := &o.db, &o.slug
	missionID, replayPath, metaPath := &o.missionID, &o.replay, &o.meta

	if missing := requiredMissing(o); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "corral-recordings-import: missing required flag(s): %s\n", strings.Join(missing, " "))
		os.Exit(2)
	}
	path := strings.TrimSpace(*dbPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("CORRALAI_RECORDINGS_DB"))
	}
	if path == "" {
		path = recordings.DefaultDB
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- db path is operator-chosen via --db, CORRALAI_RECORDINGS_DB, or fixed DefaultDB; not remote attacker input
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
