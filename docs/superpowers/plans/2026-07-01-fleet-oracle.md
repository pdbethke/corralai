# Brain/Fleet Oracle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ask the fleet a natural-language question and get a narrated answer — the brain's LLM writes a read-only `SELECT` over #19's curated `fleet_*` metadata in MotherDuck, executed in a locked DuckDB sandbox proven safe by an adversarial test matrix, then narrated.

**Architecture:** A security sandbox (`internal/oracle`) is built and proven FIRST (Task 1) — the lockdown + the exfiltration test matrix — before any query logic exists. On it: a schema/views layer (Task 2), the NL→SQL→narrate pipeline (Task 3), a process-env scrub that removes the git PAT from the environment (Task 4), and two front-ends — an `ask_fleet` MCP tool with a per-principal rate limit (Task 5) and a UI panel (Task 6).

**Tech Stack:** Go 1.26; `github.com/marcboeker/go-duckdb/v2 v2.4.3` (DuckDB 1.4.1, CGO); the brain's `internal/llm` client; #19's `internal/fleet`.

## Global Constraints (from the spec — bind every task)

- **The oracle can only read curated `fleet_*` metadata.** Content stores (memory/reference/repoindex), source code, secrets, and the git token are NEVER reachable — enforced by the sandbox, NOT by trusting the LLM.
- **Definitive lockdown (verified for DuckDB 1.4.1):** a dedicated oracle connection that loads ONLY `motherduck`, ATTACHes ONLY `md:` (READ_ONLY), then `SET disabled_filesystems='LocalFileSystem'` + `autoinstall_known_extensions=false` + `autoload_known_extensions=false` + `allow_community_extensions=false` + resource caps + `SET lock_configuration=true`. **`enable_external_access=false` is UNUSABLE (boot-time-only AND blocks `md:`); `disabled_functions` is NOT a real setting** — do not use either.
- **The lockdown `SET`s and the query MUST execute on the SAME connection.** `database/sql` pools; a `SET` on one pooled conn does not protect a query dispatched on another. Pin one `*sql.Conn` (or `db.SetMaxOpenConns(1)` on a per-ask handle) for the whole attach→views→lockdown→query sequence.
- **`getenv` git-token defense = process-env scrub:** `cmd/corral` reads `CORRALAI_GIT_TOKEN` (+ other high-value secrets) into their structs at startup then `os.Unsetenv`s them; the lowercase `motherduck_token` stays (md: needs it; lower-value reporting-DB token). The git PAT lives only in `repo.Engine.token`.
- **Read-only, always.** No writes/DDL/side-effects. `validateSelect` is defense-in-depth (single `SELECT`/`WITH`, reject inner `;`, reject normalized substrings incl. `read_ getenv attach copy pragma_ …`) — NEVER the sole wall.
- **Cost bounds:** result row cap (1000), narration-input cap (top-K=50 for LLM #2, distinct from the result), context-deadline statement timeout, per-principal rate limit on the MCP tool, ≤2 NL→SQL retries.
- **Adversarial exfiltration tests are a hard requirement** (Task 1): each vector driven through the real locked path, asserting the target's canary never surfaces.
- `go build ./...` + `go test ./...` stay green each task; `internal/oracle` is CGO (DuckDB).

---

## File Structure

- `internal/oracle/sandbox.go` (create) — `applyLockdown`, `validateSelect`, `runLocked` (the locked single-conn execution).
- `internal/oracle/sandbox_test.go` (create) — **the adversarial exfiltration matrix** + lockdown behavior.
- `internal/fleet/schema.go` (create) — `ReportingSchema()` + `CurrentStateViews()` exports (schema string + view DDL, derived from `tableSpecs`).
- `internal/fleet/schema_test.go` (create).
- `internal/oracle/oracle.go` (create) — `Client`, `New`, `Ask` (the pipeline), `LLM` interface, prompts.
- `internal/oracle/oracle_test.go` (create) — pipeline with a fake LLM + in-mem `fleet_*`.
- `cmd/corral/main.go` (modify) — process-env scrub; construct the oracle; pass to brain + ui.
- `internal/brain/askfleet.go` (create) + `server.go`/`identity.go` (modify) — `ask_fleet` tool + rate limit + `Options.Oracle`.
- `internal/brain/askfleet_test.go` (create).
- `internal/ui/askfleet.go` (create) + `ui.go` (modify) — panel + `/api/ask_fleet`.

---

## Task 1: `internal/oracle` — the security sandbox (proven first)

**Files:** Create `internal/oracle/sandbox.go`, `internal/oracle/sandbox_test.go`

**Interfaces:**
- Produces:
  - `func applyLockdown(ctx context.Context, conn *sql.Conn) error` — the SET sequence on ONE pinned conn.
  - `func validateSelect(sql string) error` — normalize + structural/substring reject (defense-in-depth).
  - `func runLocked(ctx context.Context, conn *sql.Conn, userSQL string, rowCap int) (cols []string, rows [][]string, err error)` — validate → execute on the locked conn → scan at most `rowCap` rows.

- [ ] **Step 1: Write the failing adversarial + behavior tests**

```go
// internal/oracle/sandbox_test.go
package oracle

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// lockedConn opens a fresh in-mem DuckDB, seeds an in-MEMORY fleet_* table (NOT a file — a
// file-backed remote would be blocked by disabled_filesystems), applies the lockdown, and
// returns a single pinned connection. This mirrors production (where md: is attached before
// the lock) while staying hermetic (no network, no file remote).
func lockedConn(t *testing.T) (*sql.Conn, func()) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// seed an in-memory reporting table (stands in for the md:-attached fleet_missions)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE fleet_missions (brain VARCHAR, id BIGINT, status VARCHAR)`); err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, `INSERT INTO fleet_missions VALUES ('A', 1, 'done'), ('A', 2, 'running')`)
	if err := applyLockdown(ctx, conn); err != nil {
		t.Fatalf("applyLockdown: %v", err)
	}
	return conn, func() { conn.Close(); db.Close() }
}

