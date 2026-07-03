# The Human Gate: agents must not vet their own knowledge

**Status:** approved design, 2026-07-03
**Follows:** the learning loop (2026-07-03-learning-loop-design.md), whose trust model says nothing shapes agent behavior without human approval. This spec closes the gap where "human" was actually "principal": agents could pass the admin gate in both auth modes.

## Problem

Two live findings:

- **Auth on:** delegation tokens roll `UserID` up to the owning principal (`internal/auth/oidc.go:143`). An agent spawned under a superuser principal passes `isAdmin` and can `approve_proposal` — the herd approving its own proposals.
- **Dev mode:** every unauthenticated caller passes `isAdmin` (empty principals table ⇒ dev-open). During the learning-loop demo, agents' `add_memory(shared=true)` landed instantly vetted and crowded operator-promoted guidance out of the injection slots.

## Design

One rule, enforced at every admin write path: **the human gate refuses worker sessions.** The gated paths are `approve_proposal`, `reject_proposal`, `add_memory` with `shared=true`, `promote_memory`, `promote_reference`, and the UI's `/api/proposal/approve|reject`.

### Auth on: deny delegation tokens

A delegated token always carries `TokenInfo.Extra["subagent"]` (`oidc.go:148`); a human's OIDC token never does. The existing `subagentOf(req)` helper (`internal/brain/identity.go:232-240`) already reads it — it is simply not consulted by `isAdmin`. Fix: a `isHumanAdmin(req)` check = `isAdmin(req) && subagentOf(req) == ""`, used by all gated paths. The UI side gets the same via an `auth.Subagent(ctx)` helper beside the existing `auth.ReadOnly` — the read-only observer flag is the established precedent for token-type gating.

**Accepted limitation (documented in code):** in-process subagents share their parent's session and token; their calls are indistinguishable from the parent's. The boundary is per-session, and out-of-process is the spawn mode that matters for autonomous workers.

### Dev mode: worker sessions are marked, then denied

No cryptography exists in dev mode — anyone on the port is anonymous — so this is a **truthfulness guardrail, not a security boundary** (dev mode has none by definition; the code comment must say exactly this). Signals, both honest-by-default:

1. The MCP handshake's `ClientInfo.Name` (`req.Session.InitializeParams().ClientInfo`): `corral-agent` identifies itself; `corral-admin` and the UI do not claim to be workers.
2. Behavior: a session that calls `bootstrap` or `report_host` is a worker by its own declaration.

The brain keeps a per-session worker mark (set on either signal, session-scoped, in-memory — sessions are the brain's unit of identity here). Gated paths refuse marked sessions with a clear error in corral voice ("the human gate: workers propose, the operator disposes"). The demo's agents use `corral-agent` and always bootstrap, so the demo becomes truthful with zero agent changes.

### Drive-by fix

UI-driven approvals currently stamp telemetry actor `"operator"` unconditionally (`cmd/corral/main.go:946-952`). When auth is on, pass the real principal through the Promote/Reject closures.

## Testing

- Auth on: seeded principals + minted delegation token for a superuser principal → all gated paths refuse (per-path table test); the human's own token still passes; observer tokens still refuse everything.
- Dev: a session that called `bootstrap` → gated paths refuse; a fresh `corral-admin`-shaped session → passes; the mark does not leak across sessions.
- The learning-loop wire tests must stay green (they drive the human path).

## Deliberately out of scope

- Per-agent principals or a role column in `internal/principals` (heavier axis; revisit if worker identity ever needs auditing beyond namespace).
- Rate/quorum schemes on approvals.
- Injection-slot reservation (guidance vs lessons crowding) — related but separate; tracked as its own follow-up.
