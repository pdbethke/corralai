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

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/brainclient"
	"github.com/pdbethke/corralai/internal/bugcatch"
)

// scorecardReader is the read surface runScorecard needs — narrowed to a
// single method so tests can inject a fake without standing up a real
// DuckDB-backed store. Returns brain.ScorecardCell (not the raw
// bugcatch.Cell) because the critic-precision join (internal/criticscore,
// Task 7) only exists at that layer.
type scorecardReader interface {
	Scorecard(context.Context) ([]brain.ScorecardCell, error)
}

// localScorecardReader adapts a directly-opened *bugcatch.Store (the
// offline/no-brain-running fallback in main.go's `scorecard` case) to the
// scorecardReader interface by running it through
// brain.BuildBugCatchScorecard with a nil criticStore — the local file path
// never has the criticscore store open at the same time (CORRALAI_CRITICSCORE_DB
// is a separate single-process DuckDB file the running brain holds too), so
// the C-PREC column is legitimately empty for this path; use `corral
// criticscore` or CORRAL_BRAIN for that data.
type localScorecardReader struct{ store *bugcatch.Store }

func (r localScorecardReader) Scorecard(ctx context.Context) ([]brain.ScorecardCell, error) {
	sc, err := brain.BuildBugCatchScorecard(r.store, nil)
	if err != nil {
		return nil, err
	}
	return sc.Cells, nil
}

// httpScorecardReader reads the scorecard over the wire from a running
// brain's GET /api/bugcatch (see internal/ui.Server.bugcatch), instead of
// opening the bugcatch DuckDB file directly.
//
// This exists because DuckDB is single-process: the brain (corral.service)
// already holds the bugcatch store file read-write, and a second process —
// even a read-only open (verified: dsn "?access_mode=read_only" still fails
// with "Conflicting lock is held") — cannot open it concurrently. `corral
// scorecard` must go over HTTP whenever a brain is reachable; opening the
// file directly is only safe for the offline/no-brain-running case.
type httpScorecardReader struct {
	brainURL string
	client   *http.Client
}

func newHTTPScorecardReader(brainURL, token string) *httpScorecardReader {
	hc := brainclient.AuthedHTTPClient(token)
	hc.Timeout = 15 * time.Second
	return &httpScorecardReader{brainURL: brainURL, client: hc}
}

func (r *httpScorecardReader) Scorecard(ctx context.Context) ([]brain.ScorecardCell, error) {
	url := strings.TrimRight(r.brainURL, "/") + "/api/bugcatch"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("scorecard: build request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scorecard: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scorecard: GET %s: unexpected status %s", url, resp.Status)
	}
	var body brain.Scorecard
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("scorecard: decode response: %w", err)
	}
	return body.Cells, nil
}

// runScorecard renders the bug-catching scorecard: a table by default (MODEL,
// ROLE, CATCHES as catches/opportunities, RECALL, PRECISION, RUNS with a
// "(provisional)" tag when a cell hasn't accumulated enough runs to trust
// yet), or the raw cells as indented JSON with --json.
func runScorecard(args []string, store scorecardReader, stdout io.Writer) int {
	fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the raw cells as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cells, err := store.Scorecard(context.Background())
	if err != nil {
		fmt.Fprintf(stdout, "corral scorecard: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cells)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tROLE\tCATCHES\tRECALL\tPRECISION\tC-PREC\tRUNS\t")
	for _, c := range cells {
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\t%s\t%d%s\t\n",
			c.Model, c.Role, c.Catches, c.Opportunities,
			pctOrDash(c.Recall), pctOrDash(c.Precision), criticPrecisionCell(c), c.Runs, provisionalTag(c.Runs))
	}
	tw.Flush()
	return 0
}

// criticPrecisionCell renders the C-PREC column: the test-critic role's
// execution-checked precision (confirmed/(confirmed+refuted), from
// internal/criticscore via brain.BuildBugCatchScorecard's join) with a
// provisionalTag when the adjudicated sample is thin, or "—" for every
// other role / a critic cell with no adjudicated evidence yet.
func criticPrecisionCell(c brain.ScorecardCell) string {
	if c.Role != advpool.RoleTestCritic || c.CriticPrecision == nil {
		return "—"
	}
	return pctOrDash(c.CriticPrecision) + provisionalTag(c.CriticConfirmed+c.CriticRefuted)
}

func pctOrDash(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *p*100)
}

func provisionalTag(runs int) string {
	if runs < 3 {
		return " (provisional)"
	}
	return ""
}
