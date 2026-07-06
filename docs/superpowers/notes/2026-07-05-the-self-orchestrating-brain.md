<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The Self-Orchestrating Brain — design thesis (2026-07-05)

> Companion to [`2026-07-05-corral-as-an-operator-system.md`](2026-07-05-corral-as-an-operator-system.md).
> This note captures a live design conversation, so it argues rather than
> specifies. It is the *why* behind wiring an LLM into the brain — and, just as
> load-bearing, the *why not* about where an LLM must never go.

## The provocation: "a brain without a brain"

Corral calls itself a **headless brain**. But the brain, as built, has no LLM in
its judgment path:

- **Directive → tasks** is `mission.ScaledPlan(directive)` — a keyword tier
  heuristic (`internal/mission/complexity.go`: lean / standard / full).
- **Re-plan on a finding** is `fmt.Sprintf("Fix this %s (severity %s) in %s…")`
  (`internal/mission/replan.go`).
- **Staffing** is role-pull: workers declare the roles they cover and *claim*
  eligible tasks; the brain never assigns.

The only LLM surfaces are the **narrator** (`internal/llm/client.go` — the Shep
scrum theatre; describes, never decides) and the **oracle** (`ask_fleet` →
NL→SQL→narrate over the analytics DuckDB). Both are *conditional* — they light up
only when a model backend is configured, and the narrator UI hides when it isn't.
So a local demo with nothing wired presents as brain-less, which is exactly how it
feels: the cognition the word "brain" promises is pattern-matching.

The risk isn't the architecture — it's the **name writing a check the mechanism
doesn't cash**. A skeptical dev who opens `replan.go` and finds `Sprintf` feels
bait-and-switched. ("It's a job queue with a thesaurus.")

## The resolution: LLM at the edges, determinism at the spine

Split what the brain does into **cognition** and **correctness**, and treat them
oppositely:

- **Cognition — wire the LLM in.** Directive intake (the #33 front door),
  planning/decomposition, re-plan judgment, and **staffing**. This is where a
  keyword heuristic is weakest and a model shines. This is the brain the name
  promises.
- **Correctness — keep it deterministic. Never an LLM.** "Is this task/mission
  *actually* done?" stays Go: a recorded passing verify run, final-state checked.
  This is load-bearing — it is the standing directive (*correctness in the brain,
  not LLM judgment*) and the exact thing that stopped the #40 livelock (a 7B
  tester *misjudged* its own completion and looped 21×). An LLM gate reintroduces
  that failure.

**LLM plans and adapts; Go gates and coordinates.** Determinism becomes the flex,
not the confession: *"the brain reasons about the plan with a model — and refuses
to trust a model on 'is it done.'"* Skeptical devs respect that more than a fully
-LLM brain.

## The elegant form: the brain orchestrates its own cognition

The LLM cognition need not be a bolted-on subsystem. It can be **dispatched as
herd tasks through the machinery the brain already has** — `spawn_subagent` +
the harness loop is already "hand a unit of thinking to a swappable model."
"Plan this mission" / "staff this mission" becomes a task with `role: planner`,
claimed by a model-worker, gated and coordinated by the deterministic core.
**The brain uses corral to be the brain.**

