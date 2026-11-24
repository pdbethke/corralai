//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"reflect"
	"strings"
	"testing"

	plugins "github.com/pdbethke/corralai/internal/lang"
)

// TestRustOwnTestLinesFindTestCodeAtAnyDepth: the lines a mutant may not edit
// are every piece of the source's own test code the grammar placed, at any
// nesting level, not only a top-level `#[cfg(test)] mod tests` written
// exactly so: a test module nested in an inline module, one gated on a cfg
// predicate holding `test` (all(test, …), any(test, …)), and a test-attributed
// or cfg(test) item outside any module. `cfg(not(test))` is code the tests
// never compile, and a bare `mod tests;` puts no test line in this file.
func TestRustOwnTestLinesFindTestCodeAtAnyDepth(t *testing.T) {
	src := strings.Join([]string{
		"pub fn f() -> i32 { 1 }",              // 1
		"mod imp {",                            // 2
		"    pub fn g() {}",                    // 3
		"    #[cfg(test)]",                     // 4
		"    mod tests {",                      // 5
		"        #[test]",                      // 6
		"        fn t() {}",                    // 7
		"    }",                                // 8
		"}",                                    // 9
		"#[cfg(all(test, feature = \"std\"))]", // 10
		"mod checks {",                         // 11
		"    fn c() {}",                        // 12
		"}",                                    // 13
		"#[test]",                              // 14
		"fn loose() { assert_eq!(f(), 1); }",   // 15
		"#[tokio::test]",                       // 16
		"async fn loose_async() {}",            // 17
		"#[cfg(test)]",                         // 18
		"fn helper() -> i32 { 2 }",             // 19
		"#[cfg(not(test))]",                    // 20
		"fn prod_only() {}",                    // 21
		"#[cfg(any(test, feature = \"x\"))]",   // 22
		"mod either { fn e() {} }",             // 23
		"#[rstest]",                            // 24
		"#[case(1)]",                           // 25
		"fn cases(#[case] x: i32) {}",          // 26
		"mod tests;",                           // 27
		"mod test {",                           // 28
		"    fn named() {}",                    // 29
		"}",                                    // 30
		"",
	}, "\n")
	lines, unread := OwnTestLines("rust", []byte(src))
	want := []plugins.LineRange{{Start: 4, End: 8}, {Start: 10, End: 13}, {Start: 14, End: 15}, {Start: 16, End: 17}, {Start: 18, End: 19}, {Start: 22, End: 23}, {Start: 24, End: 26}, {Start: 28, End: 30}}
	if !reflect.DeepEqual(lines, want) || unread != "" {
		t.Fatalf("OwnTestLines = %v, %q\nwant %v, \"\"", lines, unread, want)
	}
}

// TestRustOwnTestLinesFailClosedOnCodeSetAside: tree-sitter wraps code it
// cannot parse in an ERROR node and goes on, and test code inside one is
// invisible to the walk. A parse that set code aside outside every test span
// and every function it placed is no reading of where the tests are, so it
// answers why (unread) and the caller invalidates every mutant. An error
// inside a function body is kept, as unreadSpan keeps one inside a symbol:
// libtest collects no test from a function body.
func TestRustOwnTestLinesFailClosedOnCodeSetAside(t *testing.T) {
	// Line 2 is wrapped in an ERROR node between two items: what it held
	// cannot be read, so nothing says it is not test code.
	setAside := "pub fn f() -> i32 { 1 }\n@@@\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n"
	if _, unread := OwnTestLines("rust", []byte(setAside)); unread != "the grammar set aside lines 2-2, which could hold the source's own tests" {
		t.Errorf("a parse that set code aside outside every function: unread = %q", unread)
	}
	inFn := "pub fn f() -> i32 { let x = (1; 1 }\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n"
	lines, unread := OwnTestLines("rust", []byte(inFn))
	if unread != "" || !reflect.DeepEqual(lines, []plugins.LineRange{{Start: 2, End: 6}}) {
		t.Errorf("an error inside a function body: %v, %q; want the test module and a complete reading", lines, unread)
	}
	if lines, unread := OwnTestLines("python", []byte("def f(): pass\n")); lines != nil || unread != "" {
		t.Errorf("a language with no reader answered %v, %q", lines, unread)
	}
}
