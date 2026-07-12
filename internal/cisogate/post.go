// SPDX-License-Identifier: Elastic-2.0

package cisogate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Certifier signs a gate run's outcome, returning the signed record's id and
// the resulting chain head. In production the brain's certify adapter
// satisfies this; here it's a consumer-side interface so cisogate never
// imports the gate package.
type Certifier interface {
	Certify(ctx context.Context, repo, commit, command string, exitCode int, outputDigest string) (recordID int64, head string, err error)
}

// StatusPoster posts a commit status (pending/success/failure/error) back to
// the forge hosting repoURL. In production repo.Engine satisfies this.
type StatusPoster interface {
	SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error
}

// PostRequest carries the addressing info PostCisoGate needs to sign and
// post a CISO-gate verdict for one PR head commit.
type PostRequest struct {
	RepoURL   string
	HeadSHA   string
	Context   string
	RecordURL func(sha string) string
}

// PostCisoGate signs the CISO verdict FIRST, then posts the corral/ciso-gate
// status. THE LOAD-BEARING INVARIANT: no unsigned green — if Certify fails,
// PostCisoGate returns the error and never calls SetCommitStatus.
func PostCisoGate(ctx context.Context, cert Certifier, poster StatusPoster, req PostRequest, res CisoResult) error {
	state, exit := "success", 0
	if !res.Pass {
		state, exit = "failure", 1
	}
	b, _ := json.Marshal(res) // stable: struct + ordered slice
	sum := sha256.Sum256(b)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	if _, _, err := cert.Certify(ctx, req.RepoURL, req.HeadSHA, "corral/ciso-gate", exit, digest); err != nil {
		// Never post a status without a signed record behind it — return and let the poller retry.
		return fmt.Errorf("cisogate: certify verdict (not posting unsigned): %w", err)
	}
	return poster.SetCommitStatus(ctx, req.RepoURL, req.HeadSHA, req.Context, state, req.RecordURL(req.HeadSHA), describeResult(res))
}

// describeResult renders a CisoResult as a human-readable status
// description for the CISO-gate check posted on the PR.
func describeResult(res CisoResult) string {
	if len(res.Results) == 0 {
		return "no CISO controls apply"
	}
	var failed []string
	for _, r := range res.Results {
		if !r.Passed {
			failed = append(failed, r.Goal+"@"+r.Target)
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("all %d CISO controls passed", len(res.Results))
	}
	return fmt.Sprintf("%d/%d CISO controls FAILED: %s", len(failed), len(res.Results), strings.Join(failed, ", "))
}
