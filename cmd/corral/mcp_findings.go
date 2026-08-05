// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/criticscore"
)

// `corral mcp` serves the audit's FINDINGS to a coding agent over MCP on
// stdio, read-only, from the local store `certify --local` writes.
//
// The point is what a developer does with a verdict. A kill rate printed to a
// terminal is a fact nobody acts on; a finding delivered to the agent already
// sitting in the repository — with the evidence, the exact test, and the signed
// record it came from — is a task. That is the loop this closes: corral audits
// somewhere isolated (it mutates files in place, so it must not run in a tree
// you care about), and the agent that lives in the repo reads the finding,
// checks it against the real code, and fixes it.
//
// EVERY TOOL HERE IS READ-ONLY, and adjudication is deliberately absent.
//
// That is not an oversight or a phase-one limitation. `corral scorecard`'s
// C-PREC column is the test-critic's precision as judged by a human confirming
// or refuting its findings. Hand an agent the ability to confirm findings and
// that number becomes self-graded: the model marks its own exam, which is the
// single thing this project exists to prevent. An agent may gather evidence and
// argue; `corral criticscore confirm|refute --why` stays a human keystroke.
//
// Serving over stdio (not HTTP) is what makes it safe to ship on by default: a
// stdio server is reachable only by the process that spawned it, so there is no
// port, no listener, and no authentication question to get wrong.

// findingOut is one finding as an agent receives it. It is deliberately fatter
// than the CLI's table: an agent that cannot see the evidence can only relay a
// claim, and a relayed claim is exactly the unverified-model-opinion this tool
// exists to replace.
type findingOut struct {
	ID       string `json:"id"`
	Model    string `json:"critic_model" jsonschema:"which model produced this finding — its precision is measured, see corral scorecard"`
	Severity string `json:"severity"`
	Scope    string `json:"scope" jsonschema:"whole-test (the entire test cannot fail) or dead-check (one check inside it is inert)"`

	TestFile     string `json:"test_file"`
	TargetTest   string `json:"target_test"`
	TestSelector string `json:"test_selector" jsonschema:"the exact runnable selector for this one test, when the language plugin could derive one"`
	Evidence     string `json:"evidence" jsonschema:"the critic's stated reasoning — UNVERIFIED. Check it against the real code before acting."`

	Repo       string `json:"repo"`
	Commit     string `json:"commit"`
	RecordID   int64  `json:"record_id" jsonschema:"the signed audit record this finding came from"`
	RecordHead string `json:"record_head"`

	Adjudication string `json:"adjudication" jsonschema:"unadjudicated|confirmed|refuted — a HUMAN verdict, or none yet"`
	Source       string `json:"adjudication_source" jsonschema:"human or auto"`
	By           string `json:"adjudicated_by,omitempty"`
	Rationale    string `json:"adjudication_rationale,omitempty" jsonschema:"why a human reached that verdict, and what they checked"`
}

func toFindingOut(f criticscore.Finding) findingOut {
	return findingOut{
		ID: f.ID, Model: f.Model, Severity: f.Severity, Scope: f.Scope,
		TestFile: f.TestFile, TargetTest: f.TargetTest, TestSelector: f.TestSelector,
		Evidence: f.Evidence,
		Repo:     f.Repo, Commit: f.Commit, RecordID: f.RecordID, RecordHead: f.RecordHead,
		Adjudication: f.Adjudication, Source: f.Source, By: f.AdjudicatedBy, Rationale: f.Rationale,
	}
}

type listFindingsIn struct {
	TestFile string `json:"test_file,omitempty" jsonschema:"only findings against this test file (repo-relative)"`
	Status   string `json:"status,omitempty" jsonschema:"unadjudicated|confirmed|refuted — default: all"`
}

// fileGroup is findings grouped by the file they are about, which is how a
// developer actually works: one file open, every finding against it at hand.
type fileGroup struct {
	TestFile string       `json:"test_file"`
	Count    int          `json:"count"`
	Findings []findingOut `json:"findings"`
}

type listFindingsOut struct {
	Files []fileGroup `json:"files"`
	Total int         `json:"total"`
	Note  string      `json:"note"`
}

type getFindingIn struct {
	ID string `json:"id" jsonschema:"the finding id, e.g. \"32:2\""`
}

// findingReader is the read surface these tools need — satisfied by
// *criticscore.Store, and by a fake in tests.
type findingReader interface {
	All(ctx context.Context) ([]criticscore.Finding, error)
	Get(ctx context.Context, id string) (criticscore.Finding, bool, error)
}

// registerFindingTools wires the read-only findings tools onto s.
func registerFindingTools(s *mcp.Server, r findingReader) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_audit_findings",
		Description: "List corral's test-critic findings, grouped by the test file they are about. " +
			"Each carries the critic's evidence and the signed audit record it came from. " +
			"Findings are UNVERIFIED model opinions until a human adjudicates them — check one against the real code before acting on it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listFindingsIn) (*mcp.CallToolResult, listFindingsOut, error) {
		all, err := r.All(ctx)
		if err != nil {
			return nil, listFindingsOut{}, err
		}
		want := strings.TrimSpace(in.Status)
		file := strings.TrimSpace(in.TestFile)
		byFile := map[string][]findingOut{}
		total := 0
		for _, f := range all {
			if want != "" && f.Adjudication != want {
				continue
			}
			if file != "" && f.TestFile != file {
				continue
			}
			byFile[f.TestFile] = append(byFile[f.TestFile], toFindingOut(f))
			total++
		}
		names := make([]string, 0, len(byFile))
		for n := range byFile {
			names = append(names, n)
		}
		sort.Strings(names)
		out := listFindingsOut{Total: total, Note: findingsNote}
		for _, n := range names {
			g := byFile[n]
			sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
			out.Files = append(out.Files, fileGroup{TestFile: n, Count: len(g), Findings: g})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_audit_finding",
		Description: "Read one finding in full: the critic's evidence, the exact test it targets, " +
			"the signed record it came from, and any human verdict with the reasoning behind it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFindingIn) (*mcp.CallToolResult, findingOut, error) {
		f, ok, err := r.Get(ctx, strings.TrimSpace(in.ID))
		if err != nil {
			return nil, findingOut{}, err
		}
		if !ok {
			return nil, findingOut{}, fmt.Errorf("no finding %q — call list_audit_findings for the current ids", in.ID)
		}
		return nil, toFindingOut(f), nil
	})
}

// findingsNote rides on every list response. An agent that treats a critic
// finding as established fact is precisely the failure corral was built to
// catch, so the caveat travels WITH the data rather than living in a README the
// agent will not read.
const findingsNote = "These are a second model's opinions about your tests, checked for execution but NOT proven. " +
	"Verify one before changing anything: the reliable move is to break the code the test claims to cover and confirm the test fails. " +
	"Adjudication is deliberately not available here — recording a verdict is a human's call, via `corral criticscore confirm|refute <id> --why \"...\"`, " +
	"because that verdict is what scores the critic, and a model scoring itself is the thing this tool exists to prevent."

// runFindingsMCP serves the read-only findings tools over stdio until the
// client disconnects.
func runFindingsMCP(ctx context.Context, r findingReader, stderr io.Writer) int {
	s := mcp.NewServer(&mcp.Implementation{Name: "corralai-findings", Version: version}, nil)
	registerFindingTools(s, r)
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(stderr, "corral mcp:", err)
		return 1
	}
	return 0
}
