#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/test-build-warehouse-parquet.py — unit tests for the public
warehouse extract's per-model filter.

Run with: python3 scripts/test-build-warehouse-parquet.py -v
(the extract tests need the duckdb python package and skip without it, as
the script itself cannot export without it; the privacy-gate tests are pure
Python and always run.)

The scorecard extract (bug_catches) is a per-model analysis on a public page,
so it applies the same rule as `corral models rank`: a row whose seat named a
TOOL, not a model (an unpinned agent seat, model_unresolved = true), is not
evidence about a model and is left out — and counted, never silently dropped.
A store from before the column existed must still export.
"""
import importlib.util
import os
import tempfile
import unittest

HAVE_DUCKDB = importlib.util.find_spec("duckdb") is not None

if HAVE_DUCKDB:
    import duckdb

_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "build-warehouse-parquet.py")
_spec = importlib.util.spec_from_file_location("build_warehouse_parquet", _PATH)
bwp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(bwp)

COLUMNS = """record_id BIGINT, repo VARCHAR, model VARCHAR, role VARCHAR, source VARCHAR,
    mutants_planted INT, mutants_survived INT, catches INT, opportunities INT,
    sound_tests INT, authored_tests INT, critic_flags INT, shard INT, region VARCHAR,
    region_complexity INT, region_lines INT, shadow BOOLEAN, ts TIMESTAMP"""


def _store(with_column: bool):
    """An in-memory DuckDB holding a bugcatch store: one pinned row, one
    unpinned agent row (when the column exists), one private-repo row."""
    con = duckdb.connect()
    con.execute("ATTACH ':memory:' AS bugcatch")
    extra = ", model_unresolved BOOLEAN" if with_column else ""
    con.execute(f"CREATE TABLE bugcatch.bugcatch_observations ({COLUMNS}{extra})")
    rows = [
        (1, "pdbethke/corralai", "codex:gpt-6-astra", False),
        (2, "pdbethke/corralai", "codex", True),
        (3, "someone/private", "gemini-3.6-flash", False),
        (4, "pdbethke/corralai", "claude-sonnet-5", None),  # a legacy row: NULL reads as resolved
    ]
    for rid, repo, model, unresolved in rows:
        vals = f"{rid}, '{repo}', '{model}', 'test-writer', 'dev', 5, 1, 1, 1, 0, 0, 0, 0, 'r', 1, 1, false, now()"
        if with_column:
            vals += ", " + ("NULL" if unresolved is None else str(unresolved).lower())
        elif unresolved:
            continue  # a store without the column has no agent rows
        con.execute(f"INSERT INTO bugcatch.bugcatch_observations VALUES ({vals})")
    con.create_function("short_repo", bwp.short_repo, ["VARCHAR"], "VARCHAR")
    return con


@unittest.skipUnless(HAVE_DUCKDB, "duckdb python package not installed")
class CatchesExtract(unittest.TestCase):
    allow = "('pdbethke/corralai')"

    def models(self, con):
        sql, _ = bwp.catches_sql(con, self.allow)
        return sorted(r[0] for r in con.execute(f"SELECT model FROM ({sql})").fetchall())

    def test_unpinned_agent_rows_are_left_out_and_counted(self):
        con = _store(with_column=True)
        self.assertEqual(self.models(con), ["claude-sonnet-5", "codex:gpt-6-astra"])
        _, unresolved = bwp.catches_sql(con, self.allow)
        self.assertEqual(unresolved, 1)

    def test_a_store_without_the_column_still_exports(self):
        con = _store(with_column=False)
        self.assertEqual(self.models(con), ["claude-sonnet-5", "codex:gpt-6-astra"])
        _, unresolved = bwp.catches_sql(con, self.allow)
        self.assertEqual(unresolved, 0)

    def test_the_predicate_is_the_one_the_go_stores_use(self):
        # Pinned from the Go side too (internal/seatrecord): the same text.
        self.assertEqual(bwp.RESOLVED_ONLY, "NOT COALESCE(model_unresolved, false)")


class PrivacyGate(unittest.TestCase):
    """The allowlist fails CLOSED on a repo that names nothing. A --local run
    from inside a private checkout with no --repo-dir records "" (an older
    corral recorded 'local'); short_repo('') used to be 'local', which the
    allowlist passed, so a regeneration would have published that private
    checkout's paths and metrics (final whole-branch review, 2026-10-20)."""

    def test_an_unidentified_repo_is_never_public(self):
        for raw in ("", None, "local", " local ", "  "):
            self.assertFalse(bwp.is_public(raw), f"{raw!r} passed the gate")
            self.assertEqual(bwp.short_repo(raw), "", f"{raw!r} rendered as a name")

    def test_the_allowlist_holds_no_unidentified_spelling(self):
        self.assertFalse(bwp.UNIDENTIFIED_REPOS & bwp.PUBLIC_REPOS)

    def test_adding_local_back_is_refused(self):
        with self.assertRaises(SystemExit):
            bwp.require_closed_gate(bwp.PUBLIC_REPOS | {"local"})
        with self.assertRaises(SystemExit):
            bwp.require_closed_gate(bwp.PUBLIC_REPOS | {""})
        bwp.require_closed_gate(bwp.PUBLIC_REPOS)  # the shipped list passes

    def test_a_listed_repo_passes_in_every_spelling(self):
        for raw in ("pdbethke/corralai", "https://github.com/pdbethke/corralai",
                    "https://github.com/pdbethke/corralai.git", "git@github.com:pdbethke/corralai.git"):
            self.assertTrue(bwp.is_public(raw), f"{raw!r} was withheld")

    def test_an_unlisted_repo_is_withheld(self):
        for raw in ("someone/private", "https://github.com/someone/private.git", "a-directory-basename"):
            self.assertFalse(bwp.is_public(raw), f"{raw!r} passed the gate")


if __name__ == "__main__":
    unittest.main()
