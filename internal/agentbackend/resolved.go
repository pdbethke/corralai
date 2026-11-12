// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"fmt"

	"github.com/pdbethke/corralai/internal/models"
)

// DeclaredBy says where a seat's routing came from, so a disclosure line can
// tell an operator whether the endpoint was chosen or inferred.
type DeclaredBy string

const (
	DeclaredRegistry   DeclaredBy = "registry"
	DeclaredAgent      DeclaredBy = "agent"
	DeclaredPinned     DeclaredBy = "pinned"
	DeclaredDiscovered DeclaredBy = "discovered"
	// DeclaredVendor is a seat named in the explicit vendor/model form
	// (anthropic/…, google/…, openai/…): the operator named the vendor.
	DeclaredVendor DeclaredBy = "vendor/model"
	// DeclaredFlag is a seat the operator placed on a daemon with
	// --local-endpoint: the most specific statement there is about one seat.
	DeclaredFlag DeclaredBy = "--local-endpoint"
)

// ResolvedSeat is one seat's routing, decided once: which provider serves it,
// over which wire, at which endpoint, with which key. Build is the only
// constructor that turns it into a Backend, so no door re-guesses the answer
// from the model's name.
type ResolvedSeat struct {
	Flag, Role string // e.g. "writer-model", advpool.RoleTestWriter ("" for derive/review)
	Spec       string // concrete model name, or the agent spec
	Kind       string // "api" | "agent"
	Provider   string // registry provider or vendor
	Wire       models.Wire
	Endpoint   string // "" = the hosted vendor's own endpoint
	KeyEnv     string // env var holding the key; "" when keyless (local) or the vendor default
	DeclaredBy DeclaredBy
}

// Describe is the one disclosure line for a seat.
func (rs ResolvedSeat) Describe() string {
	if rs.Kind == "agent" {
		return fmt.Sprintf("agent %s (%s)", rs.Spec, rs.DeclaredBy)
	}
	at := rs.Endpoint
	if at == "" {
		at = "vendor endpoint"
	}
	return fmt.Sprintf("%s/%s @ %s (%s)", rs.Provider, rs.Spec, at, rs.DeclaredBy)
}

// Build is the single constructor from a ResolvedSeat to a Backend. An agent
// seat goes through newAgentSeat, the path ForSeat uses, so agent handling is
// not duplicated here.
func Build(rs ResolvedSeat, opt SeatOptions) (Backend, error) {
	if rs.Kind == "agent" {
		b, isAgent, err := newAgentSeat(rs.Spec, opt)
		if err != nil {
			return nil, err
		}
		if !isAgent {
			return nil, fmt.Errorf("agentbackend: --%s: %q is not an agent seat", rs.Flag, rs.Spec)
		}
		return b, nil
	}
	switch rs.Wire {
	case models.WireOllama:
		if rs.Endpoint == "" {
			return nil, fmt.Errorf("agentbackend: --%s: model %q is served over the ollama wire but no endpoint was resolved", rs.Flag, rs.Spec)
		}
		return &ollamaBackend{url: rs.Endpoint, model: rs.Spec}, nil
	case models.WireOpenAIChat:
		if rs.Endpoint == "" {
			return nil, fmt.Errorf("agentbackend: --%s: model %q is served over the openai-chat wire but no endpoint was resolved", rs.Flag, rs.Spec)
		}
		key, err := seatKey(rs)
		if err != nil {
			return nil, err
		}
		return &openaiBackend{base: rs.Endpoint, key: key, model: rs.Spec}, nil
	case models.WireHosted:
		if rs.Endpoint == "" {
			// The vendor the resolver recorded from a declaration, never one
			// read off the model's name.
			return ForVendor(rs.Provider, rs.Spec)
		}
		key, err := seatKey(rs)
		if err != nil {
			return nil, err
		}
		return newOpenAIBackend(rs.Endpoint, key, rs.Spec), nil
	}
	return nil, fmt.Errorf("agentbackend: --%s: model %q has unknown wire %q", rs.Flag, rs.Spec, rs.Wire)
}

// seatKey reads the seat's key from KeyEnv ("" KeyEnv = keyless). A named but
// empty variable is an error, never a silent unauthenticated call.
func seatKey(rs ResolvedSeat) (string, error) {
	if rs.KeyEnv == "" {
		return "", nil
	}
	k := agentSecret(rs.KeyEnv)
	if k == "" {
		return "", fmt.Errorf("agentbackend: --%s: model %q needs a key — set %s", rs.Flag, rs.Spec, rs.KeyEnv)
	}
	return k, nil
}
