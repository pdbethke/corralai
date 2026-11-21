//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// swiftSurface is Swift written for these tests: one of each unit the reader
// collects, and one of each it must not.
const swiftSurface = `import Foundation

protocol Priced {
    func price() -> Int
    var label: String { get }
}

extension Priced {
    func doubled() -> Int { price() * 2 }
}

public struct Box {
    var w: Double
    private(set) var h: Double = 0

    init() { w = 0 }
    init(w: Double, h: Double) { self.w = w; self.h = h }
    init(_ side: Double) { w = side; h = side }

    var area: Double { w * h }
    var observed: Int = 0 {
        didSet { if observed < 0 { observed = 0 } }
    }
    let scale = { (x: Double) -> Double in x > 0 ? x : 0 }
    var stored = 3

    subscript(i: Int) -> Double { i == 0 ? w : h }
    static func + (l: Box, r: Box) -> Box { Box(w: l.w + r.w, h: 0) }
    func resize(to w: Double, _ h: Double, tags: String...) -> Box { Box(w: w, h: h) }

    struct Inner {
        func go() {}
    }
}

extension Box.Inner {
    func stop() {}
}

private struct Hidden {
    func run() {
        func helper() -> Int { 1 }
        _ = helper()
    }
}

final class Node {
    deinit { print("bye") }
    fileprivate func secret() {}
}

func free(_ items: [Int]) -> Int {
    items.count
}
`

func swiftSigs(t *testing.T, src string) map[string]Signature {
	t.Helper()
	sigs, err := ExtractSignatures(src, "swift")
	if err != nil {
		t.Fatalf("ExtractSignatures(swift): %v", err)
	}
	return idsOf(sigs)
}

// TestSwiftSignatures: every unit with a body, named as Swift names it and
// attributed to its type; no protocol requirement, stored property or nested
// function.
func TestSwiftSignatures(t *testing.T) {
	got := swiftSigs(t, swiftSurface)
	want := []string{
		"Priced.doubled()",
		"Box.init()", "Box.init(w:h:)", "Box.init(_:)",
		"Box.area", "Box.observed", "Box.scale",
		"Box.subscript(_:)", "Box.+(_:_:)", "Box.resize(to:_:tags:)",
		"Box.Inner.go()", "Box.Inner.stop()",
		"Hidden.run()",
		"Node.deinit", "Node.secret()",
		"free(_:)",
	}
	keys := keysOfSigs(got)
	sort.Strings(keys)
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("symbols:\n got  %v\n want %v", keys, want)
	}

	if s := got["Box.resize(to:_:tags:)"]; !reflect.DeepEqual(s.Params, []Param{{"w", "Double"}, {"h", "Double"}, {"tags", "String..."}}) ||
		!reflect.DeepEqual(s.Results, []string{"Box"}) || s.Kind != "method" {
		t.Errorf("resize: params %v results %v kind %q", s.Params, s.Results, s.Kind)
	}
	freeLine := strings.Count(swiftSurface[:strings.Index(swiftSurface, "func free")], "\n") + 1
	if s := got["free(_:)"]; s.Kind != "func" || s.Receiver != "" || s.Line != freeLine || s.Lines != 3 {
		t.Errorf("free: kind %q receiver %q line %d lines %d", s.Kind, s.Receiver, s.Line, s.Lines)
	}
	// private and fileprivate, on the unit or on its type, are not
	// exported; private(set) on a property is not private; the default
	// (internal) is.
	for id, exp := range map[string]bool{
		"Box.area": true, "Box.init()": true, "free(_:)": true,
		"Hidden.run()": false, "Node.secret()": false, "Node.deinit": true,
	} {
		if got[id].Exported != exp {
			t.Errorf("%s: exported %v, want %v", id, got[id].Exported, exp)
		}
	}
	// The nested helper is part of run()'s span, not a symbol of its own.
	if s := got["Hidden.run()"]; s.Lines != 4 {
		t.Errorf("Hidden.run(): %d lines, want 4 (the nested function included)", s.Lines)
	}
}

// swiftComplexity is the complexity of the one function in body.
func swiftComplexity(t *testing.T, body string) (int, []Decision) {
	t.Helper()
	sigs, err := ExtractSignatures("func f(_ x: Int?, _ y: Int, _ v: [Int]) -> Int {\n"+body+"\n}\n", "swift")
	if err != nil || len(sigs) != 1 {
		t.Fatalf("%q: %d signatures, err %v", body, len(sigs), err)
	}
	return sigs[0].Complexity, sigs[0].Decisions
}

