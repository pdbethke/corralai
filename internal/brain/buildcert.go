// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/certify"
)

// reportBuildIn is the raw build record a CI/build principal reports —
// corral certify's ingest shape.
type reportBuildIn struct {
	Repo         string   `json:"repo" jsonschema:"the repository (e.g. owner/name)"`
	Commit       string   `json:"commit" jsonschema:"the commit sha this build ran against"`
	Branch       string   `json:"branch,omitempty" jsonschema:"the branch this build ran on"`
	Command      string   `json:"command" jsonschema:"the command that was executed"`
	ExitCode     int      `json:"exit_code" jsonschema:"the command's exit code (0 = pass)"`
	DurationS    float64  `json:"duration_s,omitempty" jsonschema:"how long the command ran, in seconds"`
	OutputDigest string   `json:"output_digest,omitempty" jsonschema:"a digest of the command's output"`
	ProducedBy   []string `json:"produced_by,omitempty" jsonschema:"models that produced the change under certification"`
}

// reportBuildOut is the signed, tamper-evident accountability record
// report_build hands back: the ledger head, its Ed25519 signature, the
// stored SLSA/in-toto statement, and the assigned build_records id.
type reportBuildOut struct {
	ID        int64          `json:"id"`
	Head      string         `json:"head"`
	Signature string         `json:"signature"`
	Statement map[string]any `json:"statement"`
}

// registerBuildCert registers the report_build tool — corral certify's ingest
// endpoint. Only registered when opts.BuildStore is set (the brain's build
// store is required to persist the signed record); a brain without one never
// exposes the tool.
func registerBuildCert(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "report_build",
		Description: "Certify a build: turn a raw build record (repo, commit, command, exit code) into a signed, tamper-evident, stored accountability record. This is corral certify's ingest endpoint."},
		func(_ context.Context, req *mcp.CallToolRequest, in reportBuildIn) (*mcp.CallToolResult, reportBuildOut, error) {
			actor := actorOf(req)

			steps := []certify.Step{
				{
					Kind:    "context",
					Actor:   actor,
					Subject: in.Repo + "@" + in.Commit,
					Detail: map[string]any{
						"repo":   in.Repo,
						"commit": in.Commit,
						"branch": in.Branch,
					},
				},
				{
					Kind:    "execution",
					Actor:   actor,
					Subject: in.Command,
					Detail: map[string]any{
						"exit_code":     in.ExitCode,
						"ok":            in.ExitCode == 0,
						"duration_s":    in.DurationS,
						"output_digest": in.OutputDigest,
					},
				},
			}
			_, head := certify.BuildLedger(steps)

			br := certify.BuildRecord{
				Repo:         in.Repo,
				Commit:       in.Commit,
				Branch:       in.Branch,
				Actor:        actor,
				Command:      in.Command,
				ExitCode:     in.ExitCode,
				DurationS:    in.DurationS,
				OutputDigest: in.OutputDigest,
				ProducedBy:   in.ProducedBy,
			}
			stmt := certify.BuildAttestation(br, head)

			sig := certify.Sign(head, opts.CertifyKey)

			stmtJSON, err := json.Marshal(stmt)
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: marshaling statement: %w", err)
			}

			id, err := opts.BuildStore.Save(in.Repo, in.Commit, in.Branch, actor, head, sig, string(stmtJSON))
			if err != nil {
				return nil, reportBuildOut{}, err
			}

			rec(opts.Telemetry, 0, "build_certified", actor, in.Repo+"@"+in.Commit, map[string]any{
				"repo":   in.Repo,
				"commit": in.Commit,
				"head":   head,
			})

			return nil, reportBuildOut{ID: id, Head: head, Signature: sig, Statement: stmt}, nil
		})
}
