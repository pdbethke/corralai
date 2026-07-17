// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"context"
	"fmt"
	"io"
)

type RunResult struct {
	TargetID     string
	Iteration    int
	Status       string
	DevKillRate  float64
	MutantsTotal int
	Survivors    int
	ProvenMissed int
	RecordID     int64
}

// PoolRunner triggers ONE adversarial-pool run for a target and returns its
// verdict. The CLI implements this over the real brain client; tests fake it.
type PoolRunner interface {
	RunOne(ctx context.Context, t Target) (RunResult, error)
}

type Config struct {
	Iterations   int
	Only         []string // target ids; empty = all
	ProgressPath string
}

func selected(m Manifest, only []string) []Target {
	if len(only) == 0 {
		return m.Targets
	}
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	var out []Target
	for _, t := range m.Targets {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func Run(ctx context.Context, m Manifest, cfg Config, runner PoolRunner, out io.Writer) ([]RunResult, error) {
	if cfg.Iterations < 1 {
		cfg.Iterations = 1
	}
	prog, err := loadProgress(cfg.ProgressPath)
	if err != nil {
		return nil, err
	}
	targets := selected(m, cfg.Only)
	// Count the actual remaining work for the cost plan.
	remaining := 0
	for _, t := range targets {
		for i := 1; i <= cfg.Iterations; i++ {
			if !prog.done(m.CorpusVersion, t.ID, i) {
				remaining++
			}
		}
	}
	fmt.Fprintf(out, "eval: %d target(s) × %d iteration(s), %d run(s) to trigger (corpus %s)\n",
		len(targets), cfg.Iterations, remaining, m.CorpusVersion)

	var results []RunResult
	n := 0
	for _, t := range targets {
		for i := 1; i <= cfg.Iterations; i++ {
			if prog.done(m.CorpusVersion, t.ID, i) {
				continue
			}
			n++
			fmt.Fprintf(out, "eval: [%d/%d] %s iter %d…\n", n, remaining, t.ID, i)
			r, err := runner.RunOne(ctx, t)
			if err != nil {
				return results, fmt.Errorf("eval: run %s iter %d: %w", t.ID, i, err)
			}
			r.TargetID, r.Iteration = t.ID, i
			results = append(results, r)
			if err := prog.mark(m.CorpusVersion, t.ID, i); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}
