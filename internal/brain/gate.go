// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/pdbethke/corralai/internal/gate"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// certifierAdapter implements gate.Certifier over the Task-3 signing seam
// (certifyBuild) — the SAME in-process function report_build's MCP handler
// calls, so a gate run and a `corral certify` build report produce
// byte-identical signed records for the same inputs.
type certifierAdapter struct{ opts Options }

// Certify signs a gate run's outcome and returns the resulting record's id
// and chain head. actor is fixed to "corral-gate" so a signed record is
// always attributable to the gate, distinct from a human-run
// `corral certify` submission or another principal's report_build call.
func (c certifierAdapter) Certify(ctx context.Context, repoName, commit, command string, exitCode int, outputDigest string) (int64, string, error) {
	out, err := certifyBuild(ctx, c.opts, reportBuildIn{
		Repo:         repoName,
		Commit:       commit,
		Command:      command,
		ExitCode:     exitCode,
		OutputDigest: outputDigest,
	}, "corral-gate")
	if err != nil {
		return 0, "", err
	}
	return out.ID, out.Head, nil
}

// jailAdapter implements gate.Jail over sandbox.Run, using backend — which
// MUST be the brain's real isolation backend (the same one NewSandboxVerify
// runs the independent verify-gate check under; see cmd/corral/main.go).
// Never construct a second Isolator for this adapter.
//
// LOAD-BEARING CONTRACT (relied on by gate.Runner's fail-closed check
// `exit == 0`): Run hands back sandbox.Result.ExitCode UNCHANGED in every
// case, including a nil backend or a timeout — both of those come back
// from sandbox.Run as ExitCode -1, which Run passes through as-is. A
// timeout or a non-empty Result.Err is ALSO surfaced as a non-nil error,
// so the runner takes its "error" exit (never "success") regardless of
// what happens to be sitting in ExitCode. Run never remaps a nonzero,
// negative, or timed-out result to 0. The "failed run must not read as
// success" interpretation itself lives in sandbox.RunGuarded — this
// adapter is just transport glue over it (see also
// internal/adequacy/jail.go's bwrapJail, which delegates to the same
// RunGuarded).
type jailAdapter struct{ backend sandbox.Isolator }

func (j jailAdapter) Run(ctx context.Context, command, workspace string, network bool, timeout time.Duration) (int, string, error) {
	res, err := sandbox.RunGuarded(ctx, command, sandbox.Options{Workspace: workspace, Network: network, Backend: j.backend, Timeout: timeout})
	return res.ExitCode, res.Output, err
}

// StartGate wires and starts the repo merge gate: the gate.Store, the
// Runner (checkout -> jail -> certify -> store -> post status), and the
// Poller that discovers new PR heads and drives it. It returns the opened
// Store so the caller can wire the /api/gate/run read endpoint
// (GateRunHandler) — or (nil, nil) when the feature is off/disabled.
//
// opts.GatePolicies == nil/empty is the feature's OFF switch: StartGate is
// a complete no-op (nil, nil) — zero behavior change for a brain that
// sets no CORRALAI_GATE_POLICY_<NAME>.
//
// opts.GateBackend == nil DISABLES gating even when policies ARE
// configured: this is the fail-closed contract carried up from
// jailAdapter's doc comment — corralai never runs an untrusted PR check
// unsandboxed. StartGate logs loudly and returns (nil, nil) rather than
// starting a poller whose every run could only ever error.
func StartGate(ctx context.Context, opts Options) (*gate.Store, error) {
	if len(opts.GatePolicies) == 0 {
		return nil, nil
	}
	if opts.GateBackend == nil {
		log.Printf("gate: DISABLED — %d gate polic(ies) are configured but no sandbox isolation backend is available; refusing to run PR checks unsandboxed (set CORRALAI_GATE_EXEC_BACKEND)", len(opts.GatePolicies))
		return nil, nil
	}
	if opts.Repo == nil {
		log.Printf("gate: DISABLED — a gate policy is configured but no repo.Engine is configured (Options.Repo is nil)")
		return nil, nil
	}

	dsn := opts.GateDB
	if dsn == "" {
		dsn = "corralai_gate.duckdb"
	}
	store, err := gate.OpenStore(dsn)
	if err != nil {
		return nil, fmt.Errorf("gate: open store: %w", err)
	}

	recordURL := opts.GateRecordURL
	if recordURL == nil {
		recordURL = defaultGateRecordURL
	}

	runner := &gate.Runner{
		Checkout:  opts.Repo,
		Jail:      jailAdapter{backend: opts.GateBackend},
		Certify:   certifierAdapter{opts: opts},
		Status:    opts.Repo,
		Store:     store,
		RecordURL: recordURL,
		Now:       time.Now,
	}

	interval := opts.GatePollInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	poller := &gate.Poller{
		Policies:  opts.GatePolicies,
		List:      opts.Repo,
		Store:     store,
		Run:       runner.Run,
		Redeliver: runner.Redeliver,
		Interval:  interval,
	}

	log.Printf("gate: ENABLED — %d polic(ies) configured, polling every %s", len(opts.GatePolicies), interval)
	go poller.Loop(ctx)
	return store, nil
}

