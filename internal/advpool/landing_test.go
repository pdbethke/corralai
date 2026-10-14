// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/pairing"
)

// declaredLanding is what a door computes for a Warp-shaped Rust source:
// src/foo.rs declares `#[cfg(test)] #[path = "foo_tests.rs"] mod tests;`.
func declaredLanding(existing string) AuthoredLanding {
	return AuthoredLanding{
		Landing:  pairing.Landing{Path: "src/foo_tests.rs", Shape: pairing.Declared, Label: "path-attr", Into: true, Depth: 1, DeclarationsRead: true},
		Existing: existing,
	}
}

// TestStartRunRefusesAFileWhoseTestWouldOverwriteIt: StartRun refuses on the
// RunSpec's carried Landing before anything is enqueued (the queue is the
// first store a run writes), so no seat is ever claimed:
//   - no landing at all (a caller that computed none) refuses a Rust source
//     — fail closed, since Primary is the source;
//   - an inline landing (the source itself) refuses;
//   - a Declared landing (the separate test file) is admitted;
//   - a Go run on the same driver still starts.
func TestStartRunRefusesAFileWhoseTestWouldOverwriteIt(t *testing.T) {
	q := newTestQueue(t)
	d, err := NewDriver(q, &fakeScorer{}, &fakeValidator{}, decorrelatedAssign(), 0.8)
	if err != nil {
		t.Fatal(err)
	}
	rust := func(l AuthoredLanding) RunSpec {
		rs := testRunSpec()
		rs.CodePath, rs.DevTestPath, rs.Lang, rs.TestCmd, rs.Landing = "src/foo.rs", "src/foo_tests.rs", "rust", "cargo test", l
		return rs
	}
	inline := AuthoredLanding{Landing: pairing.Landing{Path: "src/foo.rs", Shape: pairing.Inline, Label: "inline", Into: true, DeclarationsRead: true}}
	for id, tc := range map[int64]struct {
		l    AuthoredLanding
		says string
	}{
		41: {AuthoredLanding{}, "--repo-dir"},
		43: {inline, "not built yet"},
	} {
		err := d.StartRun(id, rust(tc.l), nil)
		if err == nil || !strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "test landing is not built") {
			t.Fatalf("StartRun(%+v) err = %v, want the landing refusal saying %q", tc.l.Landing, err, tc.says)
		}
		if tasks, lerr := q.List(id); lerr != nil || len(tasks) != 0 {
			t.Fatalf("a refused run enqueued %d task(s) (err %v)", len(tasks), lerr)
		}
	}
	if err := d.StartRun(44, rust(declaredLanding("")), nil); err != nil {
		t.Fatalf("a Declared landing was refused: %v", err)
	}
	if err := d.StartRun(42, testRunSpec(), nil); err != nil {
		t.Fatalf("a Go run was refused: %v", err)
	}
}

// TestTheWritePathIsTheAdmittedLanding: the file the pool writes the
// authored test into is the landing the door admitted — for the validator
// (compile), the scorer (grading, positive control) and the writer's prompt
// alike — never the relocated `_corral` name a path language gets, which no
// `mod` declaration would compile. And it is COMPOSED: the project's own test
// file is kept byte for byte with the landed block appended; the audited
// source is the code handed in, untouched by the landing.
func TestTheWritePathIsTheAdmittedLanding(t *testing.T) {
	const existing = "use super::*;\n\n#[test]\nfn project_own() { assert_eq!(quadruple(1), 4); }\n"
	const code = "fn private_double(x: i32) -> i32 { x * 2 }\n#[cfg(test)]\n#[path = \"foo_tests.rs\"]\nmod tests;\n"
	l := declaredLanding(existing)
	base := map[string]string{"Cargo.toml": "[package]\n", "src/foo.rs": code, "src/foo_tests.rs": existing}

	if got := authoredTestPath(l, "src/foo.rs", "src/foo_tests.rs", base); got != "src/foo_tests.rs" {
		t.Fatalf("authoredTestPath = %q, want the declared test file", got)
	}
	if err := golang.CheckTestLanding("src/foo.rs", l.Landing); err != nil {
		t.Fatalf("the door refuses the landing the write path uses: %v", err)
	}

	v := JailValidator{Jail: &recordingJail{}, BaseFiles: base, DevTestPath: "src/foo_tests.rs", Landing: l}
	test := v.ParseTest("#[test]\nfn kills() { assert_eq!(private_double(2), 4); }")
	if !strings.HasPrefix(test, "#[cfg(test)]\nmod corral_tests {\nuse super::super::*;") {
		t.Fatalf("ParseTest did not land the body for the declared file:\n%s", test)
	}
	jail := v.Jail.(*recordingJail)
	if err := v.CompileTest(context.Background(), "src/foo.rs", code, test); err != nil {
		t.Fatal(err)
	}
	assertComposed(t, "CompileTest", jail.lastFiles, code, existing, test)

	s := JailScorer{Jail: &recordingJail{}, BaseFiles: base, DevTestPath: "src/foo_tests.rs", Landing: l}
	if _, err := s.ScoreAuthoredReport(context.Background(), "src/foo.rs", code, test, nil, "cargo test"); err != nil {
		t.Fatal(err)
	}
	assertComposed(t, "ScoreAuthoredReport", s.Jail.(*recordingJail).lastFiles, code, existing, test)

	// The workspace substrate's BaseFiles is EMPTY: the composition must
	// still keep the project's tests, from the carried Existing.
	ws := JailScorer{BaseFiles: map[string]string{}, DevTestPath: "src/foo_tests.rs", Landing: l}.authoredWorkspace("src/foo.rs", test)
	if !strings.HasPrefix(ws["src/foo_tests.rs"], existing) {
		t.Fatalf("on an empty base the landing dropped the project's tests:\n%s", ws["src/foo_tests.rs"])
	}

	rs := testRunSpec()
	rs.CodePath, rs.DevTestPath, rs.Lang, rs.Code, rs.Landing = "src/foo.rs", "src/foo_tests.rs", "rust", code, l
	prompt := renderTestWriter(rs, nil, nil)
	if !strings.Contains(prompt, `APPENDED to "src/foo_tests.rs"`) || !strings.Contains(prompt, "use super::super::*;") || strings.Contains(prompt, "_corral") {
		t.Fatalf("the writer is not told where its test lands:\n%s", prompt)
	}
}

