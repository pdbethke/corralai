//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"reflect"
	"testing"
)

func assertSignatures(t *testing.T, want, got []Signature) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("signature count mismatch: want %d, got %d\nwant=%+v\ngot=%+v", len(want), len(got), want, got)
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Name != g.Name {
			t.Errorf("signature[%d].Name: want %q, got %q", i, w.Name, g.Name)
		}
		if w.Kind != g.Kind {
			t.Errorf("signature[%d].Kind: want %q, got %q", i, w.Kind, g.Kind)
		}
		if w.Receiver != g.Receiver {
			t.Errorf("signature[%d].Receiver: want %q, got %q", i, w.Receiver, g.Receiver)
		}
		if !reflect.DeepEqual(w.Params, g.Params) {
			t.Errorf("signature[%d].Params: want %+v, got %+v", i, w.Params, g.Params)
		}
		if !reflect.DeepEqual(w.Results, g.Results) {
			t.Errorf("signature[%d].Results: want %+v, got %+v", i, w.Results, g.Results)
		}
		if w.Exported != g.Exported {
			t.Errorf("signature[%d].Exported: want %v, got %v", i, w.Exported, g.Exported)
		}
		if w.Line != g.Line {
			t.Errorf("signature[%d].Line: want %d, got %d", i, w.Line, g.Line)
		}
	}
}

func TestExtractSignaturesGoFuncs(t *testing.T) {
	src := `package p

import "context"

func Add(a int, b int) int { return a + b }

func fetch(ctx context.Context, url string) ([]byte, error) { return nil, nil }

func noResult() {}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "Add", Kind: "func", Params: []Param{{"a", "int"}, {"b", "int"}}, Results: []string{"int"}, Exported: true, Line: 5},
		{Name: "fetch", Kind: "func", Params: []Param{{"ctx", "context.Context"}, {"url", "string"}}, Results: []string{"[]byte", "error"}, Exported: false, Line: 7},
		{Name: "noResult", Kind: "func", Params: nil, Results: nil, Exported: false, Line: 9},
	}
	assertSignatures(t, want, got)
}

func TestExtractSignaturesGoMethods(t *testing.T) {
	src := `package p

func (e *Engine) CheckoutPR(ctx Ctx, pr int, sha string) error { return nil }

func (s Store) get(a, b string) (Row, bool) { return Row{}, false }

func Variadic(prefix string, rest ...int) {}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "CheckoutPR", Kind: "method", Receiver: "*Engine", Params: []Param{{"ctx", "Ctx"}, {"pr", "int"}, {"sha", "string"}}, Results: []string{"error"}, Exported: true, Line: 3},
		{Name: "get", Kind: "method", Receiver: "Store", Params: []Param{{"a", "string"}, {"b", "string"}}, Results: []string{"Row", "bool"}, Exported: false, Line: 5},
		{Name: "Variadic", Kind: "func", Params: []Param{{"prefix", "string"}, {"rest", "...int"}}, Results: nil, Exported: true, Line: 7},
	}
	assertSignatures(t, want, got)
}
