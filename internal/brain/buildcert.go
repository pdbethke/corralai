// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
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
// report_build hands back: the ledger head, the Ed25519 signature over the
// FULL canonical statement (binding the predicate, not just the head), the
// stored SLSA/in-toto statement, the assigned build_records id, the
// certify public key a third party needs to verify Signature independently,
// and Steps (certify.MarshalSteps output, decoded to a generic slice so the
// MCP tool's auto-derived JSON schema sees an array of objects rather than
// treating a json.RawMessage's underlying []byte as an array of integers) —
// carrying the full ledger in the response means a caller (corral certify's
// --out) can build a completely self-verifying record with no further
// round trip to the brain.
type reportBuildOut struct {
	ID        int64            `json:"id"`
	Head      string           `json:"head"`
	Signature string           `json:"signature"`
	Statement map[string]any   `json:"statement"`
	PublicKey string           `json:"public_key"`
	Steps     []map[string]any `json:"steps"`
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
			built, head := certify.BuildLedger(steps)

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

			// Sign the FULL canonical statement (not just the head): a
			// head-only signature leaves the predicate (repo/commit/command/
			// exit code) freely editable in storage without invalidating the
			// signature. SignStatement returns the exact canonical bytes
			// that were signed; those bytes — not a re-marshal — are what
			// gets persisted, so a later VerifyStatement call checks the
			// identical bytes the signature covers.
			sigHex, canonical, err := certify.SignStatement(stmt, opts.CertifyKey)
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: signing statement: %w", err)
			}

			stepsJSON, err := certify.MarshalSteps(built)
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: marshaling steps: %w", err)
			}
			var stepsOut []map[string]any
			if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: decoding steps for response: %w", err)
			}

			id, err := opts.BuildStore.Save(in.Repo, in.Commit, in.Branch, actor, head, sigHex, string(canonical), string(stepsJSON))
			if err != nil {
				return nil, reportBuildOut{}, err
			}

			rec(opts.Telemetry, 0, "build_certified", actor, in.Repo+"@"+in.Commit, map[string]any{
				"repo":   in.Repo,
				"commit": in.Commit,
				"head":   head,
			})

			pub, _ := opts.CertifyKey.Public().(ed25519.PublicKey)
			return nil, reportBuildOut{
				ID:        id,
				Head:      head,
				Signature: sigHex,
				Statement: stmt,
				PublicKey: hex.EncodeToString(pub),
				Steps:     stepsOut,
			}, nil
		})
}
