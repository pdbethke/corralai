// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/agentrole"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

type claimTaskIn struct {
	Name     string   `json:"name" jsonschema:"your agent (bee) name — who is claiming"`
	Roles    []string `json:"roles,omitempty" jsonschema:"roles you can serve, e.g. [\"builder\"]; omit to claim any ready task"`
	Instance string   `json:"instance,omitempty" jsonschema:"your instance identity (hostname) — lets the brain re-issue YOUR claim if the reply was lost, without letting a same-named replica steal live work"`
}
type claimTaskOut struct {
	Task *queue.Task `json:"task"` // null when nothing is claimable
}

type completeTaskIn struct {
	ID       int64       `json:"id" jsonschema:"the id of the task you claimed"`
	Name     string      `json:"name" jsonschema:"your agent (bee) name — must be the claimer"`
	Result   string      `json:"result,omitempty" jsonschema:"a short summary of what you did"`
	Findings []findingIn `json:"findings,omitempty" jsonschema:"structured findings you discovered doing this task (vulns, bugs, design flaws)"`
}
type completeTaskOut struct {
	OK      bool   `json:"ok"`                // false if you weren't the claimer, it was already done, OR the verify gate refused it
	Message string `json:"message,omitempty"` // why a gated completion was refused
}

// findingIn is the structured finding shape, shared by report_finding and the
// findings attached to complete_task.
type findingIn struct {
	Type            string `json:"type" jsonschema:"vuln|bug|design-flaw|missing-req|regression|note"`
	Severity        string `json:"severity" jsonschema:"low|medium|high|critical"`
	Target          string `json:"target,omitempty" jsonschema:"the file or area affected"`
	Evidence        string `json:"evidence,omitempty" jsonschema:"what you observed"`
	SuggestedAction string `json:"suggested_action,omitempty" jsonschema:"how to fix it"`
}

func (fi findingIn) toFinding(missionID, taskID int64, reporter string) queue.Finding {
	return queue.Finding{
		MissionID: missionID, TaskID: taskID, Reporter: reporter,
		Type: fi.Type, Severity: fi.Severity, Target: fi.Target,
		Evidence: fi.Evidence, SuggestedAction: fi.SuggestedAction,
	}
}

// stampModel attributes a finding to the model that filed it by looking the
// reporter up in the HostBook. Shared by BOTH finding paths (report_finding and
// complete_task inline findings) so attribution is consistent. Degrade-never-
// block: a nil book or a missing entry leaves ReporterModel/ReporterBackend ""
// and the finding still files.
func stampModel(book *HostBook, f *queue.Finding) {
	if book == nil {
		return
	}
	if h, ok := book.Get(f.Reporter); ok {
		f.ReporterModel = h.Model
		f.ReporterBackend = h.Backend
	}
}

type reportFindingIn struct {
	Name            string `json:"name" jsonschema:"your agent (bee) name"`
	MissionID       int64  `json:"mission_id" jsonschema:"the mission this finding is about"`
	TaskID          int64  `json:"task_id,omitempty" jsonschema:"the task that surfaced it, if any"`
	Type            string `json:"type" jsonschema:"vuln|bug|design-flaw|missing-req|regression|note"`
	Severity        string `json:"severity" jsonschema:"low|medium|high|critical"`
	Target          string `json:"target,omitempty" jsonschema:"the file or area affected"`
	Evidence        string `json:"evidence,omitempty" jsonschema:"what you observed"`
	SuggestedAction string `json:"suggested_action,omitempty" jsonschema:"how to fix it"`
}
type findingOut struct {
	ID int64 `json:"id"`
}

type listFindingsIn struct {
	MissionID int64  `json:"mission_id,omitempty" jsonschema:"limit to one mission; omit for all"`
	Status    string `json:"status,omitempty" jsonschema:"filter by status: open|addressed|dismissed"`
	ByModel   string `json:"by_model,omitempty" jsonschema:"filter by reporter model (e.g. claude-opus, gemini-3)"`
}
type listFindingsOut struct {
	Findings []queue.Finding `json:"findings"`
}

