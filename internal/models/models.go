// SPDX-License-Identifier: Elastic-2.0

// Package models is corral's model registry: the ONE place a project declares
// which models it is willing to run, so every seat can be named by a short
// alias instead of a vendor model string copied into a dozen commands, CI
// files and docs.
//
// WHY IT EXISTS. corral has no default models — every seat is named by the
// operator or the run refuses to start — and the cost of that rule is a model
// name written once, in many places, and never re-verified. Three separate
// incidents in one week traced back to a stale name: a documented example
// naming a model that has never existed, a CI workflow pinned to the same
// phantom, and a daemon running a critic below the project's own recency
// floor. A registry does not verify names by itself, but it gives the gates
// that will one thing to check instead of a corpus to grep.
//
// WHAT IT IS NOT. It is not a default. The registry declares what MAY be used;
// the operator still names a seat or the run refuses, exactly as before. An
// alias literally named "default" is refused at load — the word is the trap
// this project spent a release removing. And it is never a FALLBACK: nothing
// here substitutes one model for another, because a substituted critic breaks
// decorrelation and a signed verdict would then name a model nobody chose.
//
// NO NETWORK. This package reads files and parses JSON. It never contacts a
// provider: resolving an entry against a provider's live model listing is a
// separate, key-requiring gate (`corral doctor`), and a package that could
// reach the network from a parse would make every unit test that loads a
// registry a potential model call.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The env overrides, named as constants so a test and the loader cannot
// disagree about their spelling.
const (
	// EnvInline carries a whole registry as inline JSON. Highest precedence:
	// it is the most explicit thing an operator can say, and it is how a CI
	// job declares a herd without committing a file.
	EnvInline = "CORRALAI_MODELS"
	// EnvFile names a registry file elsewhere on disk. Beats the repo file,
	// loses to inline JSON.
	EnvFile = "CORRALAI_MODELS_FILE"
)

// StrictKey is the ONE reserved top-level key in a registry document. It is a
// sibling of the aliases rather than a wrapper around them because the
// published design shows a flat object and every example, doc line and CI
// snippet already written follows it: nesting the aliases under "models" would
// invalidate all of them on the morning the feature ships, to buy a collision
// with a single word. An entry that tries to USE this word as an alias is
// refused by name, so the collision is impossible rather than silent.
const StrictKey = "strict"

// RepoRelPath is where a project's own registry lives, relative to its root.
var RepoRelPath = filepath.Join(".corral", "models.json")

// localProviders are the providers served by a daemon the OPERATOR runs, which
// is why they carry an endpoint and hosted providers must not. Local models are
// first-class here on purpose: a local generator against a hosted writer is the
// strongest decorrelation available (no shared vendor, lineage or training run)
// and it is the honest answer to the cost objection.
var localProviders = map[string]bool{
	"ollama":    true,
	"llamacpp":  true,
	"llamafile": true,
	"lmstudio":  true,
	"localai":   true,
	"vllm":      true,
}

// hostedProviders are the vendors corral can route to by name. An endpoint on
// one of these is refused rather than ignored: the operator is describing a
// placement that cannot happen, and silently dropping it would leave them
// believing a seat runs somewhere it does not.
var hostedProviders = map[string]bool{
	"anthropic":  true,
	"google":     true,
	"openai":     true,
	"openrouter": true,
}

// Entry is one declared model: the provider that serves it, the concrete model
// name that provider knows it by, and — for a local provider only — the daemon
// endpoint it is served from.
//
// The concrete Model is what everything downstream records. The alias is a
// label for humans; it is never authoritative and never reaches a cache key.
type Entry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint,omitempty"`
}

// IsLocal reports whether this entry is served by a daemon the operator runs.
func (e Entry) IsLocal() bool { return localProviders[strings.ToLower(strings.TrimSpace(e.Provider))] }

