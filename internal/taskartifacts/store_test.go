// SPDX-License-Identifier: Elastic-2.0

package taskartifacts

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLookbookStore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "a.sqlite3"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	id1, err := store.SaveLookbookItem("mock1", "desc1", "image/png", []byte("PNGDATA-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveLookbookItem("mock2", "desc2", "image/jpeg", []byte("JPEGDATA-longer-2")); err != nil {
		t.Fatal(err)
	}

	// GetLookbookItem: single row, data included.
	it, err := store.GetLookbookItem(id1)
	if err != nil {
		t.Fatal(err)
	}
	if it == nil || it.Name != "mock1" || string(it.Data) != "PNGDATA-1" || it.MimeType != "image/png" {
		t.Fatalf("GetLookbookItem(%d) = %+v", id1, it)
	}
	// A missing id returns (nil, nil), not an error.
	miss, err := store.GetLookbookItem(9999)
	if err != nil {
		t.Fatal(err)
	}
	if miss != nil {
		t.Fatalf("missing id should be nil, got %+v", miss)
	}

	// GetLookbookItemsMeta: metadata + byte size, newest-first, no data loaded.
	metas, err := store.GetLookbookItemsMeta()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 metas, got %d", len(metas))
	}
	if metas[0].Name != "mock2" || metas[0].Size != len("JPEGDATA-longer-2") {
		t.Fatalf("meta[0] = %+v (want mock2, size 17)", metas[0])
	}
	if metas[1].Name != "mock1" || metas[1].Size != len("PNGDATA-1") {
		t.Fatalf("meta[1] = %+v (want mock1, size 9)", metas[1])
	}
}

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