func TestSandboxBenignQueryWorks(t *testing.T) {
	conn, done := lockedConn(t)
	defer done()
	cols, rows, err := runLocked(context.Background(), conn, `SELECT status, count(*) c FROM fleet_missions GROUP BY status ORDER BY status`, 1000)
	if err != nil {
		t.Fatalf("benign query should work after lockdown: %v", err)
	}
	if len(cols) != 2 || len(rows) != 2 {
		t.Fatalf("expected 2 cols/2 rows, got %v %v", cols, rows)
	}
}

// TestSandboxExfilBlocked is the load-bearing security matrix. Each vector must be blocked:
// the call errors OR returns no rows, and the canary never appears in any cell.
func TestSandboxExfilBlocked(t *testing.T) {
	// a canary file the query will try to read
	dir := t.TempDir()
	canaryFile := filepath.Join(dir, "secret.txt")
	const canary = "CANARY_TOKEN_ghp_do_not_leak"
	os.WriteFile(canaryFile, []byte(canary), 0o600)

	vectors := []struct{ name, sql string }{
		{"read_text", `SELECT * FROM read_text('` + canaryFile + `')`},
		{"read_csv", `SELECT * FROM read_csv('` + canaryFile + `')`},
		{"read_blob", `SELECT * FROM read_blob('` + canaryFile + `')`},
		{"glob", `SELECT * FROM glob('` + dir + `/*')`},
		{"read_json", `SELECT * FROM read_json('` + canaryFile + `')`},
		{"attach_local", `ATTACH '` + canaryFile + `' AS x`},
		{"copy_to", `COPY (SELECT 1) TO '` + filepath.Join(dir, "out.csv") + `'`},
		{"url_read", `SELECT * FROM read_csv('https://example.com/x.csv')`},
		{"second_stmt", `SELECT 1; ATTACH '` + canaryFile + `' AS y`},
		{"obfuscated", `SEL/**/ECT * FROM read_text('` + canaryFile + `')`},
		{"pragma_fn", `SELECT * FROM pragma_table_info('fleet_missions')`},
	}
	conn, done := lockedConn(t)
	defer done()
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			_, rows, err := runLocked(context.Background(), conn, v.sql, 1000)
			if err == nil {
				// if it didn't error, it MUST NOT have returned the canary
				for _, r := range rows {
					for _, cell := range r {
						if strings.Contains(cell, canary) {
							t.Fatalf("VECTOR %s LEAKED the canary: %q", v.name, cell)
						}
					}
				}
			}
		})
	}
}

