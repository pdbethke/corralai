# memory-etiquette

Corralai's memory (`internal/memory`) is the brain's shared, searchable state
across brain missions and the agents connected to it. It is brain-only, and
no audit reads it: `corral certify --local`, `certify --repo` and the GitHub
Action never open the memory store, and even a brain-connected worker in an
audit seat (mutant-generator, test-writer, test-critic) runs without a
`search_memory` tool — nothing recorded here changes a verdict. Two rules
make it worth having for the mission work that does use it.

## Search before work

Before starting a phase of a brain mission, search first: `search_memory`
(BM25 full-text, `internal/brain/memory.go`) over everything you can see —
your own entries plus the shared knowledge base. Search again as you
work — memory grows as work runs, and a finding another role just recorded
may bear directly on what you're working on.

## Write lessons liberally

Every freeform phase of a mission — build/test/verify, adversarial review
— should record a concrete LESSON with `add_memory` (type `lesson`,
`shared=true`) for each thing that broke or surprised the agent: the
trigger, what went wrong, the corrective guidance. Don't wait until the run
wraps up, either: any phase can `add_memory` findings and notes as it goes
(`internal/brain/memory.go`'s `add_memory` tool). More lessons written means
more raw material for the learning loop below.

## Lessons are advisory until promoted

A freshly-written `lesson` isn't automatically authoritative. The **learning
loop** (`internal/learn`) periodically clusters recurring lesson signatures
into `Proposal`s. A human reviews them with `list_proposals` /
`approve_proposal` / `reject_proposal` (`internal/brain/learn.go`) — only
`approve_proposal` (superuser-gated) promotes a proposal's guidance into
`shared=true` vetted memory, or syncs a skill fleet-wide. That human click is
the only place anything starts shaping instructions automatically; nothing
promotes itself.

## Repo-shipped docs are advisory too

This corpus (`CORRAL.md` + `docs/corral/*.md`) is ingested the same way:
`shared=false`, tagged to the repo. Read it via search; it never auto-injects.
