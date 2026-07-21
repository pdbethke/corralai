# Bind-Mount Dependency Directories Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `corral certify --local --repo-dir` audit projects with heavy dependency trees by bind-mounting dep dirs (`node_modules`, `vendor`, `.venv`, …) read-only into the jail instead of copying them into the 64 MiB seed budget.

**Architecture:** A new `sandbox.Bind`/`Options.ReadOnlyBinds` honored by each backend's `Wrap` (bwrap `--ro-bind`, container `-v :ro`, macOS SBPL `file-read*`). The audit jail (`bwrapJail`) carries the run's dep binds (constant per run) — set at construction via a `NewJail`/`NewEnumerator` option — and resolves each repo-relative `DepBind` to an absolute `Target` under the per-run temp workspace. `loadRepoFiles` auto-detects dep dirs, skips copying them, and returns them as `DepBind`s; on the container backend it falls back to copying a dep dir that isn't world-readable.

**Tech Stack:** Go 1.26.x, `internal/sandbox` (bwrap/container/sandbox-exec), `internal/adequacy`, `cmd/corral`.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-07-21-bind-mount-dependency-dirs-design.md` — every task implicitly includes it.
- **Deploy gate (all tasks):** `gofmt -l .` prints nothing; `bash scripts/check-security.sh` exits 0 (gosec MEDIUM+); `go test ./... -race` passes.
- **Read-only, always.** Binds are `--ro-bind` / `-v H:T:ro` / `(allow file-read* (subpath H))`. The sandboxed process can never write the operator's real dep tree. Network stays off; cap-drop/tmpfs/proc unchanged.
- **No behavior change when there are no dep dirs.** A `--repo-dir` audit of a repo with no dep dirs (e.g. more-itertools) is byte-for-byte unchanged — nil binds, empty `ReadOnlyBinds`, the same seed.
- **The 64 MiB cap stays** (`maxTotal = 64<<20`); deps just stop counting against it. Non-dep source over 64 MiB still fails closed with the existing message.
- **Auto-detect set (verbatim):** `node_modules`, `vendor`, `.venv`, `venv`, `.bundle`. Flags: `--bind-dir <path>` (repeatable, repo-relative), `--no-bind-deps` (disable auto-detect → copy).
- **Container degrades loudly, never silently:** bind when the dep dir is world-readable; else copy it into the seed (subject to the cap error). bwrap + macOS always bind cleanly.
- **Scope:** the `--local --repo-dir` path (`loadRepoFiles`). The brain gate path is out of scope.
- **SPDX headers** on any new file; `#nosec` with a reason for any flagged line.

---

### Task 1: `sandbox.Bind` + `Options.ReadOnlyBinds` + honor in all three backends

**Files:**
- Modify: `internal/sandbox/sandbox.go` (`Options` struct ~22-29; add `Bind`)
- Modify: `internal/sandbox/isolator_linux.go` (bwrap `Wrap` ~76-80)
- Modify: `internal/sandbox/container.go` (container `Wrap` ~69-71)
- Modify: `internal/sandbox/isolator_darwin.go` (sandbox-exec `Wrap` profile ~71-88)
- Test: `internal/sandbox/isolator_linux_test.go`, `internal/sandbox/container_test.go` (or the existing backend test files — grep for `_test.go` in the package)

**Interfaces:**
- Produces:
  ```go
  type Bind struct {
      Host   string // absolute host directory
      Target string // absolute path inside the jail (under Workspace)
  }
  ```
  and `Options.ReadOnlyBinds []Bind`. Each backend's `Wrap` mounts every bind read-only. Nil/empty ⇒ current argv exactly.

- [ ] **Step 1: Write the failing tests**

