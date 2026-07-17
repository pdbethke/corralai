// SPDX-License-Identifier: Elastic-2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/eval"
)

// mcpPoolRunner adapts the real adversarial-pool client to eval.PoolRunner:
// one converged run per target, verdict mapped to eval.RunResult. Provenance
// (corpus version + target digest) rides in the run's Repo/Commit metadata.
type mcpPoolRunner struct {
	client        advPoolClient
	brainURL      string
	corpusVersion string
	poll, timeout time.Duration
}

func (r mcpPoolRunner) RunOne(ctx context.Context, t eval.Target) (eval.RunResult, error) {
	spec := advStartSpec{
		Repo:        "eval:" + r.corpusVersion,
		Commit:      t.ID + "@" + t.Digest(),
		Goal:        t.Goal,
		CodePath:    t.CodePath,
		Code:        t.Code(),
		DevTestPath: t.TestPath,
		DevTestCode: t.TestCode(),
		TestCmd:     t.TestCmd,
		NMutants:    t.NMutants,
	}
	runID, err := r.client.StartRun(ctx, r.brainURL, spec)
	if err != nil {
		return eval.RunResult{}, err
	}
	deadline := time.Now().Add(r.timeout)
	for {
		st, err := r.client.RunStatus(ctx, r.brainURL, runID)
		if err != nil {
			return eval.RunResult{}, err
		}
		if st.Converged && st.Verdict != nil {
			v := st.Verdict
			return eval.RunResult{
				Status: v.Status, DevKillRate: v.DevKillRate, MutantsTotal: v.MutantsTotal,
				Survivors: v.Survivors, ProvenMissed: v.ProvenMissed, RecordID: v.RecordID,
			}, nil
		}
		if time.Now().After(deadline) {
			return eval.RunResult{}, fmt.Errorf("run %d did not converge within %s", runID, r.timeout)
		}
		time.Sleep(r.poll)
	}
}

func runEval(args []string, newRunner func(brainURL, corpusVersion string) eval.PoolRunner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	corpus := fs.String("corpus", "eval/corpus/manifest.json", "corpus manifest path")
	iterations := fs.Int("iterations", 1, "iterations per target")
	only := fs.String("only", "", "comma-separated target ids (default: all)")
	brainURL := fs.String("brain", os.Getenv("CORRAL_BRAIN"), "brain endpoint (or $CORRAL_BRAIN)")
	progress := fs.String("progress", "eval/.eval-progress.json", "resumable progress file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	m, err := eval.Load(*corpus)
	if err != nil {
		fmt.Fprintf(stderr, "corral eval: %v\n", err)
		return 2
	}
	var onlyIDs []string
	if strings.TrimSpace(*only) != "" {
		onlyIDs = strings.Split(*only, ",")
	}
	runner := newRunner(*brainURL, m.CorpusVersion)
	results, err := eval.Run(context.Background(), m, eval.Config{
		Iterations: *iterations, Only: onlyIDs, ProgressPath: *progress,
	}, runner, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "corral eval: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout)
	eval.WriteReport(stdout, eval.Report(m, results))
	return 0
}
