# Show HN — ready-to-post draft

> Drafted from show-hn-five-languages.md + the "delegate the labor" field note + the
> real more-itertools runs. Register: helpful builder (Ebert affection), not hot-take.
> Everything below is true. Posting is yours — this is the paste-ready text.
>
> **NUMBERS STATUS:**
> 1. The **5-language validator table** was regenerated 2026-07-20 Claude-default (sonnet
>    mutant+writer, haiku critic), single-generator / 5 mutants each: Python 1/5, Ruby 2/5,
>    JavaScript 2/5, TypeScript 0/5 (Go gappy 2/5). Current. They move run-to-run (which
>    faults get planted is non-deterministic) — quote the shape (universal blind spot, TS
>    worst), not the decimal, and re-run if posting is more than a few days out.
> 2. The **more-itertools kill-rate** (~39 faults / 90% below) is the 2026-07-19 Haiku-critic
>    run — the run that produced the hallucination beat, so keep it as the narrative anchor.
>    It moves run-to-run; quote the shape. (A fresh Claude-default re-run is noted at the
>    bottom of this file if you want a current figure.)
>
> **NOW LIVE since drafting (fold in — these strengthen the post, see the marked edits):**
> - The more-itertools **reliable-critic recording is live** at corralai.dev/recordings
>   (the `more-itertools-reliable-critic` tape) — a stronger, replayable asset than a
>   local capture, and it closes the hallucination arc (a cross-vendor Gemini-3.1-Pro
>   critic on the SAME file did NOT repeat the islice hallucination and found two *real*
>   dead checks instead).
> - **Critic accuracy is now a recorded metric** — `corral scorecard` carries a critic
>   precision (C-PREC) column and `corral criticscore` adjudicates each critic finding
>   real-vs-hallucination by execution. So the hallucination isn't just fenced off; it's
>   *scored*. (Shipped + deployed.)

## Title (recommend #1)

1. **Show HN: Corral – finds the gaps in your test suite and writes the missing tests**
2. **Show HN: An adversarial test auditor that shows you what your tests miss (5 languages)**

## URL

https://corralai.dev  (link the repo + field notes; attach the terminal capture)

---

## Post body

I built Corral to answer a question I could never answer honestly about my own code: *do my
tests actually test anything, or do they just pass?*

That question got sharper this year, because a model will now write your tests for you — by
the hundred, all green on arrival. Which feels like relief after twenty years of testing
guilt. But if the same lineage of model writes the code and the tests, the tests don't
verify the code — they mirror it. A faithfully-implemented bug gets a green test. You've
automated the ritual of testing without the substance, and the "all green" you've used as a
proxy for "safe" quietly stopped meaning that.

You can't tell a load-bearing test from a decorative one by reading a thousand of them. The
only honest way to tell them apart is to break the code on purpose and see which tests
notice. That's mutation testing — decades old — and AI just turned it from a nice-to-have
into the only signal left standing.

Corral is a small headless "brain" that runs an adversarial herd: one model plants
deliberate, goal-violating faults in your code, a *different* model reads your suite, and
the brain itself **runs your tests against every planted fault in a sandbox** and signs the
result. The judge is never the author. Nothing is taken on a model's word — it's executed.

The example that made me trust it: a password validator that's only "strong" if a password
is ≥12 chars AND has an upper, a lower, a digit, and a symbol — plus a test any of us might
write, checking that a short password fails and a good one passes. Corral planted
goal-violating variants and ran that test against each:

```
  Python      1/5 faults caught   →  4 slipped through
  Ruby        2/5                 →  3
  JavaScript  2/5                 →  3
  TypeScript  0/5                 →  5
```

The test never checks the four character-class rules at all, so a variant that quietly drops
"must contain a digit" passes every time. Same blind spot, every language, under a minute
each. (Go's gappy version misses too — 2/5 — I left it out of the table only because Go is my
*clean* counterexample below.) I've shipped that bug.

