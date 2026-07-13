// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Task statuses for re-planning (the LLM lead's mechanism). Both are terminal and
// non-open, so they never block MissionDone.
//
// CancelTask abandons work that is no longer needed; ReopenTask re-does finished
// work whose foundation changed; SupersedeTask replaces a task with a newer one
// and records the lineage.

// CancelTask marks a non-terminal task cancelled. Returns false if the task is
// missing or already terminal (done/cancelled/superseded).
func (s *Store) CancelTask(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE tasks SET status=?, claimed_by=NULL, claim_expires_ts=NULL
		 WHERE id=? AND status IN (?,?,?)`,
		StatusCancelled, id, StatusPending, StatusReady, StatusClaimed,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReopenTask sends a done task back to ready — re-doing completed work whose
// premise changed (e.g. rebuild on a corrected data layer). Returns false if the
// task isn't done.
func (s *Store) ReopenTask(id int64) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE tasks SET status=?, claimed_by=NULL, claimed_ts=NULL, done_ts=NULL, result=NULL
		 WHERE id=? AND status=?`,
		StatusReady, id, StatusDone,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SupersedeTask marks oldID superseded and enqueues spec as its replacement,
// recording lineage (new.supersedes = oldID). Pending dependents that waited on
// the old task are rewritten to wait on the replacement, so the DAG stays valid
// and the mission can't hang on a dependency that will never complete. The new
// task starts pending. Done atomically so a crash can't leave the old task
// superseded with no replacement.
//
// Two recovery rules learned live (2026-07-03, a lead un-stranding a
// cancelled pipeline):
//   - The replacement KEY is auto-uniquified: reusing the old key (the natural
//     thing for a model to do) used to hard-fail on UNIQUE(mission_id,key)
//     mid-recovery. Empty key => derived from the old key.
//   - The replacement INHERITS the old task's verify gate when the spec doesn't
//     set one — re-planning must not silently drop the mission's guarantees.
func (s *Store) SupersedeTask(oldID int64, spec TaskSpec) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var missionID int64
	var oldKey, oldVerify string
	if err := tx.QueryRow(`SELECT mission_id, key, verify FROM tasks WHERE id=?`, oldID).Scan(&missionID, &oldKey, &oldVerify); err != nil {
		return 0, err
	}
	if spec.Key == "" {
		spec.Key = oldKey
	}
	uk, err := s.uniqueKeyTx(tx, missionID, spec.Key)
	if err != nil {
		return 0, err
	}
	spec.Key = uk
	if spec.Verify == "" {
		spec.Verify = oldVerify
	}

	// Read pending dependents inside the same tx as the rewrite below, so the
	// read-decide-write sequence is atomic: nothing can enqueue or change a
	// dependent between the read and the commit and be missed (the TOCTOU
	// that could otherwise orphan a dependency on the superseded key).
	type dep struct {
		id   int64
		deps []string
	}
	var dependents []dep
	rows, err := tx.Query(`SELECT id, depends_on FROM tasks WHERE mission_id=? AND status=?`, missionID, StatusPending)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		var depJSON string
		if err := rows.Scan(&id, &depJSON); err != nil {
			_ = rows.Close()
			return 0, err
		}
		var ds []string
		_ = json.Unmarshal([]byte(depJSON), &ds)
		for _, d := range ds {
			if d == oldKey {
				dependents = append(dependents, dep{id, ds})
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	if _, err := tx.Exec(
		`UPDATE tasks SET status=?, claimed_by=NULL, claim_expires_ts=NULL WHERE id=?`,
		StatusSuperseded, oldID,
	); err != nil {
		return 0, err
	}
	deps := spec.DependsOn
	if deps == nil {
		deps = []string{}
	}
	b, _ := json.Marshal(deps)
	res, err := tx.Exec(
		`INSERT INTO tasks (mission_id,key,role,title,instruction,status,depends_on,verify,created_ts,supersedes)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		missionID, spec.Key, spec.Role, spec.Title, spec.Instruction, StatusPending, string(b), spec.Verify, now(), oldID,
	)
	if err != nil {
		return 0, err
	}
	newID, _ := res.LastInsertId()

	// Rewrite each pending dependent: depend on the replacement, not the old task.
	for _, d := range dependents {
		rewritten := make([]string, len(d.deps))
		for i, k := range d.deps {
			if k == oldKey {
				rewritten[i] = spec.Key
			} else {
				rewritten[i] = k
			}
		}
		nb, _ := json.Marshal(rewritten)
		if _, err := tx.Exec(`UPDATE tasks SET depends_on=? WHERE id=?`, string(nb), d.id); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}

// uniqueKeyTx returns base if unused in the mission, else base-r2, base-r3, …
// (bounded; the queue caps out long before 10k replacements of one task).
// Takes tx (never s.db) so callers inside SupersedeTask's transaction don't
// deadlock the single-connection pool.
func (s *Store) uniqueKeyTx(tx *sql.Tx, missionID int64, base string) (string, error) {
	key := base
	for i := 2; i < 10000; i++ {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE mission_id=? AND key=?`, missionID, key).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return key, nil
		}
		key = fmt.Sprintf("%s-r%d", base, i)
	}
	return "", fmt.Errorf("could not derive a unique key from %q", base)
}

