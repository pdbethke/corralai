// SPDX-License-Identifier: Elastic-2.0

// Command campaign drives corral's proven-contributions campaign: it audits a
// named list of upstream repositories with the real `corral certify --repo`.
//
// It lives in scripts/ and not cmd/ ON PURPOSE, for the reason ledgersite
// does: scripts/gen-cli-docs.sh publishes a CLI reference page for every
// cmd/*/ binary, and this is internal tooling no user runs.
//
// The targets file (targets.json) names every value of every audit and
// defaults none; see Target.
//
//	campaign -targets targets.json list
//	campaign -targets targets.json run <name>
//
// `run` needs CORRAL_CAMPAIGN_DIR, the directory holding one folder per target
// plus _corral-ledger/. Exit 2 means the invocation or the targets file is
// wrong; exit 1 means a run failed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli(ctx, os.Args[1:], os.Getenv, execRunner{echo: os.Stderr}, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// cli is main with its inputs passed in, so the exit-code rules are tested.
func cli(ctx context.Context, args []string, getenv func(string) string, r Runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("campaign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetsPath := fs.String("targets", "", "the targets file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintln(stderr, "campaign:", msg)
		return 2
	}
	rest := fs.Args()
	if *targetsPath == "" {
		return usage("-targets is required")
	}
	if len(rest) == 0 {
		return usage("a command is required: list | run <name>")
	}
	var campaignDir string
	switch rest[0] {
	case "list":
	case "run":
		if len(rest) != 2 {
			return usage("usage: run <name>")
		}
		campaignDir = getenv("CORRAL_CAMPAIGN_DIR")
		if campaignDir == "" {
			return usage("CORRAL_CAMPAIGN_DIR is required: the directory holding one folder per target")
		}
	default:
		return usage(fmt.Sprintf("unknown command %q: list | run <name>", rest[0]))
	}

	ts, err := LoadTargets(*targetsPath)
	if err != nil {
		fmt.Fprintln(stderr, "campaign:", err)
		if errors.Is(err, ErrInvalid) {
			return 2 // the file is wrong: name what to fix, as for a missing flag
		}
		return 1 // could not read it at all
	}
	if rest[0] == "list" {
		for _, t := range ts {
			fmt.Fprintln(stdout, t.Name)
		}
		return 0
	}
	for _, t := range ts {
		if t.Name != rest[1] {
			continue
		}
		// goals are named relative to the targets file; the audit runs
		// elsewhere, so hand it an absolute path.
		if t.Goals != "" && !filepath.IsAbs(t.Goals) {
			t.Goals = filepath.Join(filepath.Dir(*targetsPath), t.Goals)
		}
		if err := RunTarget(ctx, r, t, campaignDir, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "campaign:", err)
			return 1
		}
		return 0
	}
	return usage(fmt.Sprintf("no target named %q (see: list)", rest[1]))
}
