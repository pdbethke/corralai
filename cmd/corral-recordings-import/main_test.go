// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"sort"
	"strings"
	"testing"
)

// TestImportFlagsBindAndExposeOnlyTheirOwn is this binary's first test.
//
// It had none, and no entry in the generated CLI reference either — because
// scripts/gen-cli-docs.sh listed its binaries by hand and omitted this one. So
// five flags were shipped, documented in the publishing guide, and exercised by
// nothing. The executed-surface manifest found them the moment that list was
// derived from cmd/ instead.
//
// Both halves matter. Binding: a flag that parses and is then dropped on the
// floor is this codebase's named recurring defect. Exposure: corral-observe was
// advertising go-rod's `-rod` flag as part of corral's interface because it
// registered on the shared flag.CommandLine, and a private set is what stops
// the next import from doing it silently.
func TestImportFlagsBindAndExposeOnlyTheirOwn(t *testing.T) {
	var o importOpts
	if err := importFlags(&o).Parse([]string{
		"-db", "/tmp/rec.duckdb", "-slug", "the-run", "-mission-id", "42",
		"-replay", "/tmp/replay.json", "-meta", "/tmp/meta.json",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"db", o.db, "/tmp/rec.duckdb"},
		{"slug", o.slug, "the-run"},
		{"replay", o.replay, "/tmp/replay.json"},
		{"meta", o.meta, "/tmp/meta.json"},
	} {
		if c.got != c.want {
			t.Errorf("-%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if o.missionID != 42 {
		t.Errorf("-mission-id = %d, want 42", o.missionID)
	}

	var got []string
	importFlags(&importOpts{}).VisitAll(func(f *flag.Flag) { got = append(got, f.Name) })
	sort.Strings(got)
	want := []string{"db", "meta", "mission-id", "replay", "slug"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("flags = %v, want %v — a flag appeared or vanished; if an import added it, this binary is advertising something it did not choose to", got, want)
	}
}

// TestRequiredMissingNamesEveryMissingFlagAtOnce: the original code exited on
// the first missing flag, so an operator running an import by hand learned one
// requirement per attempt. Reporting all of them is the difference between one
// correction and three.
func TestRequiredMissingNamesEveryMissingFlagAtOnce(t *testing.T) {
	if got := requiredMissing(importOpts{}); len(got) != 3 {
		t.Errorf("requiredMissing(empty) = %v, want all three (--slug --replay --meta)", got)
	}
	complete := importOpts{slug: "s", replay: "r", meta: "m"}
	if got := requiredMissing(complete); got != nil {
		t.Errorf("requiredMissing(complete) = %v, want nil", got)
	}
	// db and mission-id are optional: db defaults, and a recording without a
	// mission is still a recording.
	if got := requiredMissing(importOpts{slug: "s", replay: "r", meta: "m", db: "", missionID: 0}); got != nil {
		t.Errorf("db and mission-id must be optional, got %v", got)
	}
}
