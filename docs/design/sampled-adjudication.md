<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Sampled adjudication — a person rules on a declared fraction the record draws

**Status: designed, not built (2026-10-01).** Today a person adjudicates
whatever they choose to, and every finding they do not reach stays
unadjudicated forever. This note replaces "a human approves everything" with
a sample: an operator declares a rate, the record itself draws which
findings a person owes a ruling on, and every surface reports the sample
with its size and its uncertainty. Execution evidence of a fix gets its own
entry kind so that it never has to pass for a person's word.

## Where this came from

Checking the churn numbers in [adversarial-review.md](adversarial-review.md)
("Is the loop converging?") against the ledger on 2026-10-01 found that
**none of the 23 findings in rounds two and three carries an adjudication.**
Rounds two and three reproduce exactly from the ledger plus `git blame`, and the three
high-severity churn findings are REPRODUCED — a script ran and demonstrated
each one — but no person ever put their name to any of the 23. The
obvious fix, ruling on all 23 now, three weeks later, to support a public
claim, is exactly the rubber stamp the ledger exists to make impossible.

The whole ledger, counted the same day (121 entries, chain intact):

| tier | findings | ruled by a person | unruled |
|---|---|---|---|
| REPRODUCED | 96 | 41 | 55 |
| CODE-READ | 104 | 27 | 77 |
| HYPOTHESIS | 7 | 1 | 6 |
| **all** | **207** | **69 (33%)** | **138** |

Of the 69 rulings, 65 confirmed and 4 refuted. **All four refutations were
CODE-READ findings. None of the 41 ruled REPRODUCED findings was refuted.**

That looks like a measurement of how often a reproduced finding is wrong. It
is not one, and the reason is the point of this note: the 69 were not drawn,
they were *chosen* — a ruling was made when a fix was waiting on it. A
self-selected set cannot estimate an error rate, however large it grows.

## The problem, stated as three options that each fail

- **Rule on everything.** It does not happen. The 138 unruled findings are
  what that policy produces once ruling stops blocking anything. In a loop
  where agents generate findings faster than a person reads them, the gap
  only widens.
- **Rule on nothing.** Then "human-checked" means nothing, and the only
  check on a CODE-READ claim — the tier where every refutation so far has
  landed — is another model's opinion.
- **Rule on what you choose.** That is today, and it is the self-selection
  problem. *Nemo iudex* applies to the human too (see "The human's role" in
  adversarial-review.md): a person picking which claims to check is choosing
  which claims will look checked.

An auditor checking a company's books does not inspect every transaction.
They inspect a sample, drawn by a method that the company cannot steer, and
they sign for the method and the sample size. That is the shape here.

## The design

### 1. A policy entry declares the rate, before the findings exist

A new entry kind, `policy`, signed and chained like every other:

```
corral ledger policy <dir> --audit-rate 0.15
corral ledger policy <dir> --audit-rate code-read=1,reproduced=0.1,hypothesis=0.25
```

A single number applies to every tier; the per-tier form overrides it. A
policy governs **only review entries that come after it in the chain**. The chain
order is what proves the rate was fixed before the findings it samples were
known, so an operator cannot look at a review and then pick the rate that
draws the convenient findings. Changing the rate is a new policy entry; the
old one stays, and each review is governed by the newest policy that
precedes it.

No policy means rate 0, which is today's behaviour: nothing is drawn and
nothing is owed. Corral imposes no rate, the same way it imposes no models;
an operator who wants sampling declares it.

Per-tier rates are there because the tiers carry different evidence.
A REPRODUCED finding already has an executed script behind it; a CODE-READ
finding has only a model's reading. The ledger's own history says where a
person's attention catches errors. `code-read=1` — every CODE-READ finding
ruled — is a reasonable policy where a 100% rate on REPRODUCED would not be.

### 2. The record draws the sample, and anyone can redraw it

A finding is drawn when

```
u = first 8 bytes of SHA-256(policy entry hash ‖ canonical(finding)) as a uint64 / 2^64
drawn = u < rate(finding's tier)
```

where `canonical(finding)` is the canonical JSON of the fields the reviewer
produced — claim, file, line, declared tier, script.

Three properties follow, and each is needed:

- **Nobody chooses.** Not the operator, not the person who will rule, not a
  model. The draw is a function of things already fixed in the record.
- **Anyone can check it.** A stranger with the ledger branch recomputes every
  draw and sees whether each drawn finding has a ruling.
- **It is monotone in the rate.** Raising the rate adds findings to the
  sample and never removes one, so an operator who raises it later cannot
  use the change to drop a finding that was owed.

**The seed is deliberately not the review entry's own hash.** `corral ledger
append` re-hashes an entry as it re-links it to a moved head, so an entry's
hash is not fixed until it is placed, and each re-link would be a free
re-roll of the draw. The finding's content does not change when the entry
moves. Re-rolling it means re-running the reviewer.

### 3. Every surface reports the sample, never just the rulings

For each policy period and tier, the surfaces that count rulings — the
ledger page, `corral review` output, `corral brief`, `models rank` — report
the sample, not just a confirmed count:

```
REPRODUCED, 10% sample: 8 drawn · 7 ruled (7 confirmed, 0 refuted) · 1 owed
    refuted rate: 0 of 7, 95% upper bound 35%