type taskIDIn struct {
	ID int64 `json:"id" jsonschema:"the task id"`
}
type cancelIn struct {
	ID      int64 `json:"id" jsonschema:"the task id"`
	Cascade bool  `json:"cascade,omitempty" jsonschema:"also cancel every live task that depends on it (the whole subtree)"`
}
type cancelOut struct {
	OK        bool     `json:"ok"`
	Cancelled int      `json:"cancelled,omitempty"` // how many tasks went down (root + cascade)
	Blocked   []string `json:"blocked,omitempty"`   // live dependent keys when refused
	Message   string   `json:"message,omitempty"`
}
type retargetIn struct {
	MissionID int64  `json:"mission_id" jsonschema:"the mission"`
	FromKey   string `json:"from_key" jsonschema:"the dead dependency key"`
	ToKey     string `json:"to_key" jsonschema:"the task key dependents should wait on instead"`
}
type retargetOut struct {
	OK         bool `json:"ok"`
	Retargeted int  `json:"retargeted"`
}
type taskSpecIn struct {
	MissionID   int64    `json:"mission_id,omitempty" jsonschema:"the mission to add the task to (enqueue_task only)"`
	OldID       int64    `json:"old_id,omitempty" jsonschema:"the task to replace (supersede_task only)"`
	Key         string   `json:"key" jsonschema:"a unique key for the task within its mission"`
	Role        string   `json:"role,omitempty" jsonschema:"builder|tester|pentester|reviewer|lead|'' (any)"`
	Title       string   `json:"title" jsonschema:"short label for the UI"`
	Instruction string   `json:"instruction" jsonschema:"what the task does"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"task keys that must be done first"`
}

func (in taskSpecIn) spec() queue.TaskSpec {
	return queue.TaskSpec{Key: in.Key, Role: in.Role, Title: in.Title, Instruction: in.Instruction, DependsOn: in.DependsOn}
}

type supersedeOut struct {
	NewID int64 `json:"new_id"`
	OK    bool  `json:"ok"`
}

type resolveFindingIn struct {
	ID     int64  `json:"id" jsonschema:"the finding id"`
	Status string `json:"status" jsonschema:"open|addressed|dismissed"`
}

type listTasksIn struct {
	MissionID int64  `json:"mission_id,omitempty" jsonschema:"limit to one mission; omit for all"`
	Status    string `json:"status,omitempty" jsonschema:"filter by status: pending|ready|claimed|done"`
}
type listTasksOut struct {
	Tasks []queue.Task `json:"tasks"`
}

// notifyRecurrence closes the efficacy loop: report_finding already inserted
// finding id via AddFinding, which flags "recurring" whenever a PRIOR finding
// shared the same type+target — evidence a past fix or promoted guidance
// didn't hold. On a recurring insert, tell the learn store; at the SECOND
// recurrence since an approved proposal's promotion, the store reopens it as
// a revision (nil, nil below the threshold). Degrade-never-block: any error
// here is logged loudly, never returned — a learn-store hiccup must not stop
// a finding from being filed.
func notifyRecurrence(ls *learn.Store, tel *telemetry.Store, q *queue.Store, findingID, missionID int64, reporter, ftype, target string) {
	if ls == nil {
		return
	}
	f, ok, err := q.FindingByID(findingID)
	if err != nil {
		log.Printf("learn: recurrence check for finding #%d FAILED (finding still filed): %v", findingID, err)
		return
	}
	if !ok || !f.Recurring {
		return
	}
	sig := ftype + "|" + target
	r, err := ls.RecordRecurrence(sig)
	if err != nil {
		log.Printf("learn: record recurrence for %s FAILED (finding still filed): %v", sig, err)
		return
	}
	if r == nil {
		return
	}
	log.Printf("learn: guidance for %s didn't land — revision proposal #%d opened", sig, r.ID)
	rec(tel, missionID, "proposal_reopened", reporter, sig, map[string]any{"id": r.ID, "supersedes": r.Supersedes})
}