// TestGetenvCannotReadScrubbedSecret proves the credential-boundary defense at the DuckDB
// level: even if getenv is reachable (it is NOT blocked by disabled_filesystems), an env that
// has been scrubbed of the secret yields nothing. This runs getenv on a RAW conn (bypassing
// validateSelect) precisely to test the ENV defense, not the validator. (validateSelect also
// rejects getenv — that is the separate second layer, covered by TestValidateSelectRejects.)
func TestGetenvCannotReadScrubbedSecret(t *testing.T) {
	const secret = "ghp_MUST_NOT_LEAK_123456"
	os.Setenv("ORACLE_CANARY_SECRET", secret)
	os.Unsetenv("ORACLE_CANARY_SECRET") // the scrub (cmd/corral does this for CORRALAI_GIT_TOKEN)
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got sql.NullString
	// getenv may error "unavailable in this client" (fine) or return NULL/empty (fine) — the
	// ONLY failure is returning the secret.
	if err := db.QueryRow(`SELECT getenv('ORACLE_CANARY_SECRET')`).Scan(&got); err == nil {
		if strings.Contains(got.String, secret) {
			t.Fatalf("getenv leaked a scrubbed secret: %q", got.String)
		}
	}
}

func TestValidateSelectRejects(t *testing.T) {
	bad := []string{
		`INSERT INTO fleet_missions VALUES (1)`,
		`DROP TABLE fleet_missions`,
		`ATTACH 'x' AS y`,
		`SELECT 1; DELETE FROM fleet_missions`,
		`SELECT getenv('CORRALAI_GIT_TOKEN')`,
		`SELECT * FROM read_text('/etc/passwd')`,
		`COPY x TO 'y'`,
		`SET lock_configuration=false`,
		`SEL/**/ECT * FROM read_text('/x')`, // obfuscation defeated by normalization
	}
	for _, s := range bad {
		if err := validateSelect(s); err == nil {
			t.Fatalf("validateSelect should reject: %q", s)
		}
	}
	if err := validateSelect(`WITH t AS (SELECT 1 x) SELECT * FROM t`); err != nil {
		t.Fatalf("a plain CTE SELECT should pass: %v", err)
	}
}
```

> IMPLEMENTER: `runLocked` MUST call `validateSelect` before executing (so the obfuscation + getenv + read_ vectors are rejected at that layer too). The lockdown (`disabled_filesystems` etc.) is the primary wall; `validateSelect` is the second. If any exfil vector's blocking is version-dependent, PROBE it and adjust the lockdown (the red-team flagged `disabled_filesystems='LocalFileSystem'` as the key runtime-settable control; verify `md:`-equivalent in-mem tables still query after it — they do because they're in-memory, no filesystem). If a vector genuinely leaks the canary, that is a BLOCKING failure — escalate.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/oracle/`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/oracle/sandbox.go`**

```go
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// applyLockdown freezes a connection that ALREADY has the reporting schema present so it can
// read only that schema — no local files, no new attach, no URL, no extension autoload. MUST
// be applied to the SAME *sql.Conn the query later runs on (database/sql pools connections).
func applyLockdown(ctx context.Context, conn *sql.Conn) error {
	stmts := []string{
		`SET disabled_filesystems = 'LocalFileSystem'`, // kills read_csv/read_text/read_blob/glob/local ATTACH+COPY; md: survives
		`SET autoinstall_known_extensions = false`,
		`SET autoload_known_extensions = false`,          // kills httpfs autoload → no URL/SSRF
		`SET allow_community_extensions = false`,
		`SET memory_limit = '512MB'`,
		`SET threads = 2`,
		`SET max_expression_depth = 100`,
		`SET lock_configuration = true`, // freeze — nothing above can be undone
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("lockdown %q: %w", s, err)
		}
	}
	return nil
}

var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	wsRun        = regexp.MustCompile(`\s+`)
	// banned normalized substrings (defense-in-depth; the lockdown is the real wall)
	bannedSubstr = []string{
		"attach", "copy", "install ", "load ", "pragma", "set ", "call ", "export",
		"read_", "glob(", "sqlite_", "getenv", "parquet_scan", "duckdb_",
	}
)

// validateSelect is defense-in-depth: normalize (strip comments, collapse whitespace) then
// require a single SELECT/WITH statement and reject the banned substrings. NEVER the sole wall.
func validateSelect(userSQL string) error {
	s := blockComment.ReplaceAllString(userSQL, " ")
	s = lineComment.ReplaceAllString(s, " ")
	s = wsRun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "select ") && !strings.HasPrefix(low, "with ") {
		return fmt.Errorf("only SELECT/WITH queries are allowed")
	}
	// reject any inner ';' (a trailing one is fine)
	if i := strings.IndexByte(strings.TrimRight(s, "; "), ';'); i >= 0 {
		return fmt.Errorf("multiple statements are not allowed")
	}
	for _, b := range bannedSubstr {
		if strings.Contains(low, b) {
			return fmt.Errorf("query uses a disallowed construct (%q)", strings.TrimSpace(b))
		}
	}
	return nil
}

