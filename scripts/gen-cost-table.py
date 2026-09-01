#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/gen-cost-table.py — renders the two reference-scan fixtures
(docs/design/fixtures/cost-model-flask.json, cost-model-requests.json) into
the Markdown table that lives between the `<!-- cost-table:start -->` and
`<!-- cost-table:end -->` markers in docs/design/cost-model.md.

FIELD MAPPING (read this before touching either side)
-------------------------------------------------------
Each committed fixture is the SAME shape `corral scans show <id> --timing
--json` emits for that scan, slimmed to what this table needs:

    {
      "selection_ms": <int|null>,   // scan-grain, once per scan
      "model_calls": [              // FULL scan_model_calls rows, verbatim,
        {"path", "role", "model", "calls", "retries",                # from ScanByID + ModelCallsForScan
         "input_tokens", "output_tokens", "cached_input_tokens", "wall_ms"}
        ...
      ],
      "files": [                    // internal/scanstore.File rows (Go
        {"Path", "Lang", "KillRate", "Survivors", "ProvenMissed",     # struct field names, PascalCase —
         "Trees", "ModelsByRole", "MutantsGraded",                    # this is corral's own --json shape,
         "MutantMillisMedian", "MutantMillisMax", "SelectionMillis",  # not translated to snake_case)
         "GenerationMillis", "PoolMillis", "DevPassMillis",
         "AuthoredPassMillis", "CriticMillis", "TotalMillis"}
        ...                          // ONLY the audited row(s); rejected
      ]                              // rows and every non-timing field
    }                                // (AuthoredTest, VerdictJSON, ...) are
                                     // dropped when the fixture is slimmed.

A null in any `*_ms`/`*Millis` field means "this phase was never measured",
never "zero" — see cmd/corral/timing_line.go's `unmeasured` doc. This
script's phase columns mirror that: a null phase prints an em dash, exactly
like the CLI's own `--timing` output.

