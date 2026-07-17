// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/bugcatch"
)

type fakeScore struct{ cells []bugcatch.Cell }

func (f fakeScore) Scorecard(context.Context) ([]bugcatch.Cell, error) { return f.cells, nil }

func TestScorecardTableAndJSON(t *testing.T) {
	r := 0.5
	f := fakeScore{cells: []bugcatch.Cell{{Model: "claude-sonnet-5", Role: "test-writer", Catches: 1, Opportunities: 2, Recall: &r, Runs: 2}}}

	var table bytes.Buffer
	if rc := runScorecard(nil, f, &table); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(table.String(), "claude-sonnet-5") || !strings.Contains(table.String(), "test-writer") {
		t.Fatalf("table missing model/role:\n%s", table.String())
	}
	if !strings.Contains(table.String(), "provisional") { // runs=2 < 3
		t.Fatalf("thin cell must be marked provisional:\n%s", table.String())
	}

	var j bytes.Buffer
	if rc := runScorecard([]string{"--json"}, f, &j); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(j.String(), `"recall"`) || !strings.Contains(j.String(), "0.5") {
		t.Fatalf("json missing recall:\n%s", j.String())
	}
}
