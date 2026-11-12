// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/modelcatalog"
	"github.com/pdbethke/corralai/internal/models"
)

// seatSet is every seat of one invocation, resolved once, keyed by the seat's
// flag name. It replaces the several maps (endpoints, providers, deriveURL)
// that each re-derived a different slice of the same fact.
type seatSet map[string]agentbackend.ResolvedSeat

// byRole is the seat staffing an advpool role.
func (s seatSet) byRole(role string) (agentbackend.ResolvedSeat, bool) {
	if role == "" {
		return agentbackend.ResolvedSeat{}, false
	}
	for _, rs := range s {
		if rs.Role == role {
			return rs, true
		}
	}
	return agentbackend.ResolvedSeat{}, false
}

// providers is role -> provider, the shape sharedSeatProvider reads. A seat
// whose provider could not be determined is absent, never guessed.
func (s seatSet) providers() map[string]string {
	out := map[string]string{}
	for _, rs := range s {
		if rs.Role != "" && rs.Provider != "" {
			out[rs.Role] = rs.Provider
		}
	}
	return out
}

// listOllama is a var so tests substitute a fake daemon.
var listOllama = modelcatalog.ListOllama

// errNoSource is the refusal for a bare model name that no declared source
// serves. A distinct type so the transitional resolveSeatRegistry adapter can
// tell it from a real configuration error.
type errNoSource struct{ flag, value string }

func (e errNoSource) Error() string {
	return fmt.Sprintf("--%s %q: no declared source serves this model — declare it in .corral/models.json, set OLLAMA_URL to the daemon that serves it, or pin MODEL_BACKEND", e.flag, e.value)
}

// resolveSeats resolves every seat flag ONCE, in one stated order, and
// rewrites an alias hit in place to its concrete model before anything else
// reads the value (so a cache key never sees an alias). Per seat value:
//
//  1. a registry alias (strict mode refuses a miss);
//  2. a coding-agent seat;
//  3. a pinned MODEL_BACKEND gateway (ollama, openrouter, openai with
//     OPENAI_BASE_URL) — never re-routed by the model's name;
//  4. a model OLLAMA_URL's daemon lists (a set OLLAMA_URL that cannot be
//     listed refuses the seat, naming the URL and the cause);
//  5. a cloud vendor named by the model's prefix;
//  6. otherwise refuse, naming the three ways to declare a source.
//
// A declared provider therefore always outranks what a name looks like. An
// unnamed seat and "off" are skipped: corral has no default models.
func resolveSeats(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, stderr io.Writer) (seatSet, error) {
	return resolveSeatsOpt(ctx, cmdName, repoRoot, seats, stderr, false)
}

// resolveSeatsOpt is resolveSeats with the adapter's one concession:
// skipNoSource leaves a seat no source declares unresolved instead of refusing.
func resolveSeatsOpt(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, stderr io.Writer, skipNoSource bool) (seatSet, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	reg, err := models.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	if reg.Len() == 0 && models.RepoLocalIgnored() && models.RepoLocalExists(repoRoot) {
		// Said once, so a workflow that relied on the checkout's own
		// registry learns why its aliases are now concrete names.
		fmt.Fprintf(stderr, "%s: %s in the checkout is IGNORED on a CI runner — the checkout is the change under audit and may not choose its own auditors; declare the registry with %s or %s instead\n",
			cmdName, models.RepoRelPath, models.EnvFile, models.EnvInline)
	}
	var (
		listed    []string
		listedOK  bool
		listedErr error
	)
	listing := func() ([]string, error) {
		if !listedOK && listedErr == nil {
			hc := &http.Client{Timeout: 10 * time.Second}
			listed, listedErr = listOllama(ctx, hc, strings.TrimSpace(os.Getenv("OLLAMA_URL")))
			listedOK = listedErr == nil
		}
		return listed, listedErr
	}

	set := seatSet{}
	var order []string
	for _, s := range seats {
		if s.val == nil {
			continue
		}
		v := strings.TrimSpace(*s.val)
		if v == "" || strings.EqualFold(v, "off") {
			continue
		}
		rs := agentbackend.ResolvedSeat{Flag: s.flag, Role: s.role, Spec: v, Kind: "api"}

		if e, ok := reg.Lookup(v); ok { // (1)
			*s.val = e.Model
			rs.Spec, rs.Provider, rs.Wire, rs.Endpoint, rs.KeyEnv = e.Model, e.Provider, e.Wire(), e.Endpoint, e.KeyEnv
			rs.DeclaredBy = agentbackend.DeclaredRegistry
		} else if reg != nil && reg.Strict {
			return nil, reg.UnknownAliasErr(s.flag, v)
		} else if isAgentSeat(v) { // (2)
			rs.Kind, rs.DeclaredBy = "agent", agentbackend.DeclaredAgent
			rs.Provider = agentbackend.VendorOf(v)
		} else if pinned, ok, err := pinnedGateway(rs); err != nil { // (3)
			return nil, err
		} else if ok {
			rs = pinned
		} else if served, u, err := servedBy(listing, v); err != nil { // (4)
			return nil, fmt.Errorf("--%s %q: OLLAMA_URL is set, but corral could not list models at %s: %w — start that daemon, or unset OLLAMA_URL if no seat runs there", s.flag, v, u, err)
		} else if served {
			rs.Provider, rs.Wire, rs.Endpoint = "ollama", models.WireOllama, u
			rs.DeclaredBy = agentbackend.DeclaredDiscovered
		} else if vendor := agentbackend.VendorOf(v); vendor != "" { // (5)
			rs.Provider, rs.Wire, rs.DeclaredBy = vendor, models.WireHosted, agentbackend.DeclaredName
		} else if skipNoSource { // (6), tolerated
			continue
		} else { // (6)
			return nil, errNoSource{flag: s.flag, value: v}
		}
		set[s.flag] = rs
		order = append(order, s.flag)
	}
	if len(order) > 0 {
		fmt.Fprintf(stderr, "  models: %s\n", resolutionSource(reg))
		for _, f := range order {
			fmt.Fprintf(stderr, "    %-19s %s\n", f+":", set[f].Describe())
		}
	}
	return set, nil
}

