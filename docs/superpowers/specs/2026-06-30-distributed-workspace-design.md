# Distributed Workspace (snapshot-bracket) — Design

**Status:** design · **Date:** 2026-06-30 · **Sub-project:** #16

## Where this fits

Repo-work mode (sub-project #15) makes a mission operate on a real git repo and
produce a PR. Its first draft assumed brain and bees share a `/workspace` volume —
the brain clones, the bees edit the same mount, the brain commits. That only holds
when every bee runs on the brain's host. The product goal is **remote bees** — a
heterogeneous swarm across machines and vendors ("the ecosystem"). This sub-project
removes the shared-volume assumption so a bee anywhere can do repo work, while the
brain remains the sole holder of `.git`, the remote, and the token.

It **supersedes** the shared-volume parts of the repo-work-mode design: there is no
shared working-copy mount, and the "reset origin to a token-less URL" mitigation is
no longer needed because a bee never receives `.git` at all.

## First principle: the brain owns the working copy; bees mirror it

The brain holds **one authoritative working copy per mission** at
`<workspace>/m<id>` — the only copy with `.git`, the remote, and the credential
surface. A bee never clones and never sees `.git`. Instead it pulls a `.git`-free
**snapshot** of the tree into its own local jail, works exactly as it does today
(`write_file`/`edit_file`/`run_command` against a local dir), and pushes the files it
changed back to the brain, which applies them to the authoritative copy and commits.
Untrusted build/test compute stays on the bee's host; the trusted brain never runs
mission code.

## Architecture / data flow

```
create_mission(repo)  → BRAIN clone (token) → <workspace>/m<id>  (authoritative, .git here)
bee claims a task      → repo_snapshot{}  → tar.gz of the tree (.git EXCLUDED) + manifest + base_rev
                         bee lays it down at  <agent-ws>/m<id>/   (per-mission local mirror)
bee works (UNCHANGED)  → write_file / edit_file (records written paths) ; run_command builds locally
bee ends a phase/task  → repo_push{ files:[{path,content}], base_rev }
                         BRAIN ApplyFiles onto <workspace>/m<id>  → {applied, stale}
engine (gate passed)   → git commit (repo-work-mode)  ·  mission done → git push + OpenPR
```

The brain↔bee channel is the existing MCP connection; "remote" vs "co-located" is
just whether that hop is a network or localhost. There is one model, not two.

## Components

### 1. `internal/repo` — snapshot + apply (extends the repo-work-mode engine)

- `Snapshot(dir string) (data []byte, manifest map[string]string, err error)` — walk
  the working copy, **skip `.git`, `node_modules`, `vendor`** (reuse the read-surface
  skip list), produce a gzip'd tar of the remaining files and a `path → sha256`
  manifest. A size cap (default 64 MiB uncompressed) guards pathological repos;
  exceeding it returns a clear error rather than a truncated tree.
- `ApplyFiles(dir string, writes []FileWrite) ([]string, error)` where
  `FileWrite{Path, Content string}` — write each file under `dir`, creating parent
  dirs, **rejecting path escapes** via the existing `safeJoin` (`..`/absolute). Returns
  the list of paths applied. No deletions in v1 (a feature mission adds/edits; a
  later `delete_file` tool can add removals).

These are pure functions over a directory — unit-testable with no MCP/git.

### 2. Brain MCP tools (`internal/brain/reposync.go`)

Both resolve the caller's claimed repo mission → `<workspace>/m<id>` (via
`queue.ClaimedMission` + `mission.Mission`, exactly like the read tools), and error
clearly with "not on a repo mission" otherwise.

- `repo_snapshot{}` → `{ data_b64, manifest, base_rev }`. `data_b64` is the base64 of
  the gzip'd tar (the `sync_pull` artifact precedent — base64 file bytes over MCP).
  `base_rev` is the authoritative copy's current commit sha (or `""` pre-first-commit).
- `repo_push{ files:[{path,content}], base_rev }` → `{ applied:[...], stale:bool }`.
  Applies the files; `stale` is `true` when the supplied `base_rev` ≠ the current
  authoritative `base_rev` (logged + surfaced, not fatal — see concurrency).