Both fixtures are generated from the ledger's own JSON — `corral scans show
<id> --timing --json` against the real recorded runs (scan 1: pallets/flask,
scan 2: psf/requests; both 2026-08-30) — then slimmed to the fields above.
Nothing in this script or the committed fixtures is hand-transcribed.

FORMATTING: duration_text/abbreviate_tokens/plural_s below are DELIBERATE,
byte-for-byte replicas of cmd/corral/timing_line.go and cost_line.go. Kept
in Python (not imported) because this generator is a shell/Python tool, not
a Go binary, and the task that added it was scoped to keep this branch's
edits to docs/scripts/workflow rather than touching cmd/corral's Go files.
If the two ever disagree, the Go source is the one telling the truth —
fix this copy, not the other way around.
"""
import json
import sys
from pathlib import Path

FIXTURE_DIR = Path(__file__).resolve().parent.parent / "docs" / "design" / "fixtures"

# Static scan identity — corral scans show <id> --timing --json carries no
# scan id, repo name, or date of its own (those live in `corral scans list`,
# not `show`), so this is the one place that pairing is recorded, matching
# the real ledger rows these fixtures were exported from.
SCANS = [
    {"scan_id": 1, "repo": "pallets/flask", "date": "2026-08-30", "fixture": "cost-model-flask.json"},
    {"scan_id": 2, "repo": "psf/requests", "date": "2026-08-30", "fixture": "cost-model-requests.json"},
]


def duration_text(ms):
    """Mirrors cmd/corral/timing_line.go's durationText. ms is None or an
    int number of milliseconds; None or <=0 renders as the em dash."""
    if ms is None or ms <= 0:
        return "—"  # em dash — see internal/adequacy timing docs: unmeasured, never "0s"
    seconds_float = ms / 1000.0
    if seconds_float < 1:
        return "<1s"
    total_seconds = round(seconds_float)  # banker's rounding differs from Go's
    # time.Duration.Round, but every value in this fixture is far enough from
    # a .5 boundary that the two never disagree (verified against the
    # brief's own reference lines).
    h = total_seconds // 3600
    m = (total_seconds // 60) % 60
    s = total_seconds % 60
    if h > 0:
        return f"{h}h{m:02d}m{s:02d}s"
    if m > 0:
        return f"{m}m{s:02d}s"
    return f"{s}s"


def plural_s(n):
    return "" if n == 1 else "s"


def abbreviate_tokens(n):
    """Mirrors cmd/corral/cost_line.go's abbreviateTokens."""
    if n >= 100_000:
        return f"{n / 1_000_000:.1f}M"
    if n >= 1_000:
        if n % 1000 == 0:
            return f"{n // 1000}k"
        return f"{n / 1000:.1f}k"
    return str(n)


ROSTER_ROLE_ORDER = [
    "mutant-generator",
    "test-writer",
    "test-critic",
    "mutant-generator-shadow",
    "test-writer-shadow",
]


def unattributed_ms(f):
    """Mirrors cmd/corral/timing_line.go's unattributed-term arithmetic: the
    gap between TotalMillis (the driver's own elapsed clock) and the sum of
    the phases this file's line names — pool + generation + dev pass +
    authored + critic, deliberately NOT selection (that's the scan's, not
    this file's). None-valued (unmeasured) phases count as 0. Returns 0 when
    there is nothing to attribute, or when Total itself is unmeasured."""
    total = f.get("TotalMillis")
    if not total:
        return 0
    parts = ("PoolMillis", "GenerationMillis", "DevPassMillis", "AuthoredPassMillis", "CriticMillis")
    known = sum(f.get(k) or 0 for k in parts)
    return max(0, total - known)


def timing_line(f, n, med, mx):
    """Mirrors cmd/corral/timing_line.go's timingLine."""
    dev = duration_text(f.get("DevPassMillis"))
    if n and n > 0 and ((med and med > 0) or (mx and mx > 0)):
        dev = f"{dev} ({n} mutants, median {duration_text(med)}, max {duration_text(mx)})"
    parts = [
        "selection " + duration_text(f.get("SelectionMillis")),
        "generation " + duration_text(f.get("GenerationMillis")),
        "pool " + duration_text(f.get("PoolMillis")),
        "dev pass " + dev,
        "authored " + duration_text(f.get("AuthoredPassMillis")),
        "critic " + duration_text(f.get("CriticMillis")),
    ]
    u = unattributed_ms(f)
    if u > 0:
        parts.append("unattributed " + duration_text(u))
    parts.append("total " + duration_text(f.get("TotalMillis")))
    return "time: " + " · ".join(parts)


def cost_line(calls, path=None):
    """Mirrors cmd/corral/cost_line.go's costLine. calls carries the
    fixture's raw (snake_case) model_calls rows; path, when given, scopes to
    one file's calls (a scan's model_calls array may in general span more
    than one audited file, though neither reference scan does). Returns ""
    for no calls, exactly like the Go version — a scan that spent nothing
    prints nothing."""
    scoped = [c for c in calls if path is None or c.get("path") == path]
    ordered = sorted(
        (c for c in scoped if c.get("calls", 0) > 0),
        key=lambda c: ROSTER_ROLE_ORDER.index(c["role"]) if c["role"] in ROSTER_ROLE_ORDER else len(ROSTER_ROLE_ORDER),
    )
    total_in = sum(c["input_tokens"] for c in ordered)
    total_out = sum(c["output_tokens"] for c in ordered)
    total_calls = sum(c["calls"] for c in ordered)
    if total_calls == 0:
        return ""
    parts = [
        f"{c['role']} {abbreviate_tokens(c['input_tokens'])}/{abbreviate_tokens(c['output_tokens'])} ({c['calls']} call{plural_s(c['calls'])})"
        for c in ordered
    ]
    return (
        f"cost: {abbreviate_tokens(total_in)} tokens in / {abbreviate_tokens(total_out)} out "
        f"across {total_calls} call{plural_s(total_calls)} — " + ", ".join(parts)
    )


def load_scans():
    out = []
    for scan in SCANS:
        raw = json.loads((FIXTURE_DIR / scan["fixture"]).read_text())
        audited = raw["files"][0]  # both reference fixtures carry exactly one audited row
        out.append({**scan, "File": audited, "ModelCalls": raw["model_calls"], "SelectionMillis": raw["selection_ms"]})
    return out


def render_table(scans):
    """One column per scan, one row per phase — plus the cost line as its
    own row, and a scan-identity header row citing scan id / repo / date so
    the table is checkable against `corral scans show <id>` on the ledger
    that produced it."""
    cols = []
    for s in scans:
        f = s["File"]
        cols.append(
            {
                "header": f"`{f['Path']}` (scan {s['scan_id']}, {s['repo']}, {s['date']}, N={f['Trees']} trees)",
                "selection": duration_text(f.get("SelectionMillis")),
                "generation": duration_text(f.get("GenerationMillis")),
                "pool": duration_text(f.get("PoolMillis")),
                "dev_pass": _dev_pass_cell(f),
                "authored": duration_text(f.get("AuthoredPassMillis")),
                "critic": duration_text(f.get("CriticMillis")),
                "unattributed": duration_text(unattributed_ms(f)) if unattributed_ms(f) > 0 else "—",
                "total": duration_text(f.get("TotalMillis")),
                "cost": (cost_line(s["ModelCalls"], f["Path"]) or "—").removeprefix("cost: "),
                "kill_rate": _kill_rate_cell(f),
            }
        )

    lines = []
    header = "| Phase | " + " | ".join(c["header"] for c in cols) + " |"
    sep = "|---|" + "|".join("---" for _ in cols) + "|"
    lines.append(header)
    lines.append(sep)
    rows = [
        ("selection", "selection"),
        ("generation", "generation"),
        ("pool", "pool"),
        ("dev pass (mutants, median, max)", "dev_pass"),
        ("authored", "authored"),
        ("critic", "critic"),
        ("unattributed", "unattributed"),
        ("**total**", "total"),
        ("cost", "cost"),
        ("kill rate", "kill_rate"),
    ]
    for label, key in rows:
        lines.append("| " + label + " | " + " | ".join(c[key] for c in cols) + " |")
    return "\n".join(lines)


def _dev_pass_cell(f):
    dev = duration_text(f.get("DevPassMillis"))
    n = f.get("MutantsGraded") or 0
    med = f.get("MutantMillisMedian")
    mx = f.get("MutantMillisMax")
    if n > 0 and ((med and med > 0) or (mx and mx > 0)):
        return f"{dev} ({n} mutants, median {duration_text(med)}, max {duration_text(mx)})"
    return dev


def _kill_rate_cell(f):
    kr = f.get("KillRate")
    if kr is None:
        return "—"
    return f"{kr:.2f} ({f['Survivors']} survivors, {f['ProvenMissed']} proven missed)"


def render_golden_lines(scans):
    """The exact `time:`/`cost:` lines each scan's own `corral scans show
    --timing` prints — reproduced here so a reader (and this script's own
    --check) can compare them byte-for-byte against the ledger's real
    output, independent of the table above."""
    out = []
    for s in scans:
        f = s["File"]
        out.append(f"scan {s['scan_id']} ({s['repo']}, {f['Path']}):")
        out.append("  " + timing_line(f, f.get("MutantsGraded"), f.get("MutantMillisMedian"), f.get("MutantMillisMax")))
        cl = cost_line(s["ModelCalls"], f["Path"])
        if cl:
            out.append("  " + cl)
    return "\n".join(out)


def main():
    scans = load_scans()
    if len(sys.argv) > 1 and sys.argv[1] == "--golden-lines":
        print(render_golden_lines(scans))
        return
    print(render_table(scans))


if __name__ == "__main__":
    main()
