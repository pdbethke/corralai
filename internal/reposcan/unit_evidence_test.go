// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// unitFakeRunner answers each command by its argv, as the jail would: a
// canned stdout and exit code per command, and a record of what ran. A
// command it has no answer for is a test bug, reported as a run error so
// the pass under test can never read it as evidence.
type unitFakeRunner struct {
	mu    sync.Mutex
	res   map[string]sandbox.EnumerateResult
	errs  map[string]error
	block map[string]bool // wait for the context to end, then answer
	ran   []string
}

func argvKey(cmd []string) string { return strings.Join(cmd, "\x00") }

func (f *unitFakeRunner) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	r, err := f.EnumerateDetailed(ctx, files, cmd)
	return r.Output, err
}

func (f *unitFakeRunner) EnumerateDetailed(ctx context.Context, _ map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	k := argvKey(cmd)
	f.mu.Lock()
	f.ran = append(f.ran, k)
	block := f.block[k]
	res, ok := f.res[k]
	err := f.errs[k]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
	}
	if err != nil {
		return sandbox.EnumerateResult{ExitCode: -1}, err
	}
	if !ok {
		return sandbox.EnumerateResult{ExitCode: -1}, errors.New("unitFakeRunner: no answer for " + strings.Join(cmd, " "))
	}
	return res, nil
}

func (f *unitFakeRunner) didRun(cmd []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.ran {
		if k == argvKey(cmd) {
			return true
		}
	}
	return false
}

// unitFixture is one language's command, listing and per-unit coverage
// commands, built through the same lang functions the pass calls, so a test
// keys its canned answers on exactly the argv the pass will run.
type unitFixture struct {
	t    *testing.T
	p    lang.Plugin
	inv  lang.Invocation
	run  *unitFakeRunner
	list []string
}

func newUnitFixture(t *testing.T, langName string, argv []string, listing string) *unitFixture {
	t.Helper()
	p, ok := lang.ByName(langName)
	if !ok {
		t.Fatalf("no plugin %q", langName)
	}
	inv := lang.Cmd(argv)
	list, ok := lang.ListUnitsCmd(p, inv)
	if !ok {
		t.Fatalf("%v has no unit listing", argv)
	}
	f := &unitFixture{t: t, p: p, inv: inv, list: list, run: &unitFakeRunner{
		res:  map[string]sandbox.EnumerateResult{argvKey(list): {Output: listing, ExitCode: 0}},
		errs: map[string]error{}, block: map[string]bool{},
	}}
	return f
}

// unitCmd is the coverage command the pass runs for unit.
func (f *unitFixture) unitCmd(unit string) []string {
	f.t.Helper()
	cmd, ok := lang.UnitRunCmd(f.p, f.inv, unit)
	if !ok {
		f.t.Fatalf("no one-unit command for %q", unit)
	}
	cov, ok := f.p.(lang.CoverageReporter).CoverageCmd(cmd)
	if !ok {
		f.t.Fatalf("no coverage command for %v", cmd)
	}
	return cov
}

func (f *unitFixture) answer(unit string, res sandbox.EnumerateResult) {
	f.run.res[argvKey(f.unitCmd(unit))] = res
}

func (f *unitFixture) collect(ctx context.Context, modulePath string, workers int) UnitEvidence {
	return CollectUnitEvidence(ctx, f.run, map[string]string{}, f.p, f.inv, modulePath, workers)
}

func unitByName(t *testing.T, ev UnitEvidence, name string) UnitResult {
	t.Helper()
	for _, u := range ev.Units {
		if u.Unit == name {
			return u
		}
	}
	t.Fatalf("no unit %q in %+v", name, ev.Units)
	return UnitResult{}
}

const vitestTwoUnits = "corral-units-cwd /w\n[{\"file\":\"/w/test/alpha.test.ts\"},{\"file\":\"/w/test/beta.test.ts\"}]\n"

func jsReport(lines ...string) string {
	return "# suite-exit 0\ncorral-js-coverage: v2\n" + strings.Join(lines, "\n") + "\n"
}

// fileUnitLanding is the landing resolver for file units, over the fixture's
// repository paths.
func fileUnitLanding(p lang.Plugin, paths ...string) func(string, string) string {
	files := map[string]string{}
	for _, path := range paths {
		files[path] = ""
	}
	return UnitTestFile(p, files)
}

