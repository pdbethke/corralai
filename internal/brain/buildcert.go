// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"

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

	// The git-link fields (Task 2): pulled from `git show`/`git verify-commit`
	// by corral certify at capture time and posted verbatim — the brain never
	// re-derives or re-verifies the signature here (that's display-time
	// re-verification, out of scope for this handler; YAGNI for now).
	CommitMessage   string         `json:"commit_message,omitempty" jsonschema:"the commit's subject line"`
	CommitAuthor    string         `json:"commit_author,omitempty" jsonschema:"the commit author, \"name <email>\""`
	CommitDate      string         `json:"commit_date,omitempty" jsonschema:"the commit's authored/committed date, ISO-8601"`
	CommitSignature map[string]any `json:"commit_signature,omitempty" jsonschema:"the parsed git verify-commit outcome: {signed, signer, mechanism, verified}"`
}

// reportBuildOut is the signed, tamper-evident accountability record
// report_build hands back: the ledger head, a DSSE envelope (JSON, as text)
// carrying the Ed25519 signature over the FULL canonical statement (binding
// the predicate, not just the head) and its own embedded copy of the
// statement, the stored SLSA/in-toto statement (kept for human readability;
// the envelope is the source of truth for verification), the assigned
// build_records id, the certify public key a third party needs to verify
// Signature independently, and Steps (certify.MarshalSteps output, decoded
// to a generic slice so the MCP tool's auto-derived JSON schema sees an
// array of objects rather than treating a json.RawMessage's underlying
// []byte as an array of integers) — carrying the full ledger in the
// response means a caller (corral certify's --out) can build a completely
// self-verifying record with no further round trip to the brain.
type reportBuildOut struct {
	ID        int64            `json:"id"`
	Head      string           `json:"head"`
	Signature string           `json:"signature"`
	Statement map[string]any   `json:"statement"`
	PublicKey string           `json:"public_key"`
	Steps     []map[string]any `json:"steps"`

	// Anchored reports whether the DSSE envelope was successfully anchored to
	// the transparency witness (opts.Witness). false covers both "no witness
	// configured" and "the witness was configured but unreachable" — the
	// build is still certified (signed + stored) either way; anchoring is an
	// additional trustless guarantee, never a build-blocking gate.
	Anchored bool `json:"anchored"`
	// LogIndex is the anchored entry's position in the transparency log.
	// Only meaningful when Anchored is true; zero otherwise.
	LogIndex int64 `json:"log_index,omitempty"`
	// Rekor is the marshaled transparency.Entry (JSON) — the inclusion-proof
	// evidence a verifier needs to confirm anchoring OFFLINE, with no round
	// trip back to this brain or to Rekor itself. Only set when Anchored is
	// true; empty otherwise. corral certify's --out embeds this verbatim so
	// an exported record is completely self-verifying.
	Rekor string `json:"rekor,omitempty"`
}

// registerBuildCert registers the report_build tool — corral certify's ingest
// endpoint. Only registered when opts.BuildStore is set (the brain's build
// store is required to persist the signed record); a brain without one never
// exposes the tool.
func registerBuildCert(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "report_build",
		Description: "Certify a build: turn a raw build record (repo, commit, command, exit code) into a signed, tamper-evident, stored accountability record. This is corral certify's ingest endpoint."},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportBuildIn) (*mcp.CallToolResult, reportBuildOut, error) {
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

			// Sign the FULL canonical statement (not just the head) as a DSSE
			// envelope: a head-only signature leaves the predicate
			// (repo/commit/command/exit code) freely editable in storage
			// without invalidating the signature. The envelope embeds its
			// own copy of the canonical statement bytes it signed, so a
			// later VerifyDSSE call checks the identical bytes the
			// signature covers with no separate canonical-bytes column to
			// keep in sync.
			envelope, err := certify.SignDSSE(stmt, opts.CertifyKey, "brain")
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: signing statement: %w", err)
			}
			canonical, err := certify.CanonicalStatement(stmt)
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: canonicalizing statement: %w", err)
			}

			stepsJSON, err := certify.MarshalSteps(built)
			if err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: marshaling steps: %w", err)
			}
			var stepsOut []map[string]any
			if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
				return nil, reportBuildOut{}, fmt.Errorf("report_build: decoding steps for response: %w", err)
			}

			// Anchor the signed envelope to the transparency witness — an
			// ADDITIONAL trustless guarantee (a public, third-party-checkable
			// record that this attestation existed at this time), never a
			// build-blocking gate: an unreachable log must degrade the
			// record to anchored=false, not fail the build. opts.Witness ==
			// nil means anchoring is disabled entirely (same outcome).
			var rekorJSON string
			var anchored bool
			var logIndex int64
			if opts.Witness != nil {
				entry, anchorErr := opts.Witness.Anchor(ctx, envelope)
				if anchorErr != nil {
					// Loud but keyless: never log the envelope's signing
					// key material (there is none here — envelope/entry are
					// both public artifacts — but keep the message scoped
					// to repo/commit for consistency with the rest of the
					// brain's warning style).
					log.Printf("report_build: transparency witness unreachable for %s@%s, degrading to anchored=false: %v", in.Repo, in.Commit, anchorErr)
				} else {
					entryJSON, marshalErr := json.Marshal(entry)
					if marshalErr != nil {
						log.Printf("report_build: encoding transparency entry for %s@%s, degrading to anchored=false: %v", in.Repo, in.Commit, marshalErr)
					} else {
						rekorJSON = string(entryJSON)
						anchored = true
						logIndex = entry.LogIndex
					}
				}
			}

			// pass mirrors the "execution" step's own ok field above — it's
			// the same exit_code == 0 check, denormalized to a queryable
			// column so the dashboard doesn't have to unpack steps/statement
			// JSON per row for a cheap status filter.
			pass := in.ExitCode == 0

			var commitSignatureJSON string
			if len(in.CommitSignature) > 0 {
				b, err := json.Marshal(in.CommitSignature)
				if err != nil {
					return nil, reportBuildOut{}, fmt.Errorf("report_build: marshaling commit_signature: %w", err)
				}
				commitSignatureJSON = string(b)
			}

			id, err := opts.BuildStore.Save(in.Repo, in.Commit, in.Branch, actor, head, string(envelope), string(canonical), string(stepsJSON), rekorJSON, anchored,
				in.CommitMessage, in.CommitAuthor, in.CommitDate, commitSignatureJSON, pass)
			if err != nil {
				return nil, reportBuildOut{}, err
			}

			rec(opts.Telemetry, 0, "build_certified", actor, in.Repo+"@"+in.Commit, map[string]any{
				"repo":     in.Repo,
				"commit":   in.Commit,
				"head":     head,
				"anchored": anchored,
			})

			pub, _ := opts.CertifyKey.Public().(ed25519.PublicKey)
			out := reportBuildOut{
				ID:        id,
				Head:      head,
				Signature: string(envelope),
				Statement: stmt,
				PublicKey: hex.EncodeToString(pub),
				Steps:     stepsOut,
				Anchored:  anchored,
			}
			if anchored {
				out.LogIndex = logIndex
				out.Rekor = rekorJSON
			}
			return nil, out, nil
		})
}