// servedBy reports whether OLLAMA_URL's daemon lists the model, and the URL
// it asked. An unset OLLAMA_URL declares no daemon: (false, "", nil). A set
// one that cannot be listed is an error, never "not served": falling through
// would route a bare local name like gpt-oss:120b by what its name resembles,
// to a vendor and its spend, and tell the operator to set the OLLAMA_URL they
// already set.
func servedBy(listing func() ([]string, error), model string) (bool, string, error) {
	u := strings.TrimSpace(os.Getenv("OLLAMA_URL"))
	if u == "" {
		return false, "", nil
	}
	names, err := listing()
	if err != nil {
		return false, u, err
	}
	for _, n := range names {
		if n == model {
			return true, u, nil
		}
	}
	return false, u, nil
}

// resolutionSource heads the disclosure: where declarations came from.
func resolutionSource(reg *models.Registry) string {
	if reg.Len() == 0 {
		return "no registry declared"
	}
	mode := "concrete model names are also accepted"
	if reg != nil && reg.Strict {
		mode = "strict — every seat must name an alias"
	}
	return fmt.Sprintf("from %s (%s)", reg.Source, mode)
}

// pinnedGateway resolves a seat against an explicitly pinned MODEL_BACKEND
// that is a GATEWAY: one endpoint fronting many models, so the model's name
// says nothing about where it goes. A vendor pin (anthropic, gemini, openai
// without OPENAI_BASE_URL) is not a gateway; its seats fall through to the
// later steps, exactly as baseVendor treats it.
func pinnedGateway(rs agentbackend.ResolvedSeat) (agentbackend.ResolvedSeat, bool, error) {
	rs.DeclaredBy = agentbackend.DeclaredPinned
	switch strings.TrimSpace(os.Getenv("MODEL_BACKEND")) {
	case "ollama":
		u := strings.TrimSpace(os.Getenv("OLLAMA_URL"))
		if u == "" {
			return rs, false, fmt.Errorf("--%s %q: MODEL_BACKEND=ollama names the gateway but OLLAMA_URL is not set — set OLLAMA_URL to the daemon", rs.Flag, rs.Spec)
		}
		rs.Provider, rs.Wire, rs.Endpoint = "ollama", models.WireOllama, u
		return rs, true, nil
	case "openrouter":
		rs.Provider, rs.Wire, rs.KeyEnv = "openrouter", models.WireHosted, "OPENROUTER_API_KEY"
		rs.Endpoint = firstSet(os.Getenv("OPENROUTER_BASE_URL"), os.Getenv("OPENAI_BASE_URL"), "https://openrouter.ai/api/v1")
		return rs, true, nil
	case "openai":
		if u := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); u != "" {
			rs.Provider, rs.Wire, rs.Endpoint, rs.KeyEnv = "openai", models.WireHosted, u, "OPENAI_API_KEY"
			return rs, true, nil
		}
	}
	return rs, false, nil
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// resolveSeatRegistry is the transitional adapter that keeps the callers not
// yet moved onto resolveSeats compiling. It returns the old seatResolution
// shape derived from the resolved set.
//
// One concession, to be deleted with the adapter: a bare name no source
// declares (errNoSource) is left untouched and unresolved here, as it always
// was, because those callers still hand such names to the legacy router. The
// caller migrations make that refusal real.
func resolveSeatRegistry(cmdName, repoRoot string, seats []seatFlag, stderr io.Writer) (*seatResolution, error) {
	set, err := resolveSeatsOpt(context.Background(), cmdName, repoRoot, seats, stderr, true)
	if err != nil {
		return nil, err
	}
	res := &seatResolution{endpoints: map[string]string{}, providers: set.providers()}
	for _, rs := range set {
		local := rs.Wire == models.WireOllama || rs.Wire == models.WireOpenAIChat
		if rs.Kind == "agent" || !local || rs.Endpoint == "" {
			continue
		}
		if rs.Role != "" {
			res.endpoints[rs.Role] = rs.Endpoint
		} else if rs.Flag == "derive-model" {
			res.deriveURL = rs.Endpoint
		}
	}
	return res, nil
}
