// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// The probe is one shared rule for every language, a line comment put BEFORE
// the file's first line, in the language's own comment marker. Before, not
// after: a comment appended at the end leaves every line where it was, so the
// compiled object does not change and SwiftPM skips the relink a mutant pays
// (measured on taplin/Lariat, see RebuildProbe).
func TestEveryLanguageRebuildProbeIsALeadingLineComment(t *testing.T) {
	for _, n := range Names() {
		p := mustByName(t, n)
		src := paritySamples[n].src
		got, err := RebuildProbe(p, src)
		if err != nil {
			t.Errorf("%s: RebuildProbe refused its sample source: %v", n, err)
			continue
		}
		probe := p.LineComment() + " " + rebuildProbeText + "\n"
		at := strings.Index(got, probe)
		if at < 0 || got[:at]+got[at+len(probe):] != src {
			t.Errorf("%s: probe %q is not one %q comment line added to the unchanged source", n, got, p.LineComment())
			continue
		}
		// First, unless the language keeps its first line (PHP's open tag):
		// then second.
		if k, ok := p.(FirstLineKeeper); ok {
			if keep, _ := k.KeepFirstLine(src); keep {
				first, _, _ := strings.Cut(src, "\n")
				if at != len(first)+1 {
					t.Errorf("%s: probe at byte %d, want right after the kept first line", n, at)
				}
				continue
			}
		}
		if at != 0 {
			t.Errorf("%s: probe at byte %d, want first", n, at)
		}
	}
}

// A file that starts with a shebang keeps it first: a comment before `#!`
// turns the shebang into a syntax error, so such a file is not probed at all
// (no floor) rather than probed into a file that does not build. Rust's `#![`
// inner attribute is not a shebang and may follow a comment.
func TestRebuildProbeRefusesAShebangFile(t *testing.T) {
	for _, name := range []string{"swift", "python", "ruby", "javascript"} {
		got, err := RebuildProbe(mustByName(t, name), "#!/usr/bin/env x\nprint(1)\n")
		if err == nil || !strings.Contains(err.Error(), "shebang") {
			t.Errorf("%s: probed a shebang file into %q (err %v), want a refusal naming the shebang", name, got, err)
		}
	}
	if _, err := RebuildProbe(mustByName(t, "rust"), "#![allow(dead_code)]\nfn a() {}\n"); err != nil {
		t.Fatalf("refused an inner attribute, which a comment may precede: %v", err)
	}
}

// A byte-order mark stays the file's first bytes: a mark after a comment is
// no longer a mark but a character in the source.
func TestRebuildProbeKeepsAByteOrderMarkFirst(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	got, err := RebuildProbe(mustByName(t, "swift"), bom+"let a = 1\n")
	if err != nil {
		t.Fatalf("refused a file with a byte-order mark: %v", err)
	}
	if !strings.HasPrefix(got, bom+"// ") || !strings.HasSuffix(got, "\nlet a = 1\n") || strings.Count(got, bom) != 1 {
		t.Fatalf("probe = %q, want the mark, then the comment line, then the source", got)
	}
}

// PHP: the probe goes after the `<?php` line, because text before the open
// tag is output, and with it a declare(strict_types=1) is a fatal error
// ("strict_types declaration must be the very first statement", PHP 8, run
// 2026-11-26). A file that does not open with the tag is not probed.
func TestPHPRebuildProbeGoesAfterTheOpenTag(t *testing.T) {
	php := mustByName(t, "php")
	for _, first := range []string{"<?php", "<?php declare(strict_types=1);", "<?PHP"} {
		src := first + "\nfunction a() {}\n"
		got, err := RebuildProbe(php, src)
		if err != nil {
			t.Fatalf("%q: refused: %v", first, err)
		}
		want := first + "\n// " + rebuildProbeText + "\nfunction a() {}\n"
		if got != want {
			t.Errorf("%q: probe = %q, want %q", first, got, want)
		}
	}
	for _, src := range []string{"<html><?php echo 1; ?>\n", "<?php echo 1;"} {
		if got, err := RebuildProbe(php, src); err == nil {
			t.Errorf("probed %q into %q, want a refusal", src, got)
		}
	}
}

// Ruby: a magic encoding comment works only on the first line (or the second
// after a shebang), so the probe goes after it; on the second line it is an
// ordinary comment (ruby 3.3: `# encoding: ascii-8bit` then `puts
// __ENCODING__` printed UTF-8 after a comment line, ASCII-8BIT without, run
// 2026-11-26). frozen_string_literal keeps working after a comment line (same
// run), so it needs nothing.
func TestRubyRebuildProbeKeepsAnEncodingCommentFirst(t *testing.T) {
	rb := mustByName(t, "ruby")
	for _, first := range []string{"# encoding: ascii-8bit", "# -*- coding: utf-8 -*-", "# Encoding: UTF-8"} {
		got, err := RebuildProbe(rb, first+"\nA = 1\n")
		if err != nil {
			t.Fatalf("%q: refused: %v", first, err)
		}
		if want := first + "\n# " + rebuildProbeText + "\nA = 1\n"; got != want {
			t.Errorf("%q: probe = %q, want %q", first, got, want)
		}
	}
	got, err := RebuildProbe(rb, "# frozen_string_literal: true\nA = 1\n")
	if err != nil || !strings.HasPrefix(got, "# "+rebuildProbeText+"\n# frozen_string_literal") {
		t.Errorf("probe = %q (err %v), want the probe first and the frozen_string_literal comment after it", got, err)
	}
}

func mustByName(t *testing.T, name string) Plugin {
	t.Helper()
	p, ok := ByName(name)
	if !ok {
		t.Fatalf("no plugin %q", name)
	}
	return p
}
