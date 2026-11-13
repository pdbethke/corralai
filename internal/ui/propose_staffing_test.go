// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProposeStaffingNamesNoModelWithoutAPlanner: with no staffing planner the
// endpoint used to answer ok with four hard-coded local models
// (qwen2.5-coder:7b ×3, llama3.2:3b), presented as a proposal. It must refuse
// and say so instead of naming models nobody chose.
func TestProposeStaffingNamesNoModelWithoutAPlanner(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.proposeStaffing(rec, httptest.NewRequest(http.MethodGet, "/api/mission/propose_staffing?directive=x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		OK          bool           `json:"ok"`
		Error       string         `json:"error"`
		Assignments map[string]any `json:"assignments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	if body.OK || len(body.Assignments) != 0 || !strings.Contains(body.Error, "by hand") {
		t.Fatalf("no-planner response = %+v; want ok=false, no assignments, and an error telling the operator to assign by hand", body)
	}
}

// TestConsoleWorksOutNoBackendFromAName pins the page's half of the same rule:
// the composer used to call agentBackend(), which read "claude"/"gpt"/"gemini"
// off a name (and called everything else ollama), and offered a stand-in
// agent list when none had reported.
func TestConsoleWorksOutNoBackendFromAName(t *testing.T) {
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, banned := range []string{"agentBackend(", `["claude", "aider", "copilot", "ollama"]`, "a.backend || 'ollama'"} {
		if strings.Contains(page, banned) {
			t.Errorf("index.html still contains %q", banned)
		}
	}
}
