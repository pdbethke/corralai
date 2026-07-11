<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Phase 1, Plan 2: the control-spec store + ASVS bundle

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hold the CISO's durable test **goals** in a brain-side, owner-scoped DuckDB store, and seed it from an embedded OWASP **ASVS L1** starter bundle — the dev-untouchable place a goal lives before a test-writer ever binds it to code.

**Architecture:** A new `internal/controlspec` package. A `Goal` is durable declarative intent (id, owner, source-standard+ref, intent text, level, mode=`executable|attested`). The `Store` mirrors `internal/gate.OpenStore` exactly (DuckDB, `CREATE TABLE IF NOT EXISTS`, `INSERT OR REPLACE`, parameterized SQL, caller-stamped clock, opaque MotherDuck-ready dsn). A `Bundle` is a portable catalog of standard requirements → goals; a small ASVS L1 subset ships `//go:embed`'d, and `ImportBundle` creates one owner-scoped goal per requirement. No LLM, no network — pure data.

**Tech Stack:** Go 1.26.5; `github.com/marcboeker/go-duckdb/v2` (blank import, as `internal/gate`/`internal/buildstore` do); `//go:embed`.

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (`export PATH="$PATH:$HOME/go/bin"`) all green.
- **Deterministic**: no LLM, no network, **no `time.Now()` inside the store** — `CreatedTS` is caller-stamped (mirror `gate.Store.Save`). Same input → same output.
- **Owner-scoped**: every read/write is keyed by the owning CISO principal; one owner's goals never leak into another's list. (The AUTH gate that restricts *who* may write — making it dev-untouchable in practice — is Plan 3's wiring; this plan makes the store owner-scoped so that gate has something to enforce.)
- Mirror `internal/gate/store.go` for the DuckDB idiom — do not invent a different connection/table pattern.
- The embedded ASVS intent text is a **faithful paraphrase** of ASVS 4.0.3 L1; add a comment noting it must be verified against the published ASVS text before shipping a real bundle. corral metaphor.

## File Structure
- `internal/controlspec/types.go` — `Goal`, `Bundle`, `Requirement`. (new)
- `internal/controlspec/store.go` — DuckDB `control_goals` store: `OpenStore`/`SaveGoal`/`ListGoals`/`GetGoal`/`Close`. (new)
- `internal/controlspec/bundle.go` — `//go:embed` the ASVS subset; `LoadBundle`/`ImportBundle`. (new)
- `internal/controlspec/bundles/asvs-l1.json` — the embedded ASVS L1 starter subset. (new)
- `internal/controlspec/store_test.go`, `internal/controlspec/bundle_test.go` — tests. (new)

## Interfaces (produced — later plans consume these)
```go
type Goal struct {
    ID        string    // stable id, e.g. "asvs-v2.1.1" (bundle) or a caller slug (custom)
    Owner     string    // the CISO principal who owns this goal
    Standard  string    // "OWASP ASVS 4.0.3" — "" for a custom goal
    Ref       string    // requirement ref within the standard, e.g. "V2.1.1" — "" for custom
    Intent    string    // the durable, declarative intent (the GOAL text)
    Level     string    // "L1" | "L2" | "L3" | "high" | ...
    Mode      string    // "executable" | "attested"  (the honest execution-vs-attested seam)
    CreatedTS time.Time // caller-stamped (store never calls time.Now())
}
type Requirement struct {
    Ref    string `json:"ref"`
    Level  string `json:"level"`
    Intent string `json:"intent"`
    Mode   string `json:"mode"`
}
type Bundle struct {
    Standard     string        `json:"standard"` // "OWASP ASVS"
    Version      string        `json:"version"`  // "4.0.3"
    Requirements []Requirement `json:"requirements"`
}
func OpenStore(dsn string) (*Store, error)
func (*Store) SaveGoal(Goal) error
func (*Store) ListGoals(owner string) ([]Goal, error)
func (*Store) GetGoal(owner, id string) (Goal, bool, error)
func (*Store) Close() error
func LoadBundle(name string) (Bundle, error)   // name e.g. "asvs-l1"
func ImportBundle(s *Store, owner string, b Bundle, now time.Time) (int, error)
```

---

## Task 1: the control-spec store

**Files:**
- Create: `internal/controlspec/types.go`, `internal/controlspec/store.go`
- Test: `internal/controlspec/store_test.go`

**Interfaces:**
- Produces: `Goal`, `OpenStore`, `SaveGoal`, `ListGoals`, `GetGoal`, `Close` (above).
- Consumes: the DuckDB idiom from `internal/gate/store.go` (read it and mirror it).

- [ ] **Step 1: Failing test — save, get, list, and owner isolation.**
```go
func TestControlSpecStore(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()

	g := Goal{ID: "asvs-v2.1.1", Owner: "ciso@bankz", Standard: "OWASP ASVS 4.0.3", Ref: "V2.1.1",
		Intent: "user-set passwords are at least 12 characters", Level: "L1", Mode: "executable", CreatedTS: now}
	if err := s.SaveGoal(g); err != nil { t.Fatal(err) }

	got, ok, err := s.GetGoal("ciso@bankz", "asvs-v2.1.1")
	if err != nil || !ok { t.Fatalf("get: ok=%v err=%v", ok, err) }
	if got != g { t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, g) }

	// A different owner cannot see it (owner isolation).
	if _, ok, _ := s.GetGoal("dev@bankz", "asvs-v2.1.1"); ok {
		t.Fatal("goal leaked across owners")
	}
	// Save a second owner's goal; each list is owner-scoped.
	if err := s.SaveGoal(Goal{ID: "custom-1", Owner: "dev@bankz", Intent: "x", Mode: "attested", CreatedTS: now}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListGoals("ciso@bankz")
	if err != nil { t.Fatal(err) }
	if len(list) != 1 || list[0].ID != "asvs-v2.1.1" {
		t.Fatalf("ListGoals(ciso) = %+v, want just asvs-v2.1.1", list)
	}
}
```

- [ ] **Step 2: Run it, watch it fail** (`OpenStore`/`Goal` undefined).

- [ ] **Step 3: Implement `types.go` (the `Goal` struct) and `store.go`.** Mirror `internal/gate/store.go`:
```go
// store.go — mirror gate/store.go's DuckDB idiom.
func OpenStore(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("controlspec: open: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS control_goals (
		owner VARCHAR NOT NULL, id VARCHAR NOT NULL,
		standard VARCHAR, ref VARCHAR, intent VARCHAR NOT NULL,
		level VARCHAR, mode VARCHAR NOT NULL, created_ts TIMESTAMP NOT NULL,
		PRIMARY KEY (owner, id)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("controlspec: creating control_goals table: %w", err)
	}
	return &Store{db: db}, nil
}
```
`SaveGoal` = `INSERT OR REPLACE` on all 8 columns; `GetGoal(owner, id)` selects `WHERE owner=? AND id=?`, `sql.ErrNoRows → (Goal{}, false, nil)`, reconstructs the `Goal` (set `Owner`/`ID` from the args, scan the rest); `ListGoals(owner)` selects `WHERE owner=? ORDER BY id` → `[]Goal`. Every value bound via `?`. `CreatedTS` persisted as given — no `time.Now()`.

- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS. **Commit:** `feat(controlspec): owner-scoped DuckDB store for CISO test goals`.

---

## Task 2: the ASVS bundle — embed, load, import

**Files:**
- Create: `internal/controlspec/bundle.go`, `internal/controlspec/bundles/asvs-l1.json`
- Test: `internal/controlspec/bundle_test.go`

**Interfaces:**
- Produces: `Requirement`, `Bundle`, `LoadBundle`, `ImportBundle`.
- Consumes: Task 1's `Store`/`SaveGoal`/`Goal`.

- [ ] **Step 1: Create the embedded starter bundle** `internal/controlspec/bundles/asvs-l1.json` (a faithful paraphrase of OWASP ASVS 4.0.3 L1 — verify against the published text before shipping a real bundle):
```json
{
  "standard": "OWASP ASVS",
  "version": "4.0.3",
  "requirements": [
    {"ref": "V2.1.1", "level": "L1", "mode": "executable", "intent": "User-set passwords are at least 12 characters in length."},
    {"ref": "V3.3.1", "level": "L1", "mode": "executable", "intent": "Logout and session expiration invalidate the session token."},
    {"ref": "V4.1.1", "level": "L1", "mode": "attested",   "intent": "The application enforces access-control rules at a trusted service layer, not only in client-side code."},
    {"ref": "V7.1.1", "level": "L1", "mode": "executable", "intent": "The application does not log credentials, session tokens, or payment details."},
    {"ref": "V8.3.4", "level": "L1", "mode": "attested",   "intent": "Sensitive data is not sent in URL query strings; it is carried in the request body or headers."},
    {"ref": "V14.4.1","level": "L1", "mode": "executable", "intent": "Every HTTP response sets a Content-Type header with a safe, explicit character set."}
  ]
}
```

- [ ] **Step 2: Failing test — load the bundle and import it as owner-scoped goals.**
```go
func TestLoadAndImportASVS(t *testing.T) {
	b, err := LoadBundle("asvs-l1")
	if err != nil { t.Fatal(err) }
	if b.Standard != "OWASP ASVS" || b.Version != "4.0.3" || len(b.Requirements) < 5 {
		t.Fatalf("bundle looks wrong: %+v", b)
	}

	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()

	n, err := ImportBundle(s, "ciso@bankz", b, now)
	if err != nil { t.Fatal(err) }
	if n != len(b.Requirements) { t.Fatalf("imported %d, want %d", n, len(b.Requirements)) }

	g, ok, err := s.GetGoal("ciso@bankz", "asvs-v2.1.1")
	if err != nil || !ok { t.Fatalf("V2.1.1 goal missing: ok=%v err=%v", ok, err) }
	if g.Standard != "OWASP ASVS 4.0.3" || g.Ref != "V2.1.1" || g.Level != "L1" || g.Mode != "executable" || g.Intent == "" {
		t.Fatalf("imported goal fields wrong: %+v", g)
	}
	// Re-import is idempotent (no duplicates / no error).
	if _, err := ImportBundle(s, "ciso@bankz", b, now); err != nil { t.Fatal(err) }
	list, _ := s.ListGoals("ciso@bankz")
	if len(list) != len(b.Requirements) { t.Fatalf("re-import changed count: %d", len(list)) }
}
```

- [ ] **Step 3: Run, watch fail.** Then implement `bundle.go`:
```go
//go:embed bundles/*.json
var bundleFS embed.FS

// LoadBundle reads an embedded standard bundle by name (e.g. "asvs-l1").
func LoadBundle(name string) (Bundle, error) {
	data, err := bundleFS.ReadFile("bundles/" + name + ".json")
	if err != nil {
		return Bundle{}, fmt.Errorf("controlspec: load bundle %q: %w", name, err)
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("controlspec: parse bundle %q: %w", name, err)
	}
	return b, nil
}

// ImportBundle creates one owner-scoped Goal per requirement (idempotent —
// SaveGoal upserts on (owner,id)). Returns the number of goals written.
func ImportBundle(s *Store, owner string, b Bundle, now time.Time) (int, error) {
	std := strings.TrimSpace(b.Standard + " " + b.Version)
	n := 0
	for _, r := range b.Requirements {
		g := Goal{
			ID:        "asvs-" + strings.ToLower(r.Ref),
			Owner:     owner,
			Standard:  std,
			Ref:       r.Ref,
			Intent:    r.Intent,
			Level:     r.Level,
			Mode:      r.Mode,
			CreatedTS: now,
		}
		if err := s.SaveGoal(g); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
```
(The `asvs-` id prefix is bundle-specific; a later multi-bundle plan can derive the prefix from `b.Standard`. For this single-bundle v1 it's fine — note it in a comment.)

- [ ] **Step 4: Run, watch pass.** `go test ./internal/controlspec/...` → PASS, then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(controlspec): embedded OWASP ASVS L1 bundle + owner-scoped import`.

---

## Self-Review
- **Spec coverage (4b + 4c):** owner-scoped brain-side goal store ✓; goal shape carries the execution-vs-attested `Mode` seam ✓; ASVS L1 starter bundle ships embedded + imports to goals ✓; deterministic, clock-injected ✓; MotherDuck-ready opaque dsn (mirrors gate) ✓. No versioning-delta (Phase 3), no auth-gating (Plan 3), no test generation (spike) — correctly out of scope.
- **No placeholders:** every step has complete code + the exact embedded JSON.
- **Type consistency:** `Goal`/`Bundle`/`Requirement` identical across both tasks; `OpenStore`/`SaveGoal`/`GetGoal`/`ListGoals` signatures stable; `ImportBundle` uses Task 1's `SaveGoal`.
- **Determinism:** `CreatedTS`/`now` injected; no `time.Now()`; `ListGoals` is `ORDER BY id` (stable), not map order.
- **Honesty:** the embedded ASVS intent is a paraphrase, flagged for verification before a real bundle ships; `Mode` distinguishes executable from attested per the spec's honest seam.
