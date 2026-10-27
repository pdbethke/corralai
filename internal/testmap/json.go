// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
)

// The wire types give the map snake_case names without putting JSON tags on
// lang.LineRange, which other packages serialise on their own terms.
type wireRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type wireSource struct {
	HasStatic     bool        `json:"has_static"`
	HasStatements bool        `json:"has_statements"`
	Static        []wireRange `json:"static"`
}

type wireNode struct {
	ID    string   `json:"id"`
	Grain string   `json:"grain"`
	Unit  string   `json:"unit"`
	Files []string `json:"files"`
	Err   string   `json:"err"`
}

type wireEdge struct {
	Test     string      `json:"test"`
	Source   string      `json:"source"`
	Lines    []wireRange `json:"lines"`
	Executed int         `json:"executed"`
}

// schema has no omitempty: a file with no schema must read as schema 0 and be
// refused, not pass as a default.
type wireMap struct {
	Schema  int                   `json:"schema"`
	Lang    string                `json:"lang"`
	Runner  string                `json:"runner"`
	Command []string              `json:"command"`
	Scope   []string              `json:"scope"`
	Tree    string                `json:"tree"`
	Method  string                `json:"method"`
	Of      int                   `json:"of"`
	Sources map[string]wireSource `json:"sources"`
	Tests   map[string]wireNode   `json:"tests"`
	Edges   []wireEdge            `json:"edges"`
	Note    string                `json:"note"`
}

func toWireRanges(rs []lang.LineRange) []wireRange {
	if rs == nil {
		return nil
	}
	out := make([]wireRange, len(rs))
	for i, r := range rs {
		out[i] = wireRange{r.Start, r.End}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	return out
}

func fromWireRanges(rs []wireRange) []lang.LineRange {
	if rs == nil {
		return nil
	}
	out := make([]lang.LineRange, len(rs))
	for i, r := range rs {
		out[i] = lang.LineRange{Start: r.Start, End: r.End}
	}
	return out
}

func sortedCopy(s []string) []string {
	if s == nil {
		return nil
	}
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// toWire is the canonical form: edges sorted by (Source, Test), every range
// and file list sorted, so two maps that say the same thing serialise alike.
// Command and Scope keep their order; argv order is meaning.
func toWire(m Map) wireMap {
	w := wireMap{Schema: m.Schema, Lang: m.Lang, Runner: m.Runner, Command: m.Command,
		Scope: m.Scope, Tree: m.Tree, Method: m.Method, Of: m.Of, Note: m.Note}
	if m.Sources != nil {
		w.Sources = make(map[string]wireSource, len(m.Sources))
		for k, s := range m.Sources {
			w.Sources[k] = wireSource{s.HasStatic, s.HasStatements, toWireRanges(s.Static)}
		}
	}
	if m.Tests != nil {
		w.Tests = make(map[string]wireNode, len(m.Tests))
		for k, n := range m.Tests {
			w.Tests[k] = wireNode{n.ID, n.Grain, n.Unit, sortedCopy(n.Files), n.Err}
		}
	}
	if m.Edges != nil {
		w.Edges = make([]wireEdge, len(m.Edges))
		for i, e := range m.Edges {
			w.Edges[i] = wireEdge{e.Test, e.Source, toWireRanges(e.Lines), e.Executed}
		}
		sort.Slice(w.Edges, func(i, j int) bool {
			if w.Edges[i].Source != w.Edges[j].Source {
				return w.Edges[i].Source < w.Edges[j].Source
			}
			return w.Edges[i].Test < w.Edges[j].Test
		})
	}
	return w
}

// Write emits the canonical, indented JSON for m. Map keys are sorted by
// encoding/json; the rest is ordered by toWire.
func Write(w io.Writer, m Map) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(toWire(m))
}

// Digest is the sha256 (hex) of exactly the bytes Write produces, so a digest
// names a map's content and not the order it was built in.
func (m Map) Digest() string {
	var buf bytes.Buffer
	// Encoding these plain types cannot fail; a failure here would be a
	// programming error and must not become a digest of nothing.
	if err := Write(&buf, m); err != nil {
		panic(fmt.Sprintf("testmap: encoding a map failed: %v", err))
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// Read parses a map and refuses any Schema other than the one this build
// understands, naming the number it found.
func Read(r io.Reader) (Map, error) {
	var w wireMap
	if err := json.NewDecoder(r).Decode(&w); err != nil {
		return Map{}, fmt.Errorf("testmap: reading map: %w", err)
	}
	if w.Schema != Schema {
		return Map{}, fmt.Errorf("testmap: schema %d is not supported (this build reads schema %d)", w.Schema, Schema)
	}
	m := Map{Schema: w.Schema, Lang: w.Lang, Runner: w.Runner, Command: w.Command,
		Scope: w.Scope, Tree: w.Tree, Method: w.Method, Of: w.Of, Note: w.Note}
	if w.Sources != nil {
		m.Sources = make(map[string]Source, len(w.Sources))
		for k, s := range w.Sources {
			m.Sources[k] = Source{s.HasStatic, s.HasStatements, fromWireRanges(s.Static)}
		}
	}
	if w.Tests != nil {
		m.Tests = make(map[string]Node, len(w.Tests))
		for k, n := range w.Tests {
			m.Tests[k] = Node{n.ID, n.Grain, n.Unit, n.Files, n.Err}
		}
	}
	if w.Edges != nil {
		m.Edges = make([]Edge, len(w.Edges))
		for i, e := range w.Edges {
			m.Edges[i] = Edge{e.Test, e.Source, fromWireRanges(e.Lines), e.Executed}
		}
	}
	return m, nil
}