Then I pointed it at code I didn't write — [more-itertools](https://github.com/more-itertools/more-itertools),
a well-respected, heavily-tested Python library — because a demo on a toy validator proves
nothing. It planted 39 faults across one file and ran more-itertools' *own* test suite
against them in a jail. The suite caught 35 — a genuinely strong 90% — four bugs walked past,
and it wrote me a compiling test for a gap the suite missed. (The number moves run to run
because which faults get planted is non-deterministic — I quote the shape, not the decimal.)

One thing from that run is worth telling on myself, because it's the point. The decorrelated
critic — a second model reading the suite cold — confidently flagged a more-itertools test as
vacuous: `test_negative_take`, claiming `islice` silently accepts a negative count so the
asserted `ValueError` never fires. Tidy story. I nearly repeated it. Then I ran
`mi.take(-3, range(10))` — it *raises* `ValueError`. `islice` doesn't swallow negatives, it
raises; the test is correct; the critic hallucinated. And here's why that's a feature: the
critic's opinion is marked unverified and structurally **cannot gate the verdict** — only the
executed kill-rate certifies — so the hallucination never touched the 90%. You can't verify a
generator with another generator's opinion. Only with execution.

So I did two things about it, and together they're the whole thesis in miniature. First, that
hallucination is now a **recorded metric**: corral scores each critic finding real-vs-hallucination *by
execution* and reports a per-model critic precision (`corral scorecard`) — a model that
hand-waves a good test as vacuous earns a mark against its number, and you can query it.
Second, I re-ran the same file with a stronger, **cross-vendor** critic (the writer stayed on
one vendor, the critic on another — the strongest form of decorrelation). It did *not* repeat
the islice mistake, and it found two tests with genuinely dead checks the lighter critic
missed — both of which I confirmed by running them. That whole run is
[replayable on the site](https://corralai.dev/recordings). The tool caught its own
unreliable critic, scored it, and I could measure a better one doing better. That's the
difference between "trust the AI" and "make the AI provable."

The part I'm actually proud of: when your suite has a hole, the herd **writes the test you
were missing and hands it back.** It's not there to scold — it hands you the assertion you
didn't think to write. And it's fair: pointed at a genuinely thorough suite it kills every
fault and signs it *clean*. A gate that only ever finds fault is theatre.

### How it works, briefly

- **Mutation testing, measured by execution.** The mutants are *semantic* (drop a whole
  rule), generated by an LLM; the kill-rate is measured by actually running your tests —
  never a self-report.
- **Decorrelation is enforced.** The model that critiques the suite must differ from the one
  that wrote the exposing test; the run refuses to start otherwise. It can be a different
  *vendor* entirely — critic on Gemini while the writer/mutant run on Claude, the strongest
  form. You can't verify a generator with another generator's opinion — only with execution.
- **The critic is scored, not trusted.** Its findings are advisory and can't gate the verdict,
  and each one is checked against execution — a per-model critic precision (`corral scorecard`)
  so a hallucinating critic earns a measurable mark, not a free pass.
- **Certify by execution, fail closed.** If the toolchain to run your tests isn't present,
  or there's no sandbox, it refuses rather than guess. Five languages today (Go, Python,
  Ruby, JS, TS), each a small plugin.
- Every verdict is a hash-linked, tamper-evident record. A stranger can re-run it.

### Try it on your own code

One file, your own key, a sandbox (bwrap or docker — it won't run your tests against live
mutations unsandboxed):

```
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...
corral certify --local --code path/to/file.py --goal "what it must guarantee" -- python -m pytest
```

### Honest about the seams

