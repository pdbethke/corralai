//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"errors"
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

func TestExtractSignaturesPython(t *testing.T) {
	src := "def add(a, b):\n    return a + b\n\n" +
		"def fetch(ctx, url: str) -> bytes:\n    return b''\n\n" +
		"async def _hidden(x):\n    return x\n"
	got, err := ExtractSignatures(src, "python")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "add", Kind: "func", Params: []Param{{"a", ""}, {"b", ""}}, Exported: true, Line: 1},
		{Name: "fetch", Kind: "func", Params: []Param{{"ctx", ""}, {"url", "str"}}, Results: []string{"bytes"}, Exported: true, Line: 4},
		{Name: "_hidden", Kind: "func", Params: []Param{{"x", ""}}, Exported: false, Line: 7},
	}
	assertSignatures(t, want, got)
}

func TestGoSignatureComplexity(t *testing.T) {
	src := `package p

func Simple(a int) int { return a }

func Branchy(a int) int {
	if a > 0 {
		for i := 0; i < a; i++ {
			if i%2 == 0 && i > 2 {
				return i
			}
		}
	}
	switch a {
	case 1:
		return 1
	case 2:
		return 2
	}
	return 0
}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d: %+v", len(got), got)
	}
	if got[0].Complexity != 1 {
		t.Errorf("Simple.Complexity: want 1, got %d", got[0].Complexity)
	}
	if got[0].Lines != 1 {
		t.Errorf("Simple.Lines: want 1, got %d", got[0].Lines)
	}
	// 1 base + 2 if + 1 for + 1 "&&" + 2 case = 7
	if got[1].Complexity != 7 {
		t.Errorf("Branchy.Complexity: want 7, got %d", got[1].Complexity)
	}
	if got[1].Lines != 16 {
		t.Errorf("Branchy.Lines: want 16, got %d", got[1].Lines)
	}
}

func TestPythonSignatureComplexity(t *testing.T) {
	src := `def simple(a):
    return a


def branchy(a):
    if a > 0:
        for i in range(a):
            if i % 2 == 0 and i > 2:
                return i
    elif a < 0:
        while a < 0:
            a += 1
    return 0
`
	got, err := ExtractSignatures(src, "python")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d: %+v", len(got), got)
	}
	if got[0].Complexity != 1 {
		t.Errorf("simple.Complexity: want 1, got %d", got[0].Complexity)
	}
	// 1 base + 2 if + 1 for + 1 "and" + 1 elif + 1 while = 7
	if got[1].Complexity != 7 {
		t.Errorf("branchy.Complexity: want 7, got %d", got[1].Complexity)
	}
}

func TestExtractSignaturesUnwiredLang(t *testing.T) {
	// cobol has no extractor wired (even though it's not a repoindex grammar);
	// the contract is an explicit ErrUnsupportedLang, never a silent empty.
	_, err := ExtractSignatures("       IDENTIFICATION DIVISION.", "cobol")
	if !errors.Is(err, ErrUnsupportedLang) {
		t.Fatalf("want ErrUnsupportedLang, got %v", err)
	}
}
