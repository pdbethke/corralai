// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"database/sql"
	"strconv"
)

// Instruction is a command issued to an agent (the steer-down direction): the user
// (or another agent) posts it; the target agent pulls it, acts, and acks a result.
type Instruction struct {
	ID        int64   `json:"id"`
	Target    string  `json:"target"`
	Issuer    string  `json:"issuer,omitempty"`
	Text      string  `json:"text"`
	Status    string  `json:"status"` // pending | done
	Result    string  `json:"result,omitempty"`
	CreatedTS float64 `json:"created_ts"`
	AckedTS   float64 `json:"acked_ts,omitempty"`
}

// SendInstruction queues an instruction for target (an agent name). Returns the id.
func (s *Store) SendInstruction(issuer, target, text string) (int64, error) {
	r, err := s.db.Exec(
		"INSERT INTO instructions (target,issuer,text,status,created_ts) VALUES (?,?,?,'pending',?)",
		target, issuer, text, now())
	if err != nil {
		return 0, err
	}
	id, _ := r.LastInsertId()
	s.audit(issuer, "instruct", map[string]any{"target": target, "id": id, "text": text})
	return id, nil
}

func (s *Store) queryInstructions(where string, args ...any) ([]Instruction, error) {
	rows, err := s.db.Query(
		"SELECT id,target,issuer,text,status,result,created_ts,acked_ts FROM instructions "+where, args...) // #nosec G202 -- not injectable: where is a constant string literal from internal callers only (WHERE status=? AND target=? etc.); all values use ? placeholders
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instruction{}
	for rows.Next() {
		var in Instruction
		var result sql.NullString
		var acked sql.NullFloat64
		if err := rows.Scan(&in.ID, &in.Target, &in.Issuer, &in.Text, &in.Status, &result, &in.CreatedTS, &acked); err != nil {
			return nil, err
		}
		in.Result, in.AckedTS = result.String, acked.Float64
		out = append(out, in)
	}
	return out, rows.Err()
}

// PendingInstructions returns the agent's undelivered instructions (oldest first).
func (s *Store) PendingInstructions(agent string) ([]Instruction, error) {
	return s.queryInstructions("WHERE status='pending' AND target=? ORDER BY created_ts", agent)
}

// RecentInstructions returns the agent's instructions (pending + recently done) for display.
func (s *Store) RecentInstructions(agent string, limit int) ([]Instruction, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.queryInstructions("WHERE target=? ORDER BY created_ts DESC LIMIT "+strconv.Itoa(limit), agent)
}

// InstructionStatus returns an instruction's status ("pending"/"done"), "" if
// absent — used by the mission engine to detect phase completion.
func (s *Store) InstructionStatus(id int64) (string, error) {
	var st string
	err := s.db.QueryRow("SELECT status FROM instructions WHERE id=?", id).Scan(&st)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return st, err
}

// AckInstruction marks an instruction done with the agent's result.
func (s *Store) AckInstruction(id int64, agent, result string) (bool, error) {
	r, err := s.db.Exec("UPDATE instructions SET status='done', result=?, acked_ts=? WHERE id=? AND target=? AND status='pending'",
		result, now(), id, agent)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n > 0 {
		s.audit(agent, "ack", map[string]any{"id": id, "summary": result})
	}
	return n > 0, nil
}
