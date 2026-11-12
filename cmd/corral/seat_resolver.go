// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/modelcatalog"
	"github.com/pdbethke/corralai/internal/models"
)

// seatSet is every seat of one invocation, resolved once, keyed by the seat's
// flag name. It replaces the several maps (endpoints, providers, the derive seat's daemon)
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

// providersFor is providers() narrowed to the roles roster names with a
// model: the record's providers describe the seats its roster records, and
// no others. Nil when none has a resolved provider.
func (s seatSet) providersFor(roster map[string]string) map[string]string {
	var out map[string]string
	for role, p := range s.providers() {
		if m := strings.TrimSpace(roster[role]); m == "" || strings.EqualFold(m, "off") {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[role] = p
	}
	return out
}

// seatProvidersJSON is the ledger row's SeatProviders text: the role ->
// provider map as JSON, "" when there is none, so an entry whose seats
// resolved no provider records nothing rather than "{}".
func seatProvidersJSON(providers map[string]string) string {
	if len(providers) == 0 {
		return ""
	}
	b, _ := json.Marshal(providers)
	return string(b)
}

// listOllama is a var so tests substitute a fake daemon.
var listOllama = modelcatalog.ListOllama

// errNoSource is the refusal for a model name that no declaration serves. A
// distinct type so resolveSeatsPlaced can tell it from a real configuration
// error and offer --local-endpoint where the command takes it. placeable is
// set where the command takes --local-endpoint, the fifth way to declare a
// source; elsewhere the refusal must not offer a flag that is absent.
//
// It names every declaration, the vendor/model form among them, because a
// bare cloud name (claude-sonnet-5) lands here too: corral does not infer a
// vendor from a model's name (founder ruling 2026-11-12).
type errNoSource struct {
	flag, value string
	placeable   bool
}

func (e errNoSource) Error() string {
	const head = "--%s %q: no declared source serves this model, and corral does not infer a vendor from a model's name — declare it in .corral/models.json, name a hosted model as vendor/model (anthropic/…, google/…, openai/…), set OLLAMA_URL to the daemon that serves it, "
	if e.placeable {
		return fmt.Sprintf(head+"pin MODEL_BACKEND, or place the seat with --local-endpoint", e.flag, e.value)
	}
	return fmt.Sprintf(head+"or pin MODEL_BACKEND", e.flag, e.value)
}

// resolveSeats resolves every seat flag ONCE, in one stated order, and
// rewrites an alias hit in place to its concrete model before anything else
// reads the value (so a cache key never sees an alias). Per seat value:
//
//  1. a registry alias (strict mode refuses a miss);
//  2. a coding-agent seat;
//  3. a seat the operator placed on a daemon with --local-endpoint, unless it
//     is written in the explicit vendor/model form (that seat is hosted, and
//     the placement on it is refused as a placement on a cloud seat);
//  4. a pinned MODEL_BACKEND gateway (ollama, openrouter, openai with
//     OPENAI_BASE_URL), which receives the seat VERBATIM, a vendor/ prefix
//     included — the gateway decides what the name means, never corral;
//  5. the explicit vendor/model form, vendor exactly one of anthropic,
//     google, openai (a closed set: hf.co/… and library/… are not vendors).
//     The seat value is rewritten to the bare model, as an alias is, so a
//     cache key, --prior matching and the ledger's model fields keep their
//     spelling; the vendor lives on ResolvedSeat.Provider;
//  6. a model OLLAMA_URL's daemon lists (a set OLLAMA_URL that cannot be
//     listed refuses the seat, naming the URL and the cause);
//  7. otherwise refuse, naming every declaration.
//
// Nothing is inferred from a model's spelling. Step 6 of the earlier order
// routed claude-/gemini-/gpt- names to a vendor by prefix, and so routed a
// local gpt-oss:20b with no declared source to OpenAI — with OPENAI_API_KEY
// exported, the audited source would have gone to api.openai.com before
// anything failed. The guess decided where code and a credential went, so it
// is gone (founder ruling 2026-11-12, reversing D-20). An unnamed seat and
// "off" are skipped: corral has no default models.
func resolveSeats(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, stderr io.Writer) (seatSet, error) {
	return resolveSeatsOpt(ctx, cmdName, repoRoot, seats, nil, stderr)
}

// resolveSeatsPlaced is resolveSeats for a run that may carry --local-endpoint
// placements (role -> daemon URL, as parseLocalEndpoints returns them). A
// placement is itself a declared source for a seat with a bare local name, and
// an override of the endpoint of a seat that is already local; a placement on
// a cloud or agent seat is refused (applyLocalEndpoints). The placement is
// folded in BEFORE the disclosure prints, so the lines show where each seat
// will really talk.
func resolveSeatsPlaced(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, placed map[string]string, stderr io.Writer) (seatSet, error) {
	set, err := resolveSeatsOpt(ctx, cmdName, repoRoot, seats, placed, stderr)
	var ns errNoSource
	if errors.As(err, &ns) {
		ns.placeable = true
		return nil, ns
	}
	return set, err
}

// resolveSeatsOpt is the one body behind resolveSeats and resolveSeatsPlaced;
// placed is nil where the command takes no --local-endpoint.
func resolveSeatsOpt(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, placed map[string]string, stderr io.Writer) (seatSet, error) {
	return resolveSeatsListed(ctx, cmdName, repoRoot, seats, placed, stderr, &ollamaListing{})
}

// ollamaListing lists OLLAMA_URL's daemon at most once, however many
// resolutions share it. A caller that resolves seat by seat (the shadow pool
// check) passes one listing to every call, so a stale daemon costs one
// timeout, not one per seat. A failed listing is cached too: the error is the
// answer, and asking again would only wait again.
type ollamaListing struct {
	names []string
	err   error
	done  bool
}

func (l *ollamaListing) get(ctx context.Context) ([]string, error) {
	if !l.done {
		hc := &http.Client{Timeout: 10 * time.Second}
		l.names, l.err = listOllama(ctx, hc, strings.TrimSpace(os.Getenv("OLLAMA_URL")))
		l.done = true
	}
	return l.names, l.err
}

// resolveSeatsListed is resolveSeatsOpt over a listing the caller may share.
func resolveSeatsListed(ctx context.Context, cmdName, repoRoot string, seats []seatFlag, placed map[string]string, stderr io.Writer, lst *ollamaListing) (seatSet, error) {
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
	listing := func() ([]string, error) { return lst.get(ctx) }

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
		} else if _, _, explicit := agentbackend.SplitVendorModel(v); placed[s.role] != "" && !explicit { // (3)
			rs.Provider, rs.Wire, rs.Endpoint = "ollama", models.WireOllama, placed[s.role]
			rs.DeclaredBy = agentbackend.DeclaredFlag
		} else if pinned, ok, err := pinnedGateway(rs); err != nil { // (4)
			return nil, err
		} else if ok {
			rs = pinned
		} else if vendor, model, ok := agentbackend.SplitVendorModel(v); ok { // (5)
			*s.val = model
			rs.Spec, rs.Provider, rs.Wire, rs.DeclaredBy = model, vendor, models.WireHosted, agentbackend.DeclaredVendor
		} else if served, u, err := servedBy(listing, v); err != nil { // (6)
			return nil, fmt.Errorf("--%s %q: OLLAMA_URL is set, but corral could not list models at %s: %w — start that daemon, or unset OLLAMA_URL if no seat runs there", s.flag, v, u, err)
		} else if served {
			rs.Provider, rs.Wire, rs.Endpoint = "ollama", models.WireOllama, u
			rs.DeclaredBy = agentbackend.DeclaredDiscovered
		} else { // (7)
			return nil, errNoSource{flag: s.flag, value: v}
		}
		set[s.flag] = rs
		order = append(order, s.flag)
	}
	if err := applyLocalEndpoints(set, placed); err != nil {
		return nil, err
	}
	if len(order) > 0 {
		fmt.Fprintf(stderr, "  models: %s\n", resolutionSource(reg))
		for _, f := range order {
			fmt.Fprintf(stderr, "    %-19s %s\n", f+":", set[f].Describe())
		}
	}
	return set, nil
}

