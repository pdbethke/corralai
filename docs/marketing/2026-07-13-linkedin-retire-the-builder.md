<!-- SPDX-License-Identifier: Elastic-2.0 -->
<!-- LinkedIn article draft — paste into the LinkedIn article editor. Audience: security / engineering leaders. -->

# The thing writing your code is now an agent. Your signature proves *who*, not *what*.

Last week I deleted the feature everyone liked best in the thing I've spent months building. I want to tell you why, because the reason is a problem that's about to land on every security leader I know.

**Here's the shift, stated plainly.** The actor producing code in your repos is no longer only a human. It's an agent — a non-deterministic process that pulls dependencies, runs commands, and commits changes at machine speed, sometimes confident enough to import a package name an attacker has already registered. The volume is going up and to the right, and it isn't slowing down.

Now ask the question your auditors are going to ask: *"Who produced this change, and did the checks actually pass?"*

For most orgs the honest answer today is **nobody knows.** And the tooling we lean on doesn't close the gap, because it answers the wrong half of the question:

- A **signature** proves *who*. SolarWinds signed malware — because the build system was poisoned. The xz backdoor rode in on a *trusted* maintainer. Provenance of the author was never the hard part.
- **CI, SAST, an AI code-review bot** produce a **log**: "trust me, this ran and it was fine." A log is an assertion. It is not evidence. You cannot hand it to a regulator and say "verify this."

That's the gap. In the AI-code era you don't need another way to *write* code — the market has a dozen well-funded ones. You need a way to **prove what the code-writing actually did.** Not attestation. *Evidence.*

**So here's the bet I'm making, and what I re-pointed the whole product to do.**

A gate that sits *downstream* of every builder — human, Copilot, Cursor, Devin, doesn't matter — at the one chokepoint you actually control: the merge. It does one job:

1. It doesn't *trust* a green. It **re-earns** it — by re-running the checks itself, in a sandbox, instead of taking a worker's word.
2. It turns a **role-separated adversarial herd** on the change — a security breaker, a correctness reviewer, an exploit-attempter, an edge-case hunter — because *a judge may not certify herself*, and one static check is not the same as something actively trying to break your diff.
3. It emits a **signed, hash-chained, independently-verifiable record** of exactly what was checked and what survived — evidence you *verify*, not a log you *trust* — and **scrubbable**, so you can hand it to an outside auditor with your secrets stripped.
4. It won't let the branch merge without it.

Owned by the control function — security, compliance, audit — not bolted onto a developer's IDE. This is the SSDF / PCI / SOC 2 evidence problem, and those frameworks want the same thing my auditor friends want: proof, not a promise.

**The tell that this is the real product:** I could not describe my old "type a directive, watch the agents build it" feature without giving four different answers. That's the smell of a tool doing three half-jobs. The gate was the one piece with no incumbent — because almost nobody is doing execution-verified, adversarially-tested, *signed* certification of a change. So I deleted the builder and committed to the gate. Costs me the flashy demo. Buys me an answer to the only question that matters.

And the part I'd underestimated: **this problem gets worse exactly as the builders get better.** Every improvement in AI code generation increases the volume of machine-written code flowing at your merge queue and widens the "who wrote this, did it really pass?" gap. The gate is the thing that scales *with* that curve instead of against it.

If you're a security or platform leader watching AI-generated code arrive faster than your review process was ever designed for — I'd genuinely like to compare notes. The uncomfortable version of the question ("can you *prove* what your agents shipped?") is the one worth sitting with before the auditor asks it for you.

*— building corral, in the open. A true audit for software change.*