func assertComposed(t *testing.T, who string, files map[string]string, code, existing, test string) {
	t.Helper()
	if files["src/foo.rs"] != code {
		t.Errorf("%s: the audited source changed:\n%s", who, files["src/foo.rs"])
	}
	got := files["src/foo_tests.rs"]
	if !strings.HasPrefix(got, existing) || !strings.HasSuffix(got, test) {
		t.Errorf("%s: the landing file is not the project's tests + the landed block:\n%s", who, got)
	}
	for k := range files {
		if strings.Contains(k, "_corral") {
			t.Errorf("%s: an authored test was also written to %s", who, k)
		}
	}
}

var _ adequacy.Jail = (*recordingJail)(nil)

// TestBothVerdictPathsCarryTheLanding: the file an authored block must be
// appended to rides from RunSpec.Landing onto the signed Verdict through BOTH
// construction sites — a converged run (tickAggregate) and a timed-out one
// (timeoutVerdict) — and a path landing carries nothing.
func TestBothVerdictPathsCarryTheLanding(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.Landing = declaredLanding("use super::*;\n")
	if v := runToVerdict(t, rs); v.AuthoredLanding != "src/foo_tests.rs" {
		t.Errorf("converged verdict AuthoredLanding = %q, want src/foo_tests.rs", v.AuthoredLanding)
	}
	d := &Driver{}
	if v := d.timeoutVerdict(&runState{rs: rs}); v.AuthoredLanding != "src/foo_tests.rs" {
		t.Errorf("timed-out verdict AuthoredLanding = %q, want src/foo_tests.rs", v.AuthoredLanding)
	}
	if v := d.timeoutVerdict(&runState{rs: newTestRunSpec(t)}); v.AuthoredLanding != "" {
		t.Errorf("a path landing's verdict claims an append target: %q", v.AuthoredLanding)
	}
}

// TestBothVerdictPathsCarryTheBaselineFlakyTests: the tests the dev pass's
// baseline passed only on a retry ride the converged verdict and the
// timed-out one, so --local can name them as --repo does.
func TestBothVerdictPathsCarryTheBaselineFlakyTests(t *testing.T) {
	rs := newTestRunSpec(t)
	scorer := &writerShadowScorer{devFlaky: []string{"crate tests::flaky_one"}}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, scorer, &fakeValidator{mutants: writerShadowMutants()})
	if v := driveWriterShadow(t, d, missionID); len(v.BaselineFlaky) != 1 || v.BaselineFlaky[0] != "crate tests::flaky_one" {
		t.Errorf("converged verdict BaselineFlaky = %q", v.BaselineFlaky)
	}
	if v := (&Driver{}).timeoutVerdict(&runState{rs: rs, baselineFlaky: []string{"crate tests::flaky_one"}}); len(v.BaselineFlaky) != 1 {
		t.Errorf("timed-out verdict BaselineFlaky = %q", v.BaselineFlaky)
	}
}
