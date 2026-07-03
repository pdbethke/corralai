// SPDX-License-Identifier: Elastic-2.0

package queue

// Execution is one shell command a swarm agent ran, recorded durably so the
// verification gate can ask "did <verify> ever pass for this mission?".
type Execution struct {
	MissionID int64
	Agent     string
	Role      string
	Command   string
	ExitCode  int
	OK        bool
	TS        int64 // Unix seconds
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

// MissionPassedVerify reports whether some execution for missionID ran a command
// CONTAINING verify and exited 0 — the deterministic basis for the gate. An empty
// verify is treated as "ungated" and returns true.
func (s *Store) MissionPassedVerify(missionID int64, verify string) (bool, error) {
	if verify == "" {
		return true, nil
	}
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM executions WHERE mission_id=? AND ok=1 AND instr(command, ?)>0`,
		missionID, verify,
	).Scan(&n)
	return n > 0, err
}
