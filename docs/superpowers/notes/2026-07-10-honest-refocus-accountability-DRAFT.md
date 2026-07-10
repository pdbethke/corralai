<!-- SPDX-License-Identifier: Elastic-2.0 -->
<!-- DRAFT — an honest-re-focus post. Two versions: a blog/field-note, and a short
     LinkedIn cut. Founder's voice; edit freely before publishing. -->

# Blog / field note — "We were building the wrong thing (sort of)"

I lay awake last night asking a founder's worst question about a thing I'm proud of:
*is this actually useful, or is it just very cool to look at?*

Corralai is a headless brain that runs a herd of AI agents — any models, mixed — to
build real software. It has a beautiful replay cockpit. You can scrub through a
mission and watch the agents think. And somewhere around 3am I admitted the thing I'd
been avoiding: **if the pitch is "our agents build better than the other agents," I
lose.** That race belongs to the frontier labs and the well-funded coding tools, and
every model release makes the orchestration I built look more like a workaround for
limitations that are disappearing.

So I almost talked myself out of it. And then I looked at what I'd actually built,
versus what I'd been *saying* I built.

I'd built a verify gate that runs the real check itself and reads the exit code —
*a judge may not certify herself.* I'd built a jail so an agent that gets hijacked
can't reach past its own checkout. I'd built a ledger where every action is tied to a
verified principal on a trail the agent can't erase. I'd built a human gate for the
calls that can't be reduced to pass/fail. I'd been calling these "ways corralai is
different from the other agent demos." They're not features. They're the definition
of a word: **accountable.**

- **Attributable** — every action tied to who did it, tamper-evident.
- **Verifiable** — certified by a real recorded run, not the agent's word.
- **Contained** — bounded blast radius; nothing leaks.
- **Answerable** — a human at the gate for what can't be certified.

I wasn't building a better builder. I was building **the accountability layer for AI
agents** — the thing that makes what agents ship trustworthy enough to *accept*. And
that's not a race the labs are running, because their whole pitch is remove-the-
friction, and accountability *is* friction, on purpose.

Here's the part that convinced me it's real, not a rationalization. I took one actual
run — three frontier models, thirteen minutes, sixteen tasks — and mapped it onto the
supply-chain provenance standards a security team already uses (in-toto, SLSA). Out
came a signed statement: what was built, **by which model**, and the actual commands
that ran and *passed* — 11 of 11 — plus the human sign-off and the finding trail.
Agent-produced changes carrying the same verifiable provenance an auditor already
demands for human-produced ones. Arguably better, because the check was real.

And the cockpit I'd been sheepish about — the "isn't this neat" replay — stopped being
vanity the moment I pointed it at trust. An auditor doesn't read a log; they need to
*watch how the decision was made.* Legibility is a compliance feature.

There are whole worlds — banking, healthcare, government, anything audited — that
*can't* turn agents loose until someone answers "can you contain it, certify it, and
answer for it?" Nobody credible is standing there yet, because everyone's chasing
capability. I think that's the right kind of early: lonely, and it looks a lot like
wrong for a while.

So: same engine, honest re-aim. Corralai is **AI accountability, by execution.** If
you work somewhere that has to answer for what your automated systems ship, I'd love
to know if this is the thing you've been waiting for — or what's missing.

---

# LinkedIn cut (short)

I almost shelved my project last night.

I've been building corralai — a system that runs a herd of AI agents across any model
to build real software. It looks great. And at 3am I finally asked: *is "our agents
build better than theirs" a race I can win?* No. That belongs to the labs.

Then I looked at what I'd actually built and had been mis-selling: a gate that runs
the real check itself instead of trusting the agent's word. A jail that bounds a
hijacked agent. A tamper-evident ledger of every action. A human gate for the
judgment calls.

Those aren't features. They're the definition of one word: **accountable.**

I wasn't building a better builder. I was building **the accountability layer for AI
agents** — attributable, verifiable *by execution*, contained, answerable. The thing
that makes what agents ship trustworthy enough to *accept*. The labs won't lead here:
their pitch is remove friction, and accountability *is* friction, deliberately.

Proof it's real: I mapped one run onto the same supply-chain provenance standards
(in-toto / SLSA) a security team already uses — a signed record of what was built, by
which model, and that the checks *actually passed*. Agent changes with the provenance
an auditor already expects.

Regulated teams can't turn agents loose until someone answers "can you contain it,
certify it, answer for it?" Almost nobody's standing there. That's the right kind of
early.

Corralai: AI accountability, by execution. If you have to answer for what your
automated systems ship — tell me what's missing.

#AI #AIagents #AIgovernance #accountability
