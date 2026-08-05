// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/criticscore"
)

type fakeFindingReader struct {
	all []criticscore.Finding
	err error
}

func (f fakeFindingReader) All(context.Context) ([]criticscore.Finding, error) { return f.all, f.err }
func (f fakeFindingReader) Get(_ context.Context, id string) (criticscore.Finding, bool, error) {
	for _, x := range f.all {
		if x.ID == id {
			return x, true, nil
		}
	}
	return criticscore.Finding{}, false, nil
}

func sampleFindings() []criticscore.Finding {
	return []criticscore.Finding{
		{ID: "32:1", Model: "claude-haiku-4-5", Severity: "high", Scope: "dead-check",
			TestFile: "src/auth/__tests__/TokenManager.test.ts", TargetTest: "scheduleNext",
			Evidence: "the setTimeout branch is never exercised", RecordID: 32, RecordHead: "abc",
			Adjudication: "confirmed", Source: "human", AdjudicatedBy: "local:pd",
			Rationale: "deleted the branch; only the new tests failed"},
		{ID: "32:4", Model: "claude-haiku-4-5", Severity: "medium", Scope: "dead-check",
			TestFile: "src/auth/__tests__/TokenManager.test.ts", TargetTest: "wireToHTTPClient",
			Evidence: "passes regardless of wiring", RecordID: 32, RecordHead: "abc",
			Adjudication: "refuted", Source: "human"},
		{ID: "33:1", Model: "gemini-3.6-flash", Severity: "low", Scope: "whole-test",
			TestFile: "src/client/__tests__/http.test.ts", TargetTest: "retries",
			Evidence: "asserts nothing", RecordID: 33, RecordHead: "def",
			Adjudication: "unadjudicated", Source: "auto"},
	}
}

// callTool drives a tool through a real in-memory MCP client/server pair, so
// the schema and wiring are exercised rather than the handler alone.
func callTool(t *testing.T, r findingReader, name string, args map[string]any) string {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerFindingTools(srv, r)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cli := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil)
	cs, err := cli.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	b, _ := json.Marshal(res)
	return string(b)
}

// TestListFindingsGroupsByFile: a developer works one file at a time, so the
// findings arrive grouped by the file they are about.
func TestListFindingsGroupsByFile(t *testing.T) {
	out := callTool(t, fakeFindingReader{all: sampleFindings()}, "list_audit_findings", map[string]any{})
	for _, want := range []string{"TokenManager.test.ts", "http.test.ts", "\"total\":3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
}

// TestListFindingsCarriesEvidenceAndProvenance is the product claim: a finding
// an agent can ACT on. A bare assertion is what this replaces, so the evidence,
// the signed record it came from, and any human reasoning must all travel with
// it.
func TestListFindingsCarriesEvidenceAndProvenance(t *testing.T) {
	out := callTool(t, fakeFindingReader{all: sampleFindings()}, "list_audit_findings", map[string]any{})
	for _, want := range []string{
		"the setTimeout branch is never exercised", // evidence
		"deleted the branch",                       // the human's reasoning
		"claude-haiku-4-5",                         // which critic said it
		"\"record_id\":32",                         // the signed record
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q to travel with the finding:\n%s", want, out)
		}
	}
}

// TestListFindingsAlwaysWarnsTheseAreUnverified: the caveat rides WITH the data.
// An agent that treats a critic finding as established fact is exactly the
// failure corral exists to catch, and it will not read a README.
func TestListFindingsAlwaysWarnsTheseAreUnverified(t *testing.T) {
	out := callTool(t, fakeFindingReader{all: sampleFindings()}, "list_audit_findings", map[string]any{})
	if !strings.Contains(out, "NOT proven") {
		t.Fatalf("every response must carry the unverified caveat:\n%s", out)
	}
}

// TestListFindingsFilters: by file and by status.
func TestListFindingsFilters(t *testing.T) {
	out := callTool(t, fakeFindingReader{all: sampleFindings()}, "list_audit_findings",
		map[string]any{"status": "unadjudicated"})
	if !strings.Contains(out, "33:1") || strings.Contains(out, "32:1") {
		t.Fatalf("status filter did not apply:\n%s", out)
	}
	out = callTool(t, fakeFindingReader{all: sampleFindings()}, "list_audit_findings",
		map[string]any{"test_file": "src/client/__tests__/http.test.ts"})
	if !strings.Contains(out, "33:1") || strings.Contains(out, "32:4") {
		t.Fatalf("file filter did not apply:\n%s", out)
	}
}

// TestNoAdjudicationToolIsExposed is the load-bearing guarantee of this
// surface. C-PREC is the critic's precision as judged by a human; let an agent
// confirm findings and the model marks its own exam, which is the one thing
// this project exists to prevent.
func TestNoAdjudicationToolIsExposed(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerFindingTools(srv, fakeFindingReader{all: sampleFindings()})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cli := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil)
	cs, err := cli.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		n := strings.ToLower(tl.Name)
		if strings.Contains(n, "adjudicat") || strings.Contains(n, "confirm") || strings.Contains(n, "refute") {
			t.Fatalf("the agent surface must be read-only; found %q", tl.Name)
		}
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("expected exactly the two read tools, got %d", len(tools.Tools))
	}
}

// TestGetFindingUnknownIDIsActionable: an unknown id must point the agent at
// how to get a real one, not just fail.
func TestGetFindingUnknownIDIsActionable(t *testing.T) {
	out := callTool(t, fakeFindingReader{all: sampleFindings()}, "get_audit_finding", map[string]any{"id": "99:9"})
	if !strings.Contains(out, "list_audit_findings") {
		t.Fatalf("an unknown id should name the tool that lists real ones:\n%s", out)
	}
}
