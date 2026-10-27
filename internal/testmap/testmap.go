// SPDX-License-Identifier: Elastic-2.0

// Package testmap is the one structure for "which tests execute which code",
// shared by every language plugin and by the test-selection screen built on
// it. A map is built only from execution evidence (a coverage run, or the
// evidence each runner unit reported); nothing here infers a pairing from a
// file name, because a name proves only that a test mentions a file.
//
// The package holds the data, its canonical serialisation, and the one
// selection engine (select.go) that turns a map plus a runner row into a
// lang.Selection and its commands. How evidence is collected lives with the
// runner. internal/lang must never import this package; testmap imports lang
// for LineRange, the Runner row whose fields build every command, and the
// Selection type grading consumes.
package testmap

import (
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
)

// Schema is the only map format this build reads or writes. Read refuses any
// other number rather than guessing at its fields.
const Schema = 1

// Grain says what a Node is: a whole runner unit (a package, a spec file) or a
// single test inside one.
const (
	GrainUnit = "unit"
	GrainTest = "test"
)

// MethodUnitEvidence names a map built from the unit pass: one node per
// runner unit, an edge wherever a unit's own coverage run executed a file,
// and no line ranges. Its nodes are GrainUnit, as MethodCoverageContext's
// are GrainTest; the engine reads the grain from the method.
const MethodUnitEvidence = "unit-evidence"

// Map records which test nodes executed which source files, and under what
// conditions that evidence was collected.
type Map struct {
	Schema  int
	Lang    string            // plugin name
	Runner  string            // runner row ID
	Command []string          // the operator's argv the evidence ran under
	Scope   []string          // source roots / enumerated scope the map was built for; nil = whole repo
	Tree    string            // tree digest the evidence was collected on
	Method  string            // "coverage-context" | "unit-evidence"
	Of      int               // tests (or units) the evidence run collected
	Sources map[string]Source // key: repo-relative source path
	Tests   map[string]Node   // key: node id
	Edges   []Edge            // sorted by (Source, Test)
	Note    string            // why the evidence is partial or absent, verbatim
}

// Source is what is known about one source file independent of any test.
type Source struct {
	HasStatic     bool // Static was computed (it may legitimately be empty)
	HasStatements bool // the file has executable statements at all
	Static        []lang.LineRange
}

// Node is one test or unit the evidence run collected.
type Node struct {
	ID    string
	Grain string
	Unit  string   // containing unit; "" for a unit node
	Files []string // test files the node owns
	Err   string   // why it contributed no evidence; "" when it did
}

// Edge says Test executed Lines of Source, Executed times in all.
type Edge struct {
	Test     string
	Source   string
	Lines    []lang.LineRange
	Executed int
}

// EdgesTo returns the edges whose Source is source, in map order.
func (m Map) EdgesTo(source string) []Edge {
	var out []Edge
	for _, e := range m.Edges {
		if e.Source == source {
			out = append(out, e)
		}
	}
	return out
}

// Unreported returns the sorted ids of nodes that contributed no evidence.
// A caller selecting from partial evidence must add every one of them, or the
// selection under-selects exactly where it knows least.
func (m Map) Unreported() []string {
	var out []string
	for id, n := range m.Tests {
		if n.Err != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Coverage is the map as a per-file readout: every measured source, with
// each test that executed it mapped to how many of its lines that test
// executed. It is what lang.TestSelector.Index answered before the map
// replaced it, so a caller that ranks "which test covers this file the most"
// (reposcan's candidacy index) reads the same numbers. A source measured with
// no edge has an empty, non-nil Tests: zero coverage is a fact about a file
// the run saw, never the file's absence from the readout.
func (m Map) Coverage() map[string]lang.FileCoverage {
	out := make(map[string]lang.FileCoverage, len(m.Sources))
	for path, s := range m.Sources {
		out[path] = lang.FileCoverage{Tests: map[string]int{}, HasStatic: s.HasStatic, HasStatements: s.HasStatements}
	}
	for _, e := range m.Edges {
		if fc, ok := out[e.Source]; ok {
			fc.Tests[e.Test] += e.Executed
		}
	}
	return out
}
