<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Adversarial review — an opinion linked to signed reproductions

**Status: designed, not built.** Written 2026-09-04, the morning after the
fifth cold review of corral itself (PRs #224–#236). Everything in "What
exists" is shipped and cited; everything in "What would be built" is not.
The one-week slice at the end is the proposal.

## Where this came from

Between 2026-09-01 and 2026-09-04 we ran five rounds of review on corral's own
repository. Each round, a model that had never seen the code — no
conversation history, no memory, not the model that had written most of that
week's changes — was handed a commit and one instruction: *assume the code is
wrong, find where, and prove it by execution*. Eleven pull requests came out
of it (#224, #225, #226, #229, #230, #231, #232, #233, #234, #235, #236),
among them a trust anchor that shipped with a default key, a "proven" bug
that could be fabricated, eleven CI gates defeated by one-line edits, and a
model registry that let the repository under audit choose its own auditors.
The field note
[We put five strangers on our own code](../../site/src/content/field-notes/the-reviewer-was-right-and-we-said-no.mdx)
is the narrative; this document is the design the narrative implies.

The reviewers were not writing tests. The trust-anchor finding was a
*derivation*: the public half of the committed seed, compared to the pinned
constant. The registry finding was a *constructed state*: a hostile
`.corral/models.json`, a dry run, the printed resolution. The ranking finding
was four rows in a DuckDB file pushed through the real reader. What they had
in common was not a test — it was **a reproduction anyone can rerun, labeled
by its evidence tier**, and a list of what was checked and found sound. The
reviewers that were wrong were wrong exactly where they skipped the
reproduction and inferred: a roadmap read as shipped, two providers read as
stubs from a grep, and — once — a verifier that dismissed a true finding on a
`grep -rn "func tickAggregate"` that cannot match a method with a receiver
(`AGENTS.md`, "Claims reviewers keep getting wrong").

So the thing worth building is not "AI code review." It is the discipline
that made those five rounds worth eleven pull requests, run by the machinery
corral already has.

## The shape: opinion → citations → signed reproductions

A review is **prose**. It says things like "the herd is not an allowlist; a
worker's self-report can staff the pool." That is a judgment, and corral does
not sign judgments — a signature on an opinion is the opinion feed with a
seal on it.

What corral signs is the **reproduction**: a script, in the repository's own
language or shell, that the jail ran against the tree at a named commit, with
its output. Each reproduction is recorded under its own record id, the way a
scan's rows are, and the review carries those ids the way an audit statement
carries `warehouseRowsSha256`: a reader goes opinion → citation → record →
reruns it. A review with no citations is visibly a review with no citations.

Three tiers, declared by the reviewer and checked by the run:

| tier | what it is | what corral does with it |
| --- | --- | --- |
| **REPRODUCED** | a claim with a script the jail ran, whose output matches the claim | recorded, signed, citable |
| **CODE-READ** | a claim with `file:line` and no execution | recorded, unsigned, citable as prose only |
| **HYPOTHESIS** | a constructed scenario the reviewer did not execute | recorded, unsigned, listed separately |

Plus a fourth list the reviewer must produce: **checked and found sound** —
what it looked at and could not break. Absence of findings in a subsystem
nobody looked at is not evidence; the sound list is what makes the scope of
the review legible.

This is the same rule the audit statement already follows for numbers. A
kill rate is signed because it was executed; an uncovered file's rate is
withheld, not signed as zero; the honesty flags travel with the number. A
reviewer's claim is signed because its reproduction was executed; a claim
without one is carried as what it is.

## The seats

Four, three of them models, and the decorrelation rule applies to all of
them exactly as it applies to the writer and the critic today.

**The reviewer.** Cold: the repository at a commit, a *scope* ("the router;
assume it is wrong; aim at what the previous round did not touch"), the
brief, and the standing list of claims reviewers keep getting wrong. Its
output is structured findings — claim, tier, `file:line`, and for anything
above HYPOTHESIS a reproduction script — plus the sound list and the opinion
that ties them together. The reviewer never fixes anything.

**The reproducer.** Not a model. The jail runs each script against the tree
and records what it printed. A script that does not reproduce demotes its
finding to CODE-READ on the record, not silently. This is the existing
scorer with a script where the test command goes; the substrate,
concurrency, timeouts and the toolchain binds are already built.

**The verifier.** A *third* model, adversarial to the reviewer: it tries to
refute every REPRODUCED finding and every CODE-READ claim. Its refutations
are tiered by the same rules, and one rule is written down because it was
learned the hard way: **a search that does not find the thing is CODE-READ,
never a refutation.** A refutation that reproduces (the script passes on the
claimed input; the claim was narrower than stated) demotes the finding and
is itself signed.

**The human.** Adjudicates the residue: a reproduction that is genuine but
narrower than the claim drawn from it (the "overstated" case), a refutation
the reviewer disputes, a HYPOTHESIS worth promoting to a scope for the next
round. The human's confirm/refute is final and feeds the scorecard; the
machine's never overrides it. This is the criticscore contract today
(`internal/criticscore`): automatic adjudication is conservative, human
adjudication is authoritative, and a later automatic pass may not overwrite
a human's.

## Rounds

Two rules from the week, both mechanical:

- **Aim at what the last round did not touch.** Round one at the gates;
  round four at the scoring engine; round five at the push, the router and
  the learning loop. A planner keeps the scopes covered and uncovered.
- **Re-attack every fix batch cold.** The second pass in round four was told
  the morning's fixes were hours old and to assume they had holes; it
  defeated eleven of them (#233). A fix does not count until a fresh seat
  has tried to defeat it. In corral's own gates this became the
  negative-control rule: every gate test must fail when the gate is
  reverted.

## The scorecard

Nothing about the prose is scored. The **citations** are:

- reviewer model × language × scope → reproduced-finding rate (REPRODUCED
  claims that held through verification and the human), false-claim rate
  (claims refuted or dismissed), and uncited-claim rate (prose with nothing
  under it);
- verifier model → wrongly-overruled rate (refutations a human reversed).

This is `models rank` (`docs/design/model-ranking.md`) with reviewer and
verifier seats added, and it inherits that command's floor: a model with
fewer than five observations in a seat is printed with its real numbers,
marked insufficient, and never preferred. The "claims reviewers keep getting
wrong" list stops being a hand-maintained section of `AGENTS.md` and becomes
the refuted-citation table, fed back to the next reviewer as input.

## What exists

Every mechanism below is shipped and in use by `certify`:

- role-keyed seats with decorrelation enforced before spend
  (`internal/advpool/roles.go`, `CheckDecorrelation`), and the registry
  that names them (`internal/models`);
- a queue that fans seats out to models and collects structured results
  (`internal/queue`), with a findings table already carrying type,
  severity, target, evidence, status and a reporter model
  (`internal/queue/findings.go`);
- a jail that runs an arbitrary command against a tree on two substrates
  with per-language toolchain binds (`internal/adequacy`, `internal/sandbox`);
- execution-based adjudication of a model's claims, with human override
  and a rule that automatic passes never overwrite a human's
  (`internal/advpool/critic_adjudicate.go`, `internal/criticscore`);
- signed records and in-toto statements whose predicates cite what was
  executed (`internal/certify`), and a warehouse whose rows cite the
  statement back (`internal/auditpush`);
- a model scorecard fed only by execution-proven outcomes, with an evidence
  floor (`internal/bugcatch`, `internal/modelrank`);
- a recurrence detector over findings (`internal/learn`).

## What would be built

- **The reviewer brief and output schema.** A prompt, and a JSON shape for
  findings with tiers, scripts and the sound list. The prompt is the one we
  used by hand; the schema is the findings table plus three fields (tier,
  script, citations).
- **Reproduction scripts in the jail.** The scorer accepts a test command;
  it would accept a script and record stdout/exit rather than kill/survive.
  Small: the jail already binds the toolchain for whatever the command names.
- **The verifier seat and its rules.** A second brief, and the tier check on
  refutations.
- **`corral review`.** `--scope <dir|subsystem>`, the seats named like every
  other seat (no defaults), `--record` into the same ledger, a printed
  opinion with its citations, a `--attest` statement whose predicate is the
  list of reproductions.
- **Reviewer and verifier rows in `models rank`.**
- **A round planner** — scopes covered, scopes not, fix batches not yet
  re-attacked. Last, because a human did this well by hand all week.

## What this is not

It is not a signed design review. The signature covers the reproductions; the
design judgment drawn from them is prose, and the product must say so on
every surface or it becomes the thing it is arguing against.

It is not per-PR review. Each of the week's reviews was roughly 220 000
tokens and 90 tool calls of a frontier model; five rounds, up to three
reviewers each. That is a launch-week audit or a release gate, not a
required check on every push. Per-PR is `certify`, which already exists.

It is not a replacement for the human. The scoping ("the push, the router,
the loop next") and the residue adjudication were done by people all week,
and the one time the machine overruled a reviewer on its own, it was wrong.

## On rigor, and the field

As of 2026-09-04 we know of several AI review tools that post findings on
pull requests. We have not found one that requires a reviewer to reproduce
what it claims before the claim is recorded, that keeps the reviewer's
refuted claims and scores the reviewer by them, or that links a review's
prose to signed, rerunnable reproductions. We may have missed one; this is
what we looked for and did not find, not a claim about any product's
internals. If it exists, the comparison worth making is on those three
properties, because they are the whole difference between a review and an
opinion.

## The one-week slice

`corral review --scope internal/brain --reviewer-model <m> --record`:
the brief, the tiers, jail-run reproductions, the findings recorded in the
existing ledger, the opinion printed with its citations, no verifier seat,
the human adjudicating in the existing criticscore store. It reuses every
seam `certify` has except the prompt and the script runner. The verifier,
the statement, the scorecard rows and the planner follow, in that order,
each only after the previous one has been run on corral itself and found
wanting by a stranger.
