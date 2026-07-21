<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Bind-Mount Dependency Directories into the Audit Jail — Design

**Status:** approved design, 2026-07-21. Ready for an implementation plan.

**Goal:** Let `corral certify --local --repo-dir` audit real projects whose test
suites need heavy third-party dependency trees (`node_modules`, `vendor/`,
`.venv/`, …) — by **bind-mounting those dirs read-only** into the jail instead of
copying them into the 64 MiB workspace-seed budget. A JS/TS project whose
`node_modules` is 337 MB currently fails closed ("repo has more than 64 MiB of
text — too large to seed the jail workspace"); after this it audits.

**One-line motivation:** corral already audits code that imports things (its own
modules + stdlib — proven on more-itertools/google-uuid/threedaymonk-text). The
one gap is third-party deps whose tree blows the copy budget. Deps are read-only
at test time; they never belonged in the copied, mutable seed.

---

## Background — current state (grounded)

- **The seed + cap.** `loadRepoFiles(root) (map[string]string, error)`
  (`cmd/corral/certify_local.go:719`) walks `--repo-dir` via `os.OpenRoot` +
  `fs.WalkDir`, skips `.git` with a single `SkipDir` hook (`:737-741`), skips
  symlinks (`:743`) and files > `maxFile = 1<<20` (`:750`), and errors once the
  running `total` exceeds `maxTotal = 64<<20` (`:766`). The `.git` SkipDir is the
  **only dirname-based exclude — the exact hook this feature extends.** Result
  wired into three adapters at `:316-318`: `JailScorer.BaseFiles`,
  `JailValidator.BaseFiles`, `JailEnumerator.BaseFiles`.
- **BaseFiles → jail.** `JailScorer.scoreWorkspace` (`internal/advpool/gate.go:153`)
  copies `BaseFiles` into a per-run base map (repo-aware mode). `Score`/`ScoreReport`
  (`:110`/`:135`) pass it to `adequacy.Score`; `JailEnumerator.Enumerate` (`:192`)
  and `JailValidator.Validate` (`:230`) do the same.
- **The adequacy jail.** `bwrapJail` (`internal/adequacy/jail.go:37`);
  `writeWorkspace(files)` (`:93`) makes an `os.MkdirTemp` workspace, chmods it
  (`0700/0600`, loosened to `0755/0644` for the container backend, `:103-104`),
  writes the files. `RunTest` (`:136`) / `Enumerate` (`:167`) call
  `sandbox.RunGuarded(ctx, cmd, sandbox.Options{Workspace: dir, Backend, Network:false, Timeout})`.
- **The sandbox + backends.** `sandbox.Options` (`internal/sandbox/sandbox.go:22`):
  `Workspace/Timeout/MaxOutput/Env/Network/Backend`. Each backend implements
  `Isolator.Wrap(command, opts, env) (argv, err)` (`isolator.go:18`):
  - **bwrap** (`isolator_linux.go:40`): `--ro-bind /usr /usr` … `--bind
    opts.Workspace opts.Workspace` (`:78`), `--chdir opts.Workspace` (`:79`). Runs
    as the **same host uid**.
  - **container** (`container.go:38`): `--read-only --cap-drop=ALL … -v
    opts.Workspace:opts.Workspace` (`:70`), `-w` (`:71`). Maps host-uid → a
    different in-container uid; **no `CAP_DAC_OVERRIDE`**.
  - **macOS sandbox-exec** (`isolator_darwin.go:46`): builds an SBPL profile;
    allowed read subpaths (`:71-73`), writable workspace (`:103`). Grants access
    to real host paths **in place** (no bind primitive, no uid remap).
- **The container-perms nuance** (`jail.go:103-104` + doc `:64-92`): the container
  runs `--cap-drop=ALL` as a different uid, so it can't read host-uid-owned
  `0700/0600` files; `writeWorkspace` loosens *its own temp workspace* to
  `0755/0644`. **A read-only bind of the user's real dep dir can't be chmod'd**, so
  a non-world-readable dep dir is unreadable under the container backend.

**The gap:** there is no way to keep a large read-only dir out of the seed budget
while still making it visible in the jail.

---

## Non-goals

- **The brain repo/control-gate path.** That clones PR repos through
  `internal/gate`/`internal/repo` with its own seeding. This feature scopes to the
  `--local --repo-dir` path (`loadRepoFiles`), where the cap actually bites. The
  brain path is a documented follow-up.
- **Making deps writable.** Binds are read-only. A test toolchain that must *write*
  into a dep dir (rare — jest/vitest cache to `os.tmpdir()`, not `node_modules`)
  is out of scope; `/tmp` is a writable tmpfs for the common cache case.
- **Auto-installing deps.** corral never runs `npm install`/`bundle` — the operator
  vendors deps first (as CI does). This feature only makes *present* deps visible
  without copying them.
- **Raising the 64 MiB cap.** The cap stays; deps just stop counting against it.

---

## Architecture

Four layers, each independently testable.

### 1. `sandbox` — `Options.ReadOnlyBinds` + per-backend `Wrap`

Add to `sandbox.Options` (`sandbox.go:22`):
```go
// ReadOnlyBinds are host directories mounted read-only INTO the jail at Target
// (an absolute path under Workspace), so large read-only trees (node_modules,
// vendor, .venv) are visible to the test command without being copied into the
// workspace. Read-only: the sandboxed process can never write them.
ReadOnlyBinds []Bind
```
```go
type Bind struct {
    Host   string // absolute host path (a directory)
    Target string // absolute path inside the jail (under Workspace)
}
```
`sandbox.Bind.Target` is **absolute and fully resolved** — the backend consumes it
verbatim. Resolution (joining the repo-relative dep path onto the *per-run* temp
workspace) happens one layer up, in `adequacy` (§2), which is the only layer that
knows the temp `dir`. Each backend's `Wrap` honors it:
- **bwrap** (`isolator_linux.go`): for each bind, append `--ro-bind Host Target`
  **after** the `--bind opts.Workspace` line (`:78`) so the workspace mountpoint
  exists first; bwrap auto-creates the target. Same-uid → reads cleanly.
- **container** (`container.go`): append `-v Host:Target:ro` near `:70`.
- **macOS** (`isolator_darwin.go`): add `(allow file-read* (subpath "Host"))` to the
  profile (near `:71-73`). Reads in place; no bind needed.

`Isolator.Preflight` is unchanged. A backend with no bind concept never sees
binds (the caller decides — see §3's container fallback).

### 2. `adequacy` — thread binds through the jail

`bwrapJail.RunTest`/`Enumerate` gain the binds and pass them to `RunGuarded`.
Because the bound dirs live **outside** the per-run temp workspace, they do NOT
pass through `writeWorkspace` — but the layer DOES own the per-run temp `dir`, so
**this is where the repo-relative dep path is resolved to an absolute
`sandbox.Bind.Target`**.

The binds carried into `adequacy` use a **relative** form —
`adequacy.DepBind{Host string /*absolute*/, Rel string /*repo-relative dir*/}` —
and `RunTest`/`Enumerate` convert each to `sandbox.Bind{Host, Target:
filepath.Join(dir, Rel)}` after `writeWorkspace` returns `dir`, then pass them in
`sandbox.Options.ReadOnlyBinds`.

Wire it via an option for parity with `WithMutantTimeout` (no interface break for
callers passing no binds): `RunTest(ctx, files, testCmd, ...RunOption)` /
`Enumerate(..., ...RunOption)` with `adequacy.WithReadOnlyBinds([]DepBind)`.

### 3. `advpool` — carry binds alongside `BaseFiles`

`JailScorer`/`JailValidator`/`JailEnumerator` each gain
`ReadOnlyBinds []adequacy.DepBind` (the relative form) next to `BaseFiles`.
`scoreWorkspace` is unchanged (binds aren't files);
`Score`/`ScoreReport`/`Enumerate`/`Validate` pass `ReadOnlyBinds` into the
adequacy call via `adequacy.WithReadOnlyBinds`. Nil/empty ⇒ exact current
behavior.

### 4. `cmd/corral` — detect dep dirs, exclude from seed, record binds, wire flags

`loadRepoFiles` becomes dep-aware. New return shape:
`loadRepoFiles(root string, opts loadOpts) (files map[string]string, binds []adequacy.DepBind, err error)`
— it emits the **relative** `DepBind{Host: filepath.Join(root, rel) /*absolute*/,
Rel: rel}` form (§2 resolves `Target` per run).

- **Auto-detect** (default on): a curated dir-name set —
  `depDirNames = {"node_modules", "vendor", ".venv", "venv", ".bundle"}`. When the
  walk hits a dir whose base name is in the set, `return fs.SkipDir` (don't copy)
  and record a `DepBind{Host: abs(root/rel), Rel: rel}`. Nested dep dirs
  (monorepos) are each skipped + bound at their own rel path; bwrap/container/macOS
  all support multiple binds.
- **`--bind-dir <path>` (repeatable):** add an extra dir (repo-relative) to bind,
  for ecosystems outside the curated set.
- **`--no-bind-deps`:** disable auto-detection — dep dirs get copied (today's
  behavior), for debugging or when a run genuinely needs deps in the mutable tree.

**Container fallback (the "both" decision).** The bind is emitted for every
backend, EXCEPT: on the **container** backend, before emitting a bind, check the
dep dir is world-readable-traversable (a cheap `os.Stat` on the dir + a shallow
check, or simply: `perm & 0o005 != 0` on the top dir and rely on npm's normal
world-readable install). If a dep dir is NOT world-readable under the container
backend, **fall back to copying that one dir into the seed** (subject to the 64
MiB cap → the existing clear error if too big). bwrap/macOS never need this
(same-uid / in-place read). So container gets the bind benefit whenever perms
allow and degrades *loudly* (copy → possibly the clear cap error), never
silently. The chosen backend is known at seed time (`sandbox.Resolve` /
`--jail`), so `loadRepoFiles` can branch on it.

**Readout (honesty):** when dep dirs are bound, print
`deps: bound N dir(s) read-only (node_modules, …) — <size> not copied` so the
operator sees the mechanism. When a container fallback copies one, say so.

---

## Data flow (end-to-end)

1. `loadRepoFiles(repoDir, {backend, extraBindDirs, noBindDeps})` walks the tree:
   copies source (minus dep dirs, still under the 64 MiB cap for source), and for
   each detected/`--bind-dir` dep dir emits a `Bind{absHost, rel}` — unless the
   container-fallback rule copies it instead.
2. `certify_local.go` sets `ReadOnlyBinds` (the relative `DepBind`s) on the three
   adapters alongside `BaseFiles` (`:316-318`). Per-run `Target` resolution
   (`filepath.Join(dir, Rel)`) happens later, in `adequacy` (§2).
3. Per mutant/test: `scoreWorkspace` builds the copied base (small — no deps);
   `RunTest`/`Enumerate` pass `WithReadOnlyBinds(binds)` → `sandbox.Options.ReadOnlyBinds`
   → the backend `Wrap` mounts each dep dir read-only into the temp workspace.
4. The test command runs with deps visible (jest/pytest/rspec resolve
   `node_modules`/gems in place) and the mutated code file writable. Deps are
   never copied → the 64 MiB budget only ever sees source.

---

## Error handling / fail-closed / security

- **Read-only, always.** Binds are `--ro-bind` / `-v :ro` / `file-read*` — the
  sandboxed test can never write the operator's real dep tree. A mutant can't
  escape via a dep dir.
- **Same trust as the code under audit.** The dep tree is operator-supplied, not
  attacker-supplied; binding it read-only adds no new trust. Network stays off,
  cap-drop/tmpfs/proc unchanged.
- **Symlinks inside a bound dir** resolve within the jail's mount namespace
  (bwrap) — read-only, so no write-escape; a dangling link just fails to read.
  Note it; don't special-case for v1.
- **Container non-readable dep dir → copy fallback**, subject to the 64 MiB cap
  and its existing clear error. Never a silent skip: if a container run can't bind
  AND can't fit, it fails closed exactly as today, with the readout explaining
  which dir forced it.
- **A `--bind-dir` path that doesn't exist / isn't a dir** → clear error at seed
  time (fail-closed, before any jail run).
- **Target collision:** a dep dir at `rel` and a copied file at the same `rel`
  cannot both exist (the dir was skipped from the copy), so no overlap.
- **The cap still protects source.** If non-dep source alone exceeds 64 MiB, the
  existing error still fires — this feature doesn't lift that.

---

## Testing strategy

- **`sandbox` per backend:** `Wrap` with `ReadOnlyBinds` emits the right argv —
  bwrap `--ro-bind H T`, container `-v H:T:ro`, macOS profile `allow file-read*
  subpath H`. Unit-test the argv/profile string (no real jail needed).
- **`loadRepoFiles`:** a fixture tree with a `node_modules/` full of junk over 64
  MiB + a small source tree → returns the source files (under cap) + a bind for
  `node_modules`, and does NOT error on the cap. `--no-bind-deps` → copies (and
  errors on the cap). `--bind-dir extra/` → binds it. Nested `node_modules` →
  multiple binds. A non-dep 65 MiB source tree → still errors.
- **Container fallback:** with backend=container and a simulated non-world-readable
  dep dir, `loadRepoFiles` copies it (and hits the cap error if too big); with a
  world-readable one, it binds. (Test the decision function, not a live container.)
- **`adequacy` passthrough:** `WithReadOnlyBinds` reaches `sandbox.Options` (fake
  Isolator captures the Options).
- **End-to-end (bwrap, real):** the ms fixture (or a small vendored-deps fixture) —
  audit succeeds with `node_modules` bound, where today it fails closed on 64 MiB.
  Runs on the brain host / CI where bwrap works.
- **No-regression:** a `--repo-dir` audit with no dep dirs (more-itertools) is
  byte-for-byte unchanged (nil binds).
- Deploy gate: `gofmt -l` clean, `bash scripts/check-security.sh` OK,
  `go test ./... -race` green.

---

## Rollout / scope / honesty

- **Auto-detect on by default** (`node_modules`, `vendor`, `.venv`, `venv`,
  `.bundle`) + `--bind-dir` for extras + `--no-bind-deps` to disable. Documented.
- **bwrap + macOS bind cleanly; container binds when world-readable, else copies**
  (loudly). bwrap is the default on prod/CI/recordings, so the common path is the
  clean path.
- **`--local --repo-dir` only** this slice; the brain gate path is a noted
  follow-up.
- The readout states what was bound vs copied so the operator is never guessing.
- Docs (`running-it.mdx` + README) explain: deps must be *present* (vendored,
  like CI), corral binds them read-only, doesn't install them, and the container
  backend has the world-readable caveat.

## Future (out of scope here)

- **The brain repo/control-gate path** gets the same treatment (its own seeding).
- **`--user <hostuid>` container mode** as an opt-in to bind non-world-readable
  deps under the container backend (the code today rejects it as fragile; revisit
  behind a flag if demand appears).
- **Writable dep overlays** for the rare toolchain that writes into `node_modules`
  (an overlayfs upper, or a redirected cache dir).
