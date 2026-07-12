// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/authoring"
	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/gate"
	"github.com/pdbethke/corralai/internal/testgen"
)

// stager stages a candidate from a fully-built request. In production it's a
// closure over controlgate.StageCandidate bound to the model + jail; tests stub it.
type stager func(ctx context.Context, req controlgate.StageRequest) (controlspec.GateTest, error)

type stageControlOut struct {
	Goal     string            `json:"goal"`
	Target   string            `json:"target"`
	KillRate float64           `json:"kill_rate"`
	Survived []string          `json:"survived"`
	Triage   []testgen.Verdict `json:"triage"`
	Vetted   bool              `json:"vetted"`
}

// stageControl looks up the goal, builds the StageRequest (goal INTENT + the
// LangScaffold recipe + the workspace paths), stages a candidate, and shapes a
// summary. The candidate is stored UNVETTED by StageCandidate.
func stageControl(ctx context.Context, store *controlspec.Store, stage stager,
	owner, goalID, target, code, lang, codePath, testPath string, nMutants int, now time.Time) (stageControlOut, error) {
	g, ok, err := store.GetGoal(owner, goalID)
	if err != nil {
		return stageControlOut{}, err
	}
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: no goal %q for this owner", goalID)
	}
	base, testCmd, ok := controlgate.LangScaffold(lang)
	if !ok {
		return stageControlOut{}, fmt.Errorf("controltools: unsupported lang %q", lang)
	}
	if nMutants <= 0 {
		nMutants = 5
	}
	req := controlgate.StageRequest{
		Request: authoring.Request{
			Goal: g.Intent, Code: code, Lang: lang, CodePath: codePath, TestPath: testPath,
			Base: base, TestCmd: testCmd, NMutants: nMutants,
		},
		Owner: owner, GoalID: goalID, Target: target, Now: now,
	}
	gt, err := stage(ctx, req)
	if err != nil {
		return stageControlOut{}, err
	}
	var verdicts []testgen.Verdict
	if gt.VerdictsJSON != "" {
		_ = json.Unmarshal([]byte(gt.VerdictsJSON), &verdicts)
	}
	return stageControlOut{Goal: goalID, Target: target, KillRate: gt.KillRate,
		Survived: gt.Survived, Triage: verdicts, Vetted: false}, nil
}

// getControl returns one PENDING (unvetted) candidate by (goal,target) for the
// owner to read as code — the test source, kill rate, survivors, and triage.
func getControl(store *controlspec.Store, owner, goal, target string) (controlspec.GateTest, error) {
	gt, ok, err := store.GetCandidate(owner, goal, target)
	if err != nil {
		return controlspec.GateTest{}, err
	}
	if !ok {
		return controlspec.GateTest{}, fmt.Errorf("controltools: no pending candidate %s@%s", goal, target)
	}
	return gt, nil
}

type stageControlIn struct {
	GoalID   string `json:"goal_id" jsonschema:"the control goal id (from list_control_goals)"`
	Target   string `json:"target" jsonschema:"repo-relative path the control applies to, e.g. internal/auth/login.go"`
	Code     string `json:"code" jsonschema:"the target file's current source (the writer authors a test against this shape)"`
	Lang     string `json:"lang" jsonschema:"source language; v1 supports 'go'"`
	CodePath string `json:"code_path" jsonschema:"flat filename for the code in the jail workspace, e.g. login.go"`
	TestPath string `json:"test_path" jsonschema:"flat test filename, e.g. login_control_test.go (must end _test.go for go)"`
	NMutants int    `json:"n_mutants,omitempty" jsonschema:"how many seeded violations to score adequacy against (default 5)"`
}
type goalIn struct {
	Goal   string `json:"goal" jsonschema:"the control goal id"`
	Target string `json:"target" jsonschema:"the target path"`
}
type importBundleIn struct {
	Bundle string `json:"bundle" jsonschema:"bundle name, e.g. asvs-l1"`
}
type goalsOut struct {
	Goals []controlspec.Goal `json:"goals"`
}
type pendingOut struct {
	Pending []pendingSummary `json:"pending"`
}
type pendingSummary struct {
	Goal     string  `json:"goal"`
	Target   string  `json:"target"`
	KillRate float64 `json:"kill_rate"`
}
type importOut struct {
	Imported int `json:"imported"`
}