```

The interval is the Wilson score interval. It is printed even when it is
embarrassingly wide, because at n = 7 the honest statement is that the rate
could plausibly be a third. For scale: had the 41 ruled REPRODUCED findings
above been a random draw, 0 of 41 would bound the refuted rate below 8.6%.
They were not a random draw, so that figure is *not* a claim this note
makes. It shows what a claim from a real sample would look like.

A drawn finding with no ruling is **owed** and rendered as owed. It is never
counted as confirmed, never dropped from the denominator, and never
rendered as a pass. This is the repository's standing rule about
measurements: a could-not-measure must not render as a measured zero.

Rulings on findings the draw did not pick remain allowed. A person can
always rule on anything, and a ruling is never refused. They are reported
separately, as **off-sample**, and never enter the estimate, since they
are the self-selected set again.

`corral review audits <dir>` lists what is owed: every drawn, unruled
finding, oldest first. `corral ui --write`, which already records
adjudications, is where the same queue belongs for a person. Nothing
blocks a review or a merge on an owed ruling; the cost of an owed ruling is
that every surface says it is owed.

### 4. A fix proven by execution gets its own entry kind: `closure`

`corral review recheck` already answers "is this fixed?" by execution: it
re-runs a finding's recorded script in a disposable worktree and reports
`still-reproduces`, `no-longer-reproduces` or `could-not-run`. Today it
prints that and writes nothing. A person who wants it on the record quotes it
in an adjudication's reason, which turns an execution fact into a
person's word.

`corral review recheck --record --fix <commit>` would write a `closure` entry
instead, carrying a **bracket**: the script exits 0 at `<commit>^` (still
reproduces) and non-zero at `<commit>` (no longer does), with both outputs.
The bracket names the commit that closed the finding, not just "it is gone
by HEAD".

A closure is a different kind of fact from an adjudication and is displayed
as one. The ledger page shows three states, never merged:

| state | means | written by |
|---|---|---|
| confirmed / refuted | a named person ruled | `review adjudicate` |
| closed | a script stopped demonstrating the defect at a named commit | `review recheck --record` |
| unruled | neither | — |

**The closure's known weakness, stated up front:** "no longer exits 0" is
not "fixed". A script that stops *compiling* — a renamed function, a moved
package — also exits non-zero. `recheck` already refuses to read 126 and 127
(the shell's "not executable" and "not found") as a result, but a Go build
failure exits 1, and so does a genuine fix. Two mitigations, neither of them
complete: the closure records the output, so a build error is visible to
anyone who looks. And closures can be sampled at their own rate
(`--audit-rate closure=0.2`), so that a person checks a drawn fraction of
the machine's "fixed" verdicts, the same way they check a drawn fraction of
the reviewer's claims.

## What this does not solve

- **Grinding.** An operator can run a review many times without appending
  it and append only the run whose draw is convenient. The draw is only as
  fair as the discipline of appending every review that ran. The ledger
  cannot see a review that was never appended. This is the same exposure as
  not pushing a scan. `corral review` already writes its entry by default
  (`--no-ledger` opts out), but only to a local directory. Carrying it to
  the public branch is a separate push, and that push is the step where
  round four's review was lost. Stated, not solved.
- **The rulings themselves are not checked.** A sample tells you how often
  the machine was wrong *according to the person*. Whether the person was
  right is a second-reader problem, a sample of the sample by a second
  principal, and is out of scope here.
- **Small samples stay small.** At the volume this project produces, a 10%
  rate yields a handful of rulings a month. The interval will be wide for a
  long time. Reporting it wide is the design working; hiding it would be the
  failure.

## Existing findings: a declared backfill, not retroactive rulings

The 138 unruled findings predate any policy, so the draw does not apply to
them. Reaching for them to support a claim is the self-selection this note
exists to remove. The honest route is a **backfill policy**:

```
corral ledger policy <dir> --backfill --audit-rate reproduced=0.25,code-read=0.25
```

It draws over every review entry *before* it, by the same hash rule. Once
the policy entry is in the chain, its hash is fixed and so is the sample. It
is labelled backfill everywhere it is reported, since rulings made weeks
after the fact are weaker evidence than rulings made in-round, and a reader
should know which they are looking at.

This is what would put a person on some of round three's findings without
choosing which: the draw picks, and the published claim cites the sample
alongside the blame numbers.

## Build order

1. **`policy` kind and the draw.** The entry kind in `internal/auditpush`
   next to `KindAdjudication`, the draw as one exported function there (every
   surface calls it; none re-derives it), and `corral review audits`. A
   negative control first: a test that a finding drawn and unruled is
   reported owed, and is never counted as confirmed.
2. **Surfaces.** The ledger page (`scripts/ledgersite`), `review`, `brief`
   and `models rank` report sample, owed, off-sample and the interval. A
   test that the owed count survives each boundary, because a
   field-by-field converter fails open on whatever field nobody remembered
   to add.
3. **`closure` kind** via `review recheck --record --fix`, with the bracket.
4. **Backfill**, last, because it is the only step that touches findings
   already on the record.

Each step is useful alone. Step 1 already turns "we rule on what we get to"
into a declared, checkable obligation.
