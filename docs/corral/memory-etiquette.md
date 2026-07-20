# memory-etiquette

Corralai's memory (`internal/memory`) is the herd's shared, searchable state
across audit runs and agents. Two rules make it worth having.

## Search before work

Before starting a phase of an audit run, search first: `search_memory`
(BM25 full-text, `internal/brain/memory.go`) over everything you can see —
your own entries plus the shared knowledge base. Search again as you
work — memory grows as work runs, and a finding another role just recorded
may bear directly on what you're certifying.

## Write lessons liberally

Every phase in a run — build/test/verify, adversarial review, mutation
scoring — should record a concrete LESSON with `add_memory` (type `lesson`,
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
