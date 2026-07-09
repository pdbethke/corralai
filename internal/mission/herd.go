// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"database/sql"
	"encoding/json"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

// Herd is a mission's composed team: which model serves each role, which MCP
// gateway endpoints its agents may consume, and which lookbook items it attaches
// as design directives. Persisted per-mission in mission_herds so a mission owns
// its herd (the "compose now, concurrency later" plumbing) — v1 also applies
// RoleModels to the run at launch via the existing global policy.
type Herd struct {
	RoleModels  map[string]rolemodel.ModelRef `json:"role_models"`
	Endpoints   []string                      `json:"endpoints"`
	LookbookIDs []int64                       `json:"lookbook_ids"`
}

// IsEmpty reports whether a herd carries no per-mission overrides — no role
// models, no endpoints, no lookbook attachments. Callers skip SaveHerd for an
// empty herd so "no mission_herds row" stays the honest signal for "no override".
func (h Herd) IsEmpty() bool {
	return len(h.RoleModels) == 0 && len(h.Endpoints) == 0 && len(h.LookbookIDs) == 0
}

// SaveHerd upserts a mission's herd config. Idempotent per mission_id.
func (s *Store) SaveHerd(missionID int64, h Herd) error {
	rm, err := json.Marshal(h.RoleModels)
	if err != nil {
		return err
	}
	ep, err := json.Marshal(h.Endpoints)
	if err != nil {
		return err
	}
	lb, err := json.Marshal(h.LookbookIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO mission_herds(mission_id,role_models,endpoints,lookbook_ids,created_ts)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(mission_id) DO UPDATE SET
		   role_models=excluded.role_models, endpoints=excluded.endpoints, lookbook_ids=excluded.lookbook_ids`,
		missionID, string(rm), string(ep), string(lb), now())
	return err
}

// Herd returns a mission's herd config, or (nil, false, nil) when none was saved.
func (s *Store) Herd(missionID int64) (*Herd, bool, error) {
	var rm, ep, lb string
	err := s.db.QueryRow(`SELECT role_models,endpoints,lookbook_ids FROM mission_herds WHERE mission_id=?`, missionID).
		Scan(&rm, &ep, &lb)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var h Herd
	_ = json.Unmarshal([]byte(rm), &h.RoleModels)
	_ = json.Unmarshal([]byte(ep), &h.Endpoints)
	_ = json.Unmarshal([]byte(lb), &h.LookbookIDs)
	return &h, true, nil
}
