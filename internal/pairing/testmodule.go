// SPDX-License-Identifier: Elastic-2.0

package pairing

// TestModule is one `mod` declaration a Rust source file makes that holds (or
// points at) its tests. It is the INPUT the content-based Shapes read — the
// parse happens in repoindex (which owns the grammar); pairing does no parsing
// itself, so a Shape decides "where is this file's test" from these four facts
// and nothing else.
//
// The three forms real crates use, and what they set:
//
//	mod tests { ... }                                  Inline, no PathAttr
//	#[cfg(test)] #[path = "x_tests.rs"] mod tests;     PathAttr = "x_tests.rs", CfgTest
//	mod tests;                                         bare: the file is tests.rs
//	                                                   beside the source module
//
// Inline is the one that matters most: the tests live IN the source file, so a
// source with such a module pairs to itself, and a source with none must not.
type TestModule struct {
	// Name is the module identifier (`tests`).
	Name string
	// Inline is true when the module has a `{ ... }` body in this file, false
	// when it is a bare `mod name;` declaration whose body is another file.
	Inline bool
	// PathAttr is the value of a `#[path = "..."]` attribute on the module,
	// relative to the declaring file's directory; "" when there is none.
	PathAttr string
	// CfgTest is true when the module carries `#[cfg(test)]`.
	CfgTest bool
}
