# Repo-Work Mode (git → PR) — Design

**Status:** design · **Date:** 2026-06-30 · **Sub-project:** #15

## Where this fits

Today a mission builds in an ephemeral `/workspace` and the result evaporates on
restart — a demo on a throwaway directory. Repo-work mode makes a mission operate
on a **real git repository** and produce a **pull request**: the bridge from demo to
product, and the prerequisite for "corralai builds itself." It composes with the
verification gate (only gate-passed work is committed) and the mission engine (the
phase lifecycle drives the commits).

## First principle: secrets live only in the brain; the jail stays secret-free

The execution sandbox confines the command, runs network-off by default, and uses a
**secret-free `MinimalEnv`** — no token ever reaches executed code. Repo-work
honors that absolutely: the **brain** (the trusted control plane) owns every
privileged git operation — clone, commit, push, PR — and holds the only copy of the
git token. The **bees** only ever edit files and run build/test in the jail; they
never push, never clone, never see the token. This mirrors the spawn-governance
boundary: the brain coordinates and holds credentials; the workers do confined work.

## Architecture

```
mission(repo, base, directive)
  └─ BRAIN: git clone (token) → <workspace>/m<id> → checkout -b corralai/m<id>
  └─ SWARM (jailed, net-off, secret-free): mirror a .git-free snapshot of the tree
        locally, write_file/edit, run_command go build/test → gate, push changes back
  └─ BRAIN (Engine.Tick): per phase that completes & its gate passed → git commit
  └─ BRAIN (mission done): git push (token) → POST /pulls (token) → store PRURL
```

The brain owns the **authoritative per-mission working copy** at `<workspace>/m<id>`
— the only copy with `.git`, the remote, and the token — so it both commits it AND
serves its contents over MCP. **Bees never share this volume.** How a bee mirrors the
tree, edits locally, and ships changes back without a shared filesystem (and without
ever receiving `.git`) is defined by the **Distributed Workspace** design
(sub-project #16); this spec covers the brain-side git/PR engine, provisioning, the
read surface, and the per-phase commit/PR lifecycle. The read tools below remain the
fresh-read surface (cross-mission reads, observers/UI, and bees reading without
re-snapshotting).

## Components

### 1. `internal/repo` (new) — the git/PR engine

A small, well-bounded engine the mission engine calls. Shells to `git` (the token is
injected into the HTTPS remote URL, never logged — log lines redact it) and calls the
GitHub REST API over HTTP (the same shape as the `internal/embed`/LLM clients).

- `type Engine struct { token, apiBase string; hc *http.Client }`
- `func New(token, apiBase string) *Engine` (apiBase default `https://api.github.com`).
- `Clone(ctx, repoURL, base, destDir string) error` — clone `base` into `destDir`
  (token injected for private; works token-free for public/`file://`/local-path).
- `Checkout(ctx, dir, branch string) error` — `git checkout -b <branch>`.
- `Commit(ctx, dir, message string) (committed bool, err error)` — `git add -A`;
  commit only if there are staged changes (`committed=false` on an empty diff).
- `Push(ctx, dir, branch string) error` — `git push` the branch with the token.
- `OpenPR(ctx, owner, repo, head, base, title, body string) (prURL string, err error)`
  — `POST /repos/{owner}/{repo}/pulls`.
- `RepoIdent(repoURL string) (owner, repo string, err error)` — parse owner/repo for
  the PR call.
- **Read surface (filesystem, always fresh):** `ReadFile(dir, path string)`,
  `Tree(dir, subdir string)`, `Grep(dir, query string)` — confined to `dir` (reject
  `..`/absolute escapes), so the brain serves repo contents without an index.

`internal/repo` shells to `git` and talks HTTP; no other corralai package depends on
it. The token is a field, never a package global, never logged.

### 2. Mission model + engine (`internal/mission`)

- `Mission` gains `Repo`, `Base`, `Branch`, `PRURL string` (persisted columns via the
  idempotent `ALTER TABLE` pattern), plus a `mission.SetRepo(id, repo, base, branch)`
  setter — so `CreateMission`'s signature is unchanged (no churn to its existing
  callers).
- **Provisioning at creation:** the mission-creation path (the brain's `create_mission`
  tool, which gains `repo`/`base` inputs) does, when `repo` is set: `CreateMission`
  as today → then `repo.Clone(base)` + `repo.Checkout(corralai/m<id>)` into
  `<workspace>/m<id>` → `mission.SetRepo(...)`. On ANY provisioning failure it deletes
  the just-created mission and returns a clear error (don't start a mission you can't
  land). The clone dir is **per-mission** (`<workspace>/m<id>`), so concurrent repo
  missions never collide and a mission's read tools / snapshot resolve to its own copy.
- `Engine` gains an optional `Repo *repo.Engine`, a `Workspace string`, and a
  per-mission **committed-phases set** so each phase commits exactly once. `Engine.Tick`
  computes phase status; when a repo mission's phase newly transitions to `done`
  (in the set check, not yet committed), it calls
  `repo.Commit(workdir, "<phase>: <one-line>")` (skips on empty diff) and marks the
  phase committed. When the mission reaches `done`, it calls `repo.Push` then
  `repo.OpenPR` (title from the directive, body = the per-phase commit summary +
  open findings) and stores `PRURL`. Commit/push/PR errors are logged and recorded
  as mission events; they do NOT crash the engine and do NOT lose the local branch.

### 3. Repo read MCP tools (`internal/brain`)

Three brain-served tools so bees ground on the codebase through the single source of
truth (works for co-located AND, later, remote bees):

- `read_repo{path}` → file contents (capped size).
- `repo_tree{dir}` → a listing (respecting `.gitignore`-ish skips: `.git`, vendored
  dirs).
