// SPDX-License-Identifier: Elastic-2.0

// Package fleet replicates the brain's action stream to a remote DuckDB /
// MotherDuck database so the whole agent fleet — local and across machines — can
// be observed analytically (e.g. any BI dashboard on MotherDuck).
//
// This is the "observe analytically" half of the design: coordination stays
// transactional in SQLite; the *record* of what agents did is appended to
// MotherDuck. Never the live locks — only the audit/action stream.
//
// An in-memory DuckDB bridges the two: it ATTACHes the configured source stores
// (read-only) and the remote (a local .duckdb in tests, or `md:<db>` in prod),
// then incrementally INSERTs new metadata rows. Rows are tagged with brainID so
// multiple brains can federate into one remote.
//
// CLASSIFICATION: only curated, metadata-only tables cross the boundary.
// Content stores (memory, reference, repoindex) are never attached.
// Column allowlists are explicit — no instruction/result/verify/payload columns.
package fleet

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func esc(s string) string { return strings.ReplaceAll(s, "'", "''") }

// SyncConfig holds the source DB paths. An empty path means that store is
// skipped — a brain missing a store still syncs the rest gracefully.
type SyncConfig struct {
	Coord     string // coordination SQLite (audit)
	Mission   string // mission SQLite (missions)
	Queue     string // queue SQLite (tasks)
	Telemetry string // telemetry DuckDB (events)
}

// tableSync is one curated, metadata-only replication spec. cols is an explicit
// allowlist (never SELECT *, never a content payload column). needs names the
// SyncConfig store that must be configured for this spec to run.
type tableSync struct {
	remote     string   // remote table name (fleet_*)
	createDDL  string   // CREATE TABLE IF NOT EXISTS remote.<remote> (...)
	migrations []string // idempotent ALTER TABLE statements run after createDDL (ADD COLUMN IF NOT EXISTS …)
	src        string   // attached-alias.table, e.g. "mission.missions"
	cols       string   // curated column-NAME allowlist (reviewed comma-token set; == INSERT target columns after brain)
	project    string   // raw SELECT expression list, positionally aligned to cols. "" ⇒ default to cols (plain column read).
	// project exists so a column's VALUE can be sanitized at sync (e.g. a json_object
	// that keeps only a whitelist of keys) while its NAME stays a reviewed cols/allowedCols
	// token. This keeps the metadata-only-fleet boundary honest for JSON columns: the
	// column allowlist gates names, project gates the values inside a JSON column so no
	// content (directives, reasons, future keys) can passthrough. The tokenizer in
	// TestClassificationNoContentStore splits cols on ',', so project (which may contain
	// internal commas, e.g. json_object(...)) MUST stay separate from cols.
	cursor string // incremental cursor column (must be in createDDL)
	filter string // extra WHERE predicate ANDed in, or ""
	needs  string // "coord" | "mission" | "queue" | "telem"
}