func TestUnitPassBuildsTheIndexFromEachUnit(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts", "0 4 src/y.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})

	ev := f.collect(context.Background(), "/w", 2)
	if !ev.Ran || ev.Note != "" {
		t.Fatalf("ev = %+v, want Ran with no note", ev)
	}
	if len(ev.Units) != 2 {
		t.Fatalf("units = %+v, want 2", ev.Units)
	}
	for _, u := range ev.Units {
		if u.Err != "" {
			t.Errorf("unit %s: Err %q", u.Unit, u.Err)
		}
	}
	if got := unitByName(t, ev, "test/alpha.test.ts").Counts["src/x.ts"]; got != (lang.LineCounts{Executed: 7, Executable: 10}) {
		t.Errorf("alpha counts for src/x.ts = %+v", got)
	}

	idx, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts", "src/x.ts", "src/y.ts"))
	if !ok {
		t.Fatal("IndexFromUnits ok = false")
	}
	if !idx.UnitKind() {
		t.Error("an index built from units is not marked unit-kind")
	}
	n, most, hasStatic, hasStatements, measured := idx.CoverageFor("src/x.ts")
	if n != 2 || most != "test/alpha.test.ts" || hasStatic || !hasStatements || !measured {
		t.Errorf("CoverageFor(src/x.ts) = (%d, %q, static %v, statements %v, measured %v), want (2, alpha, false, true, true)", n, most, hasStatic, hasStatements, measured)
	}
	if units, measured := idx.UnitsFor("src/x.ts"); units != 2 || !measured {
		t.Errorf("UnitsFor(src/x.ts) = (%d, %v), want (2, true)", units, measured)
	}
	// Measured by alpha and never run: a real zero, not an absent file.
	if n, most, _, hasStatements, measured := idx.CoverageFor("src/y.ts"); n != 0 || most != "" || !hasStatements || !measured {
		t.Errorf("CoverageFor(src/y.ts) = (%d, %q, statements %v, measured %v), want a measured zero", n, most, hasStatements, measured)
	}
	if _, _, _, _, measured := idx.CoverageFor("src/z.ts"); measured {
		t.Error("a file no unit reported is measured")
	}
	if !idx.CoveredByAny("src/x.ts", map[string]bool{"test/beta.test.ts": true}) {
		t.Error("CoveredByAny misses the second covering unit")
	}
}

func TestUnitPassDropsAFailedUnitAndKeepsTheRest(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	// A build error: the suite printed its failure, and a report that
	// still parses must not be read from a run that failed.
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("9 10 src/w.ts"), Stderr: "error TS2304: Cannot find name 'q'", ExitCode: 1})

	ev := f.collect(context.Background(), "/w", 1)
	if !ev.Ran {
		t.Fatalf("ev = %+v, want Ran", ev)
	}
	beta := unitByName(t, ev, "test/beta.test.ts")
	if !strings.HasPrefix(beta.Err, "run failed") || !strings.Contains(beta.Err, "TS2304") {
		t.Errorf("beta.Err = %q, want a run failure naming the build error", beta.Err)
	}
	if beta.Counts != nil {
		t.Errorf("a failed unit kept counts %+v", beta.Counts)
	}
	idx, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
	if !ok {
		t.Fatal("IndexFromUnits ok = false with one unit succeeding")
	}
	if n, most, _, _, _ := idx.CoverageFor("src/x.ts"); n != 1 || most != "test/alpha.test.ts" {
		t.Errorf("CoverageFor(src/x.ts) = (%d, %q), want alpha alone", n, most)
	}
	if _, _, _, _, measured := idx.CoverageFor("src/w.ts"); measured {
		t.Error("the failed unit's report reached the index")
	}
}

