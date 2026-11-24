//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func TestInlineTestLinesReadsTheInlineModuleOnly(t *testing.T) {
	src := []byte("pub fn f() {}\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n")
	got := inlineTestLines("rust", src)
	if want := []lang.LineRange{{Start: 2, End: 6}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inlineTestLines = %v, want %v", got, want)
	}
	if got := inlineTestLines("rust", []byte("#[cfg(test)]\nmod tests;\n")); len(got) != 0 {
		t.Fatalf("a bare mod has no lines in this file, got %v", got)
	}
	if got := inlineTestLines("go", src); len(got) != 0 {
		t.Fatalf("a language with no reader passes nothing, got %v", got)
	}
}
