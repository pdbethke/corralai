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
// negative, or timed-out result to 0.
type jailAdapter struct{ backend sandbox.Isolator }

func (j jailAdapter) Run(ctx context.Context, command, workspace string, network bool) (int, string, error) {
	res := sandbox.Run(ctx, command, sandbox.Options{Workspace: workspace, Network: network, Backend: j.backend})
	if res.TimedOut {
		return res.ExitCode, res.Output, fmt.Errorf("gate: check timed out")
	}
	if res.Err != "" {
		return res.ExitCode, res.Output, fmt.Errorf("gate: %s", res.Err)
	}
	return res.ExitCode, res.Output, nil
}

// StartGate wires and starts the repo merge gate: the gate.Store, the
// Runner (checkout -> jail -> certify -> store -> post status), and the
// Poller that discovers new PR heads and drives it. It returns the opened
// Store so the caller can wire the /api/gate/run read endpoint
// (GateRunHandler) — or (nil, nil) when the feature is off/disabled.
//
// opts.GatePolicies == nil/empty is the feature's OFF switch: StartGate is
// a complete no-op (nil, nil) — zero behavior change for a brain that
// doesn't set CORRALAI_GATE_POLICIES.
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
		log.Printf("gate: DISABLED — CORRALAI_GATE_POLICIES is set (%d polic(ies)) but no sandbox isolation backend is available; refusing to run PR checks unsandboxed (set CORRALAI_GATE_EXEC_BACKEND)", len(opts.GatePolicies))
		return nil, nil
	}
	if opts.Repo == nil {
		log.Printf("gate: DISABLED — CORRALAI_GATE_POLICIES is set but no repo.Engine is configured (Options.Repo is nil)")
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
		recordURL = func(repoName, sha string) string {
			return "/api/gate/run?repo=" + url.QueryEscape(repoName) + "&sha=" + url.QueryEscape(sha)
		}
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
		Policies: opts.GatePolicies,
		List:     opts.Repo,
		Store:    store,
		Run:      runner.Run,
		Interval: interval,
	}

	log.Printf("gate: ENABLED — %d polic(ies) configured, polling every %s", len(opts.GatePolicies), interval)
	go poller.Loop(ctx)
	return store, nil
}

// gateRunResponse is the JSON shape /api/gate/run returns for a known
// (repo, sha). It deliberately carries no forge token, no command output,
// and no repo/sha echo beyond what the caller already supplied in the
// query — the credential boundary keeps forge credentials brain-side only.
type gateRunResponse struct {
	Passed   bool  `json:"passed"`
	PR       int   `json:"pr"`
	RecordID int64 `json:"record_id"`
}

// GateRunHandler serves GET /api/gate/run?repo=&sha=, reading store's
// dedupe/index row. Mount it behind the SAME auth wrapper the brain wraps
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
		run, ok, err := store.GetBySHA(repoName, sha)
		if err != nil {
			log.Printf("gate: /api/gate/run: lookup %s@%s: %v", repoName, sha, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gateRunResponse{Passed: run.Passed, PR: run.PR, RecordID: run.RecordID})
	}
}
