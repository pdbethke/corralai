# verify-gate

A queued task can carry a `Verify` command (`internal/queue/store.go`'s `Task.Verify`
field, e.g. `"go build"` or `"go test"`) — set on the task spec, e.g.
`build-core`/`test`/`integrate` in `mission.DefaultPlan` (`internal/mission/store.go`).
A task with a non-empty `Verify` is **gated**: `complete_task` refuses to close it
until a matching passing run is on record.

## How the gate checks

`internal/brain/tasks.go`'s `complete_task` handler, when `t.Verify != ""`, calls
`q.MissionPassedVerify(t.MissionID, t.Verify)` (`internal/queue/executions.go`).
That looks for any recorded execution on the mission whose command **contains**
the verify string and exited 0 (`ok=1`). If none exists, completion is refused
with an explanation and a suggested action: run the command, fix the failures,
then complete.

## report_execution is how runs get recorded

Agents don't complete tasks and hope — they run the verify command themselves and
call the `report_execution` MCP tool (`internal/brain/executions.go`) with the
command, exit code, and ok flag. That both feeds the live activity ring and
durably records a `queue.Execution` row (`RecordExecution`,
`internal/queue/executions.go`) keyed to the agent's currently-claimed mission.
Only after a passing `report_execution` for the gating command can
`complete_task` succeed.

## Supersede inherits the gate

When a task is replaced (`SupersedeTask`, `internal/queue/supersede.go`), the
replacement inherits the old task's `Verify` string whenever the new spec
doesn't set its own — so re-planning around a stale task never accidentally
drops its verification requirement.
