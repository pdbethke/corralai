# LinkedIn article — working notes

**Title:** "Terribly sorry to trouble you… but your tests, um, well. They suck, really"
(same as the field note)

**Subtitle / pivot:** "…but is AI really helping?" — the contrarian, timely hook. Everyone's
celebrating that AI writes their tests; almost no one has asked whether that's *help* or
*camouflage*. "Your tests suck" gets the click; "but is AI really helping?" is why they keep
reading — it names the uneasy thing they haven't let themselves think. Use it as the
subtitle and as the hinge between Beat 1 (relief) and Beat 3 (short-circuiting TDD).

**Lead image:** the Frampton interview still (fair-use commentary — the article, like the
field note, genuinely discusses the sketch). Credit Monty Python / BBC (1969).

**Voice:** helper, not provocateur (Ebert's affection + Cleese's excruciating courtesy).
"suck" is fine here (Ebert allusion, cushioned by the apology). Keep it out of product/
CISO copy.

**CTA (the whole point of shipping `--local` first):** one command a reader can run in ~2
minutes off their own key:
```
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...
corral certify --local --code path/to/file.go --goal "what it must guarantee" -- go test ./...
```

---

## TL;DR at the very top (LinkedIn is swipe-swipe-swipe)
It's a long piece; put a 2–3 line lede above the fold that delivers the whole argument, so
a swiper gets it and a reader gets hooked. Draft:
> *AI will happily write your tests now — which feels like relief after years of guilt. But
> if the same model writes the code AND the tests, the tests just rubber-stamp the code. So
> I built a gate that can't be fooled: it breaks your code and checks whether your tests
> notice — judged by a different model, run in a sandbox, signed. Here's the uncomfortable
> part, and a command you can run in two minutes.*

(Keep the real punch — five languages, `--local`, the signed record — but the lede is the
argument in miniature.)

## TODO before publishing
- **Screengrabs — DONE (2026-07-18).** The stale build-swarm stills are retired; the homepage
  "Receipts, not a highlight reel" band now shows a real audit cockpit (herd = mutant-generator
  /test-writer/test-critic, the surviving fault, the signed verdict). Fresh `certify --local`
  visuals available to embed.
- **Verify command is honest now** — `--local --out verdict.json` then `corral certify verify
  … --pubkey $(corral certify pubkey) --allow-unanchored` (the CTA is copy-paste-real; abs
  paths + `--out` fixed 2026-07-18).

## The argument (in order)

### 1. The guilt/relief trap  (the emotional on-ramp)
Developers hate writing tests — a decades-old time-suck we all feel guilty about. We say we
want verifiable software; we don't take the time. Then AI arrives and *offers to write the
tests for you.* Relief. Absolution. You're finally doing the virtuous thing.
**But:** if the same intelligence writes the code AND the tests, the tests aren't
verification — they're a mirror. An author grading their own work grades it kindly. You've
automated the *ritual* of testing without the *substance.* → lands on the thesis.

### 2. Nemo iudex in causa sua  (the thesis)
No one may judge their own cause. The model that wrote the code (or wrote the tests for its
own code) cannot certify it. You need an independent party with no skin in the game.

### 3. Is AI short-circuiting the spirit of TDD?  (the sharp turn)
Everyone loves the *idea* of TDD; almost no one is faithful to it — writing the failing test
first, from intent, before the code exists to bias you, is a real discipline-tax. TDD's
whole power is that the test is an *independent statement of what the code should do.* AI
generating tests from/alongside the code inverts it: the test now describes what the code
*does*, not what it should. A faithfully-implemented bug gets a green test. AI can hollow
out TDD — turning tests from **specifications** into **rationalizations.**

### 3b. The agentic test flood — the concrete answer to "is AI really helping?"  (NEW — the founder's own itch; the beat most likely to land with traditional devs)
This is the razor-sharp, *felt* version of beat 3, and it's what actually bothers working
devs (the founder included). Two moves:

- **Attrition was always there, quietly.** Any large suite has ~10% dead tests — you deleted
  the feature, refactored the code out from under it, and the test decayed into a **passing
  no-op** that can't fail. It's still green, so nobody notices: a false sense of security AND
  pure CI drag, minutes burned every run protecting nothing.
- **Agentic dev turned the leak into a flood.** An AI will write you a *thousand* green tests
  in an afternoon — and it's *excellent* at it, because a model asked to write passing tests
  is, above all, good at writing tests that pass. The count goes vertical; the actual
  protection barely moves. We've **automated the production of green checkmarks and called it
  coverage.**

So the two numbers everyone conflates have finally come completely apart, and *that's* the
thesis line (pull-quote / possible subtitle-adjacent):

> **The number of passing tests has never been what makes software stronger. The number of
> tests that verifiably test something is — and in the agentic era those two numbers have
> come unmoored.**

You used to be able to *almost* use "all green" as a proxy for "safe." You can't anymore, and
nobody can eyeball a thousand AI-written tests to tell the load-bearing ones from the theater.
The proxy is dead. → hands straight to beat 4: the only honest way to tell them apart is to
**measure it, by execution, at scale** (a test that kills zero planted bugs is *provably*
dead — no opinion, a receipt). That measurement is genuinely a thousand-way parallel job;
there's no smaller honest version. The category line: **AI writes the code and the tests now,
and the one question more AI can't answer is "are the tests real?" — you can't verify a
generator with another generator's opinion, only with execution.**

(Source: the field note "The critic was never the point" — reuse the attrition + agentic
paragraphs and the "signal per test, not count" framing. Also carries the *helper turn* the
article can close on: corral makes your tests stronger from BOTH ends — hands you the missing
killing test, and proves which green tests you can safely delete.)

### 4. Corral's answer  (the resolution)
It doesn't restore the ritual. It checks what the ritual was a proxy for. TDD asked you to
prove, up front and on the honor system, that your test pins real behavior. Corral proves it
*after the fact, by execution, no honor system*: it breaks your code (mutation testing) and
checks whether your tests notice — with a *decorrelated* second model that didn't write the
test. It doesn't care who wrote the tests or when; only whether they'd catch a regression.
The outcome TDD reached for, **measured instead of ritualized** — and signed.

### 5. Proof, five languages  (the evidence)
The password-validator demo: a length-only test, the same blind spot found and *proven* in
Python/Ruby/JS/TS, ~40s each, signed records (Go certified clean as the fair counterpoint).
Then: run it yourself — the `--local` command above.

### 6. Honest seams  (the D'oh register)
Link "Good baking means always mind the D'oh." A builder is bounded by what a model can
write; a certifier by what its sandbox can run. Own the mistakes.

---

## Frampton as the spine (from the field note — reuse the strongest bits)
- The interviewer who *sees* the obvious and cannot ask ("well, let me put it another way…").
- "Our viewers want proof" — and Frampton, too embarrassed to give it. Proof is trivial to
  produce and never gets produced, because demanding it and supplying it are both mortifying.
  Corral is the one panelist with no capacity for embarrassment.
- That IS your code review: the reviewer saw the empty test and typed LGTM.