func TestUnitPassTimeLimitKeepsFinishedUnits(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	// alpha finishes as the deadline passes.
	f.run.block[argvKey(f.unitCmd("test/alpha.test.ts"))] = true

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ev := f.collect(ctx, "/w", 1)
	if !ev.Ran {
		t.Fatalf("ev = %+v, want Ran", ev)
	}
	if got := unitByName(t, ev, "test/beta.test.ts").Err; got != "not run (time limit)" {
		t.Errorf("beta.Err = %q, want not run (time limit)", got)
	}
	if f.run.didRun(f.unitCmd("test/beta.test.ts")) {
		t.Error("beta was started after the deadline")
	}
	if got := unitByName(t, ev, "test/alpha.test.ts").Err; got != "" {
		t.Errorf("alpha.Err = %q, want it kept", got)
	}
	idx, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
	if n, most, _, _, _ := idx.CoverageFor("src/x.ts"); !ok || n != 1 || most != "test/alpha.test.ts" {
		t.Errorf("CoverageFor(src/x.ts) = (%d, %q) ok %v, want alpha's evidence", n, most, ok)
	}
}

func TestUnitPassRunThatTimesOutIsNotEvidence(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	alpha := argvKey(f.unitCmd("test/alpha.test.ts"))
	f.run.block[alpha] = true
	f.run.errs[alpha] = errors.New("signal: killed")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ev := f.collect(ctx, "/w", 1)
	if got := unitByName(t, ev, "test/alpha.test.ts").Err; got != "timed out" {
		t.Errorf("alpha.Err = %q, want timed out", got)
	}
	if _, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts")); ok {
		t.Error("IndexFromUnits ok with no unit finished")
	}
}

func TestUnitPassRefusesATruncatedReport(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts") + sandbox.TruncationMarker + "\n"})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})

	ev := f.collect(context.Background(), "/w", 2)
	alpha := unitByName(t, ev, "test/alpha.test.ts")
	if alpha.Err != "report truncated" || alpha.Counts != nil {
		t.Errorf("alpha = %+v, want Err report truncated and no counts", alpha)
	}
	idx, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
	if !ok {
		t.Fatal("IndexFromUnits ok = false with beta succeeding")
	}
	if _, _, _, _, measured := idx.CoverageFor("src/x.ts"); measured {
		t.Error("a truncated report reached the index")
	}
}

func TestUnitPassUnparseableReportIsNotEvidence(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: "# suite-exit 0\nSegmentation fault\n"})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})

	ev := f.collect(context.Background(), "/w", 1)
	if got := unitByName(t, ev, "test/alpha.test.ts").Err; !strings.HasPrefix(got, "unparseable: ") {
		t.Errorf("alpha.Err = %q, want unparseable", got)
	}
}

func TestUnitPassKeepsWhatAReducerCouldNotCount(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts", "# uncounted src/gen.ts: the source could not be read")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})

	ev := f.collect(context.Background(), "/w", 1)
	got := unitByName(t, ev, "test/alpha.test.ts").Uncounted
	if len(got) != 1 || got[0] != "src/gen.ts: the source could not be read" {
		t.Errorf("alpha.Uncounted = %q", got)
	}
}

func TestUnitPassNoUnitFormIsNotEvidence(t *testing.T) {
	ts, _ := lang.ByName("typescript")
	r := &unitFakeRunner{}
	ev := CollectUnitEvidence(context.Background(), r, nil, ts, lang.Cmd([]string{"make", "test"}), "/w", 2)
	if ev.Ran {
		t.Fatalf("ev = %+v, want not Ran", ev)
	}
	if !strings.Contains(ev.Note, "make test") || !strings.Contains(ev.Note, "typescript") {
		t.Errorf("Note = %q, want the language and the command named", ev.Note)
	}
	if len(r.ran) != 0 {
		t.Errorf("ran %q for a command with no unit form", r.ran)
	}
	if _, ok := IndexFromUnits(ev, fileUnitLanding(ts)); ok {
		t.Error("IndexFromUnits ok for a pass that never ran")
	}
}

func TestUnitPassMarksAUnitThatCannotRunAlone(t *testing.T) {
	// vitest runs every file whose path contains the filter, so alpha's
	// one-unit run would also run xalpha: alpha is listed and never run.
	listing := "corral-units-cwd /w\n[{\"file\":\"/w/test/alpha.test.ts\"},{\"file\":\"/w/test/xalpha.test.ts\"}]\n"
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, listing)
	f.answer("test/xalpha.test.ts", sandbox.EnumerateResult{Output: jsReport("3 10 src/x.ts")})

	ev := f.collect(context.Background(), "/w", 2)
	if !ev.Ran {
		t.Fatalf("ev = %+v", ev)
	}
	if got := unitByName(t, ev, "test/alpha.test.ts").Err; got != "not run: its one-unit form would also run another unit" {
		t.Errorf("alpha.Err = %q", got)
	}
}

