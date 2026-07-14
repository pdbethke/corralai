// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"sort"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// LeaderboardCell is one (model, role) slice of the empirical performance
// matrix: what corral's attributed telemetry says about how a model performed
// in a role, across every mission this brain has run. Samples is the number
// of completed-task observations behind this cell — the confidence signal a
// consumer (UI or otherwise) MUST weigh before treating the cell as a
// verdict; a cell with 2 samples is a data point, not a ranking.
type LeaderboardCell struct {
	Model string `json:"model"` // e.g. "gemini-3.1-pro-preview"; "(unknown model)" when unattributed
	Role  string `json:"role"`  // builder|tester|pentester|reviewer|""; "" = role not recorded for this observation

	TasksCompleted  int     `json:"tasks_completed"`
	AvgTaskDuration float64 `json:"avg_task_duration_s,omitempty"` // mean claim→done seconds, over tasks with both timestamps
	DurationSamples int     `json:"duration_samples,omitempty"`    // how many of TasksCompleted contributed a duration

	Executions      int     `json:"executions"`
	ExecutionsOK    int     `json:"executions_ok"`
	ExecPassRatePct float64 `json:"exec_pass_rate_pct,omitempty"` // 100*ExecutionsOK/Executions; omitted when Executions==0

	FindingsRaised   int `json:"findings_raised"`
	FindingsResolved int `json:"findings_resolved"` // addressed + dismissed (any terminal disposition)

	ReworkCount int `json:"rework_count"` // task_reissued events for this agent/role — a friction proxy, not "bugs caused"

	// Samples is the leaderboard's headline confidence count: completed tasks
	// attributed to this cell. Other metrics above may rest on a different,
	// independently-reported count (Executions, FindingsRaised) — those are
	// surfaced individually rather than folded into one misleading total.
	Samples int `json:"samples"`
}

// Leaderboard is the full model×role matrix.
type Leaderboard struct {
	Cells []LeaderboardCell `json:"cells"`
}

const unknownModel = "(unknown model)"

// cellAgg accumulates one cell's raw sums before the final divide — kept
// unexported so the public LeaderboardCell never carries fields that would
// need explaining in the JSON contract.
type cellAgg struct {
	tasksCompleted   int
	durationSum      float64
	durationSamples  int
	execTotal        int
	execOK           int
	findingsRaised   int
	findingsResolved int
	rework           int
}

type cellKey struct{ model, role string }

