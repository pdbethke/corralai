# memory-etiquette

Corralai's memory (`internal/memory`) is the herd's shared, searchable state
across missions and agents. Two rules make it worth having.

## Search before work

Before starting a phase, search first: `search_memory` (BM25 full-text,
`internal/brain/memory.go`) over everything you can see — your own entries plus
the shared knowledge base. (In the now-retired build-from-directive path,
`create_mission` did this automatically — calling `mem.RecallLessons` and
injecting up to 3 vetted hits into phase instructions; that verb was removed in
the 2026-07-13 re-focus, so recall is explicit now.) Search again as you work —
memory grows as work runs.

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
