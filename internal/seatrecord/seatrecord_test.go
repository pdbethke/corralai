// SPDX-License-Identifier: Elastic-2.0

package seatrecord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The public warehouse extract (scripts/build-warehouse-parquet.py) is a
// per-model reader too, written in Python, so it cannot import this constant.
// It carries the same text instead, and this test is what keeps it the same:
// the public scorecard and `corral models rank` must not disagree about which
// rows count.
func TestTheWarehouseExtractUsesTheSamePredicate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-warehouse-parquet.py"))
	if err != nil {
		t.Fatal(err)
	}
	want := `RESOLVED_ONLY = "` + ResolvedOnly + `"`
	if !strings.Contains(string(b), want) {
		t.Fatalf("scripts/build-warehouse-parquet.py must define %s — the same predicate the Go stores apply", want)
	}
}

// The rule, read from the record alone: a recorded tool says an agent sat,
// and only a pin names its model; a built-in name with no tool is an older
// agent record. (agentbackend's TestRecordedSeatNamesAModelIsEnvFree holds the
// same cases through its wrapper.)
func TestRecordedSeatNamesAModel(t *testing.T) {
	for _, c := range []struct {
		spec, tool string
		want       bool
	}{
		{"codex", "codex-cli 0.160.0", false},
		{"codex:gpt-6-astra", "codex-cli 0.160.0", true},
		{"gpt-6", "", true},
		{"", "", false},
		{"  ", "", false},
		{"claude-code", "", false},
		{"claude-code:claude-opus-5-5", "", true},
		{"antigravity", "", false},
	} {
		if got := RecordedSeatNamesAModel(c.spec, c.tool); got != c.want {
			t.Errorf("RecordedSeatNamesAModel(%q, %q) = %v, want %v", c.spec, c.tool, got, c.want)
		}
	}
}
