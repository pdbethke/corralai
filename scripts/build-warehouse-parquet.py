#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""
scripts/build-warehouse-parquet.py — regenerate the /warehouse page's parquet
extracts from the local DuckDB stores.

These extracts used to be produced by hand, which is why they drifted: the site
shipped a snapshot nobody could reproduce, and the audit ledger behind half the
page's deep links was never exported at all.

PRIVACY GATE, not a nicety. The ledger records every repo corral has audited on
this machine, including PRIVATE ones. A static site is public forever the moment
it deploys, so a repo is exported only if it appears in PUBLIC_REPOS below —
an allowlist, never a denylist, because the failure modes are not symmetric:
forgetting to add a public repo costs a missing row, forgetting to exclude a
private one publishes its name.

Withheld rows are COUNTED AND PRINTED, never silently dropped. A tool that
quietly truncates looks more complete than it is, which is the same dishonesty
this project exists to measure.

Usage:  python3 scripts/build-warehouse-parquet.py [--out site/public/data]
"""
import argparse
import json
import os
import sys

try:
    import duckdb
except ImportError:  # pragma: no cover - operator tooling
    sys.exit("duckdb python package required: pip install duckdb")

HOME_DB = os.path.expanduser("~/.claude")

#: Repositories whose names may appear on the public site. 'local' is the
#: repo-less --repo-dir form and names nothing.
PUBLIC_REPOS = {
    "local",
    "pdbethke/corralai",
    "pdbethke/sportspicker-core",
    "vercel/ms",
    "minitest/minitest",
    "wvanbergen/chunky_png",
    "debug-js/debug",
    "google/uuid",
    "more-itertools/more-itertools",
    "threedaymonk/text",
}


def short_repo(raw: str) -> str:
    """git@github.com:owner/name.git and https://… both render as owner/name."""
    if not raw or raw == "local":
        return "local"
    s = raw.removesuffix(".git")
    for prefix in ("git@github.com:", "https://github.com/", "http://github.com/"):
        s = s.removeprefix(prefix)
    return s


def export(con, sql: str, out_path: str) -> int:
    """
    Write one extract, refusing to replace published data with nothing.

    The guard is not hypothetical: this machine's scan store is EMPTY while the
    committed scans.parquet holds a real series recorded elsewhere, so a naive
    regeneration silently replaced six published scans with zero rows. An empty
    result means the source is missing, not that the data is gone — and a
    generator that can quietly delete the only copy of something is worse than
    no generator at all.
    """
    rows = con.execute(f"SELECT count(*) FROM ({sql})").fetchone()[0]
    if rows == 0 and os.path.exists(out_path):
        print(f"REFUSED to overwrite {out_path}: the query returned 0 rows and a "
              f"populated extract already exists (source store is empty on this machine)")
        return -1
    con.execute(f"COPY ({sql}) TO '{out_path}' (FORMAT parquet)")
    return con.execute(f"SELECT count(*) FROM '{out_path}'").fetchone()[0]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="site/public/data")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)

    withheld = {}
    con = duckdb.connect()
    con.execute(f"ATTACH '{HOME_DB}/corralai_build.duckdb' AS build (READ_ONLY)")
    con.execute(f"ATTACH '{HOME_DB}/corralai_bugcatch.duckdb' AS bugcatch (READ_ONLY)")
    con.execute(f"ATTACH '{HOME_DB}/corralai_scans.duckdb' AS scanstore (READ_ONLY)")

    con.create_function("short_repo", short_repo, ["VARCHAR"], "VARCHAR")
    allow = "(" + ",".join(f"'{r}'" for r in sorted(PUBLIC_REPOS)) + ")"

    # --- the audit ledger: one row per signed verdict -----------------------
    ledger_sql = f"""
        SELECT id,
               short_repo(repo)            AS repo,
               branch,
               actor,
               "pass"                      AS certified,
               to_timestamp(created_ts)    AS ts,
               anchored
        FROM build.build_records
        WHERE short_repo(repo) IN {allow}
        ORDER BY id
    """
    kept = export(con, ledger_sql, f"{args.out}/audit_ledger.parquet")
    total = con.execute("SELECT count(*) FROM build.build_records").fetchone()[0]
    withheld["audit_ledger"] = total - kept
    print(f"audit_ledger:  {kept} rows exported, {total - kept} withheld (repo not on the public allowlist)")

    # --- the per-seat scorecard: which model caught what, in which role -----
    catches_sql = f"""
        SELECT record_id,
               short_repo(repo)                          AS repo,
               model,
               role,
               source,
               mutants_planted,
               mutants_survived,
               mutants_planted - mutants_survived        AS mutants_killed,
               catches,
               opportunities,
               sound_tests,
               authored_tests,
               critic_flags,
               shard,
               region,
               region_complexity,
               region_lines,
               shadow,
               ts
        FROM bugcatch.bugcatch_observations
        WHERE short_repo(repo) IN {allow}
        ORDER BY record_id, shard
    """
    kept = export(con, catches_sql, f"{args.out}/bug_catches.parquet")
    total = con.execute("SELECT count(*) FROM bugcatch.bugcatch_observations").fetchone()[0]
    withheld["bug_catches"] = total - kept
    print(f"bug_catches:   {kept} rows exported, {total - kept} withheld (repo not on the public allowlist)")

    # --- the repo-scan series, already published; regenerated for parity ----
    for table in ("scans", "scan_files"):
        try:
            kept = export(con, f"SELECT * FROM scanstore.{table}", f"{args.out}/{table}.parquet")
            if kept >= 0:
                print(f"{table + ':':14} {kept} rows exported")
        except duckdb.Error as exc:
            print(f"{table + ':':14} skipped ({exc})")

    # A manifest travels with the extracts so the page never hand-writes a row
    # count. Those numbers drifted before — the footer described a dataset the
    # files no longer matched — and a warehouse whose own labels are stale is a
    # poor advertisement for auditing anything.
    manifest = {
        "generated_note": "built by scripts/build-warehouse-parquet.py — do not hand-edit",
        "tables": {},
        "withheld": withheld,
    }
    for table in ("audit_ledger", "bug_catches", "scans", "scan_files"):
        path = f"{args.out}/{table}.parquet"
        if os.path.exists(path):
            manifest["tables"][table] = con.execute(f"SELECT count(*) FROM '{path}'").fetchone()[0]
    manifest_path = os.path.join("site", "src", "data", "warehouse-manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2)
        fh.write("\n")
    print(f"\nwrote to {args.out}/ and {manifest_path} — rebuild the site to pick them up")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
