// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"io"
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
	// deriveURL is the daemon the registry placed the DERIVE seat on, which
	// has no advpool role and so no entry in endpoints. It used to be printed
	// on the resolution line and then discarded: the seat was served from
	// OLLAMA_URL, not the daemon the registry declared.
	deriveURL string
}

// deriveEndpoint is the derive seat's daemon URL, "" when none (or no
// registry at all — r may be nil).
func (r *seatResolution) deriveEndpoint() string {
	if r == nil {
		return ""
	}
	return r.deriveURL
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

func resolveSeatRegistry(cmdName, repoRoot string, seats []seatFlag, stderr io.Writer) (*seatResolution, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	reg, err := models.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	if reg.Len() == 0 {
		if models.RepoLocalIgnored() && models.RepoLocalExists(repoRoot) {
			// Said once, so a workflow that relied on the checkout's own
			// registry learns why its aliases are now concrete names.
			fmt.Fprintf(stderr, "%s: %s in the checkout is IGNORED on a CI runner — the checkout is the change under audit and may not choose its own auditors; declare the registry with %s or %s instead\n",
				cmdName, models.RepoRelPath, models.EnvFile, models.EnvInline)
		}
		// No registry declared. Emit nothing at all: a repo without one must
		// behave, byte for byte, as it did before this feature existed.
		return nil, nil
	}
	res := &seatResolution{endpoints: map[string]string{}, providers: map[string]string{}}
	var lines []string
	for _, s := range seats {
		if s.val == nil {
			continue
		}
		v := strings.TrimSpace(*s.val)
		// An unnamed seat stays unnamed (the no-defaults rule), and "off" is a
		// deliberately disabled seat, not a model to look up.
		if v == "" || strings.EqualFold(v, "off") {
			continue
		}
		e, ok := reg.Lookup(v)
		if !ok && reg.Strict {
			// STRICT: a value that is not a declared alias is a typo, and the
			// fall-through would turn it into a bogus model name that fails at
			// the seat hours into a paid run — the precise stale-reference
			// failure this registry exists to remove. Refused before anything
			// is spent, naming what IS declared.
			return nil, reg.UnknownAliasErr(s.flag, v)
		}
		if !ok {
			// Not an alias: the concrete model name this flag has always
			// accepted. Disclose the provider corral INFERS from the name, so
			// the operator sees the same fact the registry would have stated.
			lines = append(lines, fmt.Sprintf("    %-19s %s (concrete name, provider inferred: %s)", s.flag+":", v, inferredProvider(v)))
			if p := agentbackend.VendorOf(v); p != "" && s.role != "" {
				res.providers[s.role] = p
			}
			continue
		}
		*s.val = e.Model
		lines = append(lines, fmt.Sprintf("    %-19s %s = %s/%s%s", s.flag+":", v, e.Provider, e.Model, endpointNote(e)))
		if s.role != "" {
			res.providers[s.role] = e.Provider
			if e.IsLocal() {
				res.endpoints[s.role] = e.Endpoint
			}
		} else if s.flag == "derive-model" && e.IsLocal() {
			res.deriveURL = e.Endpoint
		}
	}
	if len(lines) > 0 {
		// The mode is part of the disclosure: "this value was accepted" means
		// something different under each, and an operator reading the line
		// should not have to open the file to know which rule applied.
		mode := "concrete model names are also accepted"
		if reg.Strict {
			mode = "strict — every seat must name an alias"
		}
		fmt.Fprintf(stderr, "  models: from %s (%s)\n%s\n", reg.Source, mode, strings.Join(lines, "\n"))
	}
	return res, nil
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