func TestUnitPassRefusesNoWorkers(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	ev := f.collect(context.Background(), "/w", 0)
	if ev.Ran || !strings.Contains(ev.Note, "workers") {
		t.Errorf("ev = %+v, want a refusal naming workers", ev)
	}
	if len(f.run.ran) != 0 {
		t.Errorf("ran %q with no workers", f.run.ran)
	}
}

func TestUnitPassListingFailuresAreNotEvidence(t *testing.T) {
	cases := []struct {
		name string
		res  sandbox.EnumerateResult
		err  error
		want string
	}{
		{"run error", sandbox.EnumerateResult{}, errors.New("boom"), "boom"},
		{"exit", sandbox.EnumerateResult{Output: "x", Stderr: "no config", ExitCode: 1}, nil, "exited 1"},
		{"unreadable", sandbox.EnumerateResult{Output: "hello\n"}, nil, "could not read"},
		{"truncated", sandbox.EnumerateResult{Output: vitestTwoUnits + sandbox.TruncationMarker}, nil, "truncated"},
		{"no units", sandbox.EnumerateResult{Output: "corral-units-cwd /w\n[]\n"}, nil, "no test units"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, "")
			f.run.res[argvKey(f.list)] = tc.res
			if tc.err != nil {
				f.run.errs[argvKey(f.list)] = tc.err
			}
			ev := f.collect(context.Background(), "/w", 1)
			if ev.Ran || !strings.Contains(ev.Note, tc.want) {
				t.Errorf("ev = %+v, want not Ran with a note containing %q", ev, tc.want)
			}
		})
	}
}