// applyLocalEndpoints folds --local-endpoint placements into the resolved
// seats: a local seat keeps its wire and moves to the placed daemon (one
// daemon per GPU is the whole point of the flag); a seat that is not served by
// a local daemon is refused rather than ignored, because silently dropping the
// placement would leave the operator believing a seat runs somewhere it does
// not. A role with no seat (no model assigned, or off) is skipped, as it
// always was.
func applyLocalEndpoints(set seatSet, placed map[string]string) error {
	roles := make([]string, 0, len(placed))
	for role := range placed {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		url := placed[role]
		rs, ok := set.byRole(role)
		if !ok {
			continue
		}
		if rs.Kind == "agent" {
			return fmt.Errorf("--local-endpoint %s=%s: seat %s is the agent seat %q, which runs as a CLI and is not served by a local daemon — remove the endpoint, or give this seat a local model", role, url, role, rs.Spec)
		}
		if rs.Wire != models.WireOllama && rs.Wire != models.WireOpenAIChat {
			return fmt.Errorf("--local-endpoint %s=%s: seat %s runs the CLOUD model %q (vendor %s), which is not served by a local daemon — remove the endpoint, or give this seat a local model", role, url, role, rs.Spec, rs.Provider)
		}
		if rs.Endpoint != url {
			rs.Endpoint, rs.DeclaredBy = url, agentbackend.DeclaredFlag
			set[rs.Flag] = rs
		}
	}
	return nil
}

// servedBy reports whether OLLAMA_URL's daemon lists the model, and the URL
// it asked. An unset OLLAMA_URL declares no daemon: (false, "", nil). A set
// one that cannot be listed is an error, never "not served": falling through
// to the no-source refusal would tell the operator to set the OLLAMA_URL they
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
// without OPENAI_BASE_URL) is not a gateway and routes nothing here; its seats
// fall through to the later steps and need a declaration of their own.
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
		// OpenRouter's own variable and nothing else. FromEnv's openrouter arm
		// fell back to OPENAI_API_KEY, which with only an OpenAI key set sent
		// that OpenAI secret to openrouter.ai. Unset, Build refuses naming it.
		rs.Provider, rs.Wire, rs.KeyEnv = "openrouter", models.WireHosted, "OPENROUTER_API_KEY"
		// The URL likewise: OPENROUTER_BASE_URL or OpenRouter's own. Never
		// OPENAI_BASE_URL, which names an OpenAI-compatible endpoint (often a
		// company proxy) that must not receive the OpenRouter key.
		rs.Endpoint = firstSet(os.Getenv("OPENROUTER_BASE_URL"), "https://openrouter.ai/api/v1")
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
