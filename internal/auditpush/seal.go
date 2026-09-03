// SPDX-License-Identifier: Elastic-2.0

package auditpush

// SealViewDDL creates `corral_seal`: the latest kill-rate-bearing row per
// (repo, path).
//
// It is the accumulation the per-diff gate was always meant to feed. A single
// pull request's audit says something about the files that PR touched;
// nothing in the warehouse said anything about the repo. "The repo's current
// state is the union of the verdicts that are still valid" needs exactly two
// things: the newest row per file, and each row's parent_sha256 so a reader
// holding the checkout can tell a LIVE verdict (the file's bytes still hash
// to it) from a STALE one. This view is the first; corral_audits.parent_sha256
// is the second.
//
// `kill_rate IS NOT NULL` is what keeps it honest: since schema_version 2 the
// warehouse holds every file at every disposition, and a rejected file was
// never graded. A seal that listed the files corral REFUSED, alongside the
// ones it scored, would be a coverage claim nobody earned.
//
// PARTITIONING ON (repo, path) ORDER BY ts IS LOAD-BEARING, not incidental:
// the view must never key on scan_id. That column is a PER-LEDGER sequence —
// every local `--record` ledger starts again at 1 — so the same small integers
// recur across every ledger that has pushed to a given warehouse. A seal built
// on scan_id would union unrelated scans from different operators and report
// the result as one repository's state. The row key that IS unique is
// (repo, run_url, scan_id); this view needs neither, because "newest row per
// file" is a question ts answers directly. See docs/corral/actions-as-swarm.md.
//
// Created on every push rather than by a separate command: the view IS the
// share an operator hands someone, and a share that has to be created by hand
// is one nobody creates. `IF NOT EXISTS` (not `OR REPLACE`) so a warehouse
// whose owner has customized the view keeps their definition.
//
// The body names corral_audits UNQUALIFIED. PushBundle runs `USE warehouse`
// first, so this resolves at creation time — and, because DuckDB stores the
// view's body as written, it keeps resolving when the same file is later
// attached under a different alias or opened directly.
const SealViewDDL = `CREATE VIEW IF NOT EXISTS corral_seal AS
SELECT * EXCLUDE rn FROM (
  SELECT a.*, row_number() OVER (PARTITION BY repo, path ORDER BY coalesce(started_at, ts) DESC, ts DESC) AS rn
  FROM corral_audits a WHERE kill_rate IS NOT NULL
) WHERE rn = 1`