The existing `read_repo`/`repo_tree`/`repo_grep` (repo-work-mode) stay: they serve
the authoritative copy for cross-mission reads, observers/UI, and any bee that wants
a fresh read without re-snapshotting.

### 3. Agent (`cmd/corral-agent`) — per-mission mirror + tracked writes

- **Per-mission local dir.** Today the agent uses a single `AGENT_WORKSPACE` dir for
  all tasks. For a repo mission it uses `<AGENT_WORKSPACE>/m<id>` as the task's working
  dir. On the first task it sees for a mission whose snapshot it lacks, it calls
  `repo_snapshot`, untars into `<AGENT_WORKSPACE>/m<id>`, and remembers the manifest +
  `base_rev`. Non-repo missions keep today's single-dir behavior unchanged.
- **Tracked writes.** The agent records every path it writes via `write_file`/
  `edit_file` for the current mission (a `map[missionID]map[path]bool`). This set —
  not a tree walk — is exactly what `repo_push` sends, so build artifacts (`go build`
  outputs landing in the working dir) and toolchain caches (already in the jail's
  tmpfs `/home/agent`) are never pushed back.
- **Push timing.** On `complete_task` for a repo mission, the agent `repo_push`es the
  tracked files for that mission (then clears the per-task tracking). The brain-side
  engine commits gate-passed phases as repo-work-mode already specifies.

## Concurrency / error handling

- **Serialized writers per mission.** A mission's phases run in order, so at most one
  implementer is actively pull→edit→push on a given working copy at a time. A push is
  therefore a clean file-level apply. Different missions are different working copies
  and fully parallel.
- **Stale base_rev.** If the authoritative copy advanced between a bee's snapshot and
  its push (rare, given serialization), `repo_push` still applies (last-writer-wins at
  file granularity) but returns `stale:true` and logs it — auditable, never silent.
  A bee that wants to be safe can re-snapshot and retry.
- **Snapshot too large** → `repo_snapshot` errors clearly; the mission surfaces it
  (don't ship a half tree).
- **Path escape in a pushed file** (`..`/absolute) → that file is rejected by
  `safeJoin`; the push reports what it applied so the discrepancy is visible.
- **Push transport failure** → the brain's authoritative copy is unchanged; the bee
  retries. Idempotent (re-applying the same files is a no-op diff).
- **Non-repo mission** → no snapshot, no push; today's single-workspace path,
  untouched.

## Testing

- **`internal/repo`:** `Snapshot` over a tmp tree excludes `.git`/`node_modules`/
  `vendor`, round-trips through `ApplyFiles` into a second dir to an identical tree;
  the manifest hashes match; `ApplyFiles` rejects a `../escape` path; the size cap
  trips on an over-cap tree.
- **brain (`reposync_test`):** a bee claims a repo mission; `repo_snapshot` returns a
  tar whose manifest covers the seeded files and a `base_rev`; `repo_push` with a
  changed file applies it to `<workspace>/m<id>` and returns it in `applied`; a push
  with a mismatched `base_rev` returns `stale:true` but still applies; a caller with
  no claimed repo mission gets the "not on a repo mission" error.
- **agent:** with a fake brain, the agent pulls a snapshot into `<ws>/m<id>`, a
  `write_file` is recorded in the tracked set, and `complete_task` pushes exactly the
  tracked path (and not a build-artifact path created by a `run_command`).
- **credential boundary (regression):** the bee's mirror dir contains **no `.git`**
  after a snapshot (assert `<ws>/m<id>/.git` does not exist) — proving the token
  surface never reaches a bee.

## Out of scope (follow-ups)

- **Deletions / renames** over the wire (`delete_file` tool) — v1 adds/edits only.
- **Incremental snapshot** (delta since the bee's manifest) — v1 sends the full tree;
  fine for swarm-sized repos, an optimization later.
- **Multiple concurrent implementers on one mission** with real merge — v1 serializes
  by phase; concurrent same-file editing is a future merge-policy design.
- **Snapshot caching across missions of the same repo** — v1 snapshots per mission.
