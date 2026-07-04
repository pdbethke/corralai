# corralai.dev v2: daylight, documentation, and an honest CLI reference

**Status:** approved design, 2026-07-04
**Amends:** 2026-07-03-corralai-dev-site-design.md (the shipped one-pager). User verdicts driving this: "I hate dark mode"; the site has "not 1/10 of what's in the README"; "a complete command line reference — even I don't know how to run the thing properly."

## Part A — Daylight restyle (light default)

Light-default is the norm for major OSS sites (react.dev, go.dev, rust-lang.org, python.org…); dark-default is a terminal-brand affectation. The corral goes daylight:

- **Page ground:** warm light (cream/parchment family, not sterile white), dark-ink text, the existing amber as accent — the same palette read in daylight. Derive tokens in `site/src/styles/global.css`; keep CSS custom properties so a future dark variant is a token swap.
- **The replay hero stays a dark viewport** — grass, rain, and glowing nodes need the dark canvas — framed (border/shadow) as a window onto the corral. The product-shown-in-a-dark-screen-on-a-light-page pattern.
- Optional later (not this pass): `prefers-color-scheme` dark variant. Light is default regardless.

## Part B — /docs on Starlight

- Add Starlight (Astro's official docs framework) mounted at `/docs`, landing page untouched as the showcase. Starlight gives sidebar nav, mobile drawer, and Pagefind full-text search — which runs fully locally, preserving the zero-external-requests rule (verify at build: no external fonts/assets; disable anything that phones home).
- **Content set** (restructured from README.md + docs/DESIGN.md + deploy/demo/README.md + CORRAL.md/docs/corral/* — the verified-copy discipline holds: no new capability claims; where docs pages need more depth than the README carries, the SOURCE CODE is the citation, same as the seed docs were written):
  1. Getting started — install, key-free demo, first mission (quickstart depth).
  2. Concepts — mission lifecycle; the task queue + verify gate; findings + reflex re-planning; claims & leases; memory tiers (advisory→vetted) + the human gate; the learning loop; mission history + replay.
  3. Running it — dev mode vs auth-on; every CORRALAI_* env var (from the source-of-truth doc block in cmd/corral/main.go); the demo compose profiles; deploying a brain behind a tunnel (generic write-up of the pattern, NO personal infrastructure details — hostnames/ports of the operator's own deployment stay out).
  4. Multi-model herds — role-model policy (CORRALAI_ROLE_MODELS), harness workers on their own auth, model_comparison.
  5. The knowledge corpus — CORRAL.md convention, seed docs, contribution flow.
  6. Trust & security — the trust model, human gate, observer tokens, delegation, sandbox jails; honest scope framing per the README's honesty section.
  7. The UI, tab by tab (user-requested) — one section per tab (corral, progress, topology, memory, proposals, completed + the replay bar and agent-detail window), each with a REAL screenshot and a plain description of what it shows and what the operator can do there. Screenshots are captured via the established Playwright scratch-brain pattern from a SEEDED brain only — never a personal/live one (the memory-dir privacy lesson applies); committed as local site assets; each image gets alt text describing the scene.
- Sidebar order mirrors the list above. Each page ≤ ~150 lines; split rather than scroll.

## Part C — Generated CLI reference (never hand-written)

- `scripts/gen-cli-docs.sh`: builds the real binaries (`corral`, `corral-admin`, `corral-agent`, `corral-top`, `corral-harness` — enumerate from cmd/), captures each one's usage/help output (and subcommand help where verbs exist, e.g. every `corral-admin` verb), plus the env-var doc-comment blocks from each main.go header, and emits markdown into BOTH `docs/cli/` (repo) and the Starlight tree (one page per binary).
- **Drift gate:** the script has a `--check` mode diffing regenerated output against committed pages; wired into the site deploy workflow beside the player sync check. Docs that lie about a flag fail CI.
- If a binary's usage text is too thin to document itself properly, FIX THE USAGE TEXT in the binary (that's product improvement, in scope) rather than hand-embellishing the generated docs.

## Verification

- Restyle: full-page screenshots (landing light + hero dark-framed) for the human; contrast sanity (dark-ink on cream ≥ WCAG AA for body text).
- Docs: build green; Pagefind search works offline (e2e: search a term, click a result, zero external requests — extend the existing interception test to a /docs session); every factual claim spot-traceable to README/DESIGN/source.
- CLI ref: `gen-cli-docs.sh --check` green in CI; a human-readability pass on the corral-admin page (the densest one).
- Existing e2e suite stays green; deny-list posture unchanged; sync --check unchanged.

## Deliberately out of scope

- Dark-mode variant of the page (tokens make it cheap later).
- Versioned docs, i18n, search analytics.
- Rewriting the product's usage strings beyond what the generator exposes as inadequate.
- Blog.

## Part D — Recordings gallery (amended 2026-07-04, user request)

A `/recordings` page: a mix of real recorded missions, not just the one golden run.

- **Data:** `site/src/data/recordings/<slug>.json` + `<slug>.meta.json`, each produced by the SAME export pipeline (deny gate + human manifest — no exceptions); a build-time index enumerates them (Astro glob import, no manifest file to drift).
- **Meta gains a models field:** the exporter derives the distinct backend:model values seen in the stream (per-role where recoverable) so cards can honestly say what built it — the point is showing DIFFERENT models at work (e.g. the all-local 7B run vs a mixed-model run per CORRALAI_ROLE_MODELS).
- **Page:** card grid (directive, models, task/finding counts, duration) → click opens the replay player full-width loading that stream (startReplay already takes a stream object — no player changes expected).
- **Hero unchanged:** the landing keeps its featured golden run; the gallery holds the mix, linked from the landing ("more recordings").
- **Content for launch:** record at least two contrasting runs (the existing golden run + one with a distinct model mix via the demo's MODELS_* knobs); each through the full gate + human manifest review.
- Copy discipline, zero-external-requests, and privacy posture identical to the rest of the site.
- **Build-time analytics (amended same day):** a build step runs DuckDB (CLI or node binding — decide by what installs cleanly; it must run in CI's ubuntu image) across all committed recording streams/metas and emits static aggregate JSON: per-recording findings-by-severity, per-model finding counts across recordings (the public face of model_comparison), and task-duration profiles. The recordings page renders these as simple tables/inline charts (no charting library from a CDN — hand-rolled bars/SVG or a bundled lib, zero external requests). MotherDuck explicitly stays product-side (fleet oracle, credentialed) — documented in the docs' multi-model page, never wired into the public site. In-browser DuckDB-WASM is deliberately deferred.
- **MotherDuck visibility (amended same day, user: "very trendy"):** the landing page gets a truthful built-on line (DuckDB telemetry, MotherDuck fleet sync + oracle — text/name-drop only, no external assets), and the multi-model docs page tells the fleet-oracle story. A PUBLIC MotherDuck share of the (metadata-only, allowlist-gated) fleet ledger is a separate follow-up spec with its own human gate — not this plan.
