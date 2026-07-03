---
name: how-swarm-memory-works
type: convention
shared: true
description: How the swarm uses shared memory — write liberally, search first, it's one brain across every agent and every mission
---

# How the swarm uses memory

This corpus is the swarm's shared, long-term brain. Every agent reads and writes it,
and it persists across missions — so a lesson one bee learns, every future bee can
find. Storage is unlimited; the only thing that matters is whether the useful note
got written and whether it's findable later.

## Write liberally — don't ration

As you work, record decisions, dead-ends, gotchas, and lessons with `add_memory`.
A jotting that saves a future teammate ten minutes is worth writing. The cost of an
extra entry is nearly zero; the cost of a lost lesson is the whole swarm repeating
a mistake. When in doubt, write it.

## It is SHARED — you are querying every other agent's experience

`search_memory` searches what EVERY agent has written, not only your own notes.
Before you start a task, search first: the researcher's requirements, the designer's
design, a tester's failure, a past mission's lesson may already answer your question.
This is how "hey Hawk, have you seen this vulnerability before — or did you patch it
already?" gets answered: Hawk's findings and lessons are in here, and anyone can
recall them. Consult the corpus before you repeat work or repeat a mistake.

## What to write

- **decision / convention** — a choice made and why; a naming or structural rule.
- **lesson** (`type: lesson`) — something that broke or surprised you: the trigger,
  what went wrong, and the corrective guidance, so the next mission applies it.
- **hand-off note** — what you built or found, concrete enough that the next phase
  works against it without guessing.

## How to write a good entry

- `name`: a short kebab-case slug — it's the entry's identity and how others find it.
- `body`: the fact in markdown — specific and self-contained; one fact per entry.
- `type`: `decision` | `convention` | `lesson` | `note`.
- Reference a related entry by its slug when it helps the next reader connect them.
