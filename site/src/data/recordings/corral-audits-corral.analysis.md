## What corral was asked

Certify `internal/certify/certify.go` — corral's **own** signing code, the
package that builds the hash-linked ledger, wraps the head in an in-toto/SLSA
attestation, and signs/verifies it with Ed25519 — against corral's own Go test
suite, by execution. No special-casing: the same `--repo-dir` path any other
target gets, pointed at ourselves. *Nemo iudex in causa sua* only means
something if the tool submits to it too.

## What it found

Gemini 3.6 Flash planted **78** goal-violating mutants across the file,
sharded across generator seats. The package's own suite, run in the jail
against every one, killed **43 of 78 — a 55% dev kill-rate**. **35 survived.**
That's under the certify bar, so the gate returned **NEEDS-REVIEW** and signed
the verdict anyway — a disclosed result, not a hidden one.

The decorrelated critic here is **Gemini 3.7 Flash** — a different model than
the one that planted the faults and wrote the exposing tests, reading the
suite cold. Its findings are advisory only; they never gate the signed
verdict.

Watch the tape closely mid-run and you can see the pool's own corrective loop
firing on live signing code: the writer's authored test for survivor `s0/m5`
**compiled but failed on the clean, unmutated file** — reissued once with the
failure fed back rather than accepted. A separate writer seat for `s3/m10`
didn't compile at all on the first attempt and was reissued too. Both are
ordinary parts of the per-survivor proving loop, not something dressed up for
this recording — the same retry-with-feedback mechanic every tape in this
gallery relies on, just visible here because it happened on the code that
implements it.

Where it landed matters: the writer went on to author compiling,
clean-passing tests for **30 of the 35 survivors**, proving them catchable by
execution rather than leaving them as unverified claims. Five survivors are
still open — either real untested edges in our own signing path, or
equivalent mutants nothing can catch, and corral doesn't adjudicate the
difference itself.

## Why this one is worth watching

We could have picked a flattering file. We picked our own signing code, ran
the same gate we ship, and published NEEDS-REVIEW with 35 survivors sitting in
public view — not because the code is known-broken, but because the point of
a decorrelated gate evaporates the moment its maker gets to pick which of its
own results to show. Open the **tests** tab to see the surviving faults
against the code the suite passed anyway, and the corrective retry firing in
real time.