That means **"any model" extends all the way up** — even the brain's own planning
is a swappable role→model mapping (#51), not a hardcoded call.

**The recursion must bottom out**, or it's turtles all the way down. Two things
are *never* delegated to a model:
1. **The dispatcher** — the thing that decides "spawn a planner task" is fixed Go.
2. **The gate** — "is it done" stays deterministic.
The LLM proposes; the spine disposes.

## The trust thesis: a judge may not certify herself

The sharpest framing, and the one that survives a hostile read:

- The **same model grading its own work** → no. That's the livelock; the value
  of self-evaluation collapses when actor and judge are one faculty.
- The **deterministic gate judging the LLM's cognition** → yes, because judge and
  judged differ *in kind*. The gate doesn't ask a model "is this good"; it checks
  whether *executing* the plan produced a recorded passing run against real tests.
- The **gate validating the gate** → no, not from the inside (the Gödel/halting
  shadow). It needs an anchor *outside the loop*.

So corral is architected so **the judge never has to trust herself.** Her
authority is borrowed from two things a model can't fake: **contact with reality**
(the verify gate = did real tests pass, in the final state — #42) and **the human
gate** (value/admin decisions sit behind a person). The model may propose, plan,
critique, and review a *peer's* work; it never certifies its *own* correctness.

Self-**improvement** is allowed — the shipped learning loop — but note what
corrects the judge: recorded pass/fail from reality and human-approved promotion,
not introspection. She improves because the world corrected her.

> **The line for the repositioning (#47):** *A judge may not certify herself.
> Corral anchors correctness to deterministic execution and a human gate — never
> to a model's confidence in its own work.*

## The killer first feature: resource-aware, telemetry-fed staffing

The best *first* place to put the LLM brain — useful, honest, and the literal
"watch the brain think" launch moment. The brain saying *"I see one 12 GB GPU
and qwen2.5-coder loaded; a trivial task doesn't warrant a pentester — builder
and tester on qwen, skip the rest"* is worth more than any canvas animation.

Same three-layer split as the thesis:

- **Sense = deterministic probe.** VRAM (`nvidia-smi` / `rocm-smi`), loaded models
  (Ollama `/api/ps` + `/api/tags`), cores/RAM. You never let the LLM *guess*
  hardware; you give it a true snapshot.
- **Judge = the LLM.** Given `(directive, resource snapshot, available models,
  model×role leaderboard)` → propose roles, worker count, model-per-role, and a
  **load order** that respects "one GPU loads one model at a time."
- **Clamp = deterministic.** Never exceed VRAM, never assign an unpulled model,
  always keep builder + tester + the gate. The spine bounds the proposal.

### The part that is *truly* smart: best-fit-by-earned-evidence

The measurement already exists — **model×role telemetry is shipped**: the brain
computes each model's per-role performance, sample-weighted, from the attributed
ledger. The feature is the **LLM reading data that's already there** and staffing
from it. And it's grounded: that leaderboard is **earned from the verify gate**
(reality), so "best fit for builder" means *demonstrably passed the gate
building* — not a model's opinion of itself. Same anchor as the judge thesis.

`argmax(leaderboard)` is a lookup. The **smarts** is an LLM reasoning under
uncertainty over earned evidence *and* live constraints at once. Three things
separate smart from overfit — and reasoning over them is what earns the word:

1. **Cold-start / thin data.** n=2 "best fit" is noise. The planner must *say*
   "no evidence yet, using defaults," honest about thin data (already the
   telemetry's stated posture) rather than overfit a coin flip.
2. **Exploration vs. exploitation.** Always staffing the current leader ossifies
   the board around whoever got lucky early and stops the learning loop learning.
   A smart planner *probes* — "qwen leads builder, but qwen3-coder is untested
   there; spend one task finding out" — and logs it as a probe.
3. **Confounds.** Per-role scores tangle with task difficulty and which peers ran.
   Weight the hard signal (gate pass/fail); don't over-read soft proxies (rework
   count, findings-confirmed).

## Quick vs. thorough — the cheap manual layer

Already half-built: `ScaledPlan` scales ceremony by directive complexity, so a
trivial task already drops to ~builder+tester and skips research/pentester/perf.
What's missing is **operator control** — right now it's a keyword guess you can't
override. So the cheap, honest layer is an explicit **`--quick` / `--thorough`**
(tier override) that beats trusting the heuristic. It composes with the planner:
the flag is the manual knob; the LLM planner is the same knob turned
automatically-and-smarter. Ship the flag; grow into the planner.

## Sequencing (cheap → capstone)

1. **`--quick` / `--thorough` tier override** — trivial, deterministic, immediate.
2. **Resource probe** (`corral` reports VRAM / loaded models / cores) —
   deterministic, useful standalone.
3. **LLM planner over (directive + probe)** → staffing, deterministically clamped.
   ScaledPlan is the fallback (loud log) and the default for tests.
4. **LLM planner + model×role leaderboard** → best-fit, telemetry-fed self-staffing.

## Open questions / risks

- **Who is the planner model?** On a constrained box it should be *cheap and
  local* (a small Qwen), or you've put a heavy brain on the box you're conserving.
  Runs once per mission + per re-plan — cheap. Swappable, same as any role.
- **Reproducibility.** An LLM planner makes staffing non-deterministic → keep
  `ScaledPlan` as fallback + as the test default; fail loud, never fail the
  mission because the planner hiccuped.
- **Cold-start.** No telemetry on day one → defaults, honestly labeled.
- **Exploration guard.** Without it the leaderboard ossifies around early noise.
- **Probe in the demo.** The brain container needs GPU visibility / the Ollama
  endpoint to sense resources; plumb that when we build it.

## Ties to existing threads

- **#33** front-door composer — the LLM intake is the first user-facing slice.
- **#47** repositioning — "determinism on purpose" as the credibility story.
- **#51 / #52 / #53** role→model, registry + earned leaderboard, cross-model eval
  — this note is what finally *uses* that data intelligently instead of charting
  it. Self-staffing is the capstone.

## Adjacent replay/cockpit ideas (captured same session)

Two ideas surfaced alongside this that are worth not losing — both hinge on the
story engine, so they're reasons to capture story runs by default:

- **Searchable past reasoning over MCP.** Recordings are already exposed over
  HTTP (`/api/replay`); an MCP tool is the agent-facing twin. NOT raw-tape
  browsing (too noisy/heavy — an agent would drown and burn context) but
  **searchable `report_thought` beats**: an agent asks "how did the herd approach
  X before?" and gets relevant *reasoning snippets*. Slots between `search_memory`
  (curated lessons — the distilled *what*) and the `history` tool (structured
  past missions) by adding the raw *how-they-thought* dimension. Composes with
  the oracle (`ask_fleet`) once thoughts are indexed. Guardrails: read-only,
  swarm-scoped, rate-limited like `ask_fleet`.

- **File-tree "files" view in the cockpit.** A third replay lens (swarm /
  topology / **files**): a file tree where each file lights up in the claiming
  agent's colour as they work it, scrubbable on the same replay timeline. Reuses
  all the replay infra. And it's CHEAPER than first thought — the capture already
  exists: `coordination_activity.go` records `claim_made`/`claim_released` beats
  with `{path, actor}` on every claim. `BuildReplayStream` (`replay.go:32`)
  *deliberately excludes* them (they're global, `mission_id=0`) and the code
  flags "time-window inclusion of global ambience is a **flagged v2 improvement,
  not implemented here**." So the backend is: implement that v2 (fold global
  claim beats within the mission's time window into the merge). Then a "files"
  tab reconstructs the tree from `claim_made` paths, coloured by actor. Two
  focused pieces (a known backend v2 + a cockpit tab), not a from-scratch capture.

## The handoff IS the thesis — make it visible (2026-07-06)

Coordinated specialists that *hand off* is the whole product; parallel agents are
a commodity. Load-bearing finding from a real run (`go-stack-pass`): **the handoff
is real and role-routed, it's just invisible.**

- `verify-gate`/`Iris` file a **finding** → the brain creates a `fix` task tagged
  `role: builder` (Bob claims it) AND a `re-verify` task tagged `role: tester`
  (**Tess claims it**). Testers genuinely catch what builders miss; the brain
  routes fixes and re-verifies to the right role. Roles are NOT theatre.
- **This routing is 100% DETERMINISTIC** — the reflex-replanner's templated
  `Sprintf` re-plan (`replan.go`) + keyword `ScaledPlan`. **The brain is NOT
  LLM-powered.** Honest positioning: claim "distinct roles that genuinely hand
  off" (true, data-backed); do NOT claim "an LLM brain orchestrating the herd"
  (false today — that's the thesis above, not built). Getting this wrong is the
  one thing that'd get us dinged.

Two gaps keep the (real) handoff off-screen:
1. **No narration.** The Shep narrator (`llm.FromEnv()`, `cmd/corral/main.go:631`)
   exists but only serves ask-a-bee debriefs + the oracle — it does NOT emit into
   the mission tape. **Feature:** trigger it on coordination events (finding→fix,
   re-verify pass) to emit handoff beats ("Iris flagged a build bug → Bob's
   fixing → Tess re-verifying → green") as telemetry that lands in `/api/replay`,
   attributed to Shep, rendered distinctly in the console. Additive — does NOT
   touch the gate. **Build this first.** It's the thesis, made legible.
2. **Only the builder thinks.** In `go-stack-pass`, all 9 `report_thought` beats
   were Bob's — Tess/Iris worked (claimed, filed findings, re-verified) but never
   thought out loud, so "watch the herd think" reads as "watch Bob think." Tune
   the tester/reviewer prompts to report_thought at their real decision points so
   the *team's* reasoning shows, not just the builder's.

## Not blocked on any of this

The immediate tonight-item — a trivial Ollama capture run to get real
`report_thought` beats into a tape — needs only `record_story: true`, independent
of everything above. Capture first; build the self-orchestrating brain as the
deliberate next feature.
