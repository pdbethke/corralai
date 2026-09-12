<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Grading fixes — an eval whose answer key corral already wrote

**Status: designed, not built (2026-09-12). The grader exists; the write-capable
seat does not. Prior art is [SWE-bench](https://www.swebench.com/), and the
differences from it are the whole argument for building this.**

## Where this came from

On 2026-09-12 two review rounds ran back to back on `internal/transparency` and
`internal/gate`, the second re-attacking the first's fix batch. Blaming each
finding's own `file:line` at the commit its round reviewed splits the findings
into **drain** (the defective line predates the last fix) and **churn** (the
last fix authored it). See "Is the loop converging?" in
[adversarial-review.md](adversarial-review.md) for the numbers.

The result that produced this document: **5 of round three's 13 findings were
created by round two's own fix, and those five were more severe than the eight
pre-existing ones** — 3 high and 2 medium, against 1 high and 7 lower. Ten
fixes in one commit introduced three high-severity regressions.

That raises a question nobody has an execution-proven answer to: **is a given
model any good at FIXING?** Not at writing code from a blank file — at closing
a specific known defect in code it has never seen, without breaking anything
else and without introducing a worse defect than the one it closed.

An attempt to answer it from git history alone is recorded below under
"Why the retrospective version does not work", because it is instructive and it
is wrong.

## What makes this cheap: the answer key is already written

Corral demands that any claim a reviewer calls REPRODUCED arrive with a script
that **exits 0 only if the defect is demonstrated**, and corral runs the script
rather than reading it. Every held finding therefore carries its own oracle.

So grading a fix requires no new judgment, no hidden test suite, and no model's
opinion:

- before the fix, the script exits 0 (that is what "reproduced" means)
- after a correct fix, the same script must exit non-zero

That is the expensive half of an eval, built already, as a side effect of a
rule adopted for a different reason. As of this writing the public branch holds
**42 claims declared REPRODUCED, of which 36 held** — 36 findings with working
scripts, which clears `models rank`'s five-observation evidence floor for
several seats at once. The 6 that were demoted are useful in their own right:
they are cases where a reproduction was wrong while its claim was right.

## The shape: three mechanical facts per fixer

For one finding and N fixer seats, each seat gets a disposable worktree at the
finding's own commit and one instruction: make the reproduction stop
demonstrating the defect, without breaking the suite. Then:

1. **Closed** — the finding's own script no longer exits 0.
2. **Intact** — the repository's declared test command still passes.
3. **Clean** — a cold reviewer, blinded to which model produced the patch,
   finds nothing new in the files the patch touched.

Nothing in that list is a model's opinion about a model. (1) and (2) are exit
codes. (3) is the same adversarial loop corral already runs, pointed at a diff,
and it is the fact SWE-bench does not collect.

A fourth fact is worth recording without grading on it: **how much the patch
touched.** A fix that closes the defect by deleting the feature satisfies (1)
and (2), and the diff size is what exposes it.

## The seats

Reviewer and verifier seats are **read-only** on purpose today —
`claude-code` runs `--tools Read,Grep,Glob`, `codex` runs `--sandbox
read-only`. A fixer seat must write, so it is a new seat kind, not a new entry
in `builtinAgents`.

Two rules it does not get to break:

- **It must not edit the reproduction script, the test command, or the
  finding.** That is the obvious cheat and the first thing to gate. The script
  is re-read from the ledger entry after the patch, and a patch that touches it
  is a refused run, not a failing one — the distinction matters, because a
  refusal is not evidence about the model's fixing ability.
- **It runs in the jail, not a temp directory.** A write-capable agent with a
  checkout is a larger surface than a read-only one, and
  `--dangerously-skip-permissions` is already load-bearing for one existing
  seat in a read-only role.

## Blinding

The reviewer in step (3) must not know which model wrote the patch, or its
prior about that model becomes part of the measurement. Blinding is cheap:
present the diff without commit metadata, and strip `Co-Authored-By` trailers.

The ledger entry records the mapping, so the result is reproducible after the
fact — blinded at judgement time, attributable afterwards.

## Why the retrospective version does not work

The tempting shortcut is to skip the experiment and mine git history: 1,259 of
this repository's 1,477 commits carry a `Co-Authored-By` model trailer, so
every finding's blamed line can be attributed to the model whose session
authored it. That was tried. The table it produces should be believed by
nobody, and the reasons are the design constraints for the real thing:

- **Blame attributes to the most recent toucher.** A fix that rewrites a region
  inherits blame for logic that predates it, so the newest model always looks
  guiltiest. In a repository under active fixing this is an artifact, not a
  finding.
- **The models were not doing the same job.** One wrote greenfield code in
  July; another patched already-reviewed code under review pressure in a single
  afternoon. Patching a hardened file is the harder task, and a retrospective
  comparison hides that.
- **The findings are not a random sample.** Round three was deliberately
  aimed at one model's fixes, so its lines are over-represented by
  construction.
- **Numerator and denominator do not span the same thing.** Findings come from
  all of history; lines are the ones still present. A model whose code was
  later replaced keeps its defects and loses its denominator.
- **Co-authorship is not authorship.** The trailer says a session used a model.
  A person was also there.

An eval fixes all five at once: same finding, same starting commit, same
instruction, blinded judgement, attribution recorded rather than inferred.

## What this is not

**Not SWE-bench, and not a competitor to it.** SWE-bench is a fixed public
corpus, which means it is contaminated by now and measures something closer to
recall of a known solution. Findings here are generated fresh from the
operator's own repository by an adversary that has never seen it, so there is
nothing to have trained on, and the question it answers is local: *which model
should be allowed to touch THIS code.* It also collects the fact SWE-bench does
not — whether the patch introduced something worse than it fixed.

**Not corral acquiring a builder.** The stance is that the auditor never
builds, and this does not change it: the fixer models are the **audited party**,
exactly as the operator's test suite is the audited party in `certify`. Corral
is not employing a builder; it is examining fix commits, with the same
separation it demands everywhere else — the seat that fixes is never the seat
that judges, by rule. This is a founder call and is recorded here as one.

**Not a leaderboard.** A result is about a model on a scope, in a repository, at
a size of patch. `models rank`'s existing discipline applies: fewer than five
observations prints an insufficient-evidence marker rather than a rank.

## What exists

- the oracle: every held REPRODUCED finding's script, and the outcome of
  running it, on the ledger
- the disposable worktree the scripts already run in
- the cold review loop, seat definitions, and the demote-only tier rule
- ledger entry kinds for reviews, findings and adjudications, and
  `models rank` over them

## What would be built

- a **write-capable seat kind**, jailed, with the script/test-command/finding
  files off limits
- a **fix harness**: for a finding hash and N seats, produce a patch per seat
  and record the three facts plus the diff size
- **blinded review** of each patch, reusing `corral review` against the touched
  files
- a **ledger entry kind** for a fix attempt, so the result is a signed row like
  everything else, and a `models rank` metric over it
- **cost bounding** from the start: N seats × (fix + suite + cold review) per
  finding is the most expensive thing corral would do

## The first slice

One finding hash, N seats, three facts printed. No ledger kind, no blinding, no
ranking.

The reason to cut it there is that the risky unknown is entirely in the first
step — whether write-capable seats behave usefully when told to close a
specific defect in unfamiliar code. Everything after it is bookkeeping over
machinery that already runs. If the seats thrash, the rest is not worth
building, and one afternoon establishes which.
