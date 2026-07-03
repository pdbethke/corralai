# claims-and-leases

`internal/coord` is the claim broker: agents lease paths/branches before
touching them, so concurrent agents can see (and, for exclusive leases,
avoid) each other's in-flight work.

## Exclusive vs advisory

`ClaimPaths(name, paths, ttlSeconds, exclusive, reason)` (`internal/coord/store.go`)
takes an `exclusive` flag. An exclusive claim is **enforced**: if an active
exclusive holder overlaps the requested path, the claim is refused (reported as
a `Conflict`, not granted). A non-exclusive claim is purely **advisory** — it is
always granted and reported alongside any conflicts, never blocking. The
package doc says it plainly: "Claims are ADVISORY: a conflict is reported
alongside the grant, never blocked" — exclusive is the one flavor that actually
enforces.

## TTLs

Every claim carries an `expires_ts = now + ttlSeconds`. Only unreleased,
unexpired claims (`released_ts IS NULL AND expires_ts > now`) count as active
for conflict checking (`activeClaimsTx`). A claim you never release simply ages
out.

## The slacker rule

An exclusive holder that's `awaiting_approval` (parked, e.g. waiting on client
review) keeps enforcing its lease only for a grace window —
`parkedGraceSeconds()` (default 300s, `CORRALAI_PARKED_GRACE_SECONDS` in the
demo runs it near-zero). Past the grace window, `enforcing()` treats the
holder as advisory (non-blocking) even though the row is still "active" — so a
stalled agent can't indefinitely lock out everyone else. The moment it
un-parks, enforcement resumes; nothing is mutated to make this happen.

## Re-issue semantics

There's no separate renew call: call `ClaimPaths` again with a fresh TTL before
the old one expires. Your own prior claims never conflict with your own new
ones (`c.Agent == name` is skipped in the conflict check), so re-claiming to
extend a lease is safe and idempotent from the caller's perspective.