// TestSwiftDecisionPoints pins the swift row's rule, one construct per case:
// what counts as a decision, and each thing that does not, for the reason
// its counterpart in another row does not.
func TestSwiftDecisionPoints(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"straight line", "return y", 1},
		{"if", "if y > 0 { return 1 }\nreturn 0", 2},
		{"else if is its own if", "if y > 0 { return 1 } else if y < 0 { return 2 } else { return 3 }", 3},
		{"guard", "guard y > 0 else { return 0 }\nreturn y", 2},
		{"for", "for i in v { print(i) }\nreturn 0", 2},
		{"while", "while y > 0 { break }\nreturn 0", 2},
		{"repeat while", "repeat { break } while y > 0\nreturn 0", 2},
		{"catch blocks, each", "do { try g() } catch let e as E { print(e) } catch { print(error) }\nreturn 0", 3},
		{"do alone decides nothing", "do { print(1) }\nreturn 0", 1},
		{"ternary", "return y > 0 ? 1 : 0", 2},
		{"&& and ||", "return y > 0 && y < 9 || y == 20 ? 1 : 0", 4},
		{"switch arms, not the default", "switch y {\ncase 1: return 1\ncase 2, 3: return 2\ndefault: return 0\n}", 3},
		{"@unknown default is a default", "switch y {\ncase 1: return 1\n@unknown default: return 0\n}", 2},
		// Swift's condition list: each comma short-circuits as && does.
		{"comma-joined conditions", "guard let a = x, let b = Optional(y), a < b else { return 0 }\nreturn 1", 4},
		{"if let with a condition", "if let a = x, a > 0 { return a }\nreturn 0", 3},
		{"while let with a condition", "while let a = x, a > 0 { break }\nreturn 0", 3},
		// A comma inside one condition's pattern joins nothing.
		{"comma inside a pattern", "if case .some(let a) = x, case (let b, let c) = (a, y) { return b + c }\nreturn 0", 3},
		{"?? is not counted", "return x ?? y", 1},
		{"optional chaining is not counted", "return x?.signum() ?? 0", 1},
		{"where is not counted", "for i in v where i > 0 { print(i) }\nswitch y {\ncase let n where n > 3: return n\ndefault: return 0\n}", 3},
		{"try? is not counted", "return (try? g()) ?? 0", 1},
		{"a closure's decisions are the function's", "return v.filter { $0 > 0 ? true : false }.count", 2},
	} {
		if got, _ := swiftComplexity(t, c.body); got != c.want {
			t.Errorf("%s: complexity %d, want %d\n%s", c.name, got, c.want, c.body)
		}
	}

	// A joined condition's span is its own line.
	_, ds := swiftComplexity(t, "guard let a = x,\n      a > 0\nelse { return 0 }\nreturn a")
	if !reflect.DeepEqual(ds, []Decision{{2, 4}, {2, 3}}) {
		t.Errorf("multi-line guard decisions %v, want the guard [2 4] and its second condition [2 3]", ds)
	}
}

// TestSwiftOverloadsAreDistinctSymbols: Swift overloads by argument label, so
// each initializer is its own symbol (and its own shard identity), where the
// base name alone would collapse them.
func TestSwiftOverloadsAreDistinctSymbols(t *testing.T) {
	sigs, err := ExtractSignatures(swiftSurface, "swift")
	if err != nil {
		t.Fatal(err)
	}
	inits := 0
	for _, s := range sigs {
		if s.Receiver == "Box" && strings.HasPrefix(s.Name, "init") {
			inits++
		}
	}
	if inits != 3 || len(idsOf(sigs)) != len(sigs) {
		t.Errorf("%d Box initializers, %d distinct identities of %d symbols; want 3 and all distinct", inits, len(idsOf(sigs)), len(sigs))
	}
}

// TestAParseThatSkippedCodeIsNotAReading: a grammar that cannot place part
// of a file wraps it in an ERROR node and goes on. Between symbols, that node
// may hold symbols nobody read, so the surface is refused rather than handed
// on short; inside a symbol, the symbol was placed, so it is kept. The rule
// is the shared extractSignatures', checked here in two languages.
func TestAParseThatSkippedCodeIsNotAReading(t *testing.T) {
	// This Swift grammar loses its place at a string holding "/*" followed by
	// a negated call on a string holding "*/" (the shape was found on a real
	// file), and here wraps the start of the struct, with b(), in an ERROR
	// node at the top of the file.
	swallowed := "struct Scanner {\n" +
		"    func b() {\n" +
		"        if m(\"/*\") {\n" +
		"            while i < n, !m(\"*/\") {\n" +
		"                i += 1\n" +
		"            }\n" +
		"            continue\n" +
		"        }\n" +
		"        if c == \"]\" {\n" +
		"            break\n" +
		"        }\n" +
		"    }\n" +
		"    func c() {}\n" +
		"}\n"
	if sigs, err := ExtractSignatures(swallowed, "swift"); !errors.Is(err, ErrIncompleteParse) || sigs != nil {
		t.Errorf("swift with a swallowed struct: %d signatures, err %v; want nil, ErrIncompleteParse", len(sigs), err)
	}

	// The same lines inside one free function: the function is placed.
	inside := "func b() {\n    x(\"/*\")\n    y(!m(\"*/\"))\n}\n\nfunc after() -> Int { 2 }\n"
	if sigs, err := ExtractSignatures(inside, "swift"); err != nil || len(sigs) != 2 {
		t.Errorf("swift with an error inside a function: %d signatures, err %v; want 2, nil", len(sigs), err)
	}

	// An unfinished type declaration swallows the function after it.
	goSrc := "package a\n\nfunc F() int { return 1 }\n\ntype struct {\n\nfunc H() int { return 2 }\n"
	if sigs, err := ExtractSignatures(goSrc, "go"); !errors.Is(err, ErrIncompleteParse) || sigs != nil {
		t.Errorf("go with a broken declaration between functions: %d signatures, err %v; want nil, ErrIncompleteParse", len(sigs), err)
	}
}