// CancelTaskGuarded is CancelTask with the dependency check the flat cancel
// lacks: cancelling a task whose dependents are still live strands them
// forever (promotion only fires off DONE dependencies — observed live
// 2026-07-03). Without cascade it REFUSES and returns the live dependents'
// keys so the caller (usually the LLM lead) can supersede or retarget
// instead. With cascade it deliberately cancels the whole dependent subtree,
// returning every cancelled id.
func (s *Store) CancelTaskGuarded(id int64, cascade bool) (cancelled []int64, blocked []string, err error) {
	t, err := s.TaskByID(id)
	if err != nil || t == nil {
		return nil, nil, fmt.Errorf("task %d not found", id)
	}

	// Fetch the mission's tasks ONCE and index direct dependents by dependency
	// key, so the BFS below walks in memory instead of re-scanning the whole
	// mission per frontier node (was O(nodes x mission-size)).
	all, err := s.List(t.MissionID)
	if err != nil {
		return nil, nil, err
	}
	dependentsOf := map[string][]Task{}
	for _, x := range all {
		for _, d := range x.DependsOn {
			dependentsOf[d] = append(dependentsOf[d], x)
		}
	}

	// Walk the dependent subtree (keys -> live dependents), breadth-first.
	type node struct {
		id  int64
		key string
	}
	frontier := []node{{t.ID, t.Key}}
	subtree := []node{}
	seen := map[int64]bool{t.ID: true}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		subtree = append(subtree, cur)
		for _, x := range dependentsOf[cur.key] {
			if seen[x.ID] || x.Status == StatusDone || x.Status == StatusCancelled || x.Status == StatusSuperseded {
				continue
			}
			seen[x.ID] = true
			frontier = append(frontier, node{x.ID, x.Key})
		}
	}
	dependents := subtree[1:] // everything but the root

	if !cascade && len(dependents) > 0 {
		for _, d := range dependents {
			blocked = append(blocked, d.key)
		}
		return nil, blocked, nil
	}
	for _, n := range subtree {
		if ok, cerr := s.CancelTask(n.id); cerr != nil {
			return cancelled, nil, cerr
		} else if ok {
			cancelled = append(cancelled, n.id)
		}
	}
	return cancelled, nil, nil
}

// RetargetDependents re-points every non-terminal task that depends on fromKey
// at toKey instead — the one-step recovery for "this dependency is dead, but
// that other task covers it" (the lead judged exactly this live and then
// hallucinated update_task; this is the tool it wanted). Refuses a missing
// target and refuses to create a cycle (toKey must not itself reach fromKey
// through depends_on). Returns how many tasks were rewritten.
func (s *Store) RetargetDependents(missionID int64, fromKey, toKey string) (int, error) {
	ts, err := s.List(missionID)
	if err != nil {
		return 0, err
	}
	byKey := map[string]Task{}
	for _, x := range ts {
		byKey[x.Key] = x
	}
	if _, ok := byKey[toKey]; !ok {
		return 0, fmt.Errorf("target key %q not found in mission %d", toKey, missionID)
	}
	// Cycle guard: walk toKey's dependency closure; refuse if it reaches fromKey.
	stack := []string{toKey}
	visited := map[string]bool{}
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[k] {
			continue
		}
		visited[k] = true
		if k == fromKey {
			return 0, fmt.Errorf("retargeting %q -> %q would create a dependency cycle", fromKey, toKey)
		}
		for _, d := range byKey[k].DependsOn {
			stack = append(stack, d)
		}
	}

	n := 0
	for _, x := range ts {
		if x.Status == StatusDone || x.Status == StatusCancelled || x.Status == StatusSuperseded {
			continue
		}
		changed := false
		deps := make([]string, len(x.DependsOn))
		for i, d := range x.DependsOn {
			if d == fromKey {
				deps[i] = toKey
				changed = true
			} else {
				deps[i] = d
			}
		}
		if !changed {
			continue
		}
		b, _ := json.Marshal(deps)
		if _, err := s.db.Exec(`UPDATE tasks SET depends_on=? WHERE id=?`, string(b), x.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// TaskByID returns one task (nil if missing) — used by the mutation tools to
// validate targets and report lineage.
func (s *Store) TaskByID(id int64) (*Task, error) {
	ts, err := s.query(taskSelect+` WHERE id=?`, id)
	if err != nil || len(ts) == 0 {
		if err == sql.ErrNoRows {
			err = nil
		}
		return nil, err
	}
	return &ts[0], nil
}

const taskSelect = `SELECT id,mission_id,key,role,title,instruction,status,depends_on,claimed_by,result,created_ts,claimed_ts,done_ts,claim_expires_ts,supersedes,verify FROM tasks`
