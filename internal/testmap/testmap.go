// SPDX-License-Identifier: Elastic-2.0

// Package testmap is the one structure for "which tests execute which code",
// shared by every language plugin and by the test-selection screen built on
// it. A map is built only from execution evidence (a coverage run, or the
// evidence each runner unit reported); nothing here infers a pairing from a
// file name, because a name proves only that a test mentions a file.
//
// The package holds data and its canonical serialisation, nothing else: how
// evidence is collected lives with the runner, and how a map is turned into a
// selection lives with the caller. internal/lang must never import this
// package; testmap imports lang only for LineRange.
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