func registerControlTools(s *mcp.Server, opts Options) {
	store := opts.ControlSpec

	mcp.AddTool(s, &mcp.Tool{Name: "import_control_bundle",
		Description: "ADMIN: import a control-standard bundle (e.g. asvs-l1) as owner-scoped goals."},
		func(_ context.Context, req *mcp.CallToolRequest, in importBundleIn) (*mcp.CallToolResult, importOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, importOut{}, errAdminOnly
			}
			owner := identity(req, "")
			b, err := controlspec.LoadBundle(in.Bundle)
			if err != nil {
				return nil, importOut{}, err
			}
			n, err := controlspec.ImportBundle(store, owner, b, time.Now().UTC())
			if err == nil {
				auditKnowledge(opts, req, "import_control_bundle", map[string]any{"bundle": in.Bundle, "imported": n})
			}
			return nil, importOut{Imported: n}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_control_goals",
		Description: "List your control goals (the bar the control gate holds code to)."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, goalsOut, error) {
			g, err := store.ListGoals(identity(req, ""))
			return nil, goalsOut{Goals: g}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "stage_control",
		Description: "ADMIN: author a candidate test for a goal+target and score its mutation-adequacy. Stored UNVETTED for your review — never gates until promote_control."},
		func(ctx context.Context, req *mcp.CallToolRequest, in stageControlIn) (*mcp.CallToolResult, stageControlOut, error) {
			if !opts.isHumanAdmin(req) {
				return nil, stageControlOut{}, errAdminOnly
			}
			if opts.ControlModel == nil {
				return nil, stageControlOut{}, fmt.Errorf("stage_control: no model configured (set the brain's model backend)")
			}
			if opts.GateBackend == nil {
				return nil, stageControlOut{}, fmt.Errorf("stage_control: no sandbox backend — refusing to run authored tests unsandboxed")
			}
			model := opts.ControlModel
			jail := adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)
			stage := func(ctx context.Context, sreq controlgate.StageRequest) (controlspec.GateTest, error) {
				return controlgate.StageCandidate(ctx, model, model, jail, store, sreq)
			}
			owner := identity(req, "")
			out, err := stageControl(ctx, store, stage, owner, in.GoalID, in.Target, in.Code, in.Lang, in.CodePath, in.TestPath, in.NMutants, time.Now().UTC())
			if err == nil {
				auditKnowledge(opts, req, "stage_control", map[string]any{"goal": in.GoalID, "target": in.Target, "kill_rate": out.KillRate})
			}
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_pending_controls",
		Description: "List your UNVETTED candidate controls awaiting review (goal, target, kill rate)."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, pendingOut, error) {
			pend, err := store.ListPending(identity(req, ""))
			if err != nil {
				return nil, pendingOut{}, err
			}
			out := pendingOut{}
			for _, gt := range pend {
				out.Pending = append(out.Pending, pendingSummary{Goal: gt.Goal, Target: gt.Target, KillRate: gt.KillRate})
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_control",
		Description: "Fetch one pending candidate in full — the test source, kill rate, surviving mutants, and reviewer triage — to read before promoting."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, controlspec.GateTest, error) {
			gt, err := getControl(store, identity(req, ""), in.Goal, in.Target)
			return nil, gt, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "promote_control",
		Description: "ADMIN: approve a pending candidate into the vetted store the control gate runs. The recorded, attributed human gate."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			owner := identity(req, "")
			ok, err := store.Promote(owner, in.Goal, in.Target, time.Now().UTC())
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no pending candidate to promote"}, err
			}
			auditKnowledge(opts, req, "promote_control", map[string]any{"goal": in.Goal, "target": in.Target})
			return nil, okMsg{OK: true, Message: in.Goal + "@" + in.Target + " is now vetted"}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reject_control",
		Description: "ADMIN: delete a candidate control (vetted or not)."},
		func(_ context.Context, req *mcp.CallToolRequest, in goalIn) (*mcp.CallToolResult, okMsg, error) {
			if !opts.isHumanAdmin(req) {
				return nil, okMsg{}, errAdminOnly
			}
			ok, err := store.Reject(identity(req, ""), in.Goal, in.Target)
			if err != nil || !ok {
				return nil, okMsg{OK: false, Message: "no such candidate"}, err
			}
			auditKnowledge(opts, req, "reject_control", map[string]any{"goal": in.Goal, "target": in.Target})
			return nil, okMsg{OK: true, Message: in.Goal + "@" + in.Target + " rejected"}, nil
		})
}
