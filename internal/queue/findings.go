// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"database/sql"
	"fmt"
	"strings"
)

// Finding statuses. Only open is set on write; addressed/dismissed are reached
// solely via SetFindingStatus (a manual operator action in #2). The re-planner
// that consumes open findings is sub-project #3.
const (
	FindingOpen      = "open"
	FindingAddressed = "addressed"
	FindingDismissed = "dismissed"
)

var findingTypes = map[string]bool{
	"vuln": true, "bug": true, "design-flaw": true,
	"missing-req": true, "regression": true, "note": true,
	"change-request": true, // client feedback (judgment-tier; the lead routes it)
}
var severityRank = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}
var findingStatuses = map[string]bool{FindingOpen: true, FindingAddressed: true, FindingDismissed: true}

// SeverityRank orders severities (low<medium<high<critical) for thresholds and UI
// sorting. An unknown severity ranks 0.
func SeverityRank(sev string) int { return severityRank[sev] }

// Finding is a structured, actionable observation a bee reports — the feedback
// the re-planner will act on. It is far more useful than a prose summary.
type Finding struct {
	ID              int64   `json:"id"`
	MissionID       int64   `json:"mission_id"`
	TaskID          int64   `json:"task_id,omitempty"` // the task that surfaced it; 0 = standalone
	Reporter        string  `json:"reporter"`
	ReporterModel   string  `json:"reporter_model,omitempty"`   // model that filed this finding (from HostBook)
	ReporterBackend string  `json:"reporter_backend,omitempty"` // backend that filed this finding (from HostBook)
	Type            string  `json:"type"`                       // vuln|bug|design-flaw|missing-req|regression|note
	Severity        string  `json:"severity"`                   // low|medium|high|critical
	Target          string  `json:"target,omitempty"`
	Evidence        string  `json:"evidence,omitempty"`
	SuggestedAction string  `json:"suggested_action,omitempty"`
	Status          string  `json:"status"`
	Recurring       bool    `json:"recurring,omitempty"` // a prior finding had the same type+target — the lesson didn't land
	CreatedTS       float64 `json:"created_ts"`
}

