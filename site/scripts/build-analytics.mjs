// SPDX-License-Identifier: Elastic-2.0
// site/scripts/build-analytics.mjs
//
// Build-time pipeline for static site replay data:
//   1) Prefer reading recordings from DuckDB (CORRALAI_RECORDINGS_DB).
//   2) Materialize generated recording JSON/meta under src/data/generated/.
//   3) Build analytics.json from whichever source is active (generated or legacy).
//
// Fallback policy:
// - If the DB file is missing or has zero recordings, fall back to committed
//   src/data/recordings/*.json and continue.
// - If the DB exists but cannot be read, fail loudly (corrupt/permission issue).
import { DuckDBInstance } from '@duckdb/node-api';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const SITE_ROOT = path.join(import.meta.dirname, '..');
const DATA_ROOT = path.join(SITE_ROOT, 'src', 'data');
const LEGACY_RECORDINGS_DIR = path.join(DATA_ROOT, 'recordings');
const GENERATED_ROOT = path.join(DATA_ROOT, 'generated');
const GENERATED_RECORDINGS_DIR = path.join(GENERATED_ROOT, 'recordings');
const GENERATED_ANALYTICS_OUT = path.join(GENERATED_ROOT, 'analytics.json');
const LEGACY_ANALYTICS_OUT = path.join(DATA_ROOT, 'analytics.json');

const DEFAULT_DB_PATH = path.join(os.homedir(), '.claude', 'corralai_recordings.duckdb');
const RECORDINGS_DB = process.env.CORRALAI_RECORDINGS_DB || DEFAULT_DB_PATH;

function toPlainRow(row) {
  return Object.fromEntries(
    Object.entries(row).map(([k, v]) => [k, typeof v === 'bigint' ? Number(v) : v]),
  );
}

async function q(conn, sql) {
  const reader = await conn.runAndReadAll(sql);
  return reader.getRowObjects().map(toPlainRow);
}

function readLegacyResultBySlug() {
  const out = new Map();
  if (!fs.existsSync(LEGACY_RECORDINGS_DIR)) return out;
  for (const file of fs.readdirSync(LEGACY_RECORDINGS_DIR)) {
    if (!file.endsWith('.meta.json')) continue;
    const slug = file.replace(/\.meta\.json$/, '');
    const full = path.join(LEGACY_RECORDINGS_DIR, file);
    try {
      const parsed = JSON.parse(fs.readFileSync(full, 'utf-8'));
      if (parsed?.result?.url && parsed?.result?.label) {
        out.set(slug, parsed.result);
      }
    } catch {
      // Ignore malformed legacy sidecars and proceed without result links.
    }
  }
  return out;
}

function ensureCleanGeneratedDirs() {
  fs.rmSync(GENERATED_ROOT, { recursive: true, force: true });
  fs.mkdirSync(GENERATED_RECORDINGS_DIR, { recursive: true });
}

async function maybeGenerateFromDuckDB(dbPath) {
  if (!fs.existsSync(dbPath)) {
    return { usedDb: false, reason: `recordings DB not found at ${dbPath}; using committed JSON` };
  }

  const instance = await DuckDBInstance.create(dbPath);
  const conn = await instance.connect();

  try {
    const missions = await q(conn, `
      SELECT slug, mission_id, COALESCE(directive,'') AS directive,
             task_count, done_task_count, finding_count, duration_seconds,
             COALESCE(models_json,'') AS models_json,
             COALESCE(platform_json,'') AS platform_json,
             exported_ts
      FROM recordings_missions
      ORDER BY exported_ts DESC
    `);
    if (missions.length === 0) {
      return { usedDb: false, reason: `recordings DB is empty at ${dbPath}; using committed JSON` };
    }

    const events = await q(conn, `
      SELECT slug, event_idx, ts, kind, COALESCE(actor,'') AS actor,
             COALESCE(subject,'') AS subject, COALESCE(model,'') AS model,
             COALESCE(detail_json,'') AS detail_json
      FROM recordings_events
      ORDER BY slug, event_idx ASC
    `);

    const eventsBySlug = new Map();
    for (const row of events) {
      const slug = String(row.slug);
      const detail = row.detail_json ? JSON.parse(String(row.detail_json)) : undefined;
      const event = {
        ts: Number(row.ts || 0),
        kind: String(row.kind || ''),
        ...(row.actor ? { actor: String(row.actor) } : {}),
        ...(row.subject ? { subject: String(row.subject) } : {}),
        ...(row.model ? { model: String(row.model) } : {}),
        ...(detail && Object.keys(detail).length > 0 ? { detail } : {}),
      };
      if (!eventsBySlug.has(slug)) eventsBySlug.set(slug, []);
      eventsBySlug.get(slug).push(event);
    }

    const legacyResults = readLegacyResultBySlug();
    ensureCleanGeneratedDirs();
    for (const m of missions) {
      const slug = String(m.slug);
      const models = m.models_json ? JSON.parse(String(m.models_json)) : [];
      const platform = m.platform_json ? JSON.parse(String(m.platform_json)) : undefined;
      const meta = {
        directive: String(m.directive || ''),
        task_count: Number(m.task_count || 0),
        done_task_count: Number(m.done_task_count || 0),
        finding_count: Number(m.finding_count || 0),
        duration_seconds: Number(m.duration_seconds || 0),
        models: Array.isArray(models) ? models : [],
        ...(platform && typeof platform === 'object' ? { platform } : {}),
        ...(legacyResults.has(slug) ? { result: legacyResults.get(slug) } : {}),
      };
      const replay = { events: eventsBySlug.get(slug) || [] };
      fs.writeFileSync(
        path.join(GENERATED_RECORDINGS_DIR, `${slug}.json`),
        JSON.stringify(replay, null, 2) + '\n',
      );
      fs.writeFileSync(
        path.join(GENERATED_RECORDINGS_DIR, `${slug}.meta.json`),
        JSON.stringify(meta, null, 2) + '\n',
      );
    }

    return {
      usedDb: true,
      recordingsDir: GENERATED_RECORDINGS_DIR,
      reason: `generated ${missions.length} recording(s) from ${dbPath}`,
    };
  } finally {
    await conn.closeSync();
  }
}