// tableSpecs is the classification allowlist — the ONLY tables/columns that cross.
// Content stores (memory/reference/repoindex) are deliberately absent.
var tableSpecs = []tableSync{
	{
		// fleet_actions syncs strict metadata only: id/ts/agent/action.
		// detail is free-form JSON (can carry instruction text / payloads) and is
		// excluded entirely — zero free-form content crosses the MotherDuck boundary.
		remote:    "fleet_actions",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_actions (brain VARCHAR, id BIGINT, ts DOUBLE, agent VARCHAR, action VARCHAR)`,
		src:       "coord.audit",
		cols:      "id, ts, agent_name, action",
		cursor:    "id",
		needs:     "coord",
	},
	{
		remote:    "fleet_missions",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_missions (brain VARCHAR, id BIGINT, directive VARCHAR, status VARCHAR, repo VARCHAR, branch VARCHAR, pr_url VARCHAR, review_rounds BIGINT, created_ts DOUBLE, updated_ts DOUBLE)`,
		src:       "mission.missions",
		cols:      "id, directive, status, repo, branch, pr_url, review_rounds, created_ts, updated_ts",
		cursor:    "updated_ts", // mutable rows sync as temporal versions; query max(updated_ts) per (brain,id) for current state
		needs:     "mission",
	},
	{
		remote:    "fleet_tasks",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_tasks (brain VARCHAR, id BIGINT, mission_id BIGINT, key VARCHAR, role VARCHAR, title VARCHAR, status VARCHAR, claimed_by VARCHAR, created_ts DOUBLE, done_ts DOUBLE)`,
		src:       "queue.tasks",
		cols:      "id, mission_id, key, role, title, status, claimed_by, created_ts, done_ts",
		cursor:    "done_ts",             // stream of COMPLETED tasks (done_ts set once)
		filter:    "done_ts IS NOT NULL", // only completed tasks cross; instruction/verify deliberately excluded
		needs:     "queue",
	},
	{
		// fleet_telemetry carries structured analytics metadata: id/ts/mission/kind/actor/subject
		// plus model (the LLM that filed the event) and detail.
		//
		// SANITIZE-AT-SYNC: the LOCAL telem.events.detail is a GENERIC per-event JSON map —
		// some kinds carry CONTENT (mission_created → {"directive": …}, spawn_refused →
		// {"reason": err}). The column allowlist gates column NAMES, not the values inside a
		// JSON blob, so syncing detail RAW would be uncontrolled content passthrough across the
		// metadata-only MotherDuck boundary. Instead, project rebuilds detail from a FIXED
		// WHITELIST of finding-metadata keys (type/severity/outcome/finding_id/backend) via
		// json_object(json_extract_string(...)) — every non-whitelisted key (directive, reason,
		// review, and any FUTURE key) is dropped by construction. The whitelist is exactly the
		// keys the finding_reported/finding_resolved emitters write (see internal/brain/tasks.go),
		// so T3's json_extract_string(detail,'$.severity') etc. still works on the sanitized
		// column (one query shape preserved). The TestFleetTelemetryDetailCanary test is the
		// value-level guard proving no content crosses.
		remote:    "fleet_telemetry",
		createDDL: `CREATE TABLE IF NOT EXISTS remote.fleet_telemetry (brain VARCHAR, id BIGINT, ts DOUBLE, mission_id BIGINT, kind VARCHAR, actor VARCHAR, subject VARCHAR, model VARCHAR, detail VARCHAR)`,
		migrations: []string{
			`ALTER TABLE remote.fleet_telemetry ADD COLUMN IF NOT EXISTS model VARCHAR`,
			`ALTER TABLE remote.fleet_telemetry ADD COLUMN IF NOT EXISTS detail VARCHAR`,
		},
		src:  "telem.events",
		cols: "id, ts, mission_id, kind, actor, subject, model, detail",
		// TRY_CAST(detail AS JSON) yields NULL for empty/malformed detail (many event
		// kinds store "" or non-finding JSON), so json_extract_string never errors and
		// non-whitelisted keys are simply absent from the rebuilt object.
		project: "id, ts, mission_id, kind, actor, subject, model, " +
			"json_object(" +
			"'type', json_extract_string(TRY_CAST(detail AS JSON), '$.type'), " +
			"'severity', json_extract_string(TRY_CAST(detail AS JSON), '$.severity'), " +
			"'outcome', json_extract_string(TRY_CAST(detail AS JSON), '$.outcome'), " +
			"'finding_id', json_extract_string(TRY_CAST(detail AS JSON), '$.finding_id'), " +
			"'backend', json_extract_string(TRY_CAST(detail AS JSON), '$.backend')) AS detail",
		cursor: "id",
		needs:  "telem",
	},
}

// Sync replicates the curated metadata reporting set from the configured stores
// to the remote (a local .duckdb in tests, or `md:<db>` for MotherDuck; the
// brain must set the motherduck_token env), tagging every row with brainID.
// Incremental per (brain, table): only rows past the remote's max cursor for
// this brain are copied. Returns total rows synced across all specs.
func Sync(cfg SyncConfig, remoteAttach, brainID string) (int, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	if _, err := db.Exec("INSTALL sqlite; LOAD sqlite;"); err != nil {
		return 0, fmt.Errorf("load sqlite extension: %w", err)
	}
	if strings.HasPrefix(remoteAttach, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			return 0, fmt.Errorf("load motherduck extension: %w", err)
		}
	}

	attached := map[string]bool{}
	attach := func(alias, path string, sqlite bool) error {
		if path == "" {
			return nil
		}
		typ := " (READ_ONLY)"
		if sqlite {
			typ = " (TYPE sqlite, READ_ONLY)"
		}
		if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS %s%s", esc(path), alias, typ)); err != nil {
			return fmt.Errorf("attach %s: %w", alias, err)
		}
		attached[alias] = true
		return nil
	}
	if err := attach("coord", cfg.Coord, true); err != nil {
		return 0, err
	}
	if err := attach("mission", cfg.Mission, true); err != nil {
		return 0, err
	}
	if err := attach("queue", cfg.Queue, true); err != nil {
		return 0, err
	}
	if err := attach("telem", cfg.Telemetry, false); err != nil {
		return 0, err
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS remote", esc(remoteAttach))); err != nil {
		return 0, fmt.Errorf("attach remote: %w", err)
	}

	total := 0
	for _, ts := range tableSpecs {
		if !attached[ts.needs] {
			continue // source store not configured — skip this spec
		}
		if _, err := db.Exec(ts.createDDL); err != nil {
			return total, fmt.Errorf("create %s: %w", ts.remote, err)
		}
		for _, alt := range ts.migrations {
			if _, err := db.Exec(alt); err != nil {
				return total, fmt.Errorf("migrate %s: %w", ts.remote, err)
			}
		}
		var last sql.NullFloat64
		if err := db.QueryRow(
			fmt.Sprintf("SELECT max(%s) FROM remote.%s WHERE brain = ?", ts.cursor, ts.remote),
			brainID,
		).Scan(&last); err != nil {
			return total, fmt.Errorf("cursor %s: %w", ts.remote, err)
		}
		// format the cursor floor as a plain decimal — never scientific notation.
		// updated_ts/done_ts are large Unix floats that %v/%g would render as
		// 1.7e+09, which DuckDB won't parse in an inline SQL literal.
		floor := strconv.FormatFloat(cursorFloor(last), 'f', -1, 64)
		where := fmt.Sprintf("%s > %s", ts.cursor, floor)
		if ts.filter != "" {
			where = ts.filter + " AND " + where
		}
		// project is the SELECT expression list (may sanitize a column's value, e.g. a
		// json_object whitelist); it is positionally aligned to the INSERT target cols.
		// Defaults to cols (plain column read) for specs that don't need sanitizing.
		project := ts.project
		if project == "" {
			project = ts.cols
		}
		q := fmt.Sprintf( // #nosec G201 -- not injectable: ts.remote/ts.project/ts.src/ts.cursor are constant identifiers/expressions from internal tableSync specs; brainID is esc()-escaped (single-quotes doubled); where is built from constant ts.filter + a server-formatted float (strconv.FormatFloat)
			"INSERT INTO remote.%s SELECT '%s', %s FROM %s WHERE %s ORDER BY %s",
			ts.remote, esc(brainID), project, ts.src, where, ts.cursor,
		)
		res, err := db.Exec(q)
		if err != nil {
			return total, fmt.Errorf("insert %s: %w", ts.remote, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// cursorFloor is the low-water mark for the incremental WHERE. When the remote
// holds no rows for this brain yet, NullFloat64 is invalid → return -1, which
// is below any real cursor (all cursors are non-negative), so the first sync
// copies everything.
func cursorFloor(last sql.NullFloat64) float64 {
	if !last.Valid {
		return -1
	}
	return last.Float64
}
