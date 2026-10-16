// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

// A capability a language has only through a runner other than its default
// names that runner: Ruby's stock command is shell-wrapped and never gets
// --fail-fast, so a bare ✓ would tell a Ruby user they get one.
func TestPageNamesTheRunnerBehindANonDefaultCapability(t *testing.T) {
	page := render(false)
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "| Stop at the first failure |") {
			if !strings.Contains(line, "✓ (rspec)") || !strings.Contains(line, "✓ (jest, mocha)") {
				t.Errorf("fail-fast row does not name the runners: %s", line)
			}
		}
	}
	// An unrecognised command still gets salvage's --deselect and Rust's
	// list/single arguments from its language's default row; only fail-fast
	// is withheld.
	if strings.Contains(page, "never an added flag") {
		t.Error("the page claims an unrecognised command never gets a flag")
	}
}