// escalationRefusals is the bug-#40 tripwire: after this many verify-gate
// refusals of the SAME task from the SAME bee, the brain force-releases the
// stale path leases (holders with no claimed task) that starve remediation,
// says so loudly, and resets the counter. A-2 died at 21+ silent refusals and
// C-1 at 35 — both with a long-done agent's 3600s lease pinning the artifact.
const escalationRefusals = 5

// registerTasks adds the pull-model tools: a bee claims the next ready task it
// can serve, runs it, and completes it; list_tasks is the live work list.
func registerTasks(s *mcp.Server, store *coord.Store, q *queue.Store, lease float64, tel *telemetry.Store, book *HostBook, ls *learn.Store, health *HealthBook, workspace string, verify VerifyFunc) {
	if lease <= 0 {
		lease = 300
	}

	// Refusal counts per (task, bee). In-memory on purpose: a brain restart
	// resetting the tally is harmless — a live loop re-trips it in minutes —
	// and the deterministic escalation stays free of new persistent state.
	type refusalKey struct {
		task int64
		bee  string
	}
	var refusalMu sync.Mutex
	refusals := map[refusalKey]int{}

	// escalateRefusalLoop is bug-#40 part 2: convert a silent verify-refusal
	// livelock into visible, recoverable state. Force-release path leases whose
	// holder has no claimed task (they are done or dead — their leases only
	// starve whoever must fix the failing verify), and shout in telemetry +
	// the audit trail so the standup surfaces it.
	escalateRefusalLoop := func(t *queue.Task, bee string, count int) {
		holders, err := store.LiveClaimHolders()
		if err != nil {
			log.Printf("escalation: LiveClaimHolders: %v", err)
			return
		}
		released := []string{}
		for _, holder := range holders {
			if holder == bee {
				continue // the refused bee's own leases are live work, not the wedge
			}
			holds, err := q.HoldsClaimedTask(holder)
			if err != nil {
				log.Printf("escalation: HoldsClaimedTask(%q): %v", holder, err)
				continue
			}
			if holds {
				continue
			}
			if n, err := store.ReleaseClaims(holder, nil); err == nil && n > 0 {
				recordClaimReleased(tel, holder, nil)
				released = append(released, holder)
			}
		}
		detail := map[string]any{"task_id": t.ID, "bee": bee, "refusals": count, "released_holders": released}
		log.Printf("ESCALATION (bug #40): task #%d (%s) refused %d times for %s — force-released stale leases of %v",
			t.ID, t.Key, count, bee, released)
		rec(tel, t.MissionID, "verify_refusal_escalation", "brain", t.Key, detail)
		store.Audit("brain", "escalation", detail)
	}

	mcp.AddTool(s, &mcp.Tool{Name: "claim_task",
		Description: "Atomically claim the next ready task you can serve (the pull model). Returns the task, or task:null when nothing is ready. Run it, then call complete_task. While you hold a task keep heart-beating, or it will be reaped and handed to another bee."},
		func(_ context.Context, req *mcp.CallToolRequest, in claimTaskIn) (*mcp.CallToolResult, claimTaskOut, error) {
			bee := identity(req, in.Name)
			if bee == "" {
				return nil, claimTaskOut{}, fmt.Errorf("name required (who is claiming)")
			}
			t, err := q.ClaimNextAs(bee, in.Instance, in.Roles, lease)
			if err != nil {
				return nil, claimTaskOut{}, err
			}
			if t != nil {
				// Health signal (#72): a claim is activity, but not yet progress —
				// claimsSinceSuccess only clears on a real complete_task.
				health.RecordClaim(bee)
				if t.Reissued {
					// The bee didn't know it held this — the earlier claim reply was
					// lost (or a sibling died). Say so loudly: this is the trail that
					// was missing when the 2026-07-02 demo deadlocked silently.
					log.Printf("queue: re-issued task #%d (%s) to %s — claim was orphaned", t.ID, t.Key, bee)
					rec(tel, t.MissionID, "task_reissued", bee, t.Key, map[string]any{"role": t.Role})
				} else {
					rec(tel, t.MissionID, "task_claimed", bee, t.Key, map[string]any{"role": t.Role})
				}
			}
			return nil, claimTaskOut{Task: t}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "complete_task",
		Description: "Mark a task you claimed as done, with a short result. Optionally attach structured findings you discovered (vulns, bugs, design flaws). Idempotent; only the claimer can complete it."},
		func(ctx context.Context, req *mcp.CallToolRequest, in completeTaskIn) (*mcp.CallToolResult, completeTaskOut, error) {
			bee := identity(req, in.Name)
			// Verification gate: a gated task (one with a Verify command) cannot close
			// unless that command exits 0. Otherwise refuse and raise a regression
			// finding for the reflex re-planner.
			t, terr := q.TaskByID(in.ID)
			if terr != nil {
				return nil, completeTaskOut{}, terr
			}
			if t != nil && t.Verify != "" {
				// Workstream A: when a Verify runner is wired, the BRAIN runs the check
				// itself against its own working copy and certifies on the real exit code
				// — the worker's self-reported execution row is NOT trusted ("a judge may
				// not certify herself"). Without a runner (legacy / non-repo missions) we
				// fall back to the recorded-execution lookup.
				var passed bool
				dir := mission.MissionDir(workspace, t.MissionID)
				if verify != nil && workingCopyExists(dir) {
					ok, _ := verify(ctx, dir, t.Verify)
					exit := 0
					if !ok {
						exit = 1
					}
					// The gate's own run is the authoritative, queryable ledger row.
					if rerr := q.RecordExecution(queue.Execution{
						MissionID: t.MissionID, Agent: "verify-gate", Command: t.Verify,
						ExitCode: exit, OK: ok, TS: time.Now().Unix(),
					}); rerr != nil {
						return nil, completeTaskOut{}, fmt.Errorf("verify-gate record: %w", rerr)
					}
					passed = ok
				} else {
					since := int64(math.Ceil(t.ClaimedTS))
					p, err := q.MissionPassedVerifySince(t.MissionID, t.Verify, since)
					if err != nil {
						return nil, completeTaskOut{}, err
					}
					passed = p
				}
				if !passed {
					if _, err := q.AddFinding(queue.Finding{
						MissionID: t.MissionID, TaskID: in.ID, Reporter: "verify-gate",
						Type: "regression", Severity: "high", Target: t.Key,
						Evidence:        "latest '" + t.Verify + "' run since this task was claimed is not successful",
						SuggestedAction: "run '" + t.Verify + "' and fix the failures, then complete",
					}); err != nil {
						return nil, completeTaskOut{}, fmt.Errorf("gate finding: %w", err)
					}
					rec(tel, t.MissionID, "finding_reported", "verify-gate", t.Key, map[string]any{"type": "regression", "severity": "high"})
					// Bug #40: count the refusal; on the Nth, escalate (release
					// stale leases + shout) and reset so it can fire again.
					refusalMu.Lock()
					k := refusalKey{task: in.ID, bee: bee}
					refusals[k]++
					count := refusals[k]
					if count >= escalationRefusals {
						delete(refusals, k)
					}
					refusalMu.Unlock()
					if count >= escalationRefusals {
						escalateRefusalLoop(t, bee, count)
					}
					return nil, completeTaskOut{OK: false,
						Message: "refused: latest '" + t.Verify + "' run since this task was claimed is not successful — run it, fix the failures, then complete"}, nil
				}
			}
			ok, err := q.Complete(in.ID, bee, in.Result)
			if err != nil {
				return nil, completeTaskOut{}, err
			}
			if ok {
				// Health signal (#72): a genuine completion is forward progress —
				// clear the claimed-but-not-progressing counters.
				health.RecordSuccess(bee)
				// Bug #40 part 1: a completed bee's path leases are dead weight —
				// the work that justified them is done, and A-2/C-1 both livelocked
				// on exactly such a lease. Same cleanup despawn already does.
				refusalMu.Lock()
				delete(refusals, refusalKey{task: in.ID, bee: bee})
				refusalMu.Unlock()
				if n, rerr := store.ReleaseClaims(bee, nil); rerr == nil && n > 0 {
					recordClaimReleased(tel, bee, nil)
					log.Printf("queue: released %d path lease(s) of %s on task #%d completion", n, bee, in.ID)
				}
			}
			mid, _ := q.MissionOfTask(in.ID)
			if ok {
				subject := ""
				if t, _ := q.TaskByID(in.ID); t != nil {
					subject = t.Key
				}
				rec(tel, mid, "task_completed", bee, subject, nil)
			}
			for _, fi := range in.Findings {
				f := fi.toFinding(mid, in.ID, bee)
				stampModel(book, &f) // same HostBook attribution as report_finding
				if _, err := q.AddFinding(f); err != nil {
					return nil, completeTaskOut{OK: ok}, fmt.Errorf("finding: %w", err)
				}
				recModel(tel, mid, "finding_reported", bee, fi.Target, f.ReporterModel,
					map[string]any{"type": fi.Type, "severity": fi.Severity})
			}
			return nil, completeTaskOut{OK: ok}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "report_finding",
		Description: "Report a structured finding — a vuln, bug, design flaw, missing requirement, regression, or note — with a severity. This is the swarm's feedback channel: findings are recorded, surfaced live, and (later) drive re-planning. Far more useful than a prose summary."},
		func(_ context.Context, req *mcp.CallToolRequest, in reportFindingIn) (*mcp.CallToolResult, findingOut, error) {
			reporter := identity(req, in.Name)
			f := queue.Finding{
				MissionID: in.MissionID, TaskID: in.TaskID, Reporter: reporter,
				Type: in.Type, Severity: in.Severity, Target: in.Target,
				Evidence: in.Evidence, SuggestedAction: in.SuggestedAction,
			}
			stampModel(book, &f) // HostBook attribution; missing entry degrades to "" — never blocks
			id, err := q.AddFinding(f)
			if err != nil {
				return nil, findingOut{}, err
			}
			recModel(tel, in.MissionID, "finding_reported", reporter, in.Target, f.ReporterModel,
				map[string]any{"type": in.Type, "severity": in.Severity})
			notifyRecurrence(ls, tel, q, id, in.MissionID, reporter, in.Type, in.Target)
			return nil, findingOut{ID: id}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_findings",
		Description: "List structured findings (the feedback log), optionally for one mission and/or one status (open|addressed|dismissed). Filter by by_model to isolate a specific model's findings."},
		func(_ context.Context, _ *mcp.CallToolRequest, in listFindingsIn) (*mcp.CallToolResult, listFindingsOut, error) {
			fs, err := q.FindingsFiltered(in.MissionID, in.Status, in.ByModel)
			if err != nil {
				return nil, listFindingsOut{}, err
			}
			if fs == nil {
				fs = []queue.Finding{}
			}
			return nil, listFindingsOut{Findings: fs}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_tasks",
		Description: "List queued tasks (the live work list), optionally for one mission and/or one status."},
		func(_ context.Context, _ *mcp.CallToolRequest, in listTasksIn) (*mcp.CallToolResult, listTasksOut, error) {
			var tasks []queue.Task
			var err error
			if in.MissionID != 0 {
				tasks, err = q.List(in.MissionID)
			} else {
				tasks, err = q.Active()
			}
			if err != nil {
				return nil, listTasksOut{}, err
			}
			out := make([]queue.Task, 0, len(tasks))
			for _, t := range tasks {
				if in.Status == "" || t.Status == in.Status {
					out = append(out, t)
				}
			}
			return nil, listTasksOut{Tasks: out}, nil
		})

	// Re-planning mutations (the LLM lead's surface). The lead reads findings +
	// tasks, then cancels abandoned work, reopens work whose foundation changed,
	// supersedes stale tasks with rework, and enqueues new tasks.
	mcp.AddTool(s, &mcp.Tool{Name: "cancel_task",
		Description: "Abandon a task that is no longer needed (pending/ready/claimed → cancelled). REFUSES if live tasks still depend on it (they would be stranded forever) and returns their keys — supersede_task or retarget_dependencies instead, or pass cascade:true to deliberately cancel the whole dependent subtree."},
		func(_ context.Context, req *mcp.CallToolRequest, in cancelIn) (*mcp.CallToolResult, cancelOut, error) {
			cancelled, blocked, err := q.CancelTaskGuarded(in.ID, in.Cascade)
			if err != nil {
				return nil, cancelOut{}, err
			}
			if len(blocked) > 0 {
				return nil, cancelOut{OK: false, Blocked: blocked,
					Message: "refused: live tasks depend on this one — supersede_task / retarget_dependencies, or cascade:true"}, nil
			}
			for _, id := range cancelled {
				recTask(tel, q, id, "task_cancelled", actorOf(req))
			}
			return nil, cancelOut{OK: len(cancelled) > 0, Cancelled: len(cancelled)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "retarget_dependencies",
		Description: "Re-point every live task that depends on from_key to depend on to_key instead — the one-step recovery when a dependency is dead (cancelled/superseded) but another task covers it. Refuses cycles and unknown targets."},
		func(_ context.Context, req *mcp.CallToolRequest, in retargetIn) (*mcp.CallToolResult, retargetOut, error) {
			if in.MissionID == 0 || in.FromKey == "" || in.ToKey == "" {
				return nil, retargetOut{}, fmt.Errorf("mission_id, from_key, to_key required")
			}
			n, err := q.RetargetDependents(in.MissionID, in.FromKey, in.ToKey)
			if err != nil {
				return nil, retargetOut{}, err
			}
			rec(tel, in.MissionID, "deps_retargeted", actorOf(req), in.FromKey+"->"+in.ToKey, map[string]any{"count": n})
			return nil, retargetOut{OK: true, Retargeted: n}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reopen_task",
		Description: "Re-do a finished task whose premise changed (done → ready) — e.g. rebuild on a corrected foundation."},
		func(_ context.Context, req *mcp.CallToolRequest, in taskIDIn) (*mcp.CallToolResult, okOut, error) {
			ok, err := q.ReopenTask(in.ID)
			if ok {
				recTask(tel, q, in.ID, "task_reopened", actorOf(req))
			}
			return nil, okOut{OK: ok}, err
		})

	// checkStaffedRole is the bug-#23 guard: a task whose role the mission has
	// never staffed can never be claimed, and MissionDone's zero-open-tasks bar
	// then holds the mission open forever (the role deadlock — a re-planning
	// lead LLM invents "performance" where the pipeline staffs "perf"). Refuse
	// loudly, naming the valid roles, so the lead can self-correct on retry.
	checkStaffedRole := func(missionID int64, role string) error {
		if role == "" {
			return nil // generic tasks are claimable by any role
		}
		if store != nil {
			if active, err := store.ListActive(coord.PresenceWindow); err == nil {
				activeRoles := map[string]bool{}
				hasGeneralist := false
				for _, a := range active {
					// Role holds a collapsed Display string: a generalist claims
					// any ready task, and a multi-role worker registers "a+b".
					// Expand it so a task whose role a present generalist/multi-role
					// worker actually covers isn't falsely refused.
					cov := agentrole.Coverage(a.Role)
					if cov.Any {
						hasGeneralist = true
						continue
					}
					for _, r := range cov.Roles {
						activeRoles[r] = true
					}
				}
				if hasGeneralist {
					return nil // a generalist covers this role — it will be claimed
				}
				if len(activeRoles) > 0 {
					if activeRoles[role] {
						return nil
					}
					valid := make([]string, 0, len(activeRoles))
					for r := range activeRoles {
						valid = append(valid, r)
					}
					sort.Strings(valid)
					return fmt.Errorf("refused: no active %q agent is registered right now — enqueuing this role would stall; active roles: %s", role, strings.Join(valid, ", "))
				}
			}
		}
		roles, err := q.MissionRoles(missionID)
		if err != nil || len(roles) == 0 {
			return err // nothing to validate against — let it through
		}
		for _, r := range roles {
			if r == role {
				return nil
			}
		}
		return fmt.Errorf("refused: this mission staffs no %q agents — a task with that role would never be claimed and would deadlock the mission; use one of: %s", role, strings.Join(roles, ", "))
	}

	// checkDepsExist refuses a task whose DependsOn references a key that names no
	// task in the mission: an orphan dependency never promotes (PromoteReady can't
	// satisfy it), so the task hangs invisibly until the reactive sweep cancels it.
	// Stopping it at creation — naming the missing key(s) so the lead can enqueue
	// the dependency first — is the source-side complement to that sweep.
	checkDepsExist := func(missionID int64, deps []string) error {
		if len(deps) == 0 {
			return nil
		}
		keys, err := q.MissionTaskKeys(missionID)
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(keys))
		for _, k := range keys {
			have[k] = true
		}
		var missing []string
		for _, d := range deps {
			if d != "" && !have[d] {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("refused: depends_on names task(s) not in this mission: %s — enqueue the dependency first, or fix the key; an orphan dependency never promotes", strings.Join(missing, ", "))
		}
		return nil
	}

	mcp.AddTool(s, &mcp.Tool{Name: "supersede_task",
		Description: "Replace a stale task with a reworked one (old → superseded; the replacement carries the lineage). Pending dependents are rewritten to wait on the replacement, so the plan stays valid. The role must be one the mission already staffs."},
		func(_ context.Context, req *mcp.CallToolRequest, in taskSpecIn) (*mcp.CallToolResult, supersedeOut, error) {
			if mid, err := q.MissionOfTask(in.OldID); err == nil && mid != 0 {
				if err := checkStaffedRole(mid, in.Role); err != nil {
					return nil, supersedeOut{}, err
				}
				if err := checkDepsExist(mid, in.DependsOn); err != nil {
					return nil, supersedeOut{}, err
				}
			}
			newID, err := q.SupersedeTask(in.OldID, in.spec())
			if err != nil {
				return nil, supersedeOut{}, err
			}
			recTask(tel, q, in.OldID, "task_superseded", actorOf(req))
			return nil, supersedeOut{NewID: newID, OK: true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "enqueue_task",
		Description: "Add a new task to a running mission (rework the lead decided is needed). It joins the queue and the hive picks it up. The role must be one the mission already staffs."},
		func(_ context.Context, req *mcp.CallToolRequest, in taskSpecIn) (*mcp.CallToolResult, okOut, error) {
			if in.MissionID == 0 {
				return nil, okOut{}, fmt.Errorf("mission_id required")
			}
			if err := checkStaffedRole(in.MissionID, in.Role); err != nil {
				return nil, okOut{}, err
			}
			if err := checkDepsExist(in.MissionID, in.DependsOn); err != nil {
				return nil, okOut{}, err
			}
			err := q.Enqueue(in.MissionID, []queue.TaskSpec{in.spec()})
			if err == nil {
				rec(tel, in.MissionID, "task_enqueued", actorOf(req), in.Key, nil)
			}
			return nil, okOut{OK: err == nil}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "resolve_finding",
		Description: "Set a finding's status: addressed once you've acted on it (e.g. enqueued rework), or dismissed if it's not a real problem. Keeps the feedback loop from reprocessing it."},
		func(_ context.Context, req *mcp.CallToolRequest, in resolveFindingIn) (*mcp.CallToolResult, okOut, error) {
			ok, err := q.SetFindingStatus(in.ID, in.Status)
			if ok {
				if f, fok, _ := q.FindingByID(in.ID); fok {
					recModel(tel, f.MissionID, "finding_resolved", actorOf(req), f.Target, f.ReporterModel,
						map[string]any{"outcome": in.Status, "finding_id": in.ID, "backend": f.ReporterBackend})
				}
			}
			return nil, okOut{OK: ok}, err
		})

	type isInterceptedIn struct {
		Name string `json:"name"`
	}
	type isInterceptedOut struct {
		Intercept bool `json:"intercept"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "is_intercepted",
		Description: "Check if the operator has requested to intercept this agent's next execution to perform human-in-the-loop takeover.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in isInterceptedIn) (*mcp.CallToolResult, isInterceptedOut, error) {
		agent := identity(req, in.Name)
		var intercept bool
		if book != nil {
			intercept = book.IsInterceptPending(agent)
		}
		return nil, isInterceptedOut{Intercept: intercept}, nil
	})
}
