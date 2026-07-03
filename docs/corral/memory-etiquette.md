# memory-etiquette

Corralai's memory (`internal/memory`) is the herd's shared, searchable state
across missions and agents. Two rules make it worth having.

## Search before work

Before starting a phase, search first: `search_memory` (BM25 full-text,
`internal/brain/memory.go`) over everything you can see — your own entries plus
the shared knowledge base. `create_mission` already does this automatically for
you: it calls `mem.RecallLessons` and searches `guidance`/`skill` types on the
directive, injecting up to 3 vetted hits into phase instructions
(`internal/brain/missions.go`). But mid-mission, search again — memory grows as
the mission runs.

## Write lessons liberally

`mission.DefaultPlan`'s final `retro` phase instructs the reviewing agent:
for each thing that broke or surprised you, record a concrete LESSON with
`add_memory` (type `lesson`, `shared=true`) — the trigger, what went wrong, the
corrective guidance. Don't wait for retro, either: any phase can `add_memory`
findings and notes as it goes (`internal/brain/memory.go`'s `add_memory` tool).
More lessons written means more raw material for the learning loop below.

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