// Registry is a loaded, VALIDATED set of declared models. A nil *Registry is
// the ordinary "this project declares none" case and answers every lookup with
// "not an alias" — the registry is additive, so a repo without one behaves
// exactly as it did before this package existed.
type Registry struct {
	// Strict is the registry's answer to the typo. OFF (the default), a seat
	// value that is not a declared alias is the concrete model name this flag
	// has always accepted — which means a MISTYPED alias sails through as a
	// bogus model and fails at the seat, hours into a paid run. That is the
	// exact failure this registry exists to remove, so a project can close the
	// door: with `"strict": true`, every seat must name a declared alias or
	// the run refuses before anything is spent. "off" stays special (a
	// deliberately disabled seat is not a model), and an unnamed seat still
	// hits the no-default-models refusal — strict mode narrows what a NAMED
	// seat may say, and never fills one in.
	Strict bool
	// Source is where these entries came from, for disclosure. An operator
	// reading "alias fast → google/gemini-3.6-flash" must be able to see WHICH
	// declaration answered, since three can be in play at once.
	Source  string
	entries map[string]Entry
}

// Lookup answers whether a value is a declared alias, and with what.
//
// An unknown value is NOT an error here. The seat flags accept either an alias
// or a concrete model name, so "not an alias" is an ordinary answer that the
// caller turns into "treat it as a concrete name".
func (r *Registry) Lookup(alias string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	e, ok := r.entries[strings.TrimSpace(alias)]
	return e, ok
}

// Len is how many models this project declares (0 for a nil registry).
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.entries)
}

// Aliases lists the declared aliases, sorted, for a refusal message.
func (r *Registry) Aliases() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.entries))
	for a := range r.entries {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// UnknownAliasErr is the refusal for a value that LOOKS like it was meant to be
// an alias but is not declared. Callers use it only where a concrete model name
// is impossible; the seat flags themselves fall through to the concrete-name
// path instead, because that is what they have always accepted.
func (r *Registry) UnknownAliasErr(flagName, value string) error {
	known := "none — this registry declares no models"
	if a := r.Aliases(); len(a) > 0 {
		known = strings.Join(a, ", ")
	}
	src := "the model registry"
	if r != nil && r.Source != "" {
		src = r.Source
	}
	if r != nil && r.Strict {
		return fmt.Errorf("--%s %q: no such model alias in %s, and that registry is STRICT (%q: true), so every seat must name a declared alias; declared aliases are: %s — name one of those, or declare %q in the registry, or drop strict mode to pass concrete model names", flagName, value, src, StrictKey, known, value)
	}
	return fmt.Errorf("--%s %q: no such model alias in %s; declared aliases are: %s — name one of those, or pass a concrete model name", flagName, value, src, known)
}

// Load resolves the registry for a run rooted at repoRoot.
//
// Precedence, most explicit first: inline JSON in CORRALAI_MODELS, then the
// file named by CORRALAI_MODELS_FILE, then <repoRoot>/.corral/models.json.
// Only ONE of them is read — they do not merge. Merging would mean an operator
// overriding one alias silently inherits the rest of a declaration they may not
// have read, which is the same shape of surprise as a default.
//
// Absent everything, Load returns (nil, nil): no registry is not an error.
// A CORRALAI_MODELS_FILE that does not exist IS an error — the operator named
// a file, and a silently ignored override is a run that used models they did
// not declare.
func Load(repoRoot string) (*Registry, error) {
	if inline := strings.TrimSpace(os.Getenv(EnvInline)); inline != "" {
		return Parse([]byte(inline), EnvInline)
	}
	if path := strings.TrimSpace(os.Getenv(EnvFile)); path != "" {
		b, err := os.ReadFile(path) // #nosec G304,G703 -- path is the operator's own CORRALAI_MODELS_FILE, the same trust level as the file paths they pass to --goals/--mutants; corral reads it as that operator, never as a service
		if err != nil {
			return nil, fmt.Errorf("%s=%s: cannot read the model registry: %v — fix the path, or unset %s to fall back to %s", EnvFile, path, err, EnvFile, RepoRelPath)
		}
		return Parse(b, path)
	}
	if repoRoot == "" {
		return nil, nil
	}
	path := filepath.Join(repoRoot, RepoRelPath)
	b, err := os.ReadFile(path) // #nosec G304,G703 -- a fixed relative name (.corral/models.json) under the repo root corral was already told to scan
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: cannot read the model registry: %v", path, err)
	}
	return Parse(b, path)
}