// A unit that ran and executed nothing it reports (a Rust target with no
// tests) is a successful unit with no evidence, not a failure.
func TestUnitPassAUnitThatExecutedNothingIsAQuietSuccess(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("0 10 src/x.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("4 10 src/x.ts")})
	ev := f.collect(context.Background(), "/w", 1)
	if got := unitByName(t, ev, "test/alpha.test.ts").Err; got != "" {
		t.Errorf("alpha.Err = %q, want a success", got)
	}
	idx, _ := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
	if n, most, _, _, _ := idx.CoverageFor("src/x.ts"); n != 1 || most != "test/beta.test.ts" {
		t.Errorf("CoverageFor = (%d, %q), want beta alone", n, most)
	}
}

const goTwoUnits = "corral-units-cwd /w\nunit m/other\nunit m/pkg\nnone m/cmd\n"

func goFiles() map[string]string {
	return map[string]string{
		"go.mod":              "module m\n\ngo 1.22\n",
		"pkg/a.go":            "// SPDX-License-Identifier: Elastic-2.0\n\npackage pkg\n",
		"pkg/b.go":            "package pkg\n",
		"pkg/b_test.go":       "//go:build !race\n\n/* a block\n comment */\npackage pkg\n",
		"pkg/z_test.go":       "package pkg\n",
		"other/o.go":          "package other\n",
		"other/o_test.go":     "package other\n",
		"sub/go.mod":          "module example.com/sub\n",
		"sub/inner/i.go":      "package inner\n",
		"sub/inner/i_test.go": "package inner\n",
		// mix's own m_test.go and its lexically first test file are
		// external; only z_test.go is in package mix.
		"mix/m.go":        "package mix\n",
		"mix/a_test.go":   "package mix_test\n",
		"mix/m_test.go":   "package mix_test\n",
		"mix/z_test.go":   "package mix\n",
		"ext/e.go":        "package ext\n",
		"ext/e_test.go":   "package ext_test\n",
		"blank/k.go":      "",
		"blank/k_test.go": "package blank\n",
	}
}

func TestGoUnitOutsideThePackageDoesNotLand(t *testing.T) {
	f := newUnitFixture(t, "go", []string{"go", "test", "./..."}, goTwoUnits)
	f.answer("m/other", sandbox.EnumerateResult{Output: "# suite-exit 0\nmode: set\nm/pkg/a.go:0,1 5 3\nm/other/o.go:0,1 4 4\n"})
	f.answer("m/pkg", sandbox.EnumerateResult{Output: "# suite-exit 0\nmode: set\nm/pkg/a.go:0,0 5 0\nm/pkg/b.go:0,1 2 2\n"})

	ev := f.collect(context.Background(), "m", 2)
	if !ev.Ran {
		t.Fatalf("ev = %+v", ev)
	}
	idx, ok := IndexFromUnits(ev, UnitTestFile(f.p, goFiles()))
	if !ok {
		t.Fatal("IndexFromUnits ok = false")
	}
	// m/other executed pkg/a.go, and a test in package other cannot reach
	// pkg's unexported code: no landing, so no promotion — and, since a
	// unit DID execute it, never a measured zero either.
	n, most, _, _, measured := idx.CoverageFor("pkg/a.go")
	if n != 0 || most != "" {
		t.Errorf("CoverageFor(pkg/a.go) = (%d, %q), want no covering unit", n, most)
	}
	if measured {
		t.Error("pkg/a.go reads measured: a file only an unlandable unit executed would be relabelled uncovered")
	}
	if n, most, _, _, _ := idx.CoverageFor("pkg/b.go"); n != 1 || most != "pkg/b_test.go" {
		t.Errorf("CoverageFor(pkg/b.go) = (%d, %q), want pkg's own test file", n, most)
	}

	excl := []Exclusion{{Path: "pkg/a.go", Reason: ReasonNoPairedTest}}
	cands, gotExcl, promoted := WidenCandidacyByEvidence(nil, excl, idx, ok)
	if promoted != 0 || len(cands) != 0 {
		t.Errorf("promoted %d, cands %+v: pkg/a.go must stay unpromoted", promoted, cands)
	}
	if len(gotExcl) != 1 || gotExcl[0] != excl[0] {
		t.Errorf("excl = %+v, want pkg/a.go kept under its original reason", gotExcl)
	}
}

func TestUnitTestFileGo(t *testing.T) {
	goPlug, _ := lang.ByName("go")
	land := UnitTestFile(goPlug, goFiles())
	cases := []struct{ unit, source, want string }{
		{"m/pkg", "pkg/b.go", "pkg/b_test.go"},                             // the source's own test file
		{"m/pkg", "pkg/a.go", "pkg/b_test.go"},                             // else the package's lexically first
		{"m/other", "pkg/a.go", ""},                                        // another package
		{"m/pkg/sub", "pkg/a.go", ""},                                      // a subpackage is another package
		{"example.com/sub/inner", "sub/inner/i.go", "sub/inner/i_test.go"}, // a nested module
		{"m/sub/inner", "sub/inner/i.go", ""},                              // not the nested module's path
		{"m/cmd", "cmd/main.go", ""},                                       // a package with no test file here
		{"m/mix", "mix/m.go", "mix/z_test.go"},                             // external test files never land
		{"m/ext", "ext/e.go", ""},                                          // only external test files: nothing lands
		{"m/blank", "blank/k.go", ""},                                      // no readable package clause
	}
	for _, tc := range cases {
		if got := land(tc.unit, tc.source); got != tc.want {
			t.Errorf("land(%q, %q) = %q, want %q", tc.unit, tc.source, got, tc.want)
		}
	}
	// No go.mod to name the module: nothing lands.
	bare := UnitTestFile(goPlug, map[string]string{"pkg/a.go": "", "pkg/a_test.go": ""})
	if got := bare("m/pkg", "pkg/a.go"); got != "" {
		t.Errorf("with no go.mod, land = %q, want \"\"", got)
	}
}

func rustFiles() map[string]string {
	return map[string]string{
		"Cargo.toml":           "[workspace]\nmembers = [\"a\", \"b\"]\n",
		"a/Cargo.toml":         "[package]\nname = \"units-a\"\nversion = \"0.1.0\"\n",
		"a/src/lib.rs":         "",
		"a/src/util.rs":        "",
		"a/src/main.rs":        "",
		"a/src/bin/tool.rs":    "",
		"a/tests/it.rs":        "",
		"a/tests/deep/main.rs": "",
		"b/Cargo.toml":         "[package]\nname = \"units-b\"\n",
		"b/src/lib.rs":         "",
	}
}

func TestUnitTestFileRust(t *testing.T) {
	rust, _ := lang.ByName("rust")
	land := UnitTestFile(rust, rustFiles())
	cases := []struct{ unit, source, want string }{
		// cargo test units
		{"units-a/lib", "a/src/util.rs", "a/src/util.rs"}, // in-file #[cfg(test)]
		{"units-a/lib", "b/src/lib.rs", ""},               // another package
		{"units-a/lib", "a/src/main.rs", ""},              // a bin root is not the lib
		{"units-a/test:it", "a/src/util.rs", "a/tests/it.rs"},
		{"units-a/test:deep", "a/src/util.rs", "a/tests/deep/main.rs"},
		{"units-a/test:it", "b/src/lib.rs", ""}, // its test cannot grade another crate
		{"units-a/test:gone", "a/src/util.rs", ""},
		{"units-a/bin:units-a", "a/src/main.rs", "a/src/main.rs"},
		{"units-a/bin:tool", "a/src/bin/tool.rs", "a/src/bin/tool.rs"},
		{"units-a/bin:tool", "a/src/util.rs", ""},
		{"units-zz/lib", "a/src/util.rs", ""},
		// nextest binary ids
		{"units-a", "a/src/util.rs", "a/src/util.rs"},
		{"units-a::it", "a/src/util.rs", "a/tests/it.rs"},
		{"units-a::bin/tool", "a/src/bin/tool.rs", "a/src/bin/tool.rs"},
		{"units-a::example/demo", "a/src/util.rs", ""},
	}
	for _, tc := range cases {
		if got := land(tc.unit, tc.source); got != tc.want {
			t.Errorf("land(%q, %q) = %q, want %q", tc.unit, tc.source, got, tc.want)
		}
	}
}

func TestUnitTestFileFileUnits(t *testing.T) {
	for _, name := range []string{"javascript", "typescript", "ruby", "php"} {
		p, _ := lang.ByName(name)
		land := UnitTestFile(p, map[string]string{"t/a_test": "", "src/a": ""})
		if got := land("t/a_test", "src/a"); got != "t/a_test" {
			t.Errorf("%s: land = %q, want the unit itself", name, got)
		}
		if got := land("t/missing", "src/a"); got != "" {
			t.Errorf("%s: a unit not in the repository landed at %q", name, got)
		}
	}
	py, _ := lang.ByName("python")
	if got := UnitTestFile(py, map[string]string{"tests/test_a.py": ""})("tests/test_a.py", "a.py"); got != "" {
		t.Errorf("python has no unit pass, yet landed at %q", got)
	}
}

// A Python index is not unit-kind, and says nothing by unit.
func TestParsedEvidenceIndexIsNotUnitKind(t *testing.T) {
	idx, ok := widenFixtureIndex(t, map[string]lang.FileCoverage{"pkg/a.py": {Tests: map[string]int{"tests/test_a.py::t": 3}}})
	if !ok || idx.UnitKind() {
		t.Errorf("ok %v, UnitKind %v: want a parsed index that is not unit-kind", ok, idx.UnitKind())
	}
	if n, measured := idx.UnitsFor("pkg/a.py"); n != 0 || measured {
		t.Errorf("UnitsFor = (%d, %v) on a per-test index", n, measured)
	}
}

// A partial pass never makes a measured zero: the unit that failed may be
// the one that executes the file.
func TestUnitPassPartialPassClaimsNoZero(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts", "0 4 src/y.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: "", ExitCode: 1})

	ev := f.collect(context.Background(), "/w", 1)
	idx, ok := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
	if !ok {
		t.Fatal("IndexFromUnits ok = false with alpha succeeding")
	}
	if _, _, _, _, measured := idx.CoverageFor("src/y.ts"); measured {
		t.Error("src/y.ts reads as a measured zero from a partial pass")
	}
	if n, _, _, _, measured := idx.CoverageFor("src/x.ts"); n != 1 || !measured {
		t.Errorf("CoverageFor(src/x.ts) = (%d, %v), want alpha's evidence kept", n, measured)
	}
}

