#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/gen-before-after.py — renders the before/after comparison of the
two psf/requests runs (docs/design/fixtures/before-after-requests-before.json
and -after.json) into the Markdown between the `<!-- before-after:start -->`
and `<!-- before-after:end -->` markers of docs/design/before-and-after.md,
and the same tables into site/src/content/field-notes/what-is-actually-new-here.mdx
between the same markers.

Both fixtures are `corral scans show <id> --timing --json` against the ledger
each run wrote, slimmed to the audited rows and the fields below. Nothing
here is hand-transcribed; the numbers are the ledger's. The two runs:

  before: main @ 925dddc, 2026-09-04 10:42 local, `--top 3` (which, before
          #244, widened past its bound to 11 jobs), flat exam (5 per seat).
  after:  main @ d951419, 2026-09-04 17:56 local, `--top 3` honoured (3
          files), complexity budget (#247), authored pass alone (#247),
          confidence terms (#248), plus --shadow-writer-model claude-sonnet-5.
  primed: main @ 7f49ddf, 2026-09-05 11:38 local, the after-run's herd plus
          --prior on that run's ledger and mutant set (#251), the per-command
          mutant cap and the exam-size rule (#250). All three files primed.

Same commit of requests (414f051), same herd (gemini-3.6-flash generator /
writer / deriver, claude-haiku-4-5 critic), same 30-minute per-file timeout.

THE RULE THIS PAGE MUST NOT BREAK: the kill rates are not a before/after of
the TESTS — the exam changed (the budget column says how), so a rate over 39
mutants and one over 40 differently-placed mutants are different
measurements. Comparable across the runs: wall clock, convergence, proven
gaps, the interval widths. The before-run's reach is "not recorded": mutant
spans were not stored until #248.

duration_text/wilson below mirror cmd/corral/timing_line.go and
internal/advpool/exam.go; if they ever disagree, the Go is the truth.
"""
import json
import math
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = ROOT / "docs" / "design" / "fixtures"
RUNS = [
    {"key": "before", "label": "before (main @ 925dddc)", "fixture": "before-after-requests-before.json"},
    {"key": "after", "label": "after (main @ d951419)", "fixture": "before-after-requests-after.json"},
    {"key": "primed", "label": "primed (main @ 7f49ddf, --prior on the after-run's record)", "fixture": "before-after-requests-primed.json"},
]


def duration_text(ms):
    if ms is None:
        return "—"
    s = int(round(ms / 1000))
    if s < 60:
        return f"{s}s"
    m, s = divmod(s, 60)
    if m < 60:
        return f"{m}m{s:02d}s"
    h, m = divmod(m, 60)
    return f"{h}h{m:02d}m"


def wilson(killed, graded, z=1.959964):
    if not graded:
        return None
    n = float(graded)
    p = killed / n
    denom = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / denom
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / denom
    return max(0.0, centre - half), min(1.0, centre + half)


def load(run):
    d = json.loads((FIXTURES / run["fixture"]).read_text())
    files = sorted(d["files"], key=lambda f: f["Path"])
    return d, files


def per_file_rows(run, d, files):
    rows = []
    for f in files:
        n = f.get("MutantsGraded") or 0
        killed = max(0, n - (f.get("Survivors") or 0))
        band = wilson(killed, n)
        rate = f"{f['KillRate']:.2f}" if f.get("KillRate") is not None else "—"
        if band:
            rate += f" ({band[0]:.2f}–{band[1]:.2f}, n={n})"
        budget = f.get("MutantBudget")
        budget_txt = f"{budget} ({f.get('MutantBudgetRule')})" if budget else f"{f.get('MutantsTotal') or n} (flat, 5 per seat)"
        if f.get("Symbols") is not None:
            reach = f"{f['SymbolsProbed']}/{f['Symbols']} symbols"
            if f.get("Decisions"):
                reach += f", {f['DecisionsProbed']}/{f['Decisions']} decisions"
            else:
                reach += ", no decision points"
        else:
            reach = "not recorded"
        status = "timed out" if f.get("TimedOut") else "converged"
        if f.get("PriorsApplied"):
            status += f", primed ({f['PriorsApplied']} prior edits)"
        rows.append((f["Path"].replace("src/requests/", ""), budget_txt, rate, f.get("Survivors", 0), f.get("ProvenMissed", 0),
                     reach, duration_text(f.get("DevPassMillis")), duration_text(f.get("AuthoredPassMillis")),
                     duration_text(f.get("TotalMillis")), status))
    return rows


def totals(d, files):
    audited = len(files)
    converged = sum(1 for f in files if not f.get("TimedOut"))
    proven = sum(f.get("ProvenMissed") or 0 for f in files)
    planted = sum(f.get("MutantsGraded") or 0 for f in files)
    wall = sum(f.get("TotalMillis") or 0 for f in files) + (d.get("selection_ms") or 0)
    calls = sum(c["calls"] for c in d["model_calls"])
    tin = sum(c["input_tokens"] for c in d["model_calls"])
    tout = sum(c["output_tokens"] for c in d["model_calls"])
    return audited, converged, proven, planted, wall, calls, tin, tout


def by_model(d):
    agg = {}
    for c in d["model_calls"]:
        k = (c["role"], c["model"])
        a = agg.setdefault(k, {"calls": 0, "in": 0, "out": 0, "wall": 0})
        a["calls"] += c["calls"]
        a["in"] += c["input_tokens"]
        a["out"] += c["output_tokens"]
        a["wall"] += c["wall_ms"] or 0
    return agg


def abbreviate(n):
    if n >= 1_000_000:
        return f"{n/1_000_000:.1f}M"
    if n >= 1_000:
        return f"{n/1_000:.1f}k"
    return str(n)


def render():
    out = []
    out.append(MD_START)
    out.append("")
    out.append("**Two runs of `corral certify --repo` on `psf/requests@414f051`, same herd, same 30-minute per-file timeout, 2026-09-04.** "
               "Generated from the two runs' own ledgers by `scripts/gen-before-after.py`; never hand-edited.")
    out.append("")
    out.append("| run | files audited | converged | proven gaps | mutants graded | time in audited files | model calls | tokens in / out |")
    out.append("|---|---|---|---|---|---|---|---|")
    loaded = {}
    for run in RUNS:
        d, files = load(run)
        loaded[run["key"]] = (d, files)
        a, c, p, pl, w, calls, tin, tout = totals(d, files)
        out.append(f"| {run['label']} | {a} | {c} of {a} | **{p}** | {pl} | {duration_text(w)} | {calls} | {abbreviate(tin)} / {abbreviate(tout)} |")
    out.append("")
    out.append("*Time in audited files* sums each audited file's own phases plus the one selection pass; the runs' clock times — "
               "**4h15m** before, **1h20m** after, **1h22m** primed, from the launcher logs — are longer by the files the scan probed and then could not grade "
               "(three baseline failures before, none after) and by setup nothing attributes to a file.")
    out.append("")
    out.append("The kill rates below are **not** a before/after of requests' tests: the exam changed (the *mutants* column says how — a flat five per seat "
               "became a complexity-derived budget), so a rate over one exam is not comparable to a rate over the other. What *is* comparable across the runs: "
               "wall clock, whether a file converged, the gaps proven by execution, and the width of each rate's 95% interval. The before-run's reach reads "
               "*not recorded* because mutant spans were not stored until #248.")
    out.append("")
    for run in RUNS:
        d, files = loaded[run["key"]]
        out.append(f"### {run['label']}")
        out.append("")
        out.append("| file | mutants | kill rate (95% interval) | survivors | proven | reach | dev pass | authored | total | |")
        out.append("|---|---|---|---|---|---|---|---|---|---|")
        for r in per_file_rows(run, d, files):
            out.append("| " + " | ".join(str(x) for x in r) + " |")
        out.append("")
    # the one file both runs audited
    before_paths = {f["Path"]: f for f in loaded["before"][1]}
    after_paths = {f["Path"]: f for f in loaded["after"][1]}
    primed_paths = {f["Path"]: f for f in loaded["primed"][1]}
    common = sorted(set(before_paths) & set(after_paths))
    if common:
        out.append("### The file both runs audited")
        out.append("")
        out.append("`--top 3` chose different files before and after (#244 stopped evidence widening from adding files past the bound, and stopped "
                   "`tests/utils.py` from out-ranking the library file it was named after), so only these appear in both:")
        out.append("")
        out.append("| file | | mutants | kill rate (95% interval) | survivors | proven | authored phase | total | |")
        out.append("|---|---|---|---|---|---|---|---|---|")
        for p in common:
            for idx, (key, paths) in enumerate((("before", before_paths), ("after", after_paths), ("primed", primed_paths))):
                if p not in paths:
                    continue
                row = per_file_rows(RUNS[idx], loaded[key][0], [paths[p]])[0]
                out.append(f"| {row[0]} | {key} | {row[1]} | {row[2]} | {row[3]} | {row[4]} | {row[7]} | {row[8]} | {row[9]} |")
        out.append("")
    # cumulative reach across the after and primed runs
    cr = loaded["primed"][0].get("cumulative_reach") or {}
    if cr:
        out.append("### Cumulative reach — what the prior bought")
        out.append("")
        out.append("Decision points a fault landed on, per run and across both, from the recorded mutant spans of the after-run and the primed run "
                   "against the extractor's decision spans. If the prior had done nothing, the union would sit near the larger of the two; it sits near their sum.")
        out.append("")
        out.append("| file | decision points | after-run reached | primed run reached | **both runs together** |")
        out.append("|---|---|---|---|---|")
        for path in sorted(cr):
            c = cr[path]
            out.append(f"| {path.replace('src/requests/', '')} | {c['decisions']} | {c['after']} | {c['primed']} | **{c['union']}** |")
        out.append("")
    # by model
    out.append("### By model")
    out.append("")
    out.append("Per seat, from `scan_model_calls`. The after-run adds a **shadow writer** (`claude-sonnet-5`) that attacked the *same survivors* as the primary "
               "writer in the *same run* — the only comparison between two models that is controlled. Its per-file outcome is on the run log "
               "(`the challenger writer … proved N of M survivor(s)`); the ledger records the pair's overlap only when the union of both writers' misses "
               "reaches the minimum the coefficient needs, and on these files it did not (both writers proved nearly everything), so the Jaccard column is "
               "honestly empty rather than a number over two misses — until the primed run's `adapters.py`, where the two writers' misses "
               "reached the minimum and the coefficient was computed: both missed 1 of the 5 either missed, Jaccard 0.200.")
    out.append("")
    out.append("| run | seat | model | calls | tokens in / out | model wall clock |")
    out.append("|---|---|---|---|---|---|")
    for run in RUNS:
        d, _ = loaded[run["key"]]
        for (role, model), a in sorted(by_model(d).items()):
            out.append(f"| {run['key']} | {role} | `{model}` | {a['calls']} | {abbreviate(a['in'])} / {abbreviate(a['out'])} | {duration_text(a['wall'])} |")
    out.append("")
    out.append(MD_END)
    return "\n".join(out) + "\n"


# Markdown takes an HTML comment as a marker; MDX refuses `<!--` and takes
# a JSX comment instead. Same block, two spellings of the fence.
MD_START, MD_END = "<!-- before-after:start -->", "<!-- before-after:end -->"
MDX_START, MDX_END = "{/* before-after:start */}", "{/* before-after:end */}"


def splice(path, block):
    text = path.read_text()
    start, end = (MDX_START, MDX_END) if path.suffix == ".mdx" else (MD_START, MD_END)
    body = block.rstrip("\n")
    if path.suffix == ".mdx":
        body = body.replace(MD_START, MDX_START).replace(MD_END, MDX_END)
    i, j = text.index(start), text.index(end) + len(end)
    return text[:i] + body + text[j:]


def main(argv):
    block = render()
    targets = [ROOT / "docs" / "design" / "before-and-after.md",
               ROOT / "site" / "src" / "content" / "field-notes" / "what-is-actually-new-here.mdx"]
    check = "--check" in argv
    drift = False
    for t in targets:
        new = splice(t, block)
        if check:
            if new != t.read_text():
                print(f"FAIL: {t.relative_to(ROOT)} has drifted from the fixtures — run scripts/gen-before-after.py", file=sys.stderr)
                drift = True
        else:
            t.write_text(new)
            print(f"wrote {t.relative_to(ROOT)}")
    return 1 if drift else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
