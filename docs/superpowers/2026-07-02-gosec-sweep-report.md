# gosec Sweep Report — hardening/gosec-triage

**Date:** 2026-07-02  
**Branch:** `hardening/gosec-triage`  
**Final gosec `-severity=medium -confidence=medium` issue count: 0**

## Results Summary

| Metric | Count |
|--------|-------|
| nosec annotations applied | 30 (Nosec counter in gosec summary) |
| Permission tightenings (dirs 0o755→0o700) | 11 |
| Permission tightenings (files 0o644→0o600) | 7 |
| nosec-instead-of-tighten (perm-annotate) | 0 |
| Sites STOPPED on (couldn't confirm false-positive) | 0 |

## Part A — HIGH findings (nosec annotations)

All 8 HIGH findings cleared with `// #nosec` annotations:

1. **G702** `cmd/corral-agent/launcher.go:101` — self-re-exec by design
2. **G118** `cmd/corral-agent/main.go:271,549,655` — process-lifetime agent loops; LOW confidence, below gate threshold (these annotations are belt-and-suspenders)
3. **G703** `cmd/corral/main.go:126` — fixed-name systemd credential read
4. **G703** `internal/memory/store.go:750` — server-set path, sanitized slug
5. **G703** `cmd/corral-agent/main.go:765` — workspace-confined write
6. **G115** `internal/attest/attest.go:87` — uint32 conversion guarded by `len(s) > math.MaxUint32`

## Part B — MEDIUM findings

### B1. SQL (G201 ×1, G202 ×11)
All verified before annotating: every concatenated value is either a constant table/column identifier, `lit()`-escaped string, `embed.VecLiteral()` numeric-only vector, or a server-computed integer. No raw user-supplied strings reach any SQL.

Files annotated:
- `internal/memory/store.go` (4 sites)
- `internal/reference/store.go` (3 sites)
- `internal/repoindex/search.go`
- `internal/mission/store.go`
- `internal/coord/store.go`
- `internal/coord/inbox.go`
- `internal/fleet/sync.go` (G201)

**Note on annotation format:** This gosec dev build requires `// #nosec` (with `#`) not `//nosec`. The nosec comment must appear on the **exact line** gosec flags, not the opening line of a multi-line call. Three sites required restructuring to move the comment to the flagged continuation line.

### B2. Subprocess (G204 ×4)
- `internal/repo/repo.go:234` — constant `"git"` binary
- `internal/console/console.go:143` — OS-constant browser launcher
- `internal/sandbox/sandbox.go:75` — sandbox-layer bwrap/binary
- `cmd/corral-agent/launcher.go:101` — self-re-exec (also G702)

### B3. File reads (G304 ×8)
All verified as server-configured paths or workspace-confined (via `filepath.Clean("/"+p)` or `safeJoin`). No agent-supplied unconfined paths found.

### B4. Permissions (G301 ×11, G302 ×2, G306 ×5) — all tightened
- Directories: `0o755` → `0o700` (11 sites)
- Files: `0o644` → `0o600` (7 sites across G302 + G306)
- `go test ./...` passed after all tightenings — no revert needed, no perm-annotate sites.

## Part C — CI Gate

- **Script:** `scripts/check-security.sh` (mirrors `scripts/check-licensing.sh` style)
  - Check 1: `gofmt -l` on all tracked `.go` files
  - Check 2: `gosec -quiet -severity=medium -confidence=medium -fmt=text ./...`
  - Check 3: `govulncheck ./...` (optional, non-fatal if absent)
- **CI wiring:** Added `install gosec` + `bash scripts/check-security.sh` steps to `validate` job in `.github/workflows/deploy.yml`
- **Gate result:** `bash scripts/check-security.sh` → exits 0, prints `OK: all security invariants hold`

## Build / Test / Gate Results

```
go build ./...   → OK (no output)
go vet ./...     → OK (no output)
go test ./...    → all 26 packages pass
gosec -severity=medium -confidence=medium → Issues: 0, Nosec: 30
bash scripts/check-security.sh → OK: all security invariants hold
```

## Sites Stopped On

None. All HIGH and MEDIUM findings were confirmed as either genuine false positives (per-design subprocess exec, lit()-escaped SQL, workspace-confined paths, guarded int conversion) or real permission issues (tightened in-place). No site concatenated a raw un-escaped user value or read an agent-supplied unconfined path.