// A unit is evidence only when its own suite exited 0, as the report's
// suite-exit trailer says — not when the coverage command did, which ends
// with the reducer's status.
func TestUnitPassTakesOnlyAGreenSuite(t *testing.T) {
	report := "corral-js-coverage: v2\n7 10 src/x.ts\n"
	cases := []struct {
		name, out, wantErr string
	}{
		{"green", "# suite-exit 0\n" + report, ""},
		{"red", "# suite-exit 1\n" + report, "run failed: exit 1"},
		{"no status", report, "no suite exit status in the report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
			f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: tc.out, ExitCode: 0})
			f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})
			ev := f.collect(context.Background(), "/w", 1)
			alpha := unitByName(t, ev, "test/alpha.test.ts")
			if alpha.Err != tc.wantErr {
				t.Errorf("alpha.Err = %q, want %q", alpha.Err, tc.wantErr)
			}
			if (tc.wantErr != "") != (alpha.Counts == nil) {
				t.Errorf("alpha.Counts = %+v with Err %q", alpha.Counts, alpha.Err)
			}
			idx, _ := IndexFromUnits(ev, fileUnitLanding(f.p, "test/alpha.test.ts", "test/beta.test.ts"))
			if _, _, _, _, measured := idx.CoverageFor("src/x.ts"); measured != (tc.wantErr == "") {
				t.Errorf("src/x.ts measured = %v", measured)
			}
		})
	}
}

