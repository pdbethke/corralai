# Brain/Fleet Oracle (free-form NL → guarded text-to-SQL) — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** #20

## Where this fits

#19 made every brain report a curated metadata set up to a shared MotherDuck, so
`fleet_*` tables now span all swarms. This sub-project makes that data **queryable in
free-form natural language**: ask the fleet a question ("which swarm has the most blocked
missions?", "fleet-wide PR throughput this week", "has any swarm already built an X
client?") and get a narrated answer grounded in real rows. It is the "reports" half of the
brain's role (see [[feedback_brain_coordinates_reports]]) made conversational — distinct
from the per-bee "ask a bee" narrator (`internal/ui/ask.go`), which answers about ONE bee's
work; this answers about the whole SYSTEM/fleet.

v1 targets the **fleet (MotherDuck, `md:`)** — cross-swarm. Querying a single swarm's live
local data is a deliberate follow-up.

## First principles

**1. Classification is inherited, not re-litigated.** The oracle queries *only* the curated
`fleet_*` schema in MotherDuck. It therefore *cannot* surface source code, memory bodies,
instruction text, raw command output, or a secret — none of those exist in that schema
(#19 excluded them by construction; `fleet_actions.detail` was dropped entirely). The git
token isn't in any store at all, so no SQL can reach it. The "LLM writes SQL over sensitive
data" risk is **designed out** by the choice of target, not merely guarded.

**2. The brain reports; it never acts.** The oracle is strictly **read-only**. It answers
questions and changes nothing — no writes, no DDL, no side effects, ever.

**3. The fleet is a single trust domain (explicit assumption).** The oracle shows *every*
swarm's metadata to *any* caller — cross-swarm reporting is the point. This is safe only
when all swarms syncing to the same MotherDuck belong to one owner/tenant. If the fleet ever
becomes multi-tenant, this is a cross-tenant leak; **per-tenant/per-swarm row filtering is a
follow-up**, not v1. Stated here so the boundary is a deliberate assumption, not an oversight.

## Architecture — the ask pipeline

```
question (NL)
  → LLM #1: question + fleet_* schema description → a single read-only SELECT
  → VALIDATE: statement is SELECT/WITH only; reject INSERT/UPDATE/DELETE/DDL/ATTACH/COPY/
              PRAGMA/CALL/EXPORT and multi-statement input; enforce a hard row LIMIT
  → EXECUTE: in-memory DuckDB → INSTALL/LOAD motherduck → ATTACH 'md:<db>' (READ_ONLY)
              → run under a context timeout, cap rows read
      (on a SQL execution error → feed the error + the bad SQL back to LLM #1, ≤2 retries)
  → LLM #2: the result table + the question → a natural-language narration
  → Answer{ Narration, SQL, Columns, Rows }   (SQL + rows returned as receipts)
```

Two LLM calls + one MotherDuck query per ask, all bounded. Reuses the in-memory-DuckDB
bridge pattern that `internal/fleet.Sync` already uses (INSTALL/LOAD motherduck, ATTACH),
but ATTACHes `md:` **read-only** and runs a SELECT instead of an INSERT.

## Components

### 1. `internal/oracle` (new) — the engine

- `type Answer struct{ Narration, SQL string; Columns []string; Rows [][]string }`
- `type Client struct{ … }` holding: the `md:` target + token (for the ATTACH), an LLM
  client (`internal/llm` — the same backend the "ask a bee" narrator uses), and the
  guardrail limits (row cap, timeout, retry count).
- `func New(mdTarget, mdToken string, llm LLM, opts Options) *Client` — nil / disabled when
  `mdTarget == ""` or `llm == nil` (graceful, like the reference/RAG gating).
- `func (c *Client) Ask(ctx context.Context, question string) (Answer, error)` — the pipeline
  above.
- Internal: `generateSQL(ctx, question, schema, priorErr)` (LLM #1, returns one SELECT),
  `validateSelect(sql) error` (the SELECT-only gate — see Guardrails), `run(ctx, sql)
  (cols, rows, error)` (the guarded DuckDB execution), `narrate(ctx, question, cols, rows)`
  (LLM #2).
- **Schema source of truth:** the `fleet_*` schema handed to LLM #1 comes from a shared
  `fleet.ReportingSchema() string` accessor (a new export on `internal/fleet` derived from
  its `tableSpecs`/`createDDL`), so the oracle's schema knowledge can never drift from what
  #19 actually syncs. DRY: one definition of "what's reportable."
- **Current-state views (correctness + N+1 avoidance).** `fleet_missions` is **append-
  temporal** in #19 (a new row per `updated_ts` change), so "current mission status" is a
  per-`(brain,id)` latest-version pick. If the LLM queried the raw table naively it would
  (a) return *every historical version* (wrong answers) and (b) tend to write a correlated-
  subquery (`WHERE updated_ts=(SELECT max…)`) — an O(N²)/N+1 scan that does not scale on a
  growing fleet table. So the oracle materializes **current-state views** once per ask over
  the attached remote — e.g.
  `CREATE TEMP VIEW fleet_missions_current AS SELECT * FROM remote.fleet_missions
   QUALIFY row_number() OVER (PARTITION BY brain,id ORDER BY updated_ts DESC)=1` —
  and the schema description handed to LLM #1 presents THOSE views (with a one-line note that
  they are current-state, deduped) plus the append tables where history is wanted
  (`fleet_actions`/`fleet_tasks`/`fleet_telemetry` are naturally append-only streams, no
  dedup needed). Views also narrow the LLM's query surface — a security nicety. The view SQL
  is generated by the oracle (trusted), not the LLM.

`LLM` is a tiny local interface (`Complete(ctx, system, user string) (string, error)`) so the
oracle depends on an abstraction, not the concrete client — and tests inject a fake.

### 2. `internal/brain` — the `ask_fleet` MCP tool

- `ask_fleet{question}` → `oracle.Ask` → `{narration, sql, columns, rows}`.
- Registered only when the oracle is configured (md: + a model backend present).
- **Per-principal rate limit** (a small in-memory token-bucket keyed on the caller identity,
  e.g. N asks/min) — because a *bee* can call this, and each ask costs two LLM calls + a
  MotherDuck query. Over the limit → a clear "rate limit, try again shortly" error, no query
  run. This is the same governance stance as spawn/review.

### 3. `internal/ui` — the panel

- A panel (sibling to the "ask a bee" box) + `/api/ask_fleet` endpoint calling the SAME
  `oracle.Client`; renders the narration prominently + the result table (columns/rows) + the
  generated SQL (collapsible "show query"). Disabled with a clear message when the oracle is
  unconfigured.

### 4. `cmd/corral` — wiring

- Construct one `oracle.Client` from the existing `CORRALAI_MOTHERDUCK` / `CORRALAI_MOTHERDUCK_TOKEN`
  and the brain's LLM client; pass it to both the brain server (for `ask_fleet`) and the UI
  server (for the panel). Log "fleet oracle enabled/disabled".

## Execution sandbox — the definitive lockdown (from the security red-team)

This section is authoritative; it corrects two things a naive design gets wrong. **`SET
enable_external_access=false` is unusable here** — it is boot-time-only (throws if set after
attach) AND it blocks `LOAD motherduck`/`ATTACH 'md:'` if set at open time, so it is mutually
exclusive with a live `md:` connection. And **`disabled_functions` is not a real DuckDB
setting.** The real containment is `disabled_filesystems` + extension-autoload-off +
`lock_configuration` + **process-env isolation**, on a connection dedicated to the oracle.

**The oracle's DuckDB connection is separate from the `fleet.Sync` connection** (that one
loads the `sqlite` extension and attaches the local stores — pre-attaching exactly the DBs we
must never expose). The oracle connection loads **only** `motherduck`, attaches **only** `md:`,
and applies, in this order, before any untrusted SQL runs:

```
INSTALL motherduck; LOAD motherduck;              -- only this extension; never sqlite/httpfs
ATTACH 'md:<db>' AS remote (READ_ONLY);           -- the sole schema the query can see
SET disabled_filesystems = 'LocalFileSystem';     -- kills read_csv/read_text/read_blob/glob/
                                                  --   local ATTACH/COPY; md: survives (own transport)
SET autoinstall_known_extensions = false;
SET autoload_known_extensions    = false;         -- kills httpfs-autoload → no URL fetch / SSRF
SET allow_community_extensions   = false;
SET memory_limit = '512MB'; SET threads = 2; SET max_expression_depth = 100;  -- resource caps
SET lock_configuration = true;                    -- freeze: no SET can run after this
```
(All runtime-settable in DuckDB 1.4.1 = go-duckdb/v2 v2.4.3. The current-state views §Components-1
are created BEFORE the lock.)

**The `getenv` residual — the one that touches the credential boundary.** DuckDB's `getenv()`
runs inside a SELECT, and no config disables a function (the only gate is the unusable
`enable_external_access`). The oracle runs in the brain process, whose env holds
`CORRALAI_GIT_TOKEN` — so `SELECT getenv('CORRALAI_GIT_TOKEN')` would exfiltrate the PAT the
whole system exists to contain. Mandatory mitigation, both:
1. **The git PAT must not be in the environment the oracle's DuckDB can read — via a
   process-env scrub (chosen mechanism).** `cmd/corral` reads `CORRALAI_GIT_TOKEN` (and every
   other high-value secret) once into its owning struct (`repo.Engine.token`, …) at startup,
   then immediately `os.Unsetenv`s it from the process environment. After that, NO in-process
   env-reader — DuckDB `getenv`, an accidental log of `os.Environ`, a subprocess inheriting env
   — can retrieve the PAT, because it is no longer there; the value lives only in its owning
   struct field. This hardens the #15 credential boundary generally, not just the oracle.
   - **Required audit (part of the work):** confirm nothing re-reads a scrubbed var from the
     env after startup. Known reader to reconcile: the fleet-sync + the oracle both need the
     lowercase `motherduck_token` for the `md:` attach, so **that one stays** — it is the
     lower-value *reporting-DB* credential (it grants only the same curated `fleet_*` metadata
     the oracle already exposes; no escalation), never the git PAT. Every other secret
     (`CORRALAI_GIT_TOKEN`, `CORRALAI_MOTHERDUCK_TOKEN` uppercase, DB URLs, etc.) is scrubbed.
   - A dedicated-subprocess isolation is the documented fallback *only* if the audit surfaces a
     re-reader that can't be eliminated; not planned for v1.
2. **Probe `getenv` at oracle startup** (`SELECT getenv('PATH')`): if it errors "unavailable in
   this client," env-isolation is belt-and-suspenders; if it returns a value, env-isolation is
   **required** and must be verified by the git-token canary test (below).

Read-only is thus enforced by the connection config (primary) and by:
- **`validateSelect` (defense-in-depth ONLY, never the wall):** normalize the SQL (strip `--`
  and `/* */` comments, collapse whitespace) then require a single statement starting with
  `SELECT`/`WITH`, reject an inner `;`, and reject the normalized substrings `attach copy
  install load pragma "set " call export read_ glob( sqlite_ getenv parquet_scan`. Better,
  wrap the user SQL as a subquery the oracle controls: `SELECT * FROM (<user>) LIMIT 1000`.
  The red-team is explicit: obfuscation defeats substring checks, so safety must NOT rest here.

**Metadata info-leak (accepted, low severity):** `duckdb_functions()/settings()`, `pragma_*`
table-function forms, and md: catalog reads run regardless and reveal the config + `fleet_*`
catalog — no file content. The validator rejects `duckdb_`/`pragma_` prefixes; the residual
(catalog names) is inherent and harmless (only `fleet_*` are attached). `duckdb_secrets()`
redacts values.

## Governance (the DoS lens)

Cost bounds:
- **Hard row `LIMIT`** injected/enforced on the returned result (default 1000) so a result
  can't be unbounded.
- **Narration input cap (distinct from the result cap).** LLM #2 must NOT be fed the full
  result — 1000 rows would blow its context and cost. It receives at most a **top-K slice
  (default 50 rows) + the total row count + column names**; when the result exceeds K, the
  narration is told "showing the first K of N" so it summarizes rather than enumerates. The
  caller still gets the full (capped-at-1000) table; only the *narrator's* input is the small
  slice.
- **Statement timeout** via the context deadline (default a few seconds) — a slow/expensive
  query is cancelled.
- **Per-principal rate limit** on the MCP tool (default N/min) — bounds LLM + MotherDuck spend
  under a misbehaving or looping caller. This is the dominant cost governor (each ask = two
  LLM calls + one MotherDuck query, both billed).
- **Bounded NL→SQL retry** (≤2) — a query that errors is retried with the error fed back, then
  gives up honestly.

The classification inheritance (First principle 1) means even a maximally-clever generated
query can only ever read curated metadata — there is no sensitive column to exfiltrate.

## Error handling / edge cases

- **No model backend or no `md:`** → oracle disabled; the tool/panel return a clear
  "fleet oracle unavailable (configure CORRALAI_MOTHERDUCK + a model backend)" — graceful,
  never a crash.
- **Generated SQL invalid after retries** → return the error + the last attempted SQL; never
  fabricate an answer.
- **Non-SELECT generated** → rejected by `validateSelect`; surfaced as "the oracle only runs
  read queries" (and retried once in case the LLM drifted).
- **MotherDuck unreachable / bad token** → clear error, no crash, no partial answer.
- **Empty result set** → LLM #2 narrates "nothing in the fleet matches that," not an error.
- **Rate-limited** → clear message, no LLM/DB work performed.
- **Huge/hostile question** → the question is bounded in length before it reaches the LLM.

## Testing

- **Adversarial exfiltration matrix (the load-bearing security tests).** Because this is
  open-source and adopters WILL point their own agents at it to find holes, containment must
  be **proven by executable red-team tests, not prose.** Each test drives the exact malicious
  SQL through the REAL locked execution path and asserts it is **blocked** — errors or returns
  nothing — and, via a **canary technique**, that the target's contents never appear in a
  result. The matrix (from the red-team; each row is a required test):
  - **File read** — `read_text`/`read_csv`/`read_parquet`/`read_json`/`read_blob('<canary file>')`:
    seed a temp file with a unique canary string; assert the canary never surfaces (blocked by
    `disabled_filesystems='LocalFileSystem'`).
  - **Filesystem enumeration** — `glob('/...')` / `read_text(glob(...))` → blocked.
  - **Local DB attach/scan** — an embedded `ATTACH '<local .duckdb>'` and `sqlite_scan('<coord.sqlite>',…)`
    → blocked (validator + no-sqlite-loaded + DFS).
  - **URL / SSRF** — `read_csv('https://…')`, `ATTACH 'https://…'` → blocked (autoload-off).
  - **`COPY … TO/FROM`** → rejected (statement, not SELECT).
  - **`getenv` git-token canary (the credential-boundary test):** set a canary secret in the
    oracle's execution env, run `SELECT getenv('<that var>')`, and assert the canary is NOT in
    the result — AND assert `CORRALAI_GIT_TOKEN` is not present in the oracle's execution env at
    all. This proves the PAT can't be read even if `getenv` is live.
  - **Validator obfuscation** — `SEL/**/ECT`, `read_/**/text(...)`, `--`-hidden second
    statement, `;`-stacked `SELECT 1; ATTACH …`, `pragma_table_info(...)` function form →
    rejected after normalization.
  These are the executable proof that "the oracle can only read curated fleet metadata." They
  must exist and pass; a reviewer's agent running `go test ./internal/oracle/` sees them green.
  (The plan enumerates the full vector table and the exact canary assertions.)
- **`internal/oracle` functional (fake `LLM` + a local `.duckdb` standing in for `md:`, seeded
  with `fleet_*` rows):**
  - a question → the fake returns a `SELECT` → it executes → the narration reflects the seeded
    rows (end-to-end happy path);
  - `validateSelect` rejects `INSERT`/`DROP`/`ATTACH`/`PRAGMA`/multi-statement (the secondary
    string layer; the primary control is the connection lockdown proven by the matrix above);
  - the **current-state view** returns latest-per-mission, not every version (N+1/correctness);
  - the **narration input cap** slices a large result to top-K for LLM #2 while the caller still
    gets the full capped table;
  - the retry loop: the fake returns a broken SQL once, then a valid one → recovers;
  - the row cap truncates a large result; the context timeout is honored (a deliberately slow
    query is cancelled);
  - disabled (`New` with empty target / nil llm) → `Ask` returns the "unavailable" error.
- **brain (`ask_fleet`):** returns a narration for a seeded remote; unregistered when
  unconfigured; the per-principal rate limit trips after N calls.
- **ui:** `/api/ask_fleet` renders narration + table; disabled message when unconfigured.

## Out of scope (follow-ups)

- **Local (this-swarm) target** — querying a brain's own live stores via an in-memory curated
  view (the `fleet.Sync` ATTACH pattern read locally); v1 is fleet/`md:` only.
- **Any write / action** — the oracle is read-only forever; acting on findings is a separate,
  governed capability (cross-swarm coordination).
- **Learned / dynamic schema** — the schema is the fixed curated `fleet_*` set from #19.
- **Saved queries / scheduled reports / dashboards** — MotherDuck's own web UI already serves
  raw SQL + dashboards; the oracle's unique value is the NL interface.
- **Result visualization** (charts) — text narration + table only for v1.
- **Per-tenant/per-swarm row filtering** — v1 assumes a single-owner fleet (First principle 3);
  multi-tenant isolation is a follow-up.

## Scaling notes (design-time awareness; not v1 work)

- **`fleet_*` retention/compaction.** #19 appends forever (no retention), and `fleet_missions`
  is append-versioned, so the tables grow with time × swarms × status-changes. The current-
  state views scan all versions; the LIMIT+timeout stop a hang, but a legit large aggregate
  slows or times out as history accumulates. **Retention/compaction of the `fleet_*` tables is
  the real long-term scaling lever** — a follow-up that touches #19 (e.g. compact
  `fleet_missions` to latest-per-mission, or a rolling window). Flagged so it's on the radar.
- **`md:` connection reuse.** v1 opens a fresh in-mem DuckDB + ATTACHes `md:` per ask (simple,
  bounded by the rate limit). Attach/auth overhead per query is the first thing to optimize at
  scale — pool/reuse a MotherDuck-attached connection. Deferred; the rate limit keeps v1 cheap.
