<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The model registry — declare once, name seats by alias

**Status: the registry ships in v0.8.2.** Aliases, provider-as-data, local
entries, strict mode and resolution-recording are built. The two gates (a
static build gate, a scheduled live check) and per-entry credentials are
designed and not yet built — each is marked below.

## Why corral makes you type model names

corral has no default models. Every seat — the goal-deriver, the
mutant-generator, the test-writer, the test-critic — is named by the operator
or the run refuses to start. That rule is deliberate: a verdict is only worth
something if you know who produced it, and a tool that silently picks a model
for you has signed a claim on your behalf.

The cost of the rule is visible in the quickstart: four model names on one
command line, repeated in every example, in CI configuration, and in docs.
That is friction, and it is also where **stale references** come from.

## What went wrong without one

Three times in one week, a model name that had been written once and never
re-verified cost something real:

- A documented example named `gemini-3.6-pro` for a month. That model has
  never existed — the served ≥3.6 line is flash only, and the pro line stops
  at 3.1-pro-preview. Readers were being told to name a 404.
- The self-audit workflow pinned the same phantom as its writer, for a sound
  reason (a smaller model had failed three times to write a compiling Go
  test) with an unchecked name. Because that workflow only runs on a label,
  nothing executed it for two days — then it burned two hours of runner time
  and died at the writer seat.
- A production daemon ran a critic below the project's own model-recency
  floor, re-selected by performance statistics that overrode the configured
  list.

Each is the same failure: a name verified once, or never, then trusted. A
registry alone does not fix that. Gates do.

## The design

A single declared set, referenced everywhere by alias:

```jsonc
// .corral/models.json   (overrides: CORRALAI_MODELS_FILE, or inline CORRALAI_MODELS)
{
  "fast":   { "provider": "google",    "model": "gemini-3.6-flash" },
  "strong": { "provider": "google",    "model": "gemini-3.7-flash" },
  "writer": { "provider": "anthropic", "model": "claude-sonnet-5"  },
  "local":  { "provider": "ollama",    "model": "qwen3.5:9b-q8_0",
              "endpoint": "http://127.0.0.1:11434" }
}
```

```bash
corral certify --repo . --substrate workspace \
  --derive-model fast --mutant-model fast --writer-model writer --critic-model strong \
  -- pytest
```

Four properties make it worth building:

**Local models are first-class.** An entry may name a local endpoint
instead of a hosted provider, and corral already runs this way — a recorded
audit in the public warehouse has `mutant-generator=qwen3.5:9b-q8_0` planting
faults while a hosted model wrote the tests. Two consequences worth naming.
It answers the cost objection directly: the generator and writer seats can run
on your own hardware, with a hosted model only where you want its strength.
And it is the strongest decorrelation available — a local open-weights model
and a hosted frontier model share no lineage, no vendor, and no training run.
`--local-endpoint` exists today; the registry gives it a name and records
which seat ran where.

**Strict mode makes a typo cost two seconds.** A registry may declare
`"strict": true`, and then any seat naming something that is not a declared
alias is refused before the run starts — naming the value, the registry it
came from, the declared aliases, and the ways out. Without it a mistyped
alias falls through as a "concrete model name" and fails at the seat, hours
into a paid run; that is the exact failure this page opens with.

**It is not a default.** The registry declares what *may* be used; the
operator still chooses. A seat with no alias still refuses the run, and an
alias named `default` is refused at load — the word is a trap.

**Provider is a field, not a substring.** Cross-vendor decorrelation is
currently advisory because the guard compares model names and cannot tell
two models from one vendor apart from two vendors. With `provider` as data,
it becomes enforceable.

**Two gates keep it true.** A *static* gate fails the build if a
vendor-shaped model name appears anywhere outside the registry — the same
mechanism that keeps corral's GitHub Action version pins from rotting. A
*live* gate resolves every entry against the provider's own model listing,
in `corral doctor` and on a schedule in CI, so a retirement is a red build
tomorrow morning rather than a 404 two hours into someone's audit. A
provider with no key reads "not checked", never a silent pass.

