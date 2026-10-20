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
    # Refused in main(), not here: the privacy gate below is pure Python, and
    # its tests (scripts/test-build-warehouse-parquet.py) must run on a
    # machine without duckdb rather than skip the one check that matters most.
    duckdb = None


def corral_state_dir() -> str:
    """corral's state root, resolved as internal/statedir resolves it (that
    package is the source of truth; `corral state paths` prints the result):
    CORRALAI_STATE_DIR, else an absolute $XDG_STATE_HOME/corral, else the
    platform default."""
    explicit = os.environ.get("CORRALAI_STATE_DIR", "").strip()
    if explicit:
        return explicit
    if sys.platform == "darwin":
        return os.path.expanduser("~/Library/Application Support/corral")
    xdg = os.environ.get("XDG_STATE_HOME", "").strip()
    if xdg and os.path.isabs(xdg):
        return os.path.join(xdg, "corral")
    return os.path.expanduser("~/.local/state/corral")


def state_db(name: str, override: str) -> str:
    """One store's path: its per-file override variable wins, as in corral."""
    return os.environ.get(override, "").strip() or os.path.join(corral_state_dir(), name)


#: Repositories whose names may appear on the public site.
#:
#: 'local' is NOT here, and must never be: it was the placeholder an older
#: corral recorded for a run with no derivable repository, and corral now
#: records "" for the same run (identity "none"). Neither names a project —
#: both are "a checkout we could not identify", which is exactly the
#: population a private checkout belongs to. Allowing 'local' therefore
#: published any such run's paths and metrics; when --local stopped asking
#: git about the process's cwd, every run from inside a private checkout
#: without --repo-dir became one (final whole-branch review, 2026-10-20).
#: An unidentified repo fails CLOSED: withheld and counted, like any other
#: name not on this list. require_closed_gate() refuses to run if either
#: spelling is ever added back.
PUBLIC_REPOS = {
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


#: The repo spellings that name no repository. short_repo maps every one of
#: them to "", and "" can never be on the allowlist.
UNIDENTIFIED_REPOS = {"", "local"}


def short_repo(raw: str) -> str:
    """git@github.com:owner/name.git and https://… both render as owner/name.
    A repo that names nothing ("" / NULL, or the retired 'local') renders as
    "", which the allowlist gate never passes."""
    if not raw or raw.strip() in UNIDENTIFIED_REPOS:
        return ""
    s = raw.strip().removesuffix(".git")
    for prefix in ("git@github.com:", "https://github.com/", "http://github.com/"):
        s = s.removeprefix(prefix)
    return s


def require_closed_gate(allow: set) -> None:
    """Refuse an allowlist that would pass an unidentified repo. Checked at
    run time, before anything is exported, so a well-meant edit that adds
    'local' back cannot quietly reopen the leak."""
    leaked = UNIDENTIFIED_REPOS & allow
    if leaked:
        sys.exit(f"REFUSED: PUBLIC_REPOS contains {sorted(leaked)!r}, which names no repository "
                 f"(a private checkout's run records exactly that); remove it")


def is_public(raw: str) -> bool:
    """The gate, as one predicate: the repo's short name is on PUBLIC_REPOS.
    An unidentified repo is never public."""
    name = short_repo(raw)
    return name not in UNIDENTIFIED_REPOS and name in PUBLIC_REPOS


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


#: The SQL half of the rule that keeps a seat which named a TOOL, not a model
#: (an unpinned agent seat), out of per-model analysis. The SAME text as
#: internal/seatrecord.ResolvedOnly, which every Go store applies; a Go test
#: there pins this line to it, so the public page and `corral models rank`
#: cannot disagree about which rows count.
RESOLVED_ONLY = "NOT COALESCE(model_unresolved, false)"


def catches_sql(con, allow: str):
    """The bug_catches extract's query, and how many allowlisted rows it
    leaves out because their seat named no model.

    The scorecard is a per-model analysis on a public page, so an unpinned
    agent seat is left out exactly as `corral models rank` leaves it out. A
    store written before the model_unresolved column existed has no such rows
    (certify could not seat an agent then), so the filter is applied only when
    the column exists — the query must not fail on an older store.
    """
    has_column = con.execute(
        "SELECT count(*) FROM duckdb_columns() WHERE database_name = 'bugcatch' "
        "AND table_name = 'bugcatch_observations' AND column_name = 'model_unresolved'"
    ).fetchone()[0] > 0
    where = f"short_repo(repo) IN {allow}"
    unresolved = 0
    if has_column:
        unresolved = con.execute(
            f"SELECT count(*) FROM bugcatch.bugcatch_observations WHERE {where} AND NOT ({RESOLVED_ONLY})"
        ).fetchone()[0]
        where += f" AND {RESOLVED_ONLY}"
    sql = f"""
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
        WHERE {where}
        ORDER BY record_id, shard
    """
    return sql, unresolved


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="site/public/data")
    args = ap.parse_args()
    if duckdb is None:
        sys.exit("duckdb python package required: pip install duckdb")
    require_closed_gate(PUBLIC_REPOS)
    os.makedirs(args.out, exist_ok=True)

    withheld = {}
    con = duckdb.connect()
    con.execute(f"ATTACH '{state_db('corralai_build.duckdb', 'CORRALAI_BUILD_DB')}' AS build (READ_ONLY)")
    con.execute(f"ATTACH '{state_db('corralai_bugcatch.duckdb', 'CORRALAI_BUGCATCH_DB')}' AS bugcatch (READ_ONLY)")
    # corralai_scans.duckdb is the retired name of the scan record (now the
    # ledger directory; the file that kept the name became the cache). It is
    # not a statedir row and `corral state migrate` does not move it, so it is
    # looked for in the state directory only where an operator put it.
    con.execute(f"ATTACH '{os.path.join(corral_state_dir(), 'corralai_scans.duckdb')}' AS scanstore (READ_ONLY)")

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
    # kept is -1 when export() REFUSED (an empty query against a populated
    # extract). Feeding the sentinel into `total - kept` prints total+1
    # withheld — a number describing nothing that happened. The first fix
    # guarded the stored count and left the PRINTED one beside it, which the
    # next review reproduced: one door, again. (R2, then R1 of round four.)
    withheld["audit_ledger"] = (total - kept) if kept >= 0 else 0
    print(f"audit_ledger:  {kept} rows exported, {withheld['audit_ledger']} withheld (repo not on the public allowlist)")

    # --- the per-seat scorecard: which model caught what, in which role -----
    catches, unresolved = catches_sql(con, allow)
    kept = export(con, catches, f"{args.out}/bug_catches.parquet")
    total = con.execute("SELECT count(*) FROM bugcatch.bugcatch_observations").fetchone()[0]
    # kept is -1 when export() REFUSED (an empty query against a populated
        # extract). Feeding the sentinel into `total - kept` printed
        # total+1 withheld — a number describing nothing that happened.
        # Found by a cold review, 2026-09-08 (R2).
    # Two different reasons, counted apart: a row left out because its seat
    # named no model is not a private repo, and printing it as one would
    # describe something that did not happen.
    withheld["bug_catches"] = (total - kept - unresolved) if kept >= 0 else 0
    withheld["bug_catches_model_unresolved"] = unresolved if kept >= 0 else 0
    print(f"bug_catches:   {kept} rows exported, {withheld['bug_catches']} withheld (repo not on the public allowlist), "
          f"{withheld['bug_catches_model_unresolved']} left out (unpinned agent seat: names a tool, not a model)")

    # --- the repo-scan series, already published; regenerated for parity ----
    #
    # THE ALLOWLIST APPLIES HERE TOO. These two exported `SELECT *` with no
    # PUBLIC_REPOS filter, while audit_ledger and bug_catches above were
    # filtered — so a regeneration on a machine whose scan store holds
    # private repos would publish their names, file paths and kill rates to
    # a static site. It had not happened (this machine's store held only
    # flask when the committed extract was built), which is exactly why it
    # would have gone unnoticed. scan_files carries no repo of its own, so
    # it is filtered through the scans that survive the allowlist. Found by
    # a cold review, 2026-09-08 (R1, reproduced).
    scans_sql = f"SELECT * FROM scanstore.scans WHERE short_repo(repo) IN {allow}"
    files_sql = (
        "SELECT f.* FROM scanstore.scan_files f "
        "JOIN scanstore.scans s ON s.id = f.scan_id "
        f"WHERE short_repo(s.repo) IN {allow}"
    )
    for table, sql in (("scans", scans_sql), ("scan_files", files_sql)):
        try:
            kept = export(con, sql, f"{args.out}/{table}.parquet")
            if kept >= 0:
                total = con.execute(f"SELECT count(*) FROM scanstore.{table}").fetchone()[0]
                print(f"{table + ':':14} {kept} rows exported, {max(total - kept, 0)} withheld (repo not on the public allowlist)")
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