// A reporter that could not instrument the unit at all says why on a notice
// line — Rust's llvm-tools check runs before the suite, so the report has
// no suite-exit trailer — and that reason is the unit's Err, not "no suite
// exit status in the report", which would hide what to install. The notice
// is read whether or not a trailer precedes it (a later step — the merge,
// the export — can fail after a green suite), and a red suite still reads
// as red, the more fundamental failure.
func TestUnitPassSaysWhyCoverageWasUnavailable(t *testing.T) {
	listing := strings.Replace(unitListingFixture(t, "cargo-units.txt"), "/fixture/units/cargo", "/w", -1)
	const notice = "corral-rust-coverage: unavailable llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools nor PATH\n"
	cases := []struct{ name, out, wantErr string }{
		{"before the suite", notice, "coverage unavailable: llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools nor PATH"},
		{"after a green suite", "# suite-exit 0\n" + notice, "coverage unavailable: llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools nor PATH"},
		{"after a red suite", "# suite-exit 101\n" + notice, "run failed: exit 101"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnitFixture(t, "rust", []string{"cargo", "test", "-p", "units-a"}, listing)
			for _, u := range []string{"units-a/bin:units-a", "units-a/lib", "units-a/test:it"} {
				f.answer(u, sandbox.EnumerateResult{Output: tc.out})
			}
			ev := f.collect(context.Background(), "/w", 1)
			if !ev.Ran {
				t.Fatalf("ev = %+v, want the pass to have run", ev)
			}
			if u := unitByName(t, ev, "units-a/lib"); u.Err != tc.wantErr || u.Counts != nil {
				t.Errorf("units-a/lib = %+v, want Err %q and no counts", u, tc.wantErr)
			}
		})
	}
}

func unitListingFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "lang", "testdata", "runners", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Settled is what a cache may keep: a pass whose every unit's outcome is a
// property of the tree and the command. A unit the deadline or an interrupt
// stopped, or that the runner could not start, says something about THIS
// run only; caching it would freeze the gap into every later scan of the
// same tree.
func TestUnitEvidenceSettled(t *testing.T) {
	ok := UnitResult{Unit: "a", Counts: map[string]lang.LineCounts{}}
	cases := []struct {
		name string
		ev   UnitEvidence
		want bool
	}{
		{"never ran", UnitEvidence{Note: "no unit form"}, false},
		{"all green", UnitEvidence{Ran: true, Units: []UnitResult{ok}}, true},
		{"a red unit", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: "run failed: exit 1"}}}, true},
		{"cannot run alone", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitNotAlone}}}, true},
		{"time limit", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitNotRunTimeLimit}}}, false},
		{"timed out", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitTimedOut}}}, false},
		{"interrupted", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitInterrupted}}}, false},
		{"not run, interrupted", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitNotRunInterrupted}}}, false},
		{"runner error", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitCouldNotRun + "jail: boom"}}}, false},
	}
	for _, tc := range cases {
		if got := tc.ev.Settled(); got != tc.want {
			t.Errorf("%s: Settled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