function flattenRows(recordingsDir) {
  const rows = [];
  const seenFindings = new Set();
  for (const f of fs.readdirSync(recordingsDir).sort()) {
    if (!f.endsWith('.json') || f.endsWith('.meta.json')) continue;
    const slug = f.replace(/\.json$/, '');
    const { events = [] } = JSON.parse(fs.readFileSync(path.join(recordingsDir, f), 'utf-8'));
    for (const ev of events) {
      const d = ev.detail || {};
      // BuildReplayStream can include duplicate finding beats from merged sources.
      if (ev.kind === 'finding_reported') {
        const key = `${slug}|${ev.subject || ''}|${d.severity || ''}|${Math.round(ev.ts)}`;
        if (seenFindings.has(key)) continue;
        seenFindings.add(key);
      }
      rows.push({
        slug,
        ts: ev.ts,
        kind: ev.kind || '',
        subject: ev.subject || '',
        model: ev.model || '',
        backend: String(d.backend || ''),
        severity: String(d.severity || ''),
      });
    }
  }
  return rows;
}

async function buildAnalyticsFromRecordings(recordingsDir, outPath) {
  const rows = flattenRows(recordingsDir);
  if (rows.length === 0) {
    throw new Error(`no recordings found under ${recordingsDir}`);
  }

  const ndjson = path.join(os.tmpdir(), `corralai-analytics-${process.pid}.ndjson`);
  fs.writeFileSync(ndjson, rows.map((r) => JSON.stringify(r)).join('\n'));

  const instance = await DuckDBInstance.create(':memory:');
  const conn = await instance.connect();
  try {
    await conn.run(`CREATE TABLE ev AS SELECT * FROM read_json_auto('${ndjson}', format='newline_delimited')`);
    const findings_by_severity = await q(conn, `
      SELECT slug, COALESCE(NULLIF(severity,''),'(none)') AS severity, count(*) AS n
      FROM ev WHERE kind='finding_reported' GROUP BY slug, severity ORDER BY slug, severity`);
    const findings_by_model = await q(conn, `
      SELECT CASE WHEN model='' THEN '(not recorded)'
                  WHEN backend='' THEN model
                  ELSE backend || ':' || model END AS model,
             count(*) AS findings
      FROM ev WHERE kind='finding_reported' GROUP BY 1 ORDER BY findings DESC, model`);
    const task_durations = await q(conn, `
      WITH claimed AS (SELECT slug, subject, min(ts) AS t0 FROM ev WHERE kind='task_claimed' GROUP BY slug, subject),
           done    AS (SELECT slug, subject, max(ts) AS t1 FROM ev WHERE kind='task_done'    GROUP BY slug, subject)
      SELECT claimed.slug AS slug, count(*) AS tasks,
             round(avg(t1 - t0), 1) AS avg_seconds, round(max(t1 - t0), 1) AS max_seconds
      FROM claimed JOIN done USING (slug, subject)
      WHERE t1 >= t0 GROUP BY claimed.slug ORDER BY claimed.slug`);

    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(
      outPath,
      JSON.stringify({
        generated_note: 'built by site/scripts/build-analytics.mjs (DuckDB) — do not hand-edit',
        findings_by_severity,
        findings_by_model,
        task_durations,
      }, null, 2) + '\n',
    );
    return { eventCount: rows.length, slugCount: new Set(rows.map((r) => r.slug)).size };
  } finally {
    fs.rmSync(ndjson, { force: true });
    await conn.closeSync();
  }
}

let sourceDir = LEGACY_RECORDINGS_DIR;
let analyticsOut = LEGACY_ANALYTICS_OUT;
try {
  const dbBuild = await maybeGenerateFromDuckDB(RECORDINGS_DB);
  if (dbBuild.usedDb) {
    sourceDir = dbBuild.recordingsDir;
    analyticsOut = GENERATED_ANALYTICS_OUT;
    console.log(`[recordings] ${dbBuild.reason}`);
  } else {
    // Keep fallback noisy so CI and local runs clearly state what happened.
    console.warn(`[recordings] ${dbBuild.reason}`);
    fs.rmSync(GENERATED_ROOT, { recursive: true, force: true });
  }
} catch (err) {
  console.error(`[recordings] failed reading DuckDB at ${RECORDINGS_DB}`);
  throw err;
}

const { eventCount, slugCount } = await buildAnalyticsFromRecordings(sourceDir, analyticsOut);
console.log(`[analytics] wrote ${analyticsOut} (${eventCount} events across ${slugCount} recording(s))`);
