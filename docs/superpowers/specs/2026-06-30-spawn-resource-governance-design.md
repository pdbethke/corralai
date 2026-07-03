# Spawn Resource Governance — Design

**Status:** design · **Date:** 2026-06-30 · **Sub-project:** #13

## Where this fits

corralai is a control plane: the brain coordinates, mints identity/authz, and
holds the durable record — it **never executes agent compute**. Agents (and their
subagents) run on clients: dev laptops, CI runners, prod hosts. Today
`spawn_subagent` registers a child sub-identity and optionally mints a TTL-bound
delegation token, but **nothing launches a worker** and **nothing bounds how many
agents can be requested**. The per-principal HTTP rate limiter
(`internal/limit`, 600/min) bounds spawn *call rate*, but not the *standing count*
of live agents, and there is no host-side concurrency or resource governance.

If we wire spawn-and-launch without limits, corralai becomes a DoS vector: a buggy
or hostile swarm could recursively spawn agents and overload a workstation. This
sub-project builds the governance **before** the launch path is exercised, so
bounded spawning is the only spawning that ever exists.

## First principle: both sides enforce, neither trusts the other

Defense in depth across the trust boundary:

- The **brain** bounds what can be **requested** — per the accountable principal,
  plus recursion (depth) and fan-out (breadth) caps. It refuses over-budget spawns
  before registering a child or minting a token.
- The **host** (a reference launcher) bounds what it will **run** — a local
  admission controller must grant a lease before a child process starts; at
  capacity or under load it refuses locally and launches nothing.

A coordinator cannot conscript a host beyond what the host itself permits. The
brain still never runs compute.

## Components

### 1. Brain spawn budget (`internal/brain` spawn_subagent + `internal/coord`)

Three caps, checked in `spawn_subagent` before `coord.Spawn`/`MintToken`,
env-overridable via `Options`:

- `MaxAgentsPerPrincipal` (default **64**) — standing count of **live** agents owned
  by the authenticated principal. "Live" = present within the coordination presence
  window (`coord.PresenceWindow`), so a crashed-but-not-despawned agent ages out of
  the count automatically rather than wedging the budget. Ownership: `coord` records
  the **owning principal** on each agent at registration/spawn (the authenticated
  `actor`; in dev, no auth → the single dev principal `"agent"`, so the cap bounds
  the whole fleet on that brain). The count is
  `COUNT(live agents WHERE owning_principal = P)`. (Plan adds the `principal` column +
  a `CountLiveByPrincipal` query to `coord`.)
- `MaxSpawnDepth` (default **4**) — derived from the hierarchical name
  (`parent/child/grandchild` → depth 3). Stops recursive fork-bombs (A→B→C→…).
- `MaxChildrenPerParent` (default **8**) — `len(coord.Subagents(parent))` at spawn
  time. Stops wide fan-out from one node.

`Options.SpawnBudget` carries the three values; `0` on any field means "use the
default" (never "unlimited" — a deliberate fail-safe). The brain reads
`CORRALAI_MAX_AGENTS_PER_PRINCIPAL`, `CORRALAI_MAX_SPAWN_DEPTH`,
`CORRALAI_MAX_CHILDREN_PER_PARENT` at startup.

Over-budget → `spawn_subagent` returns a structured error naming the cap that
tripped, and records a `spawn_refused` telemetry event (mission_id, actor, reason).

### 2. `internal/admission` — pluggable host admission control (new package)

Mirrors the `sandbox.Isolator` pattern (pluggable, reference + stronger impls):

```go
type Lease interface{ Release() }
type Controller interface {
    // Acquire grants a slot to launch a child of the given role, or returns an
    // error explaining the refusal (at capacity, host load high). Non-blocking.
    Acquire(role string) (Lease, error)
}
```

Reference impl `Local`:
- **Semaphore** — at most `MaxConcurrent` (default = `2 × NumCPU`, env
  `CORRAL_MAX_CONCURRENT_CHILDREN`) child leases held at once on this host.