// Parse validates a registry document. Every fault is REFUSED, never repaired:
// a registry that quietly drops a malformed entry is a registry an operator
// cannot trust to say what a run may use.
func Parse(data []byte, source string) (*Registry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: the model registry is not valid JSON: %v — it must be an object of {\"alias\": {\"provider\": …, \"model\": …}}", source, err)
	}
	reg := &Registry{Source: source, entries: make(map[string]Entry, len(raw))}
	if rm, ok := raw[StrictKey]; ok {
		// Reserved, and refused loudly if someone declares a model by this
		// name: a key that silently meant two things depending on its value
		// would be worse than either meaning.
		if err := json.Unmarshal(rm, &reg.Strict); err != nil {
			return nil, fmt.Errorf("%s: %q is a reserved key — it turns strict mode on or off and must be true or false, so a model cannot be declared under that name; rename the alias (and set \"strict\": true if that is what you meant)", source, StrictKey)
		}
		delete(raw, StrictKey)
	}
	for alias, rm := range raw {
		name := strings.TrimSpace(alias)
		if name == "" {
			return nil, fmt.Errorf("%s: an alias is empty — every declared model needs a name", source)
		}
		// "default" is refused outright. corral has no default models, and an
		// alias by that name would smuggle the idea back in through a
		// convention ("just leave it out and you get default"), which is
		// exactly the rule this project removed on purpose.
		if strings.EqualFold(name, "default") {
			return nil, fmt.Errorf("%s: alias %q is refused: corral has no default models, and an alias by that name invites a seat to be filled by convention rather than named — rename it to what the model IS (e.g. \"fast\", \"strong\")", source, alias)
		}
		var e Entry
		if err := json.Unmarshal(rm, &e); err != nil {
			return nil, fmt.Errorf("%s: alias %q: an entry must be an object like {\"provider\": \"google\", \"model\": \"gemini-3.6-flash\"}: %v", source, alias, err)
		}
		e.Provider = strings.ToLower(strings.TrimSpace(e.Provider))
		e.Model = strings.TrimSpace(e.Model)
		e.Endpoint = strings.TrimSpace(e.Endpoint)
		if e.Provider == "" {
			return nil, fmt.Errorf("%s: alias %q has no \"provider\" — add one of: %s", source, alias, knownProviders())
		}
		if e.Model == "" {
			return nil, fmt.Errorf("%s: alias %q has no \"model\" — add the concrete model name the provider serves, e.g. \"model\": \"gemini-3.6-flash\"", source, alias)
		}
		local := localProviders[e.Provider]
		if !local && !hostedProviders[e.Provider] {
			return nil, fmt.Errorf("%s: alias %q names an unknown provider %q — corral routes by provider, so it must be one of: %s", source, alias, e.Provider, knownProviders())
		}
		switch {
		case local && e.Endpoint == "":
			return nil, fmt.Errorf("%s: alias %q has provider %q, which is served by a daemon you run, so it needs an \"endpoint\" — add e.g. \"endpoint\": \"http://127.0.0.1:11434\"", source, alias, e.Provider)
		case !local && e.Endpoint != "":
			return nil, fmt.Errorf("%s: alias %q has an \"endpoint\" but provider %q is hosted by its vendor, which no local endpoint can serve — drop the endpoint, or name a local provider (%s)", source, alias, e.Provider, strings.Join(sortedKeys(localProviders), ", "))
		}
		if local {
			u, err := url.Parse(e.Endpoint)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return nil, fmt.Errorf("%s: alias %q: \"endpoint\" %q needs an absolute url with a scheme and host, e.g. http://127.0.0.1:11434", source, alias, e.Endpoint)
			}
			e.Endpoint = strings.TrimSuffix(e.Endpoint, "/")
		}
		if _, dup := reg.entries[name]; dup {
			return nil, fmt.Errorf("%s: alias %q is declared twice", source, alias)
		}
		reg.entries[name] = e
	}
	return reg, nil
}

func knownProviders() string {
	all := append(sortedKeys(hostedProviders), sortedKeys(localProviders)...)
	return strings.Join(all, ", ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
