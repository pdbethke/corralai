// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/models"
)

// seatFlag ties one `--*-model` flag to the pool role it staffs, so the
// registry can resolve every seat through ONE code path instead of five
// hand-copied ones. Role is empty for a seat the pool has no role key for (the
// goal-deriver), which only means no local endpoint can be placed for it.
type seatFlag struct {
	flag string  // the flag's name, without the leading dashes
	role string  // the advpool role key, or "" when there is none
	val  *string // the parsed flag value, REWRITTEN IN PLACE on an alias hit
}

// certifySeats is the full set of seat flags a certify run parses. Kept in one
// place because "find them all" is exactly the maintenance task that goes
// wrong: a seat missed here would accept an alias, fail to resolve it, and
// send the alias itself to a provider as a model name.
func certifySeats(derive, mutant, writer, critic, shadow, shadowWriter *string) []seatFlag {
	var out []seatFlag
	if derive != nil {
		out = append(out, seatFlag{flag: "derive-model", val: derive})
	}
	if mutant != nil {
		out = append(out, seatFlag{flag: "mutant-model", role: advpool.RoleMutantGenerator, val: mutant})
	}
	if writer != nil {
		out = append(out, seatFlag{flag: "writer-model", role: advpool.RoleTestWriter, val: writer})
	}
	if critic != nil {
		out = append(out, seatFlag{flag: "critic-model", role: advpool.RoleTestCritic, val: critic})
	}
	if shadow != nil {
		out = append(out, seatFlag{flag: "shadow-model", role: advpool.RoleMutantGeneratorShadow, val: shadow})
	}
	if shadowWriter != nil {
		out = append(out, seatFlag{flag: "shadow-writer-model", role: advpool.RoleTestWriterShadow, val: shadowWriter})
	}
	return out
}

// resolveSeatRegistry loads the project's model registry and resolves every
// seat flag through it, IN PLACE.
//
// THE RULE THIS KEEPS. A seat value is either a declared alias or a concrete
// model name, and it has always been the latter — so an alias hit is rewritten
// to its concrete model here, before any other code reads the value, and a
// miss is left exactly as the operator typed it. Everything downstream (the
// verdict, the ledger, the signed statement, the report lines and above all
// the CACHE KEY) therefore sees the concrete model and can never key on an
// alias. An alias that keyed a verdict would destroy reproducibility: two runs
// both claiming "strong" would not be comparable, and renaming an alias would
// silently invalidate — or worse, silently reuse — a cached result.
//
// A seat with NO value is untouched. corral has no default models and the
// registry is not one: an unnamed seat still refuses the run downstream.
//
// Returns the local endpoints the registry implies, as the SAME role→url map
// --local-endpoint produces. The caller merges them, with an explicit
// --local-endpoint winning: the flag is the more specific statement.
// seatResolution is what a run learned about its seats from the registry:
// which local daemons to place them on, and — the new fact — WHICH PROVIDER
// each seat runs on. Provider used to be a substring guess over a model name;
// declared, it is data, and the decorrelation disclosure can finally say
// "these two seats are the same vendor" out loud.
type seatResolution struct {
	// endpoints is role -> daemon URL, the SAME shape parseLocalEndpoints
	// produces, because it feeds the same mechanism.
	endpoints map[string]string
	// providers is role -> provider, from the registry where an alias was
	// named and from the name-based inference otherwise. A seat whose provider
	// cannot be determined is ABSENT, never guessed.
	providers map[string]string
}

func (r *seatResolution) localEndpoints() map[string]string {
	if r == nil {
		return nil
	}
	return r.endpoints
}

func (r *seatResolution) seatProviders() map[string]string {
	if r == nil {
		return nil
	}
	return r.providers
}

// endpointNote names the daemon a local seat will actually talk to. A local
// model's placement is the whole point of declaring it, and a placement the
// operator cannot see is one they cannot check.
func endpointNote(e models.Entry) string {
	if e.Endpoint == "" {
		return ""
	}
	return " @ " + e.Endpoint
}

// inferredProvider is the vendor corral derives from a bare model name — the
// same inference the router has always made, said out loud. A name it cannot
// place is a local one, served by whatever daemon the environment points at.
func inferredProvider(model string) string {
	if v := agentbackend.VendorOf(model); v != "" {
		return v
	}
	return "local daemon (no cloud vendor in the name)"
}

// mergeLocalEndpoints folds registry-implied endpoints under explicit
// --local-endpoint placements. The flag wins: it is the operator naming one
// seat's daemon for this run, against a declaration that covers every run.
func mergeLocalEndpoints(fromFlag, fromRegistry map[string]string) map[string]string {
	if len(fromRegistry) == 0 {
		return fromFlag
	}
	out := map[string]string{}
	for role, url := range fromRegistry {
		out[role] = url
	}
	for role, url := range fromFlag {
		out[role] = url
	}
	return out
}

// sharedSeatProvider is the provider the test-writer and test-critic SHARE, or
// "" when they do not share one (or when either seat's provider is unknown).
//
// Provider comes from the registry where a seat named an alias, and from the
// same name-based inference the router has always used otherwise. Unknown is
// answered with "" and never guessed: claiming two local models share a vendor
// because neither name looks hosted would be a fabricated finding, and this
// line's whole value is that it is true.
//
// It NEVER gates anything. CheckDecorrelation's model-name refusal is
// unchanged; this only says out loud what that refusal cannot see.
func sharedSeatProvider(providers map[string]string, writer, critic string) string {
	writer, critic = strings.TrimSpace(writer), strings.TrimSpace(critic)
	if writer == "" || critic == "" || strings.EqualFold(critic, "off") || writer == critic {
		return ""
	}
	pw := seatProvider(providers, advpool.RoleTestWriter, writer)
	pc := seatProvider(providers, advpool.RoleTestCritic, critic)
	if pw == "" || pw != pc {
		return ""
	}
	return pw
}

// untellableAgentSeats are the graded seats sharedSeatProvider cannot judge:
// agent seats with no declared provider and no vendor corral can infer — an
// operator's own agent, unpinned (or pinned to a model no prefix names). Its
// command is theirs, so corral does not know what it runs and will not guess.
// Returned so the caller can SAY so: sharedSeatProvider answering "" for
// these reads exactly like "two different vendors", which is not known.
func untellableAgentSeats(providers map[string]string, writer, critic string) []string {
	writer, critic = strings.TrimSpace(writer), strings.TrimSpace(critic)
	if writer == "" || critic == "" || strings.EqualFold(critic, "off") {
		return nil
	}
	var out []string
	for _, s := range []struct{ role, spec string }{{advpool.RoleTestWriter, writer}, {advpool.RoleTestCritic, critic}} {
		if !isAgentSeat(s.spec) || seatProvider(providers, s.role, s.spec) != "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%q)", s.role, s.spec))
	}
	return out
}

// seatProvider prefers the DECLARED provider (registry data) over the vendor
// inferred from a model name. The declaration is the operator's own statement;
// the inference is a substring guess that cannot see a gateway or a local
// runner at all.
func seatProvider(providers map[string]string, role, model string) string {
	if p := strings.TrimSpace(providers[role]); p != "" {
		return p
	}
	return agentbackend.VendorOf(model)
}
