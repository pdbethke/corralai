// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auth"
)

// askFleet is the read-only fleet-oracle endpoint. It accepts a natural-language
// question, runs it through the oracle's NL→SQL→narrate pipeline over the
// MotherDuck reporting DB, and returns the structured Answer as JSON (narration +
// columns + rows + generated SQL). This endpoint is purely observational: it
// cannot mutate any swarm state.
//
// Returns 503 when the oracle is unconfigured (nil or Enabled()==false), matching
// the same disabled-when-unconfigured pattern as the /api/ask narrator endpoint.
func (s *Server) askFleet(w http.ResponseWriter, r *http.Request) {
	// GET is the availability probe: always 200 with {enabled}, so the UI can
	// decide whether to show the panel without logging a red 503 on every load.
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]bool{"enabled": s.oracle != nil && s.oracle.Enabled()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// A read-only observer may probe (GET, above) but never run the NL→SQL
	// oracle — it invokes a model and the MCP ask_fleet tool is already behind
	// denyReadOnly. Gate before touching the oracle.
	if auth.ReadOnly(r) {
		http.Error(w, "forbidden: read-only observer token cannot act", http.StatusForbidden)
		return
	}
	if s.oracle == nil || !s.oracle.Enabled() {
		http.Error(w, "fleet oracle unavailable (configure CORRALAI_MOTHERDUCK + a model backend)", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Question) == "" {
		http.Error(w, "question required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	ans, err := s.oracle.Ask(ctx, body.Question)
	if err != nil {
		http.Error(w, "oracle error: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ans)
}
