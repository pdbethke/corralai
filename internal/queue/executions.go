// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"database/sql"
	"strings"
)

// Execution is one shell command a swarm agent ran, recorded durably so the
// verification gate can ask "did <verify> ever pass for this mission?".
type Execution struct {
	MissionID int64  `json:"mission_id"`
	Agent     string `json:"agent"`
	Role      string `json:"role"`
	Command   string `json:"command"`
	ExitCode  int    `json:"exit_code"`
	OK        bool   `json:"ok"`
	TS        int64  `json:"ts"` // Unix seconds
}

// RecordExecution durably stores one execution.
func (s *Store) RecordExecution(e Execution) error {
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO executions (mission_id,agent,role,command,exit_code,ok,ts) VALUES (?,?,?,?,?,?,?)`,
		e.MissionID, e.Agent, e.Role, e.Command, e.ExitCode, ok, e.TS,
	)
	return err
}

// ExecutionsByAgent returns up to limit of an agent's recorded executions, newest
// first — the DURABLE command history (unbounded by the in-memory ExecRing) that
// the brain's narrator uses for long-build post-mortems.
func (s *Store) ExecutionsByAgent(agent string, limit int) ([]Execution, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT mission_id,agent,role,command,exit_code,ok,ts FROM executions
		 WHERE agent=? ORDER BY ts DESC, id DESC LIMIT ?`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		var ok int
		if err := rows.Scan(&e.MissionID, &e.Agent, &e.Role, &e.Command, &e.ExitCode, &ok, &e.TS); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExecutionsByMission returns every recorded execution for a mission, newest
// first — the durable twin of what the live UI's ExecRing shows, and the
// source the mission-history replay draws execution bursts from.
func (s *Store) ExecutionsByMission(missionID int64) ([]Execution, error) {
	rows, err := s.db.Query(
		`SELECT mission_id,agent,role,command,exit_code,ok,ts FROM executions
		 WHERE mission_id=? ORDER BY ts DESC`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		var ok int
		if err := rows.Scan(&e.MissionID, &e.Agent, &e.Role, &e.Command, &e.ExitCode, &ok, &e.TS); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllExecutions returns every recorded execution across all missions, oldest
// first — the leaderboard's source for per-agent verify-gate pass rates. Like
// AllFindingsUnbounded, this is a full export (no row cap): a confidence count
// derived from a capped feed would silently understate sample size.
func (s *Store) AllExecutions() ([]Execution, error) {
	rows, err := s.db.Query(
		`SELECT mission_id,agent,role,command,exit_code,ok,ts FROM executions ORDER BY ts ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		var ok int
		if err := rows.Scan(&e.MissionID, &e.Agent, &e.Role, &e.Command, &e.ExitCode, &ok, &e.TS); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// MissionPassedVerify reports whether a mission's current verify state passes.
// See MissionPassedVerifySince for matching semantics.
func (s *Store) MissionPassedVerify(missionID int64, verify string) (bool, error) {
	return s.MissionPassedVerifySince(missionID, verify, 0)
}

// MissionPassedVerifySince reports whether the latest matching execution in the
// mission at/after sinceTS exited 0. Command matching is strict: command equals
// verify, or command starts with verify+" " (prefix with args). Empty verify is
// "ungated" and returns true.
func (s *Store) MissionPassedVerifySince(missionID int64, verify string, sinceTS int64) (bool, error) {
	if verify == "" {
		return true, nil
	}
	verify = strings.TrimSpace(verify)
	if verify == "" {
		return true, nil
	}
	var ok int
	err := s.db.QueryRow(
		`SELECT ok FROM executions
		 WHERE mission_id=? AND ts>=? AND (command=? OR command LIKE ?)
		 ORDER BY ts DESC, id DESC LIMIT 1`,
		missionID, sinceTS, verify, verify+" %",
	).Scan(&ok)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return ok == 1, err
}
