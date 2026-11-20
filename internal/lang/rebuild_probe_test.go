// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// The probe is one shared rule, a line comment put BEFORE the file's first
// line, in the language's own comment marker. Before, not after: a comment
// appended at the end leaves every line where it was, so the compiled object
// does not change and SwiftPM skips the relink a mutant pays (measured on
// taplin/Lariat, see PerRunBuilder).
func TestSwiftRebuildProbeIsALeadingLineComment(t *testing.T) {
	p, ok := Plugin(swiftPlugin{}).(PerRunBuilder)
	if !ok {
		t.Fatal("swift does not implement PerRunBuilder")
	}
	src := "public func add(_ a: Int, _ b: Int) -> Int { a + b }\n"
	got, ok := p.RebuildProbe("Sources/Calc/Calc.swift", src)
	if !ok {
		t.Fatal("RebuildProbe refused an ordinary source")
	}
	if !strings.HasSuffix(got, src) {
		t.Fatalf("probe %q does not keep the source unchanged after its first line", got)
	}
	first, rest, _ := strings.Cut(got, "\n")
	if rest != src {
		t.Fatalf("probe added more than one line: %q", got)
	}
	if !strings.HasPrefix(first, "// ") {
		t.Fatalf("probe's first line %q is not a Swift line comment", first)
	}
	if strings.Contains(first, "\r") {
		t.Fatalf("probe's first line %q carries a carriage return", first)
	}
}

// A file that starts with a shebang keeps it first: a comment before `#!`
// turns the shebang into a syntax error, so such a file is not probed at all
// (no floor) rather than probed into a file that does not build. Rust's `#![`
// inner attribute is not a shebang and may follow a comment.
func TestRebuildProbeRefusesAShebangFile(t *testing.T) {
	if got, ok := leadingCommentProbe("//", "#!/usr/bin/swift\nprint(1)\n"); ok {
		t.Fatalf("probed a shebang file into %q", got)
	}
	if _, ok := leadingCommentProbe("//", "#![allow(dead_code)]\nfn a() {}\n"); !ok {
		t.Fatal("refused an inner attribute, which a comment may precede")
	}
}

// A byte-order mark stays the file's first bytes: a mark after a comment is
// no longer a mark but a character in the source.
func TestRebuildProbeKeepsAByteOrderMarkFirst(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	got, ok := leadingCommentProbe("//", bom+"let a = 1\n")
	if !ok {
		t.Fatal("refused a file with a byte-order mark")
	}
	if !strings.HasPrefix(got, bom+"// ") || !strings.HasSuffix(got, "\nlet a = 1\n") || strings.Count(got, bom) != 1 {
		t.Fatalf("probe = %q, want the mark, then the comment line, then the source", got)
	}
}
