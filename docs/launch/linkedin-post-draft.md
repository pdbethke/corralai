# LinkedIn — ready-to-post draft (the short post, not the long article)

> The long article plan lives in linkedin-article-notes.md. This is the punchy POST that
> drives to the field note — LinkedIn is swipe-swipe-swipe, so the whole argument is above
> the fold and the depth is one click away. Crowned by the accountability line; your existing
> guilt/relief → flood → answer arc underneath. Lead image: the Frampton/Cleese still (fair
> use, credit Monty Python/BBC 1969), same as the field note. Posting is yours.

---

**You can delegate the labor. You can't delegate the accountability.**

AI will write your tests now — by the hundred, all of them green. After twenty years of
testing guilt, that feels like absolution. It should feel like a question.

Because if the same model writes the code *and* the tests, the tests don't verify anything —
they mirror the code. A faithfully-implemented bug gets a green checkmark. And the two numbers
everyone quietly conflates have finally come completely apart:

**The number of passing tests has never been what makes software stronger. The number of
tests that verifiably test *something* is — and in the agentic era those two numbers have come
unmoored.**

You can't eyeball a thousand AI-written tests to tell the load-bearing ones from the theater.
"All green" stopped being a proxy for "safe," and nobody said it out loud.

So I built the thing that says it. It breaks your code on purpose — plants real,
goal-violating bugs — and checks whether your tests *notice*, judged by a different model,
run in a sandbox, signed. It's mutation testing (forty years old; AI just made it mandatory),
with one rule: you cannot verify a generator with another generator's opinion. Only with
execution.

I pointed it at more-itertools — a respected, heavily-tested Python library I didn't write.
It planted 39 bugs and ran the library's *own* tests against them. The suite caught 35 (a
strong 90%); four walked past; it wrote me the missing test and handed it back.

Then it tried to fool me. Its second "critic" model confidently declared one of their tests
vacuous. I nearly repeated it — then I ran the line, and the critic was flat wrong. So I
watched my own tool hallucinate. Here's the part that matters: that opinion is fenced off — it
can't gate the verdict, only the executed number can — so the hallucination never touched the
90%.

And then I did something about it, which is the whole idea. The critic is now *scored*: every
finding it makes is checked against execution, so a model that cries wolf on a good test earns
a measurable mark against its name — hallucination as data, not as a verdict. I re-ran the
same library with a stronger critic from a different vendor entirely; it didn't repeat the
mistake, and found two real dead spots the first one missed. I could *measure* a better judge
being better. You can't verify a generator with another generator's opinion. Only with
execution.

I couldn't take the tool's word for any of it. That's the point. The 90% isn't something a
model told me — it's what happened when the tests met the bugs in a jail.

Use AI for the labor. Write the code with it, write the tests with it — I do. Just don't let
it quietly take the one thing that was never yours to give away: standing behind the word
*fit* and saying *this is good enough to ship, and it's on me.*

The full note — with the numbers, the honest floor, and a command you can run on your own code
in a few minutes — is here 👇
[link: corralai.dev/field-notes/delegate-the-labor]

*You can delegate the labor. You can't delegate the accountability.*

#softwaretesting #AI #softwareengineering #devtools

---

## Alt hooks (if the opener needs A/B)
- "When did you last actually *read* your test suite? Not the number — the tests."
- "We've automated the production of green checkmarks and called it coverage."
- "In the age of AI, who watches the watchmen? Your tests are the watchmen."
