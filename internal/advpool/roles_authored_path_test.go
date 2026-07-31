// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"
)

// TestRenderTestWriter_TellsTheWriterWhereItsFileActuallyLands is the
// regression test for a defect shipped in e83ea8d: that commit relocated the
// authored test into the DEV TEST's directory (so the project's own runner
// actually collects it), but left the test-writer prompt asserting "your test
// file will be placed in the SAME directory as it" — beside the code file.
//
// For Go that stayed true by accident (dev tests are siblings by convention,
// so only the filename changed). For Python it was merely self-contradictory,
// since ImportNote supplies a correct dotted import that resolves from
// anywhere. For Ruby and JS/TS — whose plugins return an EMPTY ImportNote, so
// the prompt tells the model to reference the code "using that exact file
// name" — it was an outright regression: a `require_relative 'pricing'` that
// was correct beside app/pricing.rb is wrong from test/.
//
// The fix states the truth the writer needs, computed rather than assumed:
// where its file lands, and how to reach the code FROM there.
func TestRenderTestWriter_TellsTheWriterWhereItsFileActuallyLands(t *testing.T) {
	cases := []struct {
		name        string
		lang        string
		codePath    string
		devTestPath string
		wantSubstr  []string
		wantAbsent  []string
	}{{
		name:        "ruby — code and dev test in different trees",
		lang:        "ruby",
		codePath:    "app/pricing.rb",
		devTestPath: "test/pricing_test.rb",
		// It must name the real destination and the relative hop back to the
		// code, and must NOT claim same-directory placement.
		wantSubstr: []string{"test/pricing_corral_test.rb", "../app/pricing.rb"},
		wantAbsent: []string{"SAME directory"},
	}, {
		name:        "javascript — dev test under __tests__",
		lang:        "javascript",
		codePath:    "src/foo.js",
		devTestPath: "src/__tests__/foo.test.js",
		wantSubstr:  []string{"src/__tests__/foo_corral.test.js", "../foo.js"},
		wantAbsent:  []string{"SAME directory"},
	}, {
		name:        "go — sibling dev test, so the code IS in the same directory",
		lang:        "go",
		codePath:    "internal/auth/login.go",
		devTestPath: "internal/auth/login_test.go",
		// Same directory here is TRUE, and the relative reference is bare.
		wantSubstr: []string{"internal/auth/login_corral_test.go", "login.go"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := RunSpec{
				Lang:        c.lang,
				Goal:        "the goal",
				CodePath:    c.codePath,
				Code:        "CODE",
				DevTestPath: c.devTestPath,
			}
			got := renderTestWriter(rs, nil, nil)
			for _, want := range c.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("prompt must state %q; it does not.\n--- prompt ---\n%s", want, got)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("prompt must NOT claim %q — the authored test does not land beside the code here.\n--- prompt ---\n%s", absent, got)
				}
			}
		})
	}
}

// TestRenderTestWriter_PathFactMatchesTheRealOverlay is the invariant that
// actually matters: whatever the prompt TELLS the writer must be the path the
// scorer and validator actually overlay the test at. Two independent
// computations of the same path is precisely how this defect arose in the
// first place — the placement moved and the prompt did not.
func TestRenderTestWriter_PathFactMatchesTheRealOverlay(t *testing.T) {
	for _, c := range []struct{ lang, code, devTest string }{
		{"ruby", "app/pricing.rb", "test/pricing_test.rb"},
		{"javascript", "src/foo.js", "src/__tests__/foo.test.js"},
		{"go", "internal/auth/login.go", "internal/auth/login_test.go"},
		{"python", "src/flask/cli.py", "tests/test_cli.py"},
	} {
		rs := RunSpec{Lang: c.lang, Goal: "g", CodePath: c.code, Code: "CODE", DevTestPath: c.devTest}
		real := authoredTestPath(c.code, c.devTest, nil)
		if got := renderTestWriter(rs, nil, nil); !strings.Contains(got, real) {
			t.Errorf("%s: prompt does not name the real overlay path %q — the writer is being told about a file that will not exist", c.lang, real)
		}
	}
}

// TestRenderTestWriter_NoDevTestPathKeepsTheOldFact pins that a run with no
// dev test path (single-file mode, and the brain/MCP path which never sets
// one) still describes same-directory placement — because authoredTestPath
// falls back to the sibling convention there, so it remains true.
func TestRenderTestWriter_NoDevTestPathKeepsTheOldFact(t *testing.T) {
	rs := RunSpec{Lang: "ruby", Goal: "g", CodePath: "app/pricing.rb", Code: "CODE"}
	got := renderTestWriter(rs, nil, nil)
	if !strings.Contains(got, "same directory") && !strings.Contains(got, "SAME directory") {
		t.Fatalf("with no dev test path the authored test IS a sibling; the prompt should still say so.\n--- prompt ---\n%s", got)
	}
}
