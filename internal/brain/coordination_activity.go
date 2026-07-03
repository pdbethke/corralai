// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// recordClaimMade emits one claim_made event per granted path — claims are
// not mission-scoped (internal/coord's schema has no mission_id), so these
// are global ambience, not part of the Part B replay merge.
func recordClaimMade(tel *telemetry.Store, actor string, r *coord.ClaimResult) {
	for _, p := range r.Granted {
		rec(tel, 0, "claim_made", actor, p, map[string]any{"path": p, "exclusive": !r.Advisory})
	}
}

// recordClaimReleased emits one claim_released event per requested path. An
// empty paths list means "release everything mine" — recorded as a single
// wildcard event rather than guessing which paths were actually held.
func recordClaimReleased(tel *telemetry.Store, actor string, paths []string) {
	if len(paths) == 0 {
		rec(tel, 0, "claim_released", actor, "*", map[string]any{"path": "*"})
		return
	}
	for _, p := range paths {
		rec(tel, 0, "claim_released", actor, p, map[string]any{"path": p})
	}
}

// recordHostSeen emits host_seen only on an agent's first sighting or a
// material change to model/backend/jail — not every heartbeat, per the spec's
// volume discipline. prev is looked up from book BEFORE h is stored.
func recordHostSeen(tel *telemetry.Store, book *HostBook, h Host) {
	prev, existed := book.Get(h.Agent)
	book.Set(h)
	material := !existed || prev.Model != h.Model || prev.Backend != h.Backend || prev.Jail != h.Jail
	if !material {
		return
	}
	rec(tel, 0, "host_seen", h.Agent, h.Host, map[string]any{"host": h.Host, "model": h.Model, "backend": h.Backend, "jail": h.Jail})
}

// recordMemoryWritten emits metadata ONLY — slug, type, shared — never the
// name/body/description, per the fleet-sync metadata-only invariant.
func recordMemoryWritten(tel *telemetry.Store, actor, slug, typ string, shared bool) {
	rec(tel, 0, "memory_written", actor, slug, map[string]any{"slug": slug, "type": typ, "shared": shared})
}