- **Load gate** — refuse if the 1-minute load average exceeds `LoadFactor × NumCPU`
  (default `LoadFactor = 2.0`, env `CORRAL_LOAD_FACTOR`). Reads `/proc/loadavg` on
  Linux via a `loadReader` field (injectable for tests); on non-Linux the gate is a
  no-op (semaphore still applies).

`Acquire` returns a refusal error rather than blocking, so the caller decides
(report + back off) instead of piling up goroutines. cgroups-v2 and
container-per-child are documented as alternate `Controller` impls, out of scope
for the reference.

### 3. Reference launcher (`cmd/corral-agent`)

A new path: given a granted spawn (a child name, role, and delegation token from
`spawn_subagent(out_of_process=true)`), the parent agent launches a **local**
child process:

1. `lease, err := admission.Acquire(role)` — refused → report
   `"admission refused: <reason>"`, launch nothing, return (the queued work simply
   waits for an existing agent; the pull model is natural backpressure).
2. Granted → `exec` a local `corral-agent` child with `CORRAL_TOKEN=<token>`,
   `AGENT_NAME=<full>`, `AGENT_ROLE=<role>`, `AGENT_MODE=queue`, pointed at the same
   brain.
3. On child exit (process-wait in a goroutine), `lease.Release()` then
   `despawn_subagent`.

Child commands keep the existing jail (`ulimit -u 1024`, `-f`, 60s timeout) — that
governs a single command; the admission controller governs the count of children.

**Spawning is deterministic / operator-triggered in the reference** — gated by an
explicit env (e.g. `AGENT_SPAWN_CHILDREN=<role>:<n>` at startup, or a dedicated
demo trigger), **not** an LLM tool the model can fire at will. An LLM that can
spawn at will is itself the vector; the reference does not enable that.

## Data flow

```
parent wants a child
  └─ spawn_subagent(out_of_process, role)
       BRAIN budget: principal-total? depth? breadth?
          over → REFUSE {reason} → telemetry "spawn_refused"        ✋
          ok   → coord.Spawn(child) + mint TTL token → return token
  └─ LAUNCHER admission.Acquire(role)
          at MaxConcurrent OR load > LoadFactor×cpus → REFUSE, no launch ✋
          granted → exec corral-agent child (token/name/role)
  └─ child pulls tasks; each command jailed (ulimit + 60s timeout)
  └─ child exits / despawn → Lease.Release() + despawn_subagent
```

## Error handling / failure modes

- **Brain refusal:** structured error names the tripped cap; `spawn_refused`
  telemetry event (auditable, post-mortem-visible, surfaced like verify-gate
  catches).
- **Host refusal:** launcher reports the reason, launches nothing; queued work
  waits for an existing agent. No goroutine pile-up (Acquire is non-blocking).
- **Child crash/exit:** lease released via a process-wait `defer`; `despawn_subagent`
  cleans the tree. A leaked lease cannot outlive its process.
- **Token TTL:** unchanged — an orphaned child's delegation expires; it can no
  longer act on the brain.
- **Budget config = 0:** treated as "use default," never "unlimited" — caps can
  never be accidentally disabled.

## Testing

- **brain/coord:** over-principal-cap / over-depth / over-breadth each refused with
  the correct reason; under-cap allowed; refusal recorded to telemetry.
- **admission:** `Acquire` refuses at `MaxConcurrent`; load gate refuses with an
  injected high-load `loadReader`; `Release` frees a slot; a fake `Controller`
  proves the interface is swappable.
- **launcher:** Acquire→launch→Release lifecycle; admission-refused ⇒ no exec;
  child env carries token/name/role. (Use a fake exec/launch hook so the test does
  not spawn real processes.)
- **integration:** spawn past the brain budget refused at the brain; spawn past
  host concurrency refused at the host; both land in telemetry.

## Out of scope (follow-ups)

- **Brain-driven autoscale** (the brain proactively requesting workers when the
  queue has unclaimed work for an unstaffed role). This design governs spawning;
  deciding *to* spawn autonomously is separate.
- **cgroups-v2 / container-per-child** admission impls (kept pluggable).
- **LLM-driven spawning** as a model tool (deliberately not enabled).
- **Cross-host scheduling / placement** (which host runs a child) — the reference
  launches locally; remote placement is a future launcher.