- `repo_grep{query, k}` → up to `k` matching `path:line: text` results (direct walk +
  match over the working copy).

All three resolve the working dir from the **caller's claimed repo mission** — the
bee's claimed task → its `mission_id` → that mission's `Repo`/clone dir (via
`queue.ClaimedMission` + `mission` lookup) — so concurrent repo missions don't
collide. If the caller has no claimed repo mission, the tool returns a clear "not on
a repo mission" error. All three reject path escapes (`..`/absolute). Registered only
when a repo engine + workspace are configured.

### 4. Brain wiring (`cmd/corral`) + infra

- Read `CORRALAI_GIT_TOKEN` and `CORRALAI_GITHUB_API` (default `https://api.github.com`)
  **only here**; construct `repo.New(token, apiBase)`; set `engine.Repo` +
  `engine.Workspace`; pass the repo engine to the brain server for the read tools.
- `Dockerfile.brain`: install `git`.
- `deploy/demo/docker-compose.yml`: only the **brain** service mounts the `workspace`
  volume (it alone owns the working copies) and gets `CORRALAI_GIT_TOKEN`. Bees do
  **not** mount it — they mirror via snapshot (sub-project #16).
- Brownfield reading: the cloned repo sits in `<workspace>/m<id>`; phase instructions
  tell bees to read it first (via their local mirror, `read_repo`/`repo_grep`, or
  `repo_search`) before editing.

## Data flow / credential handling

The token's entire lifetime: env → `cmd/corral` → `repo.Engine` field → injected into
the `git clone`/`push` HTTPS URL and the API `Authorization` header, **inside the
brain process only**. It is NEVER added to `sandbox.MinimalEnv`, never set in a bee's
environment, never written to a log (clone/push command logs redact the URL
userinfo). Bees never invoke clone/push.

**Critical — never persist the token in the working copy** (defense in depth). Under
the snapshot model (#16) a bee never receives `.git` at all, so the working copy's
`.git/config` is not a bee-readable surface — but the brain still must not leave the
token at rest there. Therefore `Clone` immediately resets `origin` to the token-LESS
URL (`git remote set-url origin <clean>`), and `Push` supplies the token via a
**one-shot** explicit URL on the command line
(`git push https://x-access-token:<token>@…`), never storing it in `.git/config`. A
test asserts the post-clone `.git/config` contains no token. The brain's process
cmdline (which momentarily carries the token on push) is in a separate PID namespace
the jailed bees cannot see, and bees run on separate hosts entirely in the remote
case.

## Error handling / edge cases

- **Provisioning fails** (bad repo/branch/auth) → `CreateMission` returns an error;
  no mission is created.
- **No token + private repo** → clone fails clearly; **public repo / local path /
  `file://`** clone token-free.
- **Push/PR fails** (network/auth/branch exists) → logged + recorded as a mission
  event; the **local branch with all commits survives** for manual push; `PRURL`
  stays empty. The mission still completes; the work is not lost.
- **Empty-diff phase** → `Commit` no-ops (no empty commits).
- **Fresh branch off base** → no push-time merge conflicts; base-vs-PR conflicts are
  the reviewer's at merge time.
- **Path traversal** in `read_repo`/`repo_grep`/`Tree` → rejected (confined to the
  working dir).
- **No repo on a mission** (plain directive) → unchanged behavior; the repo engine,
  commits, push/PR, and read tools are all skipped.

## Testing

- **`internal/repo`:** against a local `file://` bare repo + a tmp clone: `Clone`,
  `Checkout`, `Commit` (and the empty-diff no-op), `Push` to a local bare "remote",
  `OpenPR` against a **stub HTTP server** emulating `/repos/{o}/{r}/pulls` (assert the
  request shape + that the returned `html_url` is parsed). `RepoIdent` parses
  owner/repo from https/ssh forms. **Token redaction:** assert the token never
  appears in any returned error or logged command string. Read surface: `ReadFile`/
  `Tree`/`Grep` return correct results and **reject `..`/absolute escapes**.
- **credential-boundary tests:** (a) assert `sandbox.MinimalEnv()` never contains
  `CORRALAI_GIT_TOKEN` even when it's set in the brain's environment; (b) assert that
  after `Clone`, the working copy's `.git/config` contains no token (the jail-readable
  surface is clean). Both enforce the boundary as a test.
- **mission engine:** with a fake `repo.Engine` spy — a phase completing triggers
  exactly one `Commit` with the phase in the message; an empty-diff phase triggers no
  commit; mission `done` triggers `Push` then `OpenPR` and stores `PRURL`; a
  `Push`/`OpenPR` error doesn't crash `Tick` and leaves the mission `done`.
- **brain read tools:** `read_repo`/`repo_tree`/`repo_grep` over a tmp working dir
  return expected content and reject path escapes; unregistered when no repo engine.

## Companion sub-projects (now in scope)

- **Distributed workspace (#16)** — how remote bees mirror the working copy via a
  `.git`-free snapshot and push changes back, with no shared volume. This spec's
  provisioning, read surface, and per-phase commit lifecycle are designed to compose
  with it (per-mission `<workspace>/m<id>` copies, brain-owned).
- **Repo code index (#17)** — `internal/repoindex` + a `repo_search` MCP tool:
  DuckDB FTS + embeddings over the working copy, indexed at the same per-commit seam,
  for semantic/hybrid code search alongside `repo_grep`.

## Out of scope (follow-ups)

- **Provider abstraction** — GitLab/Gitea/GHE (this spec is GitHub REST + token).
- **PR-review-response loop** — the swarm reacting to PR review comments.
- **Symbol-aware code chunking, VSS/ANN index** — see #17's follow-ups.