// BuildLeaderboard computes the model×role performance matrix from the
// brain's attributed telemetry: completed tasks (queue.Store), executions
// (verify-gate ok/exit_code, queue.Store), findings (queue.Store, already
// model-attributed at write time via stampModel), and rework signals
// (task_reissued events, telemetry.Store). All three inputs are optional and
// independently nil-safe (degrade-never-block): a brain with no telemetry
// store still returns task/execution/finding cells; a brain with an empty
// queue returns an empty matrix, never an error.
//
// Model attribution for tasks and executions joins the claiming/executing
// agent's name against hosts (the live HostBook — the same join stampModel
// uses for findings), so an agent never seen via report_host contributes
// under "(unknown model)" rather than being silently dropped. Role for a
// finding is recovered from its task (when TaskID references one); a
// standalone finding (TaskID==0) or one whose task has no role attributes to
// role "".
func BuildLeaderboard(q *queue.Store, hosts *HostBook, tel *telemetry.Store) (Leaderboard, error) {
	if q == nil {
		return Leaderboard{Cells: []LeaderboardCell{}}, nil
	}

	modelOf := func(agent string) string {
		if hosts != nil {
			if h, ok := hosts.Get(agent); ok && h.Model != "" {
				return h.Model
			}
		}
		return unknownModel
	}

	agg := map[cellKey]*cellAgg{}
	get := func(model, role string) *cellAgg {
		k := cellKey{model, role}
		c, ok := agg[k]
		if !ok {
			c = &cellAgg{}
			agg[k] = c
		}
		return c
	}

	// Tasks: completion count + claim→done duration, keyed by claimer's model
	// and the task's own role. Active() returns every task across every
	// mission regardless of status, which also gives us the id->role map
	// findings below need (a finding may reference a task in any status).
	tasks, err := q.Active()
	if err != nil {
		return Leaderboard{}, err
	}
	roleByTaskID := make(map[int64]string, len(tasks))
	for _, t := range tasks {
		roleByTaskID[t.ID] = t.Role
		if t.Status != queue.StatusDone {
			continue
		}
		c := get(modelOf(t.ClaimedBy), t.Role)
		c.tasksCompleted++
		if t.ClaimedTS > 0 && t.DoneTS > t.ClaimedTS {
			c.durationSum += t.DoneTS - t.ClaimedTS
			c.durationSamples++
		}
	}

	// Executions: verify-gate proxy (share of reported executions that exited
	// ok), keyed by the executing agent's model and the execution's own role.
	execs, err := q.AllExecutions()
	if err != nil {
		return Leaderboard{}, err
	}
	for _, e := range execs {
		c := get(modelOf(e.Agent), e.Role)
		c.execTotal++
		if e.OK {
			c.execOK++
		}
	}

	// Findings: already model-attributed at write time (stampModel), so no
	// HostBook join here; role is recovered via the finding's task, when any.
	findings, err := q.AllFindingsUnbounded()
	if err != nil {
		return Leaderboard{}, err
	}
	for _, f := range findings {
		model := f.ReporterModel
		if model == "" {
			model = modelOf(f.Reporter)
		}
		role := ""
		if f.TaskID != 0 {
			role = roleByTaskID[f.TaskID]
		}
		c := get(model, role)
		c.findingsRaised++
		if f.Status == queue.FindingAddressed || f.Status == queue.FindingDismissed {
			c.findingsResolved++
		}
	}

	// Rework: task_reissued events (a bee re-claiming a task whose reply was
	// lost — see internal/brain/tasks.go) grouped by actor+role, model
	// attributed the same way as tasks/executions. Best-effort: a nil or
	// error-prone telemetry store just leaves rework at 0 everywhere.
	if tel != nil {
		if reissues, rerr := tel.CountByActorAndDetailRole("task_reissued"); rerr == nil {
			for _, r := range reissues {
				c := get(modelOf(r.Actor), r.Role)
				c.rework += r.Count
			}
		}
	}

	// Adversarial-pool outcomes (M-1): fold each certified run's gate-earned
	// (model, role, pass/fail) signal into the SAME per-role cell executions
	// feed, so a model that keeps passing the pool's adequacy gate for a
	// role ranks higher (via ExecPassRatePct) for that role on the NEXT run
	// — closing the compounding-routing loop advpoolLeaderboardSink.Record
	// writes into telemetry but nothing previously read back. Best-effort,
	// same as rework: a nil or error-prone telemetry store just leaves this
	// contribution at zero.
	if tel != nil {
		if outcomes, oerr := tel.AdvPoolOutcomeCounts(); oerr == nil {
			for _, o := range outcomes {
				model := o.Model
				if model == "" {
					model = unknownModel
				}
				c := get(model, o.Role)
				c.execTotal += o.Pass + o.Fail
				c.execOK += o.Pass
			}
		}
	}

	cells := make([]LeaderboardCell, 0, len(agg))
	for k, c := range agg {
		cell := LeaderboardCell{
			Model:            k.model,
			Role:             k.role,
			TasksCompleted:   c.tasksCompleted,
			DurationSamples:  c.durationSamples,
			Executions:       c.execTotal,
			ExecutionsOK:     c.execOK,
			FindingsRaised:   c.findingsRaised,
			FindingsResolved: c.findingsResolved,
			ReworkCount:      c.rework,
			Samples:          c.tasksCompleted,
		}
		if c.durationSamples > 0 {
			cell.AvgTaskDuration = c.durationSum / float64(c.durationSamples)
		}
		if c.execTotal > 0 {
			cell.ExecPassRatePct = 100 * float64(c.execOK) / float64(c.execTotal)
		}
		cells = append(cells, cell)
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Model != cells[j].Model {
			return cells[i].Model < cells[j].Model
		}
		return cells[i].Role < cells[j].Role
	})
	return Leaderboard{Cells: cells}, nil
}