// runLocked validates then executes userSQL on the already-locked conn, scanning at most
// rowCap rows (the read cap is bulletproof regardless of what the query produces).
func runLocked(ctx context.Context, conn *sql.Conn, userSQL string, rowCap int) ([]string, [][]string, error) {
	if err := validateSelect(userSQL); err != nil {
		return nil, nil, err
	}
	rs, err := conn.QueryContext(ctx, userSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	var rows [][]string
	for rs.Next() && len(rows) < rowCap {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = fmt.Sprintf("%v", v)
		}
		rows = append(rows, row)
	}
	return cols, rows, rs.Err()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/oracle/`
Expected: PASS — benign query works after lockdown; **every exfil vector blocked (no canary)**; validateSelect rejects the bad set + passes a plain CTE.

- [ ] **Step 5: Commit**

```bash
git add internal/oracle/sandbox.go internal/oracle/sandbox_test.go
git commit -m "feat(oracle): locked DuckDB execution sandbox + adversarial exfiltration test matrix"
```

---

## Task 2: `internal/fleet` — reporting schema + current-state views (shared source of truth)

**Files:** Create `internal/fleet/schema.go`, `internal/fleet/schema_test.go`

**Interfaces:**
- Consumes: `tableSpecs` (existing, package-internal).
- Produces:
  - `func ReportingSchema() string` — a human-readable description of the queryable schema for the LLM prompt: each `fleet_*` table + its columns, and a note that `fleet_missions_current` is the current-state (deduped) view of `fleet_missions`.
  - `func CurrentStateViews() []string` — the `CREATE TEMP VIEW …` DDL the oracle runs after attaching `md:` (e.g. `fleet_missions_current`).

- [ ] **Step 1: Write the failing test**

```go
// internal/fleet/schema_test.go
package fleet

import "strings"
import "testing"

func TestReportingSchemaListsTables(t *testing.T) {
	s := ReportingSchema()
	for _, want := range []string{"fleet_actions", "fleet_missions", "fleet_tasks", "fleet_telemetry", "fleet_missions_current"} {
		if !strings.Contains(s, want) {
			t.Fatalf("ReportingSchema missing %q", want)
		}
	}
	// it must NOT leak a content/store name — classification stays out of the prompt too
	for _, bad := range []string{"memory", "reference", "repoindex", "detail"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Fatalf("ReportingSchema references banned token %q", bad)
		}
	}
}

func TestCurrentStateViewDedup(t *testing.T) {
	views := CurrentStateViews()
	joined := strings.Join(views, "\n")
	if !strings.Contains(joined, "fleet_missions_current") || !strings.Contains(strings.ToLower(joined), "row_number") {
		t.Fatalf("expected a windowed fleet_missions_current view, got: %s", joined)
	}
}
```

> Also add a functional test (or fold into oracle Task 3): seed a `remote.fleet_missions` (in-mem) with two versions of mission id=1 (updated_ts 1.0 then 2.0), create the view via `CurrentStateViews()`, and assert `SELECT status FROM fleet_missions_current WHERE id=1` returns ONLY the latest — the N+1/correctness proof.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/fleet/ -run 'ReportingSchema|CurrentState'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/fleet/schema.go`**

