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
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	targets := flag.String("targets", "", "the targets file to validate")
	flag.Parse()
	if *targets == "" {
		fmt.Fprintln(os.Stderr, "campaign: -targets is required")
		os.Exit(2)
	}
	ts, err := LoadTargets(*targets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "campaign:", err)
		if errors.Is(err, ErrInvalid) {
			os.Exit(2) // the file is wrong: name what to fix, as for a missing flag
		}
		os.Exit(1) // could not read it at all
	}
	fmt.Printf("%d targets, all valid\n", len(ts))
}