// AddFinding validates and stores a finding (always open), returning its id. A
// junk type or severity is rejected, never silently dropped.
func (s *Store) AddFinding(f Finding) (int64, error) {
	if f.MissionID == 0 {
		return 0, fmt.Errorf("mission_id required")
	}
	if f.Reporter == "" {
		return 0, fmt.Errorf("reporter required")
	}
	if !findingTypes[f.Type] {
		return 0, fmt.Errorf("invalid finding type %q (want vuln|bug|design-flaw|missing-req|regression|note)", f.Type)
	}
	if _, ok := severityRank[f.Severity]; !ok {
		return 0, fmt.Errorf("invalid severity %q (want low|medium|high|critical)", f.Severity)
	}
	// Recurrence: a prior finding with the same type+target means a past fix or
	// lesson didn't hold — the learning loop's feedback signal.
	recurring := 0
	if f.Target != "" {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM findings WHERE type=? AND target=?`, f.Type, f.Target).Scan(&n); err == nil && n > 0 {
			recurring = 1
		}
	}
	res, err := s.db.Exec(
		`INSERT INTO findings (mission_id,task_id,reporter,type,severity,target,evidence,suggested_action,status,recurring,created_ts,reporter_model,reporter_backend)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.MissionID, f.TaskID, f.Reporter, f.Type, f.Severity, f.Target, f.Evidence, f.SuggestedAction, FindingOpen, recurring, now(),
		f.ReporterModel, f.ReporterBackend,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// OpenRemediationExists reports whether the mission already has an in-flight
// (not yet done) reflex fix task for a finding of this type+target. The reflex
// re-planner uses it to deduplicate recurring findings: nine reports of the
// same broken go.mod should ride ONE fix/re-verify pair, not spawn nine.
// Reflex task keys are finding-id-scoped ("fix-f<ID>"), so the finding table
// links a remediation task back to the type+target it was spawned for.
func (s *Store) OpenRemediationExists(missionID int64, ftype, target string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tasks t
		 JOIN findings f ON t.key = 'fix-f' || f.id AND t.mission_id = f.mission_id
		 WHERE t.mission_id=? AND f.type=? AND f.target=? AND t.status != ?`,
		missionID, ftype, target, StatusDone,
	).Scan(&n)
	return n > 0, err
}

// Findings lists a mission's findings, newest first; empty status = all.
func (s *Store) Findings(missionID int64, status string) ([]Finding, error) {
	q := findingsSelect + ` WHERE mission_id=?`
	args := []any{missionID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	return s.queryFindings(q+` ORDER BY id DESC`, args...)
}

// AllFindings returns recent findings across missions for the live UI,
// capped at 200 rows (a feed, not a full export). The learn sweep must NOT
// use this — it needs every row; see AllFindingsUnbounded.
func (s *Store) AllFindings() ([]Finding, error) {
	return s.queryFindings(findingsSelect + ` ORDER BY id DESC LIMIT 200`)
}

// AllFindingsUnbounded returns EVERY finding across all missions, no row cap —
// the learn sweep's accessor. The sweep re-feeds all findings each cycle
// (sub-threshold signature groups persist nothing between sweeps), so a capped
// listing would silently drop old occurrences from the recurrence count. The
// live UI uses the capped AllFindings instead.
func (s *Store) AllFindingsUnbounded() ([]Finding, error) {
	return s.queryFindings(findingsSelect + ` ORDER BY id DESC`)
}

// FindingsFiltered returns findings matching the given filters. missionID=0
// means all missions (capped at 200 rows). Empty status or byModel means no
// filter on that field. Clauses are built dynamically, reusing findingsSelect
// and queryFindings so the projection is never duplicated.
func (s *Store) FindingsFiltered(missionID int64, status, byModel string) ([]Finding, error) {
	q := findingsSelect
	var args []any
	var clauses []string
	if missionID != 0 {
		clauses = append(clauses, "mission_id = ?")
		args = append(args, missionID)
	}
	if status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if byModel != "" {
		clauses = append(clauses, "reporter_model = ?")
		args = append(args, byModel)
	}
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY id DESC"
	if missionID == 0 {
		q += " LIMIT 200"
	}
	return s.queryFindings(q, args...)
}

// FindingByID fetches a single finding by its primary key. Returns (Finding,
// true, nil) on success, (Finding{}, false, nil) when no row matches, and
// (Finding{}, false, err) on a DB error. The scan path is intentionally the
// same queryFindings call used by all other readers — one scan, never duplicated.
func (s *Store) FindingByID(id int64) (Finding, bool, error) {
	fs, err := s.queryFindings(findingsSelect+` WHERE id = ?`, id)
	if err != nil {
		return Finding{}, false, err
	}
	if len(fs) == 0 {
		return Finding{}, false, nil
	}
	return fs[0], true, nil
}

// SetFindingStatus transitions a finding (open|addressed|dismissed). Returns
// false if no such finding. Validates the target status.
func (s *Store) SetFindingStatus(id int64, status string) (bool, error) {
	if !findingStatuses[status] {
		return false, fmt.Errorf("invalid status %q (want open|addressed|dismissed)", status)
	}
	res, err := s.db.Exec(`UPDATE findings SET status=? WHERE id=?`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const findingsSelect = `SELECT id,mission_id,task_id,reporter,type,severity,target,evidence,suggested_action,status,recurring,created_ts,reporter_model,reporter_backend FROM findings`

func (s *Store) queryFindings(q string, args ...any) ([]Finding, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var f Finding
		var target, evidence, action sql.NullString
		var recurring int
		if err := rows.Scan(&f.ID, &f.MissionID, &f.TaskID, &f.Reporter, &f.Type, &f.Severity,
			&target, &evidence, &action, &f.Status, &recurring, &f.CreatedTS,
			&f.ReporterModel, &f.ReporterBackend); err != nil {
			return nil, err
		}
		f.Target, f.Evidence, f.SuggestedAction = target.String, evidence.String, action.String
		f.Recurring = recurring == 1
		out = append(out, f)
	}
	return out, rows.Err()
}