```go
package fleet

import "strings"

// ReportingSchema describes the queryable fleet reporting schema for the oracle's SQL-
// generating LLM. Derived from tableSpecs so it can't drift from what Sync actually writes.
// Only curated metadata tables/columns appear — no content stores, by construction.
func ReportingSchema() string {
	var b strings.Builder
	b.WriteString("Fleet reporting schema (read-only). Every row has a `brain` column = the swarm that reported it.\n")
	b.WriteString("Use `fleet_missions_current` for current mission state (it is fleet_missions deduped to the latest version per brain+id). fleet_actions/fleet_tasks/fleet_telemetry are append-only streams.\n\n")
	for _, ts := range tableSpecs {
		b.WriteString("TABLE " + ts.remote + " (brain, " + ts.cols + ")\n")
	}
	b.WriteString("VIEW fleet_missions_current — current-state (latest per brain,id) of fleet_missions\n")
	return b.String()
}

// CurrentStateViews are TEMP views the oracle creates (after attaching md:, before the
// lockdown) so the LLM can query current state without an O(N^2) correlated subquery.
func CurrentStateViews() []string {
	return []string{
		`CREATE TEMP VIEW fleet_missions_current AS
		 SELECT * FROM remote.fleet_missions
		 QUALIFY row_number() OVER (PARTITION BY brain, id ORDER BY updated_ts DESC) = 1`,
	}
}
```
(If `ts.cols`/`ts.remote` are unexported fields on `tableSync`, this file is in the same package so it can read them. Verify field names.)

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/fleet/`
Expected: PASS.

```bash
git add internal/fleet/schema.go internal/fleet/schema_test.go
git commit -m "feat(fleet): ReportingSchema + current-state views — shared oracle schema source of truth"
```

---

## Task 3: `internal/oracle` — the Ask pipeline

**Files:** Create `internal/oracle/oracle.go`, `internal/oracle/oracle_test.go`

**Interfaces:**
- Consumes: `applyLockdown`/`validateSelect`/`runLocked` (Task 1); `fleet.ReportingSchema`/`fleet.CurrentStateViews` (Task 2); an `LLM`.
- Produces:
  - `type LLM interface { Ask(ctx context.Context, system, user string) (string, error) }` (`*llm.Client` satisfies it).
  - `type Answer struct{ Narration, SQL string; Columns []string; Rows [][]string }`
  - `type Options struct{ RowCap, NarrateK, MaxRetries int; Timeout time.Duration }` (defaults 1000/50/2/5s).
  - `func New(mdTarget string, llm LLM, opts Options) *Client` — nil-returning / `Enabled()==false` when `mdTarget==""` or `llm==nil`.
  - `func (c *Client) Ask(ctx context.Context, question string) (Answer, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/oracle/oracle_test.go
package oracle
// fakeLLM returns scripted answers per call (call 0 = SQL, call 1 = narration).
// Build a Client whose "connect" is overridden for tests to seed in-mem fleet_missions +
// create the current-state view + applyLockdown (mirroring production connect, minus md:).
//
// Cases:
//  1) happy path: fakeLLM call0 returns "SELECT status, count(*) FROM fleet_missions_current GROUP BY status",
//     call1 returns "2 running, 1 done". Ask() → Answer.SQL == that select, Rows reflect the seed,
//     Narration == call1. (Assert LLM #2 was fed at most NarrateK rows.)
//  2) retry: call0 returns a broken SQL ("SELCT ..."), call1 returns a valid SELECT, call2 narrates →
//     Ask recovers, Answer.SQL == the valid one.
//  3) validateSelect gate: call0 returns "DROP TABLE fleet_missions" → after MaxRetries the
//     non-SELECT is refused; Ask returns an error mentioning read-only (no execution).
//  4) narration cap: seed 200 rows, RowCap=1000, NarrateK=50 → the fake narrator RECORDS how many
//     rows it was given; assert ≤ 50, while Answer.Rows has up to 200 (capped at RowCap).
//  5) disabled: New("", llm) or New("md:x", nil) → Enabled()==false and Ask returns the
//     "fleet oracle unavailable" error.
//
// Implementer: give Client a test seam for the connection setup — e.g. an unexported
// `connect func(ctx) (*sql.Conn, func(), error)` field defaulted to the real md: connect,
// overridden in tests to the in-mem seed. The pipeline (generate→validate→runLocked→narrate)
// is identical in both.
```

> Implementer: the production `connect` opens `sql.Open("duckdb","")` with `SetMaxOpenConns(1)`, `db.Conn(ctx)`, `INSTALL motherduck; LOAD motherduck;`, `ATTACH 'md:<target>' AS remote (READ_ONLY)`, runs `fleet.CurrentStateViews()`, then `applyLockdown(conn)`, and returns the pinned conn + a closer. The test `connect` does the same minus INSTALL/ATTACH: it CREATEs an in-mem `remote`-free `fleet_missions` (or `CREATE SCHEMA remote; CREATE TABLE remote.fleet_missions …`) so `fleet_missions_current` resolves, seeds rows, runs the views, applies the lockdown. Keep the connect seam small.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/oracle/ -run TestOracle`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/oracle/oracle.go`**

```go
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/pdbethke/corralai/internal/fleet"
)

type LLM interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

