// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestGoParseCoverageExecutedFiles(t *testing.T) {
	// go test -coverprofile emits "mode:" then one line per block:
	//   <import-path>/<file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
	const out = `mode: set
github.com/x/proj/pkg/a.go:3.10,5.2 1 1
github.com/x/proj/pkg/b.go:7.1,9.4 2 0
github.com/x/proj/c.go:1.1,2.2 1 3
`
	p := goPlugin{}
	got, err := p.ParseCoverage(out, "github.com/x/proj")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	// count>0 means executed (true); b.go has count 0 so it is MEASURED but
	// not executed (false) — present in the map either way, per the
	// tri-state contract (present-true / present-false / absent).
	want := map[string]bool{"pkg/a.go": true, "pkg/b.go": false, "c.go": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestGoParseCoverageMeasuredNeverExecutedStaysFalseAcrossBlocks pins the
// tri-state merge rule for a file with MULTIPLE blocks: once any block
// reports count > 0, a later count-0 block for the same file must not
// overwrite it back to false, and a file whose every block is count == 0
// stays false rather than being dropped from the map.
func TestGoParseCoverageMeasuredNeverExecutedStaysFalseAcrossBlocks(t *testing.T) {
	const out = `mode: set
github.com/x/proj/a.go:1.1,2.2 1 1
github.com/x/proj/a.go:3.1,4.2 1 0
github.com/x/proj/b.go:1.1,2.2 1 0
github.com/x/proj/b.go:3.1,4.2 1 0
`
	p := goPlugin{}
	got, err := p.ParseCoverage(out, "github.com/x/proj")
	if err != nil {
		t.Fatalf("ParseCoverage: %v", err)
	}
	want := map[string]bool{"a.go": true, "b.go": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("executed set = %v, want %v", got, want)
	}
}

// TestGoParseCoverageUnparseableIsError pins the design point that matters
// most here: a report the parser cannot make sense of must come back as an
// ERROR, never a silently-empty map. A later caller turns "no coverage data"
// into a repo-wide finding — if ParseCoverage swallowed garbage into an empty
// map, that finding would be fabricated from noise, not evidence.
func TestGoParseCoverageUnparseableIsError(t *testing.T) {
	p := goPlugin{}

	cases := map[string]string{
		"empty input":                "",
		"no mode header at all":      "just some random text\nthat is not a coverage profile\n",
		"garbled block after mode":   "mode: set\nthis line is not a coverage block\n",
		"non-numeric count":          "mode: set\ngithub.com/x/proj/a.go:3.10,5.2 1 notanumber\n",
		"block missing a colon path": "mode: set\nnocolonhere 1 1\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := p.ParseCoverage(in, "github.com/x/proj")
			if err == nil {
				t.Fatalf("ParseCoverage(%q) = %v, nil error; want an error", in, got)
			}
			if got != nil {
				t.Fatalf("ParseCoverage(%q) returned non-nil map %v alongside an error", in, got)
			}
		})
	}
}

// TestGoCoverageCmdIncludesCoverpkgAll pins the task-5 sweep fix: without
// -coverpkg=./..., `go test ./...` instruments each test binary for ONLY
// the package it directly tests, so a package with no _test.go files of
// its own always reports synthetic all-zero coverage — even when its code
// is genuinely executed via a shared interface from another package's
// tests (verified on gin: codec/json/json.go, called every run through
// json.API.Marshal from the root package's own errors_test.go, was
// reported "measured, never executed" without this flag). -coverpkg=./...
// is what makes cross-package execution visible to the tri-state map.
func TestGoCoverageCmdIncludesCoverpkgAll(t *testing.T) {
	p := goPlugin{}
	cmd, ok := p.CoverageCmd([]string{"go", "test", "./..."})
	if !ok {
		t.Fatalf("CoverageCmd ok=false")
	}
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("CoverageCmd = %v, want [sh -c <script>]", cmd)
	}
	script := cmd[2]
	if !strings.Contains(script, "-coverpkg=./...") {
		t.Fatalf("CoverageCmd script = %q, missing -coverpkg=./... (without it, cross-package execution is invisible and untested packages are falsely reported as unexecuted)", script)
	}
}
