// SPDX-License-Identifier: Elastic-2.0

package taskartifacts

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestTaskArtifactsStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "artifacts.sqlite3")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	// 1. Save artifact
	data := []byte("some binary data")
	id, err := store.SaveArtifact(1, 2, "Bob", "screenshot.png", "image/png", data)
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected lastInsertID > 0, got %d", id)
	}

	// 2. Get artifacts for task
	list, err := store.GetArtifactsForTask(2)
	if err != nil {
		t.Fatalf("GetArtifactsForTask: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(list))
	}
	art := list[0]
	if art.ID != id || art.MissionID != 1 || art.TaskID != 2 || art.Agent != "Bob" || art.Name != "screenshot.png" || art.MimeType != "image/png" {
		t.Fatalf("unexpected metadata: %+v", art)
	}
	if !bytes.Equal(art.Data, data) {
		t.Fatalf("data mismatch: got %q, want %q", art.Data, data)
	}

	// 3. Get artifacts for mission
	list2, err := store.GetArtifactsForMission(1)
	if err != nil {
		t.Fatalf("GetArtifactsForMission: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected 1 artifact for mission, got %d", len(list2))
	}
}
