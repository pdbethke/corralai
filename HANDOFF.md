# Handoff — 2026-10-01 (supersedes 2026-09-30, kept below)

Working note from a long session. Snapshot of what landed, what was *measured*,
what I got *wrong*, and what is actually blocking. Not a permanent doc — delete
or fold into the design docs freely.

Base: `main` @ `08f9d1f`. Open: PR #354.

## Pick up on your own machine (Warp)

Everything below is pushed. Nothing lives only in the cloud container.

```bash
cd ~/PycharmProjects/corralai
git fetch origin
git checkout docs/handoff-2026-09-30          # this note
git log --oneline origin/docs/lead-with-the-refusal -1   # PR #354, in flight
```

To keep working with an agent there, run `claude remote-control` in that folder
(or use the Claude Desktop app); the session then shows up in the Claude Code
app. A fresh session should read this file first.

**What your machine can do that the cloud container could not** — each of
these was blocked here, and each one gates a decision below:

| blocked in the container | why it matters |
|---|---|
| `docs.warp.dev`, `www.warp.dev` (egress policy) | verify the two Oz claims below before saying anything public about Warp |
| bwrap / a docker daemon | run `certify` end to end; the container could only prove `review` *starts* |
| `claude` / `codex` CLIs | run a whole `corral review` round, not just to the seat boundary |
| Chromium 1228 (container had 1194) | the site's Playwright ran here only via an uncommitted config override |

## Since 2026-09-30

### Positioning — PR #354, open

**"Everyone is building build harnesses. Corral is the one that refuses to
build."** Stated precisely, because a reader will push back in one reply:
corral **never authors the code it judges** — the faults it plants are probes,
discarded once scored; the tests it writes are evidence, each proven against
its own fault and **handed back** (not thrown away).

#354 puts that in the README H1 and first paragraph, the page `<title>` and
meta description, and the hero `<h1>`. **It reverses a test-pinned decision**
(`'the hero leads with the house question'`): the house question is demoted to
open the hero's lead, not deleted, and the one pin becomes two so neither line
can drift. Full e2e: **76 passed, 2 skipped, 0 failed** (the skips are
pre-existing conditional ones in `recordings.spec.ts`).

The thinking that produced the line, briefly: corral is an **auditor**, not an
evaluator; its sharpest buyer question is already in `fix-eval.md` — *"which
model should be allowed to touch THIS code"*; vendors could use it for
version-over-version regression and as **verifiable rewards** (an oracle that
exits 0 iff the defect is present is a reward function), at the cost that
data trained on stops being clean evaluation data.

### Seat selection — took over the parallel session; DECISION PENDING

That session designed Thompson-sampling and Fugu-style-judge seat selection. It
pushed nothing. Picking it up turned up a prior decision it had missed:

- **`model-ranking.md`: "Disclosure, never selection."** Born of a production
  incident: *performance statistics overrode the configured model list and
  re-selected a retired model, permanently … the routing was "earned", which is
  precisely why nobody looked at it.*
- **The brain already re-seats verdict roles** (`advpool.go:352`) — greedy,
  fenced by the herd allowlist and an evidence floor.

Verified in code: the **shadow seat never gates** (`driver.go:179`); shadow
rows are flagged (`bugcatch_observations.shadow`); the **local scorecard
records no language** (no `lang` column — `model-ranking.md` says so too).

Proposal: **(a) Thompson in the shadow seat only.** Verdict seats stay
operator-named; the judge *proposes* a lineup, per run, reusing
`internal/mission/routing.go`'s `Sense → Judge → Clamp`. The shadow seat is the
randomized comparison arm that has to exist from day one, and it already
exists. Thompson also addresses the incident's mechanism — greedy routing that
never re-tests an early winner.
Alternative **(b)**: also replace the brain's greedy verdict re-seater with
Thompson, every fence kept. Touches verdict seats, so it needs an explicit call.

**Not yet written as a design doc** — waiting on (a) vs (b). Slice one either
way: add `lang` to `bugcatch_observations`.

### Warp / Oz

Oz is Warp's cloud orchestration for coding agents (Claude Code, Codex, Warp
Agent), triggered from webhooks, cron, Slack or CI. A build harness. `oz-for-oss`
describes agents that triage, spec, open PRs and *review* PRs — review as an
agent's opinion — and **no verification of output correctness**: no tests run
against agent output, no regression detection, no measured comparison, no
tamper-evident record. "Auditable" appears to mean observability.

The angle: corral is the **step after** Oz, not a competitor — Oz builds, corral
judges, and Oz drives the same CLIs corral's `review` already seats.
**Unverified** (docs blocked here): whether the Oz control plane itself is open
source, or only the client and `oz-for-oss`; and whether "auditable" includes
any verification step. Check both before relying on either.

### A lesson worth keeping

The site's first e2e run reported **26 failed** — every one a browser that
could not launch (version mismatch), so the new pins had not run at all. A
summary that says "2 passed" is not a pass. Read the full counts.

## Blocking, in order

1. **Post the churn finding.** Still not done; still needs no build.
2. **Correct PR #350's body** — `corral doctor` exits 1 correctly; my claim it
   exits 0 was `$?` read after a pipe.
