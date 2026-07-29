// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
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
	// count>0 means executed; b.go has count 0 and must NOT appear.
	want := map[string]bool{"pkg/a.go": true, "c.go": true}
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
