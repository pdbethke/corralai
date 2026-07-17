// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/bugcatch"
)

// scorecardReader is the read surface runScorecard needs from a bugcatch
// store — narrowed to a single method so tests can inject a fake without
// standing up a real DuckDB-backed *bugcatch.Store.
type scorecardReader interface {
	Scorecard(context.Context) ([]bugcatch.Cell, error)
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
	fmt.Fprintln(tw, "MODEL\tROLE\tCATCHES\tRECALL\tPRECISION\tRUNS\t")
	for _, c := range cells {
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\t%d%s\t\n",
			c.Model, c.Role, c.Catches, c.Opportunities,
			pctOrDash(c.Recall), pctOrDash(c.Precision), c.Runs, provisionalTag(c.Runs))
	}
	tw.Flush()
	return 0
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
