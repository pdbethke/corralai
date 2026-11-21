// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// TestRegionsLineSaysWhyThereIsNoSurface: when the grammar set code aside
// between functions, the run's regions line names the lines, because the
// budget falls to the floor for that reason and nothing else says it.
func TestRegionsLineSaysWhyThereIsNoSurface(t *testing.T) {
	serr := fmt.Errorf("%w: lines 14-312 are not inside any function it read", repoindex.ErrIncompleteParse)
	got := regionsLine(nil, nil, serr)
	if !strings.Contains(got, "lines 14-312") || !strings.Contains(got, "no symbol surface") {
		t.Errorf("regionsLine = %q, want the unparsed lines named", got)
	}
	plain := regionsLine(nil, nil, repoindex.ErrUnsupportedLang)
	if plain != "regions: 1 generator seat (whole file — no symbol surface extracted)" {
		t.Errorf("regionsLine for a language with no reader = %q, want the existing line unchanged", plain)
	}
	sh := []advpool.Shard{{Symbols: []string{"a", "b"}}, {Symbols: []string{"c"}}}
	if got := regionsLine(sh, make([]repoindex.Signature, 3), nil); got != "regions: 2 generator seats over 3 functions" {
		t.Errorf("sharded regionsLine = %q", got)
	}
}
