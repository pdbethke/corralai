# verify-gate

This is the brain's task gate — one enforcement of the property every
certification in corral rests on: a verdict is never taken on an agent's
word. It's certified by execution — the check runs, its exit code is read by
the machine that decides rather than reported by the agent that did the
work, and only a recorded passing run can close out the work. This is what
"certify by execution, not by self-report" means concretely. The property is
shared across corral; the mechanism on this page is the brain's alone (see
the last section).

A queued task can carry a `Verify` command (`internal/queue/store.go`'s
`Task.Verify` field, e.g. `"go build"` or `"go test"`) — set on the task spec
for whatever's being audited (build, test, integrate). A task with a
non-empty `Verify` is **gated**: `complete_task` refuses to close it until a
matching passing run is on record.

## How the gate checks

`internal/brain/tasks.go`'s `complete_task` handler, when `t.Verify != ""`,
does one of two things. When a verify runner is wired and the mission's
working copy exists, the **brain runs the check itself** against its own
working copy, records that run as an execution row attributed to
`verify-gate`, and passes or refuses on the real exit code — the worker's
own `report_execution` rows are not consulted. Without a runner (legacy /
non-repo missions) it falls back to
`q.MissionPassedVerifySinceBy(t.MissionID, t.Verify, since, t.ClaimedBy)`
(`internal/queue/executions.go`): a recorded execution by **the claimer**, since
the task was claimed, whose command contains the verify string and exited 0
(`ok=1`). That fallback trusts the claimer's report of the exit code; the
runner path does not. Either way, if no passing run exists, completion is
refused with an explanation and a suggested action: run the command, fix the
failures, then complete.

## report_execution is how runs get recorded

Agents don't complete tasks and hope — they run the verify command themselves
and call the `report_execution` MCP tool (`internal/brain/executions.go`) with
the command, exit code, and ok flag. That both feeds the live activity ring
and durably records a `queue.Execution` row (`RecordExecution`,
`internal/queue/executions.go`) keyed to the agent's currently-claimed run.
Only after a passing `report_execution` for the gating command can
`complete_task` succeed.

## Supersede inherits the gate

When a task is replaced (`SupersedeTask`, `internal/queue/supersede.go`), the
replacement inherits the old task's `Verify` string whenever the new spec
doesn't set its own — so correcting course around a stale task never
accidentally drops its verification requirement.

## The same property, not the same mechanism

What is shared everywhere corral certifies something is the property — a
verdict decided by an exit code the machine read, never by a self-report —
not this page's mechanism. `Task.Verify` is set only on brain mission tasks;
the adversarial pool's tasks (mutant-generator, test-writer, test-critic)
never carry one (`internal/advpool/roles.go`'s `BuildDAG`), and
`corral certify --local` / `--repo` never contact a brain at all. Their
scoring is `adequacy.Score` (`internal/advpool/gate.go`): each mutant is
applied, the suite is executed in the jail (or the workspace substrate), and
the exit code decides `killed`/`survived` in-process. The repo gate and the
control gate run their check the same way, on the brain. Nothing in corral
marks work done on an agent's say-so; the code that reads the exit code
differs by path.