// defaultGateRecordURL builds the /api/gate/run status target_url for a
// (repo, sha), shared by StartGate and StartControlGate so their default
// wiring can never drift.
func defaultGateRecordURL(repoName, sha, statusContext string) string {
	u := "/api/gate/run?repo=" + url.QueryEscape(repoName) + "&sha=" + url.QueryEscape(sha)
	if statusContext != "" {
		u += "&context=" + url.QueryEscape(statusContext)
	}
	return u
}

// gateRunResponse is the JSON shape /api/gate/run returns for a known
// (repo, sha). It deliberately carries no forge token, no command output,
// and no repo/sha echo beyond what the caller already supplied in the
// query — the credential boundary keeps forge credentials brain-side only.
//
// passed is true only when every check that has REPORTED for the head passed,
// and contexts lists each of them with its own result. A check still running,
// or one whose result was never stored, has no row and is not listed: this
// endpoint knows what was recorded, not which policies apply to the head. The
// per-status links name their check (&context=), so the link the forge shows
// beside each status answers for exactly that check. record_id is the record when the
// answer is one check's (a status link names its context, or the head has
// only one check), and is omitted when several checks answer together: there
// is no single record to point at, and 0 is never a real one. (Review
// 8be2189163b0, R7: the endpoint used to report whichever check ran last.)
type gateRunResponse struct {
	Passed   bool                `json:"passed"`
	PR       int                 `json:"pr"`
	RecordID int64               `json:"record_id,omitempty"`
	Contexts []gateRunContextRow `json:"contexts"`
}

// gateRunContextRow is one check's answer. pr is per check because one head
// can sit in two pull requests (the same branch against main and against
// release), each answered by its own policy; record_id is omitted for a check
// that signed nothing (a fail-closed row), since 0 is never a real record.
type gateRunContextRow struct {
	Context  string `json:"context"`
	Passed   bool   `json:"passed"`
	PR       int    `json:"pr"`
	RecordID int64  `json:"record_id,omitempty"`
}

// GateRunHandler serves GET /api/gate/run?repo=&sha=[&context=], reading
// store's dedupe/index rows. With context= it answers for that one check (the
// link on a status names its own); without, for every check that has reported
// for the head. It serves the MERGE gate's store only: a control-gate link
// names a context this store never holds and gets a 404, never a borrowed
// answer. Mount it behind the SAME auth wrapper the brain wraps
// every other /api/* route in (see cmd/corral/main.go) — this handler
// itself performs no authentication or authorization.
func GateRunHandler(store *gate.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repoName := r.URL.Query().Get("repo")
		sha := r.URL.Query().Get("sha")
		if repoName == "" || sha == "" {
			http.Error(w, "repo and sha query params are required", http.StatusBadRequest)
			return
		}
		var runs []gate.Run
		var err error
		if statusCtx := r.URL.Query().Get("context"); statusCtx != "" {
			var run gate.Run
			var ok bool
			run, ok, err = store.GetByHead(repoName, sha, statusCtx)
			if ok {
				runs = []gate.Run{run}
			}
		} else {
			runs, err = store.ListBySHA(repoName, sha)
		}
		if err != nil {
			log.Printf("gate: /api/gate/run: lookup %s@%s: %v", repoName, sha, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(runs) == 0 {
			http.NotFound(w, r)
			return
		}
		resp := gateRunResponse{Passed: true, PR: runs[0].PR}
		for _, run := range runs {
			resp.Passed = resp.Passed && run.Passed
			resp.Contexts = append(resp.Contexts, gateRunContextRow{Context: run.Context, Passed: run.Passed, PR: run.PR, RecordID: run.RecordID})
		}
		if len(runs) == 1 {
			resp.RecordID = runs[0].RecordID
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
