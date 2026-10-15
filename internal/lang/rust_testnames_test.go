// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

// What libtest collects, beside a near miss it does not, in the shapes a
// paired Rust test file takes: the source itself with an inline
// `#[cfg(test)] mod tests`, and a `#[path]` file whose top level IS the test
// module's body (Warp's foo_tests.rs).
func TestRustTestNamesInSourceListsWhatLibtestCollects(t *testing.T) {
	src := `//! Crate docs: #[test] fn in_inner_doc() {}

use std::collections::HashMap;

/// Doc comment: #[test] fn in_doc() {}
pub fn add(a: i32, b: i32) -> i32 {
    #[test]
    fn inner_item_is_not_collected() {}
    a + b
}

const RAW: &str = r#"#[test] fn in_raw_string() { "}"#;
const BYTES: &[u8] = b"#[test] fn in_byte_string() {";
const CH: char = '{';
const ESC: char = '\'';

struct Holder<'a> { s: &'a str }

impl<'a> Holder<'a> {
    #[test]
    fn in_an_impl_is_not_collected() {}
}

macro_rules! make {
    () => { #[test] fn only_when_invoked() {} };
}

#[test]
fn top_level() {}

#[cfg(test)]
mod tests {
    use super::*;

    /* a block comment /* nested */ #[test] fn in_nested_comment() {} */

    #[test]
    fn plain() {
        let s = "brace } in a string";
        let c = '}';
        assert_eq!(add(1, 2), 3);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn tokio_test() {}

    #[test]
    #[serial_test::serial]
    #[should_panic(expected = "boom")]
    pub fn several_attributes() {}

    #[serial_test::serial]
    fn not_a_test() {}

    #[cfg_attr(feature = "x", ignore)]
    #[test_log::test]
    fn test_log_test() {}

    #[rstest]
    #[case(1)]
    fn rstest_cases(#[case] n: i32) {}

    #[test_case(1 ; "one")]
    fn test_case_cases(n: i32) {}

    fn helper() {}

    mod nested {
        #[test]
        fn deep() {}
    }

    proptest! {
        #[test]
        fn property(x in 0..10u8) {}
    }
}
`
	want := []string{
		"top_level",
		"tests::plain",
		"tests::tokio_test",
		"tests::several_attributes",
		"tests::test_log_test",
		"tests::rstest_cases",
		"tests::test_case_cases",
		"tests::nested::deep",
		"tests::property",
	}
	if got := (rustPlugin{}).TestNamesInSource("src/lib.rs", src); !reflect.DeepEqual(got, want) {
		t.Errorf("TestNamesInSource =\n  %q\nwant\n  %q", got, want)
	}
}

// A scan that loses its place lists nothing rather than the tests before it
// went wrong (see the Python lister): the critic is held to the list.
func TestRustTestNamesInSourceListsNothingWhenItLosesItsPlace(t *testing.T) {
	for name, src := range map[string]string{
		"unterminated string":        "#[test]\nfn a() {}\nconst S: &str = \"open;\n#[test]\nfn b() {}\n",
		"unterminated raw string":    "#[test]\nfn a() {}\nconst S: &str = r##\"open\"#;\n",
		"unterminated block comment": "#[test]\nfn a() {}\n/* open /* nested */\n#[test]\nfn b() {}\n",
		"unbalanced braces":          "mod tests {\n#[test]\nfn a() {}\n",
		"stray closing brace":        "#[test]\nfn a() {}\n}\n",
		"unterminated attribute":     "#[test\nfn a() {}\n",
	} {
		if got := (rustPlugin{}).TestNamesInSource("src/lib.rs", src); got != nil {
			t.Errorf("%s: TestNamesInSource = %q, want nil", name, got)
		}
	}
}

// Lifetimes and labels are not char literals: a scanner that took 'a as the
// start of one would blank the rest of the line and lose the braces on it.
func TestRustTestNamesInSourceReadsLifetimesAsCode(t *testing.T) {
	src := "fn f<'a>(x: &'a str) -> &'a str { 'outer: loop { break 'outer; } x }\n#[test]\nfn after() {}\n"
	if got := (rustPlugin{}).TestNamesInSource("src/lib.rs", src); !reflect.DeepEqual(got, []string{"after"}) {
		t.Errorf("TestNamesInSource = %q, want [after]", got)
	}
}
