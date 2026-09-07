// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"
)

// TestAuthoredTestPathKeepsTheSuffixForShortStems is a gemini reviewer's
// reproduction (`corral review --scope internal/advpool`, 2026-09-07; review
// a79b583bacea#R1, let stand by a Claude Code verifier), inverted. The
// marker used to be inserted by an unanchored first-occurrence replace of
// the code stem, so a stem that is a substring of the test suffix or the
// extension mangled the authored name into one no runner collects. Fails on
// the unfixed authoredTestPath.
func TestAuthoredTestPathKeepsTheSuffixForShortStems(t *testing.T) {
	if got := authoredTestPath("internal/auth/st.go", "internal/auth/login_test.go", nil); !strings.HasSuffix(got, "_test.go") {
		t.Fatalf("authoredTestPath(st.go, login_test.go) = %q — Go never collects it", got)
	}
	if got := authoredTestPath("pkg/go.go", "pkg/foo_test.go", nil); !strings.HasSuffix(got, "_test.go") {
		t.Fatalf("authoredTestPath(go.go, foo_test.go) = %q — no extension, no test", got)
	}
	// The stem as a whole token is still found, wherever it sits.
	for _, c := range []struct{ code, dev, want string }{
		{"pkg/test.go", "pkg/test_test.go", "pkg/test_corral_test.go"},
		{"src/calc.ts", "src/calc.test.ts", "src/calc_corral.test.ts"},
		{"lib/first.rb", "spec/first_spec.rb", "spec/first_corral_spec.rb"},
		{"a/st.py", "tests/test_st.py", "tests/test_st_corral.py"},
	} {
		if got := authoredTestPath(c.code, c.dev, nil); got != c.want {
			t.Errorf("authoredTestPath(%s, %s) = %q, want %q", c.code, c.dev, got, c.want)
		}
	}
}