Add to the sandbox backend tests (match the package's existing test style; these fakes need no real jail — they assert the argv/profile string). If the backend test files don't exist yet, create `internal/sandbox/binds_test.go`:
```go
package sandbox

import (
	"strings"
	"testing"
)

func TestBwrapWrapReadOnlyBinds(t *testing.T) {
	b := bwrapIsolator{} // use the real unexported bwrap isolator type — grep isolator_linux.go for its name
	argv, err := b.Wrap("echo hi", Options{
		Workspace:     "/tmp/ws",
		ReadOnlyBinds: []Bind{{Host: "/proj/node_modules", Target: "/tmp/ws/node_modules"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--ro-bind /proj/node_modules /tmp/ws/node_modules") {
		t.Fatalf("bwrap argv missing ro-bind: %v", argv)
	}
	// the dep bind must come AFTER the workspace bind so the mountpoint parent exists
	wsIdx := strings.Index(joined, "--bind /tmp/ws /tmp/ws")
	depIdx := strings.Index(joined, "--ro-bind /proj/node_modules")
	if wsIdx < 0 || depIdx < 0 || depIdx < wsIdx {
		t.Fatalf("dep bind must follow workspace bind: ws=%d dep=%d", wsIdx, depIdx)
	}
}

func TestContainerWrapReadOnlyBinds(t *testing.T) {
	c := containerIsolator{image: "img", runtime: "docker"} // match the real struct fields — grep container.go
	argv, err := c.Wrap("echo hi", Options{
		Workspace:     "/tmp/ws",
		ReadOnlyBinds: []Bind{{Host: "/proj/node_modules", Target: "/tmp/ws/node_modules"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "-v /proj/node_modules:/tmp/ws/node_modules:ro") {
		t.Fatalf("container argv missing ro volume: %v", argv)
	}
}
```
(Read `isolator_linux.go` / `container.go` first for the exact unexported isolator type names + fields — the test uses them directly. If the constructors are only via `Resolve`, build the struct literal the same way the existing backend tests do.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/sandbox/ -run 'TestBwrapWrapReadOnlyBinds|TestContainerWrapReadOnlyBinds'` → FAIL (field `ReadOnlyBinds` undefined).

- [ ] **Step 3: Implement**

In `sandbox.go`, add the `Bind` type (above the `Options` struct) and the field to `Options`:
```go
	// ReadOnlyBinds are host directories mounted read-only into the jail at
	// Target (an absolute path under Workspace), so large read-only trees
	// (node_modules, vendor, .venv) are visible to the command without being
	// copied into the workspace. The sandboxed process can never write them.
	ReadOnlyBinds []Bind
```
In `isolator_linux.go`, right AFTER the `"--bind", opts.Workspace, opts.Workspace,` / `"--chdir", opts.Workspace,` block (append before the `if opts.Network` line), add:
```go
	for _, bnd := range opts.ReadOnlyBinds {
		argv = append(argv, "--ro-bind", bnd.Host, bnd.Target)
	}
```
In `container.go`, in the `argv = append(argv, ...)` mount block, add one `-v` per bind right after the workspace `-v`:
```go
	for _, bnd := range opts.ReadOnlyBinds {
		argv = append(argv, "-v", bnd.Host+":"+bnd.Target+":ro")
	}
```
(Insert the loop so its `-v` args come before `c.image` in the argv.)
In `isolator_darwin.go`, in the SBPL profile builder, add a read-allow line per bind alongside the existing allowed-read subpaths (~71-73 / :88):
```go
	for _, bnd := range opts.ReadOnlyBinds {
		profile += fmt.Sprintf("(allow file-read* (subpath %q))\n", bnd.Host)
	}
```
(macOS reads the dir in place at `bnd.Host`; `Target` is unused there. Match the profile builder's exact string-assembly style.)

- [ ] **Step 4: Run, verify pass** — `go test ./internal/sandbox/ -race` → PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/sandbox
git commit -m "feat(sandbox): Options.ReadOnlyBinds — mount dep dirs read-only per backend"
```

---

### Task 2: `adequacy.DepBind` + the jail carries binds + resolves Target per run

**Files:**
- Modify: `internal/adequacy/jail.go` (`bwrapJail` struct ~37-40; `NewJail`/`NewEnumerator` ~46-57; `RunTest` ~136-159; `Enumerate` ~167-...)
- Test: `internal/adequacy/jail_test.go` (grep for its fake `sandbox.Isolator` / how it captures the run; if none, add a capturing fake)

**Interfaces:**
- Consumes: `sandbox.Bind`, `sandbox.Options.ReadOnlyBinds` (Task 1).
- Produces:
  ```go
  // DepBind is a read-only dependency dir to mount into the jail: Host is the
  // absolute host path, Rel is the repo-relative path where it lives (and where
  // the test command expects it). RunTest/Enumerate resolve Rel against the
  // per-run temp workspace to build sandbox.Bind.Target.
  type DepBind struct {
      Host string // absolute host dir
      Rel  string // repo-relative dir (slash-separated), e.g. "node_modules"
  }
  type JailOption func(*bwrapJail)
  func WithReadOnlyBinds(binds []DepBind) JailOption
  ```
  `NewJail(backend sandbox.Isolator, timeout time.Duration, opts ...JailOption) Jail` and `NewEnumerator(backend sandbox.Isolator, timeout time.Duration, opts ...JailOption) Enumerator` — variadic option so existing callers passing no option are unchanged.

- [ ] **Step 1: Write the failing test**

In `jail_test.go`, add a capturing fake `sandbox.Isolator` (or reuse an existing one) whose `Wrap` records the `Options` it received. Then:
```go
func TestJailResolvesDepBindsToWorkspaceTarget(t *testing.T) {
	var got sandbox.Options
	fake := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { got = o }}
	j := NewJail(fake, time.Second, WithReadOnlyBinds([]DepBind{{Host: "/proj/node_modules", Rel: "node_modules"}}))
	_, _ = j.RunTest(context.Background(), map[string]string{"a.js": "1"}, []string{"true"})
	if len(got.ReadOnlyBinds) != 1 {
		t.Fatalf("want 1 bind, got %d", len(got.ReadOnlyBinds))
	}
	b := got.ReadOnlyBinds[0]
	if b.Host != "/proj/node_modules" {
		t.Fatalf("host = %q", b.Host)
	}
	// Target is the PER-RUN temp workspace joined with Rel — not a static path.
	if b.Target != got.Workspace+"/node_modules" {
		t.Fatalf("target = %q, want %q (per-run workspace + Rel)", b.Target, got.Workspace+"/node_modules")
	}
}
```
(Model `captureIsolator` on however jail_test.go already fakes the backend — its `Wrap` must return a harmless argv like `[]string{"true"}` so `sandbox.RunGuarded` proceeds. If the existing tests use a different fake mechanism, follow it.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/adequacy/ -run TestJailResolvesDepBinds` → FAIL (undefined `DepBind`/`WithReadOnlyBinds`).

- [ ] **Step 3: Implement**

Add the `DepBind`/`JailOption`/`WithReadOnlyBinds` types. Add a `binds []DepBind` field to `bwrapJail`. Make `NewJail`/`NewEnumerator` variadic:
```go
func NewJail(backend sandbox.Isolator, timeout time.Duration, opts ...JailOption) Jail {
	j := bwrapJail{backend: backend, timeout: timeout}
	for _, o := range opts {
		o(&j)
	}
	return j
}
func WithReadOnlyBinds(binds []DepBind) JailOption {
	return func(j *bwrapJail) { j.binds = binds }
}
```
(same for `NewEnumerator`). In `RunTest`, after `dir, err := j.writeWorkspace(files)` succeeds, resolve the binds and pass them:
```go
	var roBinds []sandbox.Bind
	for _, b := range j.binds {
		roBinds = append(roBinds, sandbox.Bind{Host: b.Host, Target: filepath.Join(dir, filepath.FromSlash(b.Rel))})
	}
	res, err := sandbox.RunGuarded(ctx, strings.Join(testCmd, " "), sandbox.Options{
		Workspace:     dir,
		Backend:       j.backend,
		Network:       false,
		Timeout:       j.timeout,
		ReadOnlyBinds: roBinds,
	})
```
Do the identical resolution in `Enumerate`. (`filepath.Join`/`FromSlash` — `Rel` is slash-separated from the walk.)

- [ ] **Step 4: Run, verify pass** — `go test ./internal/adequacy/ -race` → PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/adequacy
git commit -m "feat(adequacy): jail carries DepBinds, resolves Target under the per-run workspace"
```

---

### Task 3: `loadRepoFiles` — detect dep dirs, exclude from seed, return DepBinds

**Files:**
- Modify: `cmd/corral/certify_local.go` (`loadRepoFiles` ~712-772)
- Test: `cmd/corral/certify_local_test.go` (or a new `cmd/corral/loadrepo_test.go`)

**Interfaces:**
- Consumes: `adequacy.DepBind` (Task 2), `sandbox` backend name.
- Produces:
  ```go
  type loadOpts struct {
      BackendName  string   // sandbox backend Name(): "bwrap" | "container" | "sandbox-exec" | ...
      ExtraBindDir []string // repo-relative dirs from --bind-dir
      NoBindDeps   bool     // --no-bind-deps: copy dep dirs instead of binding
  }
  func loadRepoFiles(root string, opts loadOpts) (files map[string]string, binds []adequacy.DepBind, err error)
  var depDirNames = map[string]bool{"node_modules": true, "vendor": true, ".venv": true, "venv": true, ".bundle": true}
  ```
  A detected dep dir is skipped from `files` and returned as a `DepBind{Host: abs(root/rel), Rel: rel}` — UNLESS `NoBindDeps` (copy it) or the container-fallback rule applies (copy it). `ExtraBindDir` entries are bound the same way.

- [ ] **Step 1: Write the failing tests**

`cmd/corral/loadrepo_test.go`:
```go
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { t.Fatal(err) }
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil { t.Fatal(err) }
	}
}

func TestLoadRepoFilesBindsDepDirs(t *testing.T) {
	root := t.TempDir()
	// a big node_modules that would blow the 64 MiB cap if copied
	big := strings.Repeat("x", 1<<20) // 1 MiB
	tree := map[string]string{"src/index.js": "code"}
	for i := 0; i < 80; i++ { tree[fmt.Sprintf("node_modules/pkg%d/i.js", i)] = big } // 80 MiB
	writeTree(t, root, tree)

	files, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap"})
	if err != nil { t.Fatalf("should NOT hit the cap (node_modules bound, not copied): %v", err) }
	if _, ok := files["src/index.js"]; !ok { t.Fatal("source file missing from seed") }
	for k := range files { if strings.HasPrefix(k, "node_modules/") { t.Fatalf("node_modules leaked into the copied seed: %s", k) } }
	if len(binds) != 1 || binds[0].Rel != "node_modules" { t.Fatalf("want 1 node_modules bind, got %+v", binds) }
	if !filepath.IsAbs(binds[0].Host) { t.Fatalf("bind Host must be absolute: %q", binds[0].Host) }
}

func TestLoadRepoFilesNoBindDepsCopiesAndHitsCap(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 1<<20)
	tree := map[string]string{"src/index.js": "code"}
	for i := 0; i < 80; i++ { tree[fmt.Sprintf("node_modules/pkg%d/i.js", i)] = big }
	writeTree(t, root, tree)
	_, _, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", NoBindDeps: true})
	if err == nil { t.Fatal("with --no-bind-deps the 80 MiB node_modules is copied and must hit the 64 MiB cap") }
}

func TestLoadRepoFilesExtraBindDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/a.go": "x", "thirdparty/lib.go": "y"})
	_, binds, err := loadRepoFiles(root, loadOpts{BackendName: "bwrap", ExtraBindDir: []string{"thirdparty"}})
	if err != nil { t.Fatal(err) }
	found := false
	for _, b := range binds { if b.Rel == "thirdparty" { found = true } }
	if !found { t.Fatalf("--bind-dir thirdparty not bound: %+v", binds) }
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./cmd/corral/ -run TestLoadRepoFiles` → FAIL (signature mismatch / undefined).

- [ ] **Step 3: Implement**

Change `loadRepoFiles` to the new signature + return `binds`. In the `d.IsDir()` branch, alongside the `.git` skip, add dep detection:
```go
	if d.IsDir() {
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if rel != "." && shouldBind(rel, d.Name(), opts) {
			absHost, aerr := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
			if aerr != nil {
				return aerr
			}
			binds = append(binds, adequacy.DepBind{Host: absHost, Rel: rel})
			return fs.SkipDir // do NOT copy the dep dir into the seed
		}
		return nil
	}
```
Add the decision helper:
```go
// shouldBind reports whether the dir at rel (base name name) should be
// bind-mounted read-only rather than copied. Auto-detected dep dirs and
// --bind-dir entries qualify, unless --no-bind-deps is set. On the container
// backend a non-world-readable dep dir is copied instead (degrade loudly): the
// container maps host uid → a different uid and can't read 0700 trees, and a
// read-only bind can't be chmod'd — so fall back to the seed (and its cap).
func shouldBind(rel, name string, opts loadOpts) bool {
	if opts.NoBindDeps {
		return false
	}
	auto := depDirNames[name]
	extra := false
	for _, e := range opts.ExtraBindDir {
		if e == rel {
			extra = true
		}
	}
	if !auto && !extra {
		return false
	}
	if opts.BackendName == "container" && !worldReadableDir(filepath.Join(opts.rootAbs, filepath.FromSlash(rel))) {
		return false // copy it instead; may hit the cap → the existing clear error
	}
	return true
}
```
Add `worldReadableDir` (dir readable+traversable by "other"):
```go
func worldReadableDir(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	// need o+r and o+x on the dir to traverse+read as a different uid
	const oRX = 0o005
	return fi.Mode().Perm()&oRX == oRX
}
```
Thread `opts.rootAbs` (absolute root, computed once at the top of `loadRepoFiles`) so `shouldBind` can stat real paths — OR pass the absolute dir directly. Simplest: compute `rootAbs, _ := filepath.Abs(root)` at the top and store on a local `opts.rootAbs` (add the unexported field) before the walk. Declare `var binds []adequacy.DepBind` before the walk and `return files, binds, walkErr` at the end. Validate `--bind-dir` entries exist (a `filepath.Join(rootAbs, e)` stat) up front; a missing one → a clear error before the walk.

- [ ] **Step 4: Run, verify pass** — `go test ./cmd/corral/ -run TestLoadRepoFiles -race` → PASS.

- [ ] **Step 5: Commit**
```bash
git add cmd/corral/certify_local.go cmd/corral/loadrepo_test.go
git commit -m "feat(certify --local): loadRepoFiles detects+binds dep dirs, container copy-fallback"
```

---

### Task 4: Wire it into `certify --local` — flags, jail construction, readout, docs

**Files:**
- Modify: `cmd/corral/certify_local.go` (flag defs; the `loadRepoFiles` call site ~296; `NewJail`/`NewEnumerator` construction; the readout; backend-name resolution ordering)
- Modify: `README.md`, `site/src/content/docs/docs/running-it.mdx`, `ROADMAP.md`
- Test: `cmd/corral/certify_local_test.go` (flag → loadOpts plumbing; the end-to-end is noted for the reviewer)
- Regenerate: `scripts/gen-cli-docs.sh` (new flags change `corral certify --local -h`)

**Interfaces:**
- Consumes: `loadRepoFiles(root, loadOpts)` (Task 3), `adequacy.NewJail(..., WithReadOnlyBinds(binds))` (Task 2).

- [ ] **Step 1: Write the failing test**

In `certify_local_test.go`, add a focused test that the flags reach `loadOpts` — if `runCertifyLocal` is hard to unit-test end-to-end, factor a tiny `buildLoadOpts(flags…)` helper and test it:
```go
func TestBuildLoadOpts(t *testing.T) {
	o := buildLoadOpts("container", []string{"third"}, true)
	if o.BackendName != "container" || !o.NoBindDeps || len(o.ExtraBindDir) != 1 || o.ExtraBindDir[0] != "third" {
		t.Fatalf("loadOpts not built from flags: %+v", o)
	}
}
```

- [ ] **Step 2: Run, verify fail** — FAIL (undefined `buildLoadOpts`).

- [ ] **Step 3: Implement the wiring**

Add flags (near the other `fs.*` flag defs):
```go
	bindDirFlag := fsStringSlice(fs, "bind-dir", "extra repo-relative dependency dir to mount read-only into the jail instead of copying it into the workspace (repeatable; node_modules/vendor/.venv/venv/.bundle are auto-detected)")
	noBindDepsFlag := fs.Bool("no-bind-deps", false, "copy dependency dirs into the jail workspace instead of bind-mounting them read-only (the pre-bind behavior; subject to the workspace size cap)")
```
(If the CLI has no repeatable-string-flag helper, use the standard `flag.Var` with a `stringSlice` type — grep for an existing repeatable flag in the file; `--produced-by` or similar may show the pattern.)

Resolve the backend BEFORE `loadRepoFiles` so its name is known (the `iso` isolator is already resolved for the jail — move/confirm that resolution precedes line 296). Then:
```go
	repoFiles, depBinds, lerr := loadRepoFiles(repoDir, buildLoadOpts(iso.Name(), *bindDirFlag, *noBindDepsFlag))
	...
	jail := adequacy.NewJail(iso, *timeout, adequacy.WithReadOnlyBinds(depBinds))
	enumerator := adequacy.NewEnumerator(iso, *timeout, adequacy.WithReadOnlyBinds(depBinds))
```
`JailScorer`/`JailValidator`/`JailEnumerator` are UNCHANGED — they hold the jail, which now carries the binds. Add `buildLoadOpts`:
```go
func buildLoadOpts(backendName string, bindDirs []string, noBindDeps bool) loadOpts {
	return loadOpts{BackendName: backendName, ExtraBindDir: bindDirs, NoBindDeps: noBindDeps}
}
```
Add the readout after `loadRepoFiles` succeeds (only when `len(depBinds) > 0`):
```go
	if len(depBinds) > 0 {
		names := make([]string, 0, len(depBinds))
		for _, b := range depBinds { names = append(names, b.Rel) }
		fmt.Fprintf(progressOut, "deps: bound %d dir(s) read-only (%s) — not copied into the jail seed\n", len(depBinds), strings.Join(names, ", "))
	}
```
(Use whatever the file's progress writer is — grep how the `swarm:`/`regions:` readouts print.)

- [ ] **Step 4: Run, verify pass + regen** — `go test ./cmd/corral/ -race` → PASS. Run `scripts/gen-cli-docs.sh` and commit the regenerated CLI reference (a `--check` drift gate exists).

- [ ] **Step 5: Docs**

- `README.md`: a line under `certify --local --repo-dir` — deps are bound read-only (auto-detected `node_modules`/`vendor`/`.venv`/`venv`/`.bundle`; `--bind-dir` for others; `--no-bind-deps` to copy). Deps must be **present** (vendored, like CI); corral binds, never installs.
- `site/src/content/docs/docs/running-it.mdx`: same, plus the honest container caveat — bwrap (default) + macOS bind cleanly; the container backend binds world-readable dep dirs and copies non-world-readable ones (subject to the size cap).
- `ROADMAP.md`: note it under the shipped audit-gate items; the brain-gate path is the remaining follow-up.

- [ ] **Step 6: Gate + commit**

`gofmt -l .` empty; `bash scripts/check-security.sh` OK; `go test ./... -race` green; `scripts/gen-cli-docs.sh --check` clean.
```bash
git add cmd/corral internal README.md ROADMAP.md site docs/cli
git commit -m "feat(certify --local): --bind-dir/--no-bind-deps + auto-bind dep dirs + readout + docs"
```

---

## Self-Review

**Spec coverage:**
- §1 `sandbox.Options.ReadOnlyBinds` + 3 backends → Task 1. §2 adequacy `DepBind` + per-run Target resolution → Task 2 (jail-construction option, simpler than the spec's per-call option — the binds are run-constant, so `advpool` needs NO change, which the spec's §3 allowed as the alternative). §4 `loadRepoFiles` detection + container fallback + flags → Tasks 3 (detection/fallback) + 4 (flags/wiring). Readout, docs → Task 4. Fail-closed (read-only, cap still protects source, `--bind-dir` missing → error, container copy-fallback) → Tasks 1/3. No-regression (nil binds) → every task's empty-binds path. ✅

- **Deviation from spec §3 (noted for the reviewer):** the spec described carrying `ReadOnlyBinds` on `JailScorer/JailValidator/JailEnumerator`. The plan instead sets binds on the **jail at construction** (`NewJail`/`NewEnumerator` option), because the binds are constant for the whole run and all three advpool adapters share the jail instance — so the advpool structs need no field. This is simpler and the spec's §2 explicitly permitted "add a field to `bwrapJail` set at construction." Same behavior; fewer touch-points.

**Placeholder scan:** every code step carries real code or a precise mirror-instruction; test steps carry real assertions. The Task 2 test has a redundant assertion block explicitly flagged to trim to the single `b.Target == got.Workspace+"/node_modules"` check. No TBD/TODO.

**Type consistency:** `sandbox.Bind{Host,Target}` (Task 1) is produced by `adequacy` (Task 2) from `DepBind{Host,Rel}` (Task 2), which `loadRepoFiles` returns (Task 3) and `certify_local` feeds to `NewJail(..., WithReadOnlyBinds(...))` (Task 4). `loadOpts{BackendName,ExtraBindDir,NoBindDeps}` (Task 3) is built by `buildLoadOpts` (Task 4). `depDirNames` set is the verbatim spec set. Names match across tasks.

**Known verification points for the implementer (confirm against the code):** the unexported bwrap/container isolator type names + fields (Task 1 tests); how `jail_test.go` fakes the backend (Task 2); the exact `loadRepoFiles` call-site ordering vs. `iso` resolution (Task 4 — backend must resolve first); the repeatable-string-flag helper (Task 4); the progress writer used for the readout (Task 4).
