// SPDX-License-Identifier: Elastic-2.0

package taskartifacts

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS task_artifacts (
  id           INTEGER PRIMARY KEY,
  mission_id   INTEGER NOT NULL,
  task_id      INTEGER NOT NULL,
  agent        TEXT    NOT NULL DEFAULT '',
  name         TEXT    NOT NULL DEFAULT '',
  mime_type    TEXT    NOT NULL DEFAULT '',
  data         BLOB    NOT NULL,
  created_ts   REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_task_artifacts_task ON task_artifacts(task_id);
CREATE INDEX IF NOT EXISTS ix_task_artifacts_mission ON task_artifacts(mission_id);

CREATE TABLE IF NOT EXISTS lookbook_items (
  id           INTEGER PRIMARY KEY,
  name         TEXT    NOT NULL DEFAULT '',
  description  TEXT    NOT NULL DEFAULT '',
  mime_type    TEXT    NOT NULL DEFAULT '',
  data         BLOB    NOT NULL,
  created_ts   REAL    NOT NULL
);
`

// TaskArtifact is a record of an artifact (e.g. screenshot, file) created by a task.
type TaskArtifact struct {
	ID        int64   `json:"id"`
	MissionID int64   `json:"mission_id"`
	TaskID    int64   `json:"task_id"`
	Agent     string  `json:"agent"`
	Name      string  `json:"name"`
	MimeType  string  `json:"mime_type"`
	Data      []byte  `json:"data"`
	CreatedTS float64 `json:"created_ts"`
}

type Store struct{ db *sql.DB }

// Open returns a Store backed by a SQLite database file at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialize writes to SQLite file
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }

// SaveArtifact inserts a task execution artifact (like a screenshot) into the database.
func (s *Store) SaveArtifact(missionID, taskID int64, agent, name, mimeType string, data []byte) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO task_artifacts (mission_id, task_id, agent, name, mime_type, data, created_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		missionID, taskID, agent, name, mimeType, data, float64(time.Now().UnixNano())/1e9)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetArtifactsForTask retrieves all artifacts associated with a specific task ID.
func (s *Store) GetArtifactsForTask(taskID int64) ([]TaskArtifact, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, task_id, agent, name, mime_type, data, created_ts
		FROM task_artifacts WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskArtifact
	for rows.Next() {
		var ta TaskArtifact
		if err := rows.Scan(&ta.ID, &ta.MissionID, &ta.TaskID, &ta.Agent, &ta.Name, &ta.MimeType, &ta.Data, &ta.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, ta)
	}
	return out, rows.Err()
}

// GetArtifactsForMission retrieves all artifacts associated with a specific mission ID.
func (s *Store) GetArtifactsForMission(missionID int64) ([]TaskArtifact, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, task_id, agent, name, mime_type, data, created_ts
		FROM task_artifacts WHERE mission_id = ? ORDER BY id ASC`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskArtifact
	for rows.Next() {
		var ta TaskArtifact
		if err := rows.Scan(&ta.ID, &ta.MissionID, &ta.TaskID, &ta.Agent, &ta.Name, &ta.MimeType, &ta.Data, &ta.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, ta)
	}
	return out, rows.Err()
}

type LookbookItem struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	MimeType    string  `json:"mime_type"`
	Data        []byte  `json:"data"`
	CreatedTS   float64 `json:"created_ts"`
}

// SaveLookbookItem inserts a design/lookbook item mockup into the database.
func (s *Store) SaveLookbookItem(name, description, mimeType string, data []byte) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO lookbook_items (name, description, mime_type, data, created_ts)
		VALUES (?, ?, ?, ?, ?)`,
		name, description, mimeType, data, float64(time.Now().UnixNano())/1e9)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetLookbookItems retrieves all saved lookbook items.
func (s *Store) GetLookbookItems() ([]LookbookItem, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, mime_type, data, created_ts
		FROM lookbook_items ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LookbookItem
	for rows.Next() {
		var li LookbookItem
		if err := rows.Scan(&li.ID, &li.Name, &li.Description, &li.MimeType, &li.Data, &li.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, li)
	}
	return out, rows.Err()
}

// DeleteLookbookItem deletes a lookbook item by ID.
func (s *Store) DeleteLookbookItem(id int64) error {
	_, err := s.db.Exec(`DELETE FROM lookbook_items WHERE id = ?`, id)
	return err
}