3. **Merge or revise #354.**
4. **Decide seat selection (a) or (b)**, then add `lang` to the scorecard.
5. **The write-capable seat** (#340) — the only missing fix-eval component.
6. **Verify the Oz claims** from your machine.
7. **Publish the ledger's public key.**
8. 2 dependabot alerts on `main` (1 high, 1 moderate).

---

# Handoff — 2026-09-30

Base at the time: `main` @ `08f9d1f`.

## Merged this session

| SHA | PR | What |
|---|---|---|
| `6804178` | #351 | Claude Code plugin marketplace — installed and verified end to end |
| `232d807` | #352 | README install line, written only after the public form actually ran |
| `1e58afb` | #350 | The ledger record rendered as a public page (`site/public/ledger/`) |
| `08f9d1f` | #353 | Seat records whether it named a **model** or only the **tool** |

## Measured, by execution, not inferred

**`corral review` runs where `certify` cannot start.** Verified in a container
with **no bwrap, no docker daemon, no API keys**: review resolved the repo and
commit, scanned 42 files, set up the seat, and failed only because the `codex`
CLI was absent. Its substrate is *"a detached worktree at the commit; not a
jail."* (Proven to the seat boundary only — no agent CLI present to finish a
round.)

The asymmetry this exposes, which recurred all session:

| | `certify` | `review` |
|---|---|---|
| `--goal` (unvalidated free text, steers 3 seats, absent from scoring) | required | none |
| test-pairing ceiling (flask: 9 auditable of 236) | binds | none |
| credentials | 2 vendors' API keys, metered | runs on a Pro/Max/Plus subscription |
| needs a jail | yes | no |
| **starts in a container / Codespace / most CI** | **no** | **yes** |

`review` is simultaneously the usable surface, the free surface, the research
surface and the demonstrable one — and it gets 60 README lines against
certify's 947 (Quickstart 430 + Action 314 + flags 203, of 1,251 total).

**The record, as rendered:** 121 entries, 50 reviews, 207 findings, **97
reproduced by execution**, 65 human-confirmed, 4 refuted, **47 of 50 verified
cross-vendor** (anthropic/google/openai, both directions, zero undetermined).

**Authorship has no vendor variance.** ~1,280 of 1,490 commits carry a Claude
co-author trailer; nothing else meaningful. So "are models blind to their own
lineage's defects?" **cannot be asked on this repo** — no comparison group.
That makes a **foreign corpus necessary, not optional**. Related: the confound
runs *against* the churn finding (Claude reviewers hunting Claude-written
fixes would under-detect), so **38% is a floor, not a ceiling**.

**Candidacy is Python-specific, not universal** (foreign sweep, `--dry-run`):
rubocop 737 of 2197 · gin 29 of 130 · aisuite 36 of 746 · flask 9 of 236 ·
express 0 by convention, 6 with a `--tests` map. `--dry-run` costs **nothing** —
no jail, no model call, no money.

## Corrections — kept visible, per this repo's own rule

- **`corral doctor` exits 1 on failure. It is correct.** My earlier "exits 0"
  was a shell error: `$?` after a pipe reports `head`, not `corral`. **That
  false claim is in merged PR #350's body**, under "Not addressed here". It
  should be corrected there; I offered and it was never actioned.
- I asserted the README was full of frozen-era residue. It is not — the brain
  section is 14 lines and already labelled optional. I guessed, then checked.
- I repeated *"almost nobody measures what the fix broke"* from #339's own note
  without verifying it. **Verify before it goes in anything public** — the eval
  landscape is crowded and this is the line a researcher will attack.
- **#333 (finding origin) is NOT a prerequisite for fix-eval.** Blame-based
  origin was only needed for the *retrospective* analysis. Prospectively you
  know what is new by construction: review the touched files after the patch.

## What is ready and unpublished

- **Post text** on the churn finding — drafted, caveated, in the session log.
- **Chart** — `docs/design/fixtures/` has nothing; the rendered churn-severity
  chart (light + dark + HTML source) was delivered to the operator directly.
  Palette validated: all six checks pass in both modes.
- **Ledger page** — live at `corralai.dev/ledger/`.

## The conclusion I would not re-litigate

Zero external traction is the central fact, and it is ~3 months old (day zero
2026-07-03), not a year. It will not change from more building. The one lever
that does not require anyone to care about corral first is **publishing the
churn finding**, because it is a claim about **models**, not about this tool.

Everything needed to publish exists. Every further experiment discussed —
app-build, PRs, well-regarded code, feature requests, more UI — was more
interesting than posting, and none of them return information.

## Blocking, in order

1. **Post the churn finding.** No build. Verify the round two/three numbers
   against the ledger first.
2. **Correct PR #350's body** re: doctor.
3. **The write-capable seat** (#340) — the *only* missing component for
   fix-eval. Everything else exists: `review recheck` already re-runs a
   finding's script in a disposable worktree (CLOSED), the jail runs suites
   (INTACT), `review --scope` is the cold read (CLEAN), 36 held findings are
   the task set. Days, not weeks.
4. **Publish the ledger's public key** — strangers can verify the chain's
   integrity but not its signatures.
5. 2 dependabot alerts on `main` (1 high, 1 moderate).

## Open issues, unchanged

#333 origin · #334 review priors · #335 refuted-claims channel · #336
yield-ordered scope · #337 gate the one-door class · #340 fix-eval · #201 time
reports a phase as "—" and excludes it from the total.
