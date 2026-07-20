// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pdbethke/corralai/internal/brainclient"
	"github.com/pdbethke/corralai/internal/matrixstore"
)

// matrixReader is the read surface `corral matrix list` needs — narrowed to
// a single method so tests can inject a fake without a running brain or a
// DuckDB-backed store. Returns BOTH the full row set and the pre-split
// delete-candidate subset, matching internal/ui's /api/matrix response
// shape (see matrixResponse there).
type matrixReader interface {
	Matrix(ctx context.Context) (rows, deleteCandidates []matrixstore.Row, err error)
}

// httpMatrixReader reads the tests×mutants matrix over the wire from a
// running brain's GET /api/matrix (see internal/ui.Server.matrix), instead
// of opening the matrixstore DuckDB file directly.
//
// This exists for the same reason httpScorecardReader/httpCriticScoreLister
// do (scorecard.go/criticscore.go): DuckDB is single-process, the brain
// (corral.service) already holds the matrix store file read-write, and a
// second process cannot open it concurrently — `corral matrix list` must go
// over HTTP.
type httpMatrixReader struct {
	brainURL string
	client   *http.Client
}

func newHTTPMatrixReader(brainURL, token string) *httpMatrixReader {
	hc := brainclient.AuthedHTTPClient(token)
	hc.Timeout = 15 * time.Second
	return &httpMatrixReader{brainURL: brainURL, client: hc}
}

func (r *httpMatrixReader) Matrix(ctx context.Context) ([]matrixstore.Row, []matrixstore.Row, error) {
	url := strings.TrimRight(r.brainURL, "/") + "/api/matrix"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("matrix list: build request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("matrix list: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("matrix list: GET %s: unexpected status %s", url, resp.Status)
	}
	var body struct {
		Rows             []matrixstore.Row `json:"rows"`
		DeleteCandidates []matrixstore.Row `json:"delete_candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil, fmt.Errorf("matrix list: decode response: %w", err)
	}
	return body.Rows, body.DeleteCandidates, nil
}

// runMatrix implements `corral matrix list [--json]` — the read-only surface
// over the tests×mutants matrix store (internal/matrixstore, swarm slice 5):
// per-test execution-proven adequacy against a run's own mutant set, plus the
// delete-candidate list (tests that caught none of them). There is no write
// subcommand — the matrix is pure telemetry from --matrix/matrix-opted-in
// runs, never a gate a human adjudicates the way criticscore's findings are.
func runMatrix(args []string, reader matrixReader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: corral matrix list [--json]")
		return 2
	}
	fs := flag.NewFlagSet("matrix list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit the raw rows as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	rows, candidates, err := reader.Matrix(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "corral matrix list:", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no matrix data yet — run `corral certify --local --matrix ...` (or the brain's matrix-opted-in adversarial run) at least once")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tCOMMIT\tTEST\tKILLS\tMUTANTS\tDELETE-CANDIDATE\t")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%v\t\n",
			row.Repo, shortCommit(row.Commit), row.TestSelector, row.Kills, row.MutantsTotal, row.DeleteCandidate)
	}
	tw.Flush()

	if len(candidates) > 0 {
		fmt.Fprintf(stdout, "\nsafe-to-delete candidates (%d):\n", len(candidates))
		for _, row := range candidates {
			fmt.Fprintf(stdout, "  • %s (%s) — caught 0 of %d planted mutants — review for deletion. %s\n",
				row.TestSelector, row.TestFile, row.MutantsTotal, matrixDeleteCandidateCaveat)
		}
	}
	return 0
}

// shortCommit renders a commit sha to its short (7-char) form for table
// display, matching renderAdvVerdict's own truncation (certify_adversarial.go).
func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