type Answer struct {
	Narration string     `json:"narration"`
	SQL       string     `json:"sql"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
}

type Options struct {
	RowCap, NarrateK, MaxRetries int
	Timeout                      time.Duration
}

type Client struct {
	target string
	llm    LLM
	opts   Options
	// connect yields a pinned, reporting-schema-loaded, LOCKED connection + a closer.
	connect func(ctx context.Context) (*sql.Conn, func(), error)
}

func New(mdTarget string, llm LLM, opts Options) *Client {
	if opts.RowCap == 0 {
		opts.RowCap = 1000
	}
	if opts.NarrateK == 0 {
		opts.NarrateK = 50
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 2
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	c := &Client{target: mdTarget, llm: llm, opts: opts}
	c.connect = c.connectMotherDuck
	return c
}

func (c *Client) Enabled() bool { return c != nil && c.target != "" && c.llm != nil }

func (c *Client) connectMotherDuck(ctx context.Context) (*sql.Conn, func(), error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1) // pin one conn so lockdown + query share it
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	closer := func() { conn.Close(); db.Close() }
	for _, s := range []string{"INSTALL motherduck; LOAD motherduck;",
		fmt.Sprintf("ATTACH '%s' AS remote (READ_ONLY)", strings.ReplaceAll(c.target, "'", "''"))} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			closer()
			return nil, nil, fmt.Errorf("oracle connect: %w", err)
		}
	}
	for _, v := range fleet.CurrentStateViews() {
		if _, err := conn.ExecContext(ctx, v); err != nil {
			closer()
			return nil, nil, fmt.Errorf("oracle views: %w", err)
		}
	}
	if err := applyLockdown(ctx, conn); err != nil {
		closer()
		return nil, nil, err
	}
	return conn, closer, nil
}

func (c *Client) Ask(ctx context.Context, question string) (Answer, error) {
	if !c.Enabled() {
		return Answer{}, fmt.Errorf("fleet oracle unavailable (configure CORRALAI_MOTHERDUCK + a model backend)")
	}
	if len(question) > 4000 {
		question = question[:4000]
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	conn, closer, err := c.connect(ctx)
	if err != nil {
		return Answer{}, err
	}
	defer closer()

	schema := fleet.ReportingSchema()
	var lastErr string
	var sqlText string
	var cols []string
	var rows [][]string
	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		sqlText, err = c.generateSQL(ctx, question, schema, lastErr)
		if err != nil {
			return Answer{}, err
		}
		if verr := validateSelect(sqlText); verr != nil {
			lastErr = verr.Error()
			continue
		}
		cols, rows, err = runLocked(ctx, conn, sqlText, c.opts.RowCap)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		lastErr = ""
		break
	}
	if lastErr != "" {
		return Answer{SQL: sqlText}, fmt.Errorf("could not produce a valid query: %s", lastErr)
	}
	narration, _ := c.narrate(ctx, question, cols, rows)
	return Answer{Narration: narration, SQL: sqlText, Columns: cols, Rows: rows}, nil
}

func (c *Client) generateSQL(ctx context.Context, question, schema, priorErr string) (string, error) {
	system := "You translate a question into ONE read-only DuckDB SELECT over the given fleet reporting schema. " +
		"Output ONLY the SQL, no prose, no backticks. Use fleet_missions_current for current mission state. " +
		"Never write anything but a single SELECT/WITH."
	user := "Schema:\n" + schema + "\n\nQuestion: " + question
	if priorErr != "" {
		user += "\n\nYour previous query failed with: " + priorErr + "\nFix it and output only the corrected SELECT."
	}
	out, err := c.llm.Ask(ctx, system, user)
	if err != nil {
		return "", err
	}
	return cleanSQL(out), nil
}

// cleanSQL strips ```sql fences / stray backticks the model may add.
func cleanSQL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```sql")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func (c *Client) narrate(ctx context.Context, question string, cols []string, rows [][]string) (string, error) {
	k := c.opts.NarrateK
	shown := rows
	note := ""
	if len(rows) > k {
		shown = rows[:k]
		note = fmt.Sprintf(" (showing the first %d of %d rows)", k, len(rows))
	}
	var b strings.Builder
	b.WriteString(strings.Join(cols, " | ") + "\n")
	for _, r := range shown {
		b.WriteString(strings.Join(r, " | ") + "\n")
	}
	system := "You are the fleet's analyst. Answer the question in 1-3 sentences from the result rows. " +
		"Be precise; if the result is empty, say nothing in the fleet matches."
	user := "Question: " + question + note + "\n\nResult:\n" + b.String()
	return c.llm.Ask(ctx, system, user)
}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/oracle/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/oracle/oracle.go internal/oracle/oracle_test.go
git commit -m "feat(oracle): NL→SQL→narrate pipeline over the fleet schema (retry, narration cap, disabled-graceful)"
```

---

## Task 4: `cmd/corral` — process-env scrub + oracle construction

**Files:** Modify `cmd/corral/main.go`

**Interfaces:**
- Consumes: `oracle.New`; the existing `llm.Client` (the narrator), `CORRALAI_MOTHERDUCK`.

- [ ] **Step 1: The env scrub (security) + a test asserting it**

After `cmd/corral` reads `CORRALAI_GIT_TOKEN` into the repo engine (the #15 wiring, `tok := os.Getenv("CORRALAI_GIT_TOKEN")` → `repo.New(tok, …)`), immediately scrub it (and the uppercase MotherDuck var + any other high-value secret) from the process env — keep the lowercase `motherduck_token` (md: needs it):
```go
	// Security: once secrets are loaded into their owning structs, remove them from the
	// process environment so no in-process reader (DuckDB getenv in the fleet oracle, an
	// os.Environ dump, a subprocess) can retrieve them. The git PAT now lives only in
	// repo.Engine. (motherduck_token stays — md: needs it; it is the lower-value reporting
	// credential, never the git PAT.)
	for _, k := range []string{"CORRALAI_GIT_TOKEN", "CORRALAI_MOTHERDUCK_TOKEN", "CORRALAI_DELEGATION_SECRET"} {
		os.Unsetenv(k)
	}
```
Place it AFTER every consumer of those vars has read them (audit the file: `CORRALAI_GIT_TOKEN` → repo engine; `CORRALAI_MOTHERDUCK_TOKEN` → copied to `motherduck_token` at main.go:209-210; `CORRALAI_DELEGATION_SECRET` → its consumer at ~main.go:111). Scrub each only after its read.

Add a small test (in a `cmd/corral` `_test.go`, or an exported helper you can test): a `scrubSecrets(keys []string)` function that unsets the keys; assert that after calling it with a set var, `os.Getenv` returns "". (Keep the token-value read into the struct BEFORE the scrub — the test just verifies the unset.)

- [ ] **Step 2: Construct the oracle + pass it on**

Where the fleet-sync target is read (`CORRALAI_MOTHERDUCK`), also build the oracle when a model backend is available:
```go
	var fleetOracle *oracle.Client
	if mdTarget := os.Getenv("CORRALAI_MOTHERDUCK"); mdTarget != "" && narrator.Available() {
		fleetOracle = oracle.New(mdTarget, narrator, oracle.Options{})
		log.Printf("fleet oracle enabled (ask_fleet + UI panel)")
	} else {
		log.Printf("fleet oracle disabled (needs CORRALAI_MOTHERDUCK + a model backend)")
	}
```
Pass `fleetOracle` into `brain.Options{ … Oracle: fleetOracle }` (Task 5) and `ui.Deps{ … Oracle: fleetOracle }` (Task 6). (`narrator` is the existing `*llm.Client` the brain builds for the ask-a-bee feature — reuse it; confirm the variable name.)

- [ ] **Step 3: Build + commit**

Run: `go build ./... && go test ./...`
Expected: build OK; tests PASS.

```bash
git add cmd/corral/main.go
git commit -m "feat(corral): scrub git PAT from process env (getenv defense) + construct the fleet oracle"
```

---

## Task 5: `internal/brain` — `ask_fleet` tool + per-principal rate limit

**Files:** Create `internal/brain/askfleet.go`, `internal/brain/askfleet_test.go`; Modify `internal/brain/identity.go`, `internal/brain/server.go`

**Interfaces:**
- Consumes: `oracle.Client`; `identity(req, name)`.
- Produces: `Options.Oracle *oracle.Client`; tool `ask_fleet{name, question}` → `oracle.Answer`.

- [ ] **Step 1: Failing test**

```go
// internal/brain/askfleet_test.go — build a brain with Options{Oracle: <oracle with a fake LLM
// + in-mem fleet seed>}. A caller asks ask_fleet{question:"how many missions are done?"} →
// gets a narration + rows. Then assert: calling it > the rate limit (default N/min) for the same
// principal returns a "rate limit" error without running the oracle. And that ask_fleet is
// unregistered when Options.Oracle is nil.
// Implementer: use the in-package MCP harness; construct the oracle with a fake LLM (Task 3's
// fake) and the in-mem connect seam so no md:/network is needed.
```

- [ ] **Step 2: Run red / Step 3: Implement**

Add `Oracle *oracle.Client` to `Options` (identity.go). Create `askfleet.go`:
```go
package brain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/oracle"
)

type askFleetIn struct {
	Name     string `json:"name"`
	Question string `json:"question"`
}

// a tiny per-principal fixed-window rate limiter
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}
func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-r.window)
	kept := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

func registerAskFleet(s *mcp.Server, opts Options) {
	rl := newRateLimiter(10, time.Minute) // 10 asks/min/principal
	mcp.AddTool(s, &mcp.Tool{Name: "ask_fleet",
		Description: "Ask a natural-language question about the whole fleet's state (missions, tasks, telemetry across all swarms). Read-only; returns a narrated answer + the rows."},
		func(ctx context.Context, req *mcp.CallToolRequest, in askFleetIn) (*mcp.CallToolResult, oracle.Answer, error) {
			who := identity(req, in.Name)
			if !rl.allow(who, timeNow()) { // use the brain's now() helper if present, else time.Now()
				return nil, oracle.Answer{}, fmt.Errorf("rate limit: max 10 fleet questions/minute — try again shortly")
			}
			ans, err := opts.Oracle.Ask(ctx, in.Question)
			if err != nil {
				return nil, oracle.Answer{}, err
			}
			return nil, ans, nil
		})
}
```
Register in `server.go` when configured:
```go
	if opts.Oracle != nil && opts.Oracle.Enabled() {
		registerAskFleet(s, opts)
	}
```
(Use the real `now()` helper if the brain has one, else `time.Now()`.)

- [ ] **Step 4: Run + commit**

Run: `go test ./internal/brain/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/brain/askfleet.go internal/brain/askfleet_test.go internal/brain/identity.go internal/brain/server.go
git commit -m "feat(brain): ask_fleet MCP tool + per-principal rate limit"
```

---

## Task 6: `internal/ui` — the fleet-oracle panel

**Files:** Create `internal/ui/askfleet.go`; Modify `internal/ui/ui.go`

**Interfaces:**
- Consumes: `oracle.Client` (added to `ui.Deps`/`Server`).

- [ ] **Step 1: Implement the endpoint + panel**

Add `Oracle *oracle.Client` to `ui.Deps` and the `Server` struct (mirror how `narrator` is threaded). Create `internal/ui/askfleet.go` with an `/api/ask_fleet` handler that reads `{question}`, calls `s.oracle.Ask(ctx, question)`, and returns the `Answer` as JSON (narration + columns + rows + sql); returns 503 with a clear message when `s.oracle == nil || !s.oracle.Enabled()`. Register `mux.HandleFunc("/api/ask_fleet", s.askFleet)` in `ui.go` (next to `/api/ask`). Add a small panel to the UI HTML (sibling to the "ask a bee" box): a question input, a "narration" area, a collapsible result table, and a collapsible "show SQL". Keep it consistent with the existing ask box's markup/JS.

- [ ] **Step 2: A handler test**

A test hitting `/api/ask_fleet` with an oracle wired to a fake LLM + in-mem seed → 200 with a narration; with `Oracle=nil` → 503. (Use `httptest` + the existing ui test patterns.)

- [ ] **Step 3: Build + commit**

Run: `go build ./... && go test ./...`
Expected: build OK; all PASS.

```bash
git add internal/ui/askfleet.go internal/ui/ui.go internal/ui/web/index.html
git commit -m "feat(ui): fleet-oracle panel + /api/ask_fleet"
```

---

## Final verification

- [ ] `go build ./...` — OK; `go test ./...` — all PASS.
- [ ] **Security (the load-bearing check):** `go test ./internal/oracle/` — the adversarial matrix is green (every exfil vector blocked, no canary leaks); the git PAT is scrubbed from the process env (Task 4 test); `validateSelect` rejects the bad set.
- [ ] The lockdown SET sequence + the query run on ONE pinned `*sql.Conn` (grep: `SetMaxOpenConns(1)` on the oracle handle; the conn is threaded, not re-fetched).
- [ ] Governance: `ask_fleet` rate-limits per principal; the row cap + narration cap + timeout are enforced.
- [ ] Graceful: no md: / no model backend → oracle disabled, tool/panel return a clear message.