**Resolution is recorded.** The verdict, ledger and signed statement record
the concrete provider and model as authoritative; the alias rides along as a
label, and cache keys use resolved names. Aliases without that record would
destroy reproducibility — two runs both claiming `strong` would not be
comparable, and a replay could not reconstruct what it graded.

**The registry belongs to the operator, not to the code under audit.** A
cold review (2026-09-03) showed the gap: the registry is read from the
repository root, the repository root is the repository under audit
(`certify --repo <third-party>`, the Action's checkout of a pull request's
head), and the registry rewrites seat values in place. A change under
review could therefore ship a `.corral/models.json` that re-pointed a
concrete name the operator typed at a retired model, or sent the writer
seat — and the source it is shown — to a host of its choosing, or (with
`"strict": true`) refused the run outright. Two rules close it. An alias may
not be spelled like a concrete model name (`claude-…`, `gemini-…`, `gpt-…`,
anything vendor-prefixed): the operator who types a model gets that model.
And on a CI runner (`GITHUB_ACTIONS=true`) the checkout's own registry is
ignored, with a line on stderr saying so; the workflow declares the
registry through `CORRALAI_MODELS_FILE` or `CORRALAI_MODELS`, which the
checkout cannot write. A developer's own shell is not a runner, and there
the repo-local file is theirs.

## Prior art, and the one thing we will not borrow

This pattern is well established and we are not claiming to invent it.
Django's `DATABASES` gives aliases and an override path; LiteLLM's
`model_list` is the closest analogue in this domain; Continue and Cursor
assign models to *roles*, which map almost exactly onto corral's seats.
Terraform contributes the piece people most often skip: a declared set plus
a recorded resolution, which is what makes an alias safe to sign a verdict
with.

What we will not borrow is the **fallback**. Routers commonly retry a failed
model against a substitute. For corral that is disqualifying: silently
swapping the critic breaks decorrelation, and a signed verdict would then
name a model the operator never chose. If a named model is unavailable the
run refuses and says which one — the same posture as a missing goal source
or an unreachable toolchain.

## What ships now, and what comes next

**Shipped (v0.8.2):** the declared set with file and env carriers, alias
resolution at every seat, provider as data, local entries with endpoints,
strict mode, and resolved-identity recording (verdict, ledger, statement and
cache keys all carry the concrete model — an alias rename cannot move a key).

**Next, in order:**

1. **Per-entry credentials.** Keys are still resolved by hardcoded
   per-provider environment lookups, which means one credential per provider
   for the whole machine. That is not academic: an orphaned Google project
   held the only `GEMINI_API_KEY` this repo knew about, and every run failed
   `credits depleted` next to a funded balance until the key was traced. An
   entry should name its own credential (`key_env`), refuse before spending
   when it is missing, and record which credential source served the run.
2. **Transport configuration.** Base URLs, timeouts and per-model parameters
   are read from scattered environment variables and hardcoded defaults
   today. They belong on the entry: code owns the protocol adapter, the
   registry owns the instance. Three rules go with it — an unknown field is
   an error rather than a silent no-op, anything that changes output is
   recorded in the verdict, and a capability the provider lacks may not be
   claimed by configuration.
3. **The two gates.** The static gate (no vendor-shaped model name written
   anywhere outside the registry, modelled on the Action-tag gate that keeps
   version pins from rotting) and the live gate (every entry resolved against
   its provider's own listing, in `corral doctor` and on a CI schedule).

**Known and unfixed:** with no registry declared, an unknown model name is
inferred as a local daemon rather than refused, so a typo'd cloud model is
sent to Ollama instead of rejected. Strict mode closes this for projects that
declare a registry; the general fix belongs with the live gate.