Two field notes instead of overselling. One is called ["Your tests suck"](https://corralai.dev/field-notes/your-tests-suck/)
— and I mean it the way Roger Ebert meant *Your Movie Sucks*: written by someone who loves
good tests and wants yours to be good. The other is a list of the mistakes I made building
it — a certifier is humbling in a way a builder never was, because a builder is bounded by
what a model can *write* and a certifier by what its sandbox can actually *run*.

Elastic-2.0, runs on your own keys. corralai.dev has the repo and the notes. I'd genuinely
love feedback — on mutation-testing prior art, the decorrelation guard, and where an
execute-don't-report gate *can't* help (exploratory pentesting is attributed, not certified —
I try to be clear about that seam).

---

## First-comment prep (warm, not defensive)

- **"Isn't this just mutation testing?"** — Yes, and that's the honest, load-bearing part.
  The additions are (1) enforced cross-model decorrelation, (2) semantic LLM-planted faults,
  (3) it writes the missing test back, and (4) a signed, re-runnable record. Mutation testing
  you can hand to someone else and they can check.
- **"Why an LLM for the mutants?"** — For *semantic* faults (drop a rule) and for writing the
  missing test. The scoring stays deterministic execution.
- **"Your own critic hallucinated — why should I trust the critic at all?"** — You shouldn't,
  and the design doesn't ask you to: the critic can't gate the verdict, and its findings are
  scored against execution (per-model critic precision). A hallucination is a data point that
  lowers that model's number, not a false certification. The certification only ever comes
  from the kill-rate a jail actually measured.
- **"It certified more-itertools at 90% with 4 real gaps — isn't that a pass for broken
  code?"** — It's certified-and-here's-what-you-missed. It cleared the bar I set (0.8) and
  told me the four gaps and handed back a test. The bar is the operator's to set, not the
  tool's.
- **"The frictionless one-command competitor does X"** — fair; the zero-setup story is the
  next thing I'm building. Today it's a binary plus a sandbox.

## Assets to attach

- **The live more-itertools reliable-critic recording** (corralai.dev/recordings →
  `more-itertools-reliable-critic`) — replayable, signed, and it *is* the hallucination-arc
  proof. Strongest single asset; link it inline (already done above) and/or attach a short
  screen-capture of the replay.
- **Terminal capture — DONE (2026-07-20, real Claude-default run):** a TypeScript
  `certify --local` run, `docs/launch/assets/certify-ts-1of5.{cast,gif,mp4}`. The suite
  caught **1 of 5** planted faults (NEEDS-REVIEW) and the herd handed back the missing test —
  the whole beat in ~7s. The `.cast` is the raw asciinema (provably real, replayable — attach
  it or link an asciinema player for the "you can re-run this" credibility); the `.gif`/`.mp4`
  are the shareable forms. (It's four distinct screens rather than smooth streaming because
  corral flushes its output in bursts — honest, not faked.)
- The five-language table as a clean graphic (published proof page) — regenerate per the
  header note first.
- Links: corralai.dev, /field-notes/your-tests-suck, /field-notes/delegate-the-labor,
  /recordings.

---

## Fresh re-run log (2026-07-20, Claude-default) — reference numbers

Regenerated on the uncapped Anthropic key, so these are current. Quote the shape; they move.

**5-language passwd blind-spot** (single generator, 5 mutants each, sonnet mutant+writer /
haiku critic; the table above uses these):
- Go 2/5 · Python 1/5 · Ruby 2/5 · JavaScript 2/5 · **TypeScript 0/5** (the standout).
- Every language's length-only test misses most character-class faults. Universal blind spot.

**more-itertools `recipes.py`** (6 shards, sonnet mutant+writer / haiku critic — same config as
the 2026-07-19 anchor run):
- **26 of 30 killed = 87%, CERTIFIED** (2026-07-19 was ~39/90%; same shape, non-deterministic).
- **The hallucination reproduces reliably.** The Haiku critic again declared `test_negative_take`
  vacuous — *four times in one run* — insisting `islice` "silently treats negative n as 0 and
  returns an empty list." It does not: `mi.take(-3, range(10))` **raises `ValueError`** (islice
  raises on a negative count). So the weak critic is *consistently* wrong here — which makes the
  fence the whole point: **status came back CERTIFIED at 87% with all four hallucinated flags
  present**, because a critic's opinion is UNVERIFIED and structurally cannot gate the verdict.
  Only the executed kill-rate certified. (The cross-vendor Gemini-3.1-Pro critic, by contrast,
  did *not* repeat it — see the live reliable-critic recording.)

This is the strongest single proof of the thesis in the post: the same weak judge hallucinates
the same false fact every time, and it never once moved the number, because the number is what
a jail measured, not what a model claimed.
