// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pdbethke/corralai/internal/brainclient"
)

// advFinding is the subset of a queue.Finding the verdict render shows. The
// tags match queue.Finding's own (lowercase) wire tags.
type advFinding struct {
	Type          string `json:"type"`
	Severity      string `json:"severity"`
	Target        string `json:"target"`
	Evidence      string `json:"evidence"`
	ReporterModel string `json:"reporter_model"`
}

// advVerdict mirrors advpool.Verdict on the wire. advpool.Verdict has NO json
// tags, so its keys are the Go-default CAPITALIZED field names — matched here
// verbatim. Changing these breaks decoding.
type advVerdict struct {
	Repo            string            `json:"Repo"`
	Commit          string            `json:"Commit"`
	DevKillRate     float64           `json:"DevKillRate"`
	MutantsTotal    int               `json:"MutantsTotal"`
	Survivors       int               `json:"Survivors"`
	ProvenMissed    int               `json:"ProvenMissed"`
	VacuousFindings []advFinding      `json:"VacuousFindings"`
	ModelsByRole    map[string]string `json:"ModelsByRole"`
	Status          string            `json:"Status"`
	RecordID        int64             `json:"RecordID"`
	RecordHead      string            `json:"RecordHead"`
}

// advStatus mirrors brain.AdvPoolStatusOut (get_adversarial_run's output).
type advStatus struct {
	RunID     int64       `json:"run_id"`
	Found     bool        `json:"found"`
	Converged bool        `json:"converged"`
	Verdict   *advVerdict `json:"verdict"`
}

// advStartSpec mirrors brain.AdvPoolRunSpec (start_adversarial_run's input).
type advStartSpec struct {
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Goal        string `json:"goal"`
	CodePath    string `json:"code_path"`
	Code        string `json:"code"`
	DevTestPath string `json:"dev_test_path"`
	DevTestCode string `json:"dev_test_code"`
	TestCmd     string `json:"test_cmd"`
	NMutants    int    `json:"n_mutants,omitempty"`
}

// advPoolClient triggers and polls an adversarial-pool run over the brain's
// MCP tools. Injected so runCertifyAdversarial is testable without a brain.
type advPoolClient interface {
	StartRun(ctx context.Context, brainURL string, spec advStartSpec) (runID int64, err error)
	RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error)
}

// mcpAdvClient is advPoolClient backed by real MCP calls, dialing the brain
// fresh per call with a token from the keystore (mirrors mcpPoster).
type mcpAdvClient struct{}

func (mcpAdvClient) call(ctx context.Context, brainURL, tool string, args map[string]any) (string, error) {
	token, err := brainToken()
	if err != nil {
		return "", fmt.Errorf("resolve brain token: %w", err)
	}
	cl, err := brainclient.Dial(ctx, brainURL, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.CallTool(ctx, tool, args)
	if err != nil {
		return "", err
	}
	text := brainclient.FirstText(res)
	if res.IsError {
		msg := text
		if msg == "" {
			msg = tool + " reported an error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return text, nil
}

func (c mcpAdvClient) StartRun(ctx context.Context, brainURL string, spec advStartSpec) (int64, error) {
	args := map[string]any{
		"repo": spec.Repo, "commit": spec.Commit, "goal": spec.Goal,
		"code_path": spec.CodePath, "code": spec.Code,
		"dev_test_path": spec.DevTestPath, "dev_test_code": spec.DevTestCode,
		"test_cmd": spec.TestCmd,
	}
	if spec.NMutants > 0 {
		args["n_mutants"] = spec.NMutants
	}
	text, err := c.call(ctx, brainURL, "start_adversarial_run", args)
	if err != nil {
		return 0, err
	}
	var out struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return 0, fmt.Errorf("decoding start_adversarial_run response: %w", err)
	}
	return out.RunID, nil
}

func (c mcpAdvClient) RunStatus(ctx context.Context, brainURL string, runID int64) (advStatus, error) {
	text, err := c.call(ctx, brainURL, "get_adversarial_run", map[string]any{"run_id": runID})
	if err != nil {
		return advStatus{}, err
	}
	var st advStatus
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return advStatus{}, fmt.Errorf("decoding get_adversarial_run response: %w", err)
	}
	return st, nil
}
