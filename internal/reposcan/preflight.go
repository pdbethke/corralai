// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// commandRunner runs one command in the scan's substrate and returns its
// stdout. adequacy.Enumerator satisfies this structurally; declaring it
// locally keeps reposcan free of an import edge on adequacy.
type commandRunner interface {
	Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error)
}

// CoverageMap is what one instrumented suite run learned. Ran distinguishes
// "the suite executed none of these files" from "we could not find out" —
// they are different claims and only one is a finding. Executed is nil
// unless Ran is true: a caller that forgets to check Ran gets an empty
// iteration, never a repo-wide accusation that no file is covered.
//
// Executed is itself TRI-STATE once Ran is true (see each
// lang.CoverageReporter.ParseCoverage implementation): present-true is
// executed, present-false is measured-and-never-executed (the real
// finding), and a path ABSENT from the map was never measured by this run
// at all (e.g. outside coverage.py's own [tool.coverage.run] source scope)
// — a fact a caller must report as a count, never as a per-file accusation.
// The number of files this run actually EXECUTED is therefore the count of
// true VALUES in Executed, never len(Executed) — a caller that conflates
// the two overcounts by exactly the size of the measured-but-unexecuted
// set.
type CoverageMap struct {
	Executed map[string]bool // nil unless Ran
	Ran      bool
	Note     string // why it did not run; printed verbatim
}

// Preflight runs ONE instrumented suite invocation for p's language (if it
// supports coverage instrumentation for testCmd) via runner, and reports
// which of the repo's files the suite actually executed.
//
// It never fabricates a result. Every way the run can fail to tell us
// anything — the language has no CoverageReporter, the reporter declines
// this particular testCmd, the run itself errors, or its report doesn't
// parse — is reported as Ran=false with Executed left nil and Note carrying
// an operator-readable reason naming the language (and, where relevant, the
// underlying error). Only a well-formed, successfully-parsed report sets
// Ran=true; Executed may then legitimately be empty (a real "nothing ran"
// verdict), which is exactly why Ran exists as a separate signal.
//
// repoRoot is passed through to ParseCoverage as modulePath: the repo root
// for Python (to relativize absolute paths), the module import prefix for
// Go.
func Preflight(ctx context.Context, runner commandRunner, files map[string]string, p lang.Plugin, testCmd []string, repoRoot string) CoverageMap {
	reporter, ok := p.(lang.CoverageReporter)
	if !ok {
		return CoverageMap{
			Note: fmt.Sprintf("%s: no coverage instrumentation available for this language", p.Name()),
		}
	}

	cmd, ok := reporter.CoverageCmd(testCmd)
	if !ok {
		return CoverageMap{
			Note: fmt.Sprintf("%s: no coverage instrumentation for test command %v", p.Name(), testCmd),
		}
	}

	stdout, err := runner.Enumerate(ctx, files, cmd)
	if err != nil {
		return CoverageMap{
			Note: fmt.Sprintf("%s: coverage pre-flight run failed: %v", p.Name(), err),
		}
	}

	// A head-truncated report usually fails ParseCoverage outright (broken
	// JSON, a missing "mode:" header) — but "usually" is not a guarantee: a
	// truncation that happens to land on a clean boundary yields a
	// partially-parsed, VALID-looking report, which is a partial executed
	// set — the same false-accusation shape this whole feature exists to
	// avoid ("your suite never touches this", about files simply never
	// reached before the cut). Detected structurally (the runner's own
	// truncation marker), before ParseCoverage ever sees the bytes, so
	// truncation is reported as its own distinct failure rather than
	// masquerading as either a clean parse or a generic "unparseable" one.
	if strings.Contains(stdout, sandbox.TruncationMarker) {
		return CoverageMap{
			Note: fmt.Sprintf("%s: coverage pre-flight output was truncated — the report is incomplete and cannot be trusted", p.Name()),
		}
	}

	executed, err := reporter.ParseCoverage(stdout, repoRoot)
	if err != nil {
		return CoverageMap{
			Note: fmt.Sprintf("%s: coverage report unparseable: %v", p.Name(), err),
		}
	}

	return CoverageMap{Executed: executed, Ran: true}
}
