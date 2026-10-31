// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
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
	// announced is every call the pass made to its announce callback, with
	// how many commands had run by then.
	announced []unitAnnounce
}

type unitAnnounce struct{ runnable, listed, ranBefore int }

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

// unitCmd is the coverage command the pass runs for unit: the one-unit
// command with the runner's evidence-only flags (lang.EvidenceUnitCmd),
// under coverage.
func (f *unitFixture) unitCmd(unit string) []string {
	f.t.Helper()
	cmd, ok := lang.UnitRunCmd(f.p, f.inv, unit)
	if !ok {
		f.t.Fatalf("no one-unit command for %q", unit)
	}
	cov, ok := f.p.(lang.CoverageReporter).CoverageCmd(lang.EvidenceUnitCmd(f.p, f.inv, cmd))
	if !ok {
		f.t.Fatalf("no coverage command for %v", cmd)
	}
	return cov
}

func (f *unitFixture) answer(unit string, res sandbox.EnumerateResult) {
	f.run.res[argvKey(f.unitCmd(unit))] = res
}

func (f *unitFixture) collect(ctx context.Context, modulePath string, workers int) UnitEvidence {
	return CollectUnitEvidence(ctx, f.run, map[string]string{}, f.p, f.inv, modulePath, workers, func(runnable, listed int) {
		f.run.mu.Lock()
		ran := len(f.run.ran)
		f.run.mu.Unlock()
		f.announced = append(f.announced, unitAnnounce{runnable, listed, ran})
	})
}

// The pass announces itself once, after listing and before the first unit
// runs, with how many units will run of how many were listed: a pass can
// last the whole selection budget, and the operator is told before it does.
func TestUnitPassAnnouncesBeforeTheFirstUnitRuns(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})

	f.collect(context.Background(), "/w", 1)
	want := []unitAnnounce{{runnable: 2, listed: 2, ranBefore: 1}}
	if !reflect.DeepEqual(f.announced, want) {
		t.Errorf("announced %+v, want %+v (once, after only the listing ran)", f.announced, want)
	}
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

// fileUnitLanding is the UnitFiles for file units, over the fixture's
// repository paths.
func fileUnitLanding(p lang.Plugin, paths ...string) UnitFiles {
	files := map[string]string{}
	for _, path := range paths {
		files[path] = ""
	}
	return UnitFilesFor(p, files)
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
	// The record keeps the class and exit code; the build error is the
	// operator's, on the terminal (TerminalDetail).
	if beta.Err != "run failed: exited 1" || !strings.Contains(beta.TerminalDetail, "TS2304") {
		t.Errorf("beta = (Err %q, TerminalDetail %q), want a run failure with the build error in its TerminalDetail", beta.Err, beta.TerminalDetail)
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
	announced := false
	ev := CollectUnitEvidence(context.Background(), r, nil, ts, lang.Cmd([]string{"make", "test"}), "/w", 2, func(int, int) { announced = true })
	if announced {
		t.Error("a pass that runs no unit announced itself")
	}
	if ev.Ran {
		t.Fatalf("ev = %+v, want not Ran", ev)
	}
	// The terminal names the command; the recorded Note (every file's
	// published Fallback) names the language and never the command.
	if !strings.Contains(ev.Said(), "make test") || !strings.Contains(ev.Note, "typescript") || strings.Contains(ev.Note, "make test") {
		t.Errorf("Note = %q, Said = %q, want the language in both and the command only in Said", ev.Note, ev.Said())
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
		{"run error", sandbox.EnumerateResult{}, errors.New("boom"), "the jail could not start the listing"},
		{"exit", sandbox.EnumerateResult{Output: "x", Stderr: "no config", ExitCode: 1}, nil, "exited 1"},
		{"unreadable", sandbox.EnumerateResult{Output: "hello\n"}, nil, "could not be read"},
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
			// What the listing printed or the runner said is the
			// terminal's (Said), never the recorded Note.
			for _, leak := range []string{"boom", "no config", "hello", "vitest run"} {
				if strings.Contains(ev.Note, leak) {
					t.Errorf("Note %q quotes %q", ev.Note, leak)
				}
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
	idx, ok := IndexFromUnits(ev, UnitFilesFor(f.p, goFiles()))
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
	// The instrument's notice is the unit's TerminalDetail, for the terminal; the
	// recorded Err keeps the class.
	const why = "llvm-profdata and llvm-cov are in neither the toolchain's llvm-tools nor PATH"
	cases := []struct{ name, out, wantErr, wantDetail string }{
		{"before the suite", notice, "coverage unavailable: the coverage instrument reported it unavailable", why},
		{"after a green suite", "# suite-exit 0\n" + notice, "coverage unavailable: the coverage instrument reported it unavailable", why},
		{"after a red suite", "# suite-exit 101\n" + notice, "run failed: exit 101", ""},
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
			if u := unitByName(t, ev, "units-a/lib"); u.Err != tc.wantErr || (tc.wantDetail != "" && u.TerminalDetail != tc.wantDetail) || u.Counts != nil {
				t.Errorf("units-a/lib = %+v, want Err %q, TerminalDetail %q and no counts", u, tc.wantErr, tc.wantDetail)
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

// Settled is what a cache may keep (ruling R15): a pass in which EVERY unit
// succeeded. Any failure may be the environment's rather than the tree's —
// a deadline, an interrupt, a runner that could not start, but also a
// coverage instrument the jail lacks or a unit red for a missing toolchain —
// and the cache is keyed on the tree, so a cached failure would outlive the
// operator's fix.
func TestUnitEvidenceSettled(t *testing.T) {
	ok := UnitResult{Unit: "a", Counts: map[string]lang.LineCounts{}}
	cases := []struct {
		name string
		ev   UnitEvidence
		want bool
	}{
		{"never ran", UnitEvidence{Note: "no unit form"}, false},
		{"all green", UnitEvidence{Ran: true, Units: []UnitResult{ok}}, true},
		{"a red unit", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: "run failed: exit 1"}}}, false},
		{"cannot run alone", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitNotAlone}}}, false},
		{"coverage unavailable", UnitEvidence{Ran: true, Units: []UnitResult{ok, {Unit: "b", Err: unitCoverageUnavailable + "llvm-tools missing"}}}, false},
		{"all failed", UnitEvidence{Ran: true, Units: []UnitResult{{Unit: "a", Err: "run failed: exit 1"}, {Unit: "b", Err: "run failed: exit 2"}}}, false},
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

// A failed unit says what it printed, the way every other run failure here
// does (failureTail): "run failed: exit 1" alone left an operator unable to
// tell a jail-only failure from a real red test (commander.js, Task 11a).
// The tail is the output BEFORE the suite-exit trailer: in the jail the
// streams are one, and what follows the trailer is the coverage report, not
// the failure. It is said in the unit's TerminalDetail, for the terminal, and never
// in its Err, which the verdict carries into the published ledger: a tail
// can quote a secret or a host path.
func TestUnitPassShowsAFailedUnitsOutput(t *testing.T) {
	cases := []struct {
		name                string
		res                 sandbox.EnumerateResult
		wantErr, wantDetail string
	}{
		{"red suite, one stream", sandbox.EnumerateResult{Output: "AssertionError: expected 1\n    at alpha.test.ts:3\n# suite-exit 1\ncorral-js-coverage: v2\n7 10 src/x.ts\n"},
			"run failed: exit 1", "the evidence pass runs each unit with --pool=threads: AssertionError: expected 1\n    at alpha.test.ts:3"},
		{"red suite, stderr apart", sandbox.EnumerateResult{Output: "# suite-exit 1\ncorral-js-coverage: v2\n7 10 src/x.ts\n", Stderr: "TypeError: boom\n"},
			"run failed: exit 1", "the evidence pass runs each unit with --pool=threads: TypeError: boom"},
		{"command failed", sandbox.EnumerateResult{Output: "sh: 1: go: not found\n# suite-exit 127\n", ExitCode: 127},
			"run failed: exited 127", "the evidence pass runs each unit with --pool=threads: sh: 1: go: not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
			f.answer("test/alpha.test.ts", tc.res)
			f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})
			ev := f.collect(context.Background(), "/w", 1)
			alpha := unitByName(t, ev, "test/alpha.test.ts")
			if alpha.Err != tc.wantErr || alpha.TerminalDetail != tc.wantDetail {
				t.Errorf("alpha = (Err %q, TerminalDetail %q), want (%q, %q)", alpha.Err, alpha.TerminalDetail, tc.wantErr, tc.wantDetail)
			}
		})
	}
}

// A report that counted nothing because every file it measured was
// uncountable names how many in its reason and keeps the list in Uncounted,
// never in the reason: REA's 25 units each carried ~390 paths in their Err,
// and the scan output was 1.4 MB (Task 11a, ruling R22).
func TestUnitPassKeepsUncountedListsOutOfTheReason(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("# uncounted src/a.ts: transformed code", "# uncounted src/b.ts: transformed code")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})

	alpha := unitByName(t, f.collect(context.Background(), "/w", 1), "test/alpha.test.ts")
	if !strings.HasPrefix(alpha.Err, "unparseable: ") || !strings.Contains(alpha.Err, "the reducer disclosed 2 file(s) it could not count") {
		t.Errorf("alpha.Err = %q: want the unparseable reason with the count", alpha.Err)
	}
	if strings.Contains(alpha.Err, "src/a.ts") {
		t.Errorf("alpha.Err carries the uncounted list: %q", alpha.Err)
	}
	if want := []string{"src/a.ts: transformed code", "src/b.ts: transformed code"}; !reflect.DeepEqual(alpha.Uncounted, want) {
		t.Errorf("alpha.Uncounted = %q, want %q", alpha.Uncounted, want)
	}
}

// A vitest unit whose coverage names no project file — no entry and nothing
// disclosed — is what vitest's default forks pool gives (measured on vitest
// 3.2.7: its worker writes no V8 coverage), and its reason says so and what
// to do, instead of "unparseable" (review I4). The reason is the vitest
// row's fact; jest's identical report keeps the general reason.
func TestUnitPassNamesVitestsForksPoolWhenNoWorkerCoverage(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport()})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})
	vt, _ := lang.ByName("typescript")
	want := lang.UnitNoCoverageReason(vt, lang.Cmd([]string{"vitest", "run"}))
	if want == "" || !strings.Contains(want, "forks pool") || !strings.Contains(want, "--pool=threads") {
		t.Fatalf("vitest's no-coverage reason = %q, want it to name the forks pool and --pool=threads", want)
	}
	if got := unitByName(t, f.collect(context.Background(), "/w", 1), "test/alpha.test.ts").Err; got != want {
		t.Errorf("alpha.Err = %q, want %q", got, want)
	}

	j := newUnitFixture(t, "typescript", []string{"jest"}, "corral-units-cwd /w\n[\"/w/test/alpha.test.ts\",\"/w/test/beta.test.ts\"]\n")
	j.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport()})
	j.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/y.ts")})
	if got := unitByName(t, j.collect(context.Background(), "/w", 1), "test/alpha.test.ts").Err; !strings.HasPrefix(got, "unparseable: ") {
		t.Errorf("jest alpha.Err = %q, want the general unparseable reason", got)
	}
}

// A GO UNIT COMPILES EVERY TEST FILE OF ITS PACKAGE, and the index records
// all of them as covering what it ran (final review I2). The landing stays
// one internal test file — that is where an authored test goes — but
// CoveredByAny answers --diff-base's question, "does this changed test
// defend this source?", and a change to b_test.go or to the external
// x_test.go changes the unit that ran c.go exactly as a change to a_test.go
// does. Recording only the landing made a PR that weakened b_test.go leave
// an evidence-only c.go out of scope: the gate failed open.
func TestUnitIndexRecordsEveryTestFileAGoUnitCompiled(t *testing.T) {
	goPlug, _ := lang.ByName("go")
	files := map[string]string{
		"go.mod":          "module m\n",
		"pkg/c.go":        "package pkg\n",
		"pkg/a_test.go":   "package pkg\n",
		"pkg/b_test.go":   "package pkg\n",
		"pkg/x_test.go":   "package pkg_test\n",
		"other/o.go":      "package other\n",
		"other/o_test.go": "package other\n",
	}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		{Unit: "m/pkg", Counts: map[string]lang.LineCounts{"pkg/c.go": {Executed: 2, Executable: 4}}},
	}}
	idx, ok := IndexFromUnits(ev, UnitFilesFor(goPlug, files))
	if !ok {
		t.Fatal("IndexFromUnits ok = false")
	}
	if n, most, _, _, _ := idx.CoverageFor("pkg/c.go"); n != 1 || most != "pkg/a_test.go" {
		t.Errorf("CoverageFor(pkg/c.go) = (%d, %q), want one unit landing on pkg/a_test.go (unchanged)", n, most)
	}
	for _, tf := range []string{"pkg/a_test.go", "pkg/b_test.go", "pkg/x_test.go"} {
		if !idx.CoveredByAny("pkg/c.go", map[string]bool{tf: true}) {
			t.Errorf("CoveredByAny(pkg/c.go, {%s}) = false: the unit that ran c.go compiled %s", tf, tf)
		}
	}
	if idx.CoveredByAny("pkg/c.go", map[string]bool{"other/o_test.go": true}) {
		t.Error("CoveredByAny(pkg/c.go, {other/o_test.go}) = true: another package's test was never compiled into the unit")
	}
}

func TestUnitTestFilesNamesWhatEachUnitCompiles(t *testing.T) {
	goPlug, _ := lang.ByName("go")
	ts, _ := lang.ByName("typescript")
	goTests := UnitFilesFor(goPlug, goFiles()).TestFiles
	if got, want := goTests("m/mix"), []string{"mix/a_test.go", "mix/m_test.go", "mix/z_test.go"}; !slices.Equal(got, want) {
		t.Errorf("Go m/mix test files = %q, want %q (internal and external alike)", got, want)
	}
	if got := goTests("m/nope"); len(got) != 0 {
		t.Errorf("Go unknown package test files = %q, want none", got)
	}
	fileTests := UnitFilesFor(ts, map[string]string{"test/a.test.ts": ""}).TestFiles
	if got := fileTests("test/a.test.ts"); !slices.Equal(got, []string{"test/a.test.ts"}) {
		t.Errorf("file unit test files = %q, want the unit itself", got)
	}
	if got := fileTests("test/missing.test.ts"); len(got) != 0 {
		t.Errorf("a unit the repository lacks names %q, want none", got)
	}
}

// CutShort counts the units the pass's own deadline cut: those never started
// (unitNotRunTimeLimit) and those stopped mid-run by it (unitTimedOut). The
// scan line and corral tests map name the bound and the flag that raises it
// from this number, so every other way a unit contributes nothing — a red
// run, an interrupt, a runner that could not start — must not count.
func TestUnitEvidenceCutShortCountsOnlyTheDeadline(t *testing.T) {
	ok := UnitResult{Unit: "a", Counts: map[string]lang.LineCounts{}}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		ok,
		{Unit: "b", Err: unitNotRunTimeLimit},
		{Unit: "c", Err: unitNotRunTimeLimit},
		{Unit: "d", Err: unitTimedOut},
		{Unit: "e", Err: unitInterrupted},
		{Unit: "f", Err: unitNotRunInterrupted},
		{Unit: "g", Err: "run failed: exit 1"},
		{Unit: "h", Err: unitCouldNotRun + "jail: boom"},
	}}
	if got := ev.CutShort(); got != 3 {
		t.Errorf("CutShort() = %d, want 3 (two not started, one stopped by the deadline)", got)
	}
	if got := (UnitEvidence{Ran: true, Units: []UnitResult{ok}}).CutShort(); got != 0 {
		t.Errorf("a pass the deadline never reached: CutShort() = %d, want 0", got)
	}
}

// The evidence pass, and only the evidence pass, runs a vitest unit with
// --pool=threads (lang.EvidenceUnitCmd): every unit command the jail
// receives carries it, and the listing, which collects and runs nothing,
// does not.
func TestCollectUnitEvidenceUsesEvidenceArgs(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})

	ev := f.collect(context.Background(), "/w", 2)
	if !ev.Settled() {
		t.Fatalf("ev = %+v, want both units green", ev)
	}
	units := 0
	for _, k := range f.run.ran {
		cmd := strings.Join(strings.Split(k, "\x00"), " ")
		if k == argvKey(f.list) {
			if strings.Contains(cmd, "--pool") {
				t.Errorf("the listing ran with a pool flag: %q", cmd)
			}
			continue
		}
		units++
		if !strings.Contains(cmd, "--pool=threads") {
			t.Errorf("a unit ran without --pool=threads: %q", cmd)
		}
	}
	if units != 2 {
		t.Errorf("%d unit commands ran, want 2: %q", units, f.run.ran)
	}
	// Every unit green, and the pass still says, once, that it ran them in
	// a pool the operator's command did not name.
	if want := "the evidence pass runs each unit with --pool=threads"; ev.Changed != want {
		t.Errorf("ev.Changed = %q, want %q", ev.Changed, want)
	}
	if ev.Note != "" {
		t.Errorf("ev.Note = %q on a pass that ran: Note says why a pass did not", ev.Note)
	}

	// The operator's own threads pool: nothing changed, nothing said.
	same := newUnitFixture(t, "typescript", []string{"vitest", "run", "--pool=threads"}, vitestTwoUnits)
	same.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	same.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	if got := same.collect(context.Background(), "/w", 1).Changed; got != "" {
		t.Errorf("Changed for a threads command = %q, want \"\"", got)
	}
}

// A unit that is red under --pool=threads contributes nothing, and its
// TerminalDetail says the evidence pass ran it with that flag (the scan line says
// so too, once, from Changed): the operator's own
// run may be green in the forks pool, and a red unit is unreported (every
// file then selects it) rather than graded on partial evidence.
func TestThreadsRedUnitIsUnreportedNamingThePool(t *testing.T) {
	for _, c := range []struct {
		name string
		res  sandbox.EnumerateResult
	}{
		{"command exit", sandbox.EnumerateResult{Output: jsReport("9 10 src/w.ts"), Stderr: "Error: worker_threads unsupported", ExitCode: 1}},
		{"suite exit", sandbox.EnumerateResult{Output: "FAIL tests\n# suite-exit 1\ncorral-js-coverage: v2\n9 10 src/w.ts\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
			f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
			f.answer("test/beta.test.ts", c.res)
			ev := f.collect(context.Background(), "/w", 1)
			beta := unitByName(t, ev, "test/beta.test.ts")
			if !strings.HasPrefix(beta.Err, "run failed:") || !strings.Contains(beta.TerminalDetail, "--pool=threads") {
				t.Errorf("beta = (Err %q, TerminalDetail %q), want a run failure whose TerminalDetail names --pool=threads", beta.Err, beta.TerminalDetail)
			}
			if beta.Counts != nil {
				t.Errorf("a red unit kept counts %+v", beta.Counts)
			}
			if ev.Settled() {
				t.Error("a pass with a red unit is Settled")
			}
		})
	}
	// jest has no evidence flags, so its failure names none.
	j := newUnitFixture(t, "typescript", []string{"jest"}, "corral-units-cwd /w\n[\"/w/test/alpha.test.ts\",\"/w/test/beta.test.ts\"]\n")
	j.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	j.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport(), ExitCode: 1})
	if got := unitByName(t, j.collect(context.Background(), "/w", 1), "test/beta.test.ts").Err; strings.Contains(got, "evidence pass") {
		t.Errorf("jest beta.Err = %q, want no evidence note", got)
	}
}

// seedingFake is a runner that offers Seed: the units run through the
// seeded runner it returns, and the counts say how often each was used.
// With shareCache, the seed shares the cache it is asked for; unshared is
// what it says when it does not.
type seedingFake struct {
	*unitFakeRunner
	seedErr    error
	seeds      int
	closes     int
	seededRuns int
	// ranAtClose is how many commands had run when Close was called.
	ranAtClose int
	shareCache bool
	unshared   string
	// cacheAsked is the cache Seed was asked to share; calls is every
	// seeded run in order, "warm " or "frozen " before its command key.
	cacheAsked *lang.EvidenceCache
	calls      []string
}

func (s *seedingFake) Seed(files map[string]string, cache *lang.EvidenceCache) (sandbox.SeededRunner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seeds++
	s.cacheAsked = cache
	if s.seedErr != nil {
		return nil, s.seedErr
	}
	return seededFake{s}, nil
}

type seededFake struct{ p *seedingFake }

func (f seededFake) Enumerate(ctx context.Context, files map[string]string, cmd []string) (string, error) {
	r, err := f.EnumerateDetailed(ctx, files, cmd)
	return r.Output, err
}

func (f seededFake) EnumerateDetailed(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	f.p.mu.Lock()
	f.p.seededRuns++
	f.p.calls = append(f.p.calls, "frozen "+argvKey(cmd))
	f.p.mu.Unlock()
	return f.p.unitFakeRunner.EnumerateDetailed(ctx, files, cmd)
}

func (f seededFake) Warm(ctx context.Context, files map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	f.p.mu.Lock()
	f.p.seededRuns++
	f.p.calls = append(f.p.calls, "warm "+argvKey(cmd))
	f.p.mu.Unlock()
	return f.p.unitFakeRunner.EnumerateDetailed(ctx, files, cmd)
}

func (f seededFake) Cache() sandbox.SharedCache {
	switch {
	case f.p.cacheAsked == nil:
		return sandbox.SharedCache{}
	case f.p.shareCache:
		return sandbox.SharedCache{Target: sandbox.CacheTarget}
	}
	return sandbox.SharedCache{Unshared: f.p.unshared}
}

func (f seededFake) Close() error {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	f.p.closes++
	f.p.ranAtClose = len(f.p.ran)
	return nil
}

// A go pass shares one GOCACHE among its units: the first runnable unit
// runs ONCE, alone, with the cache writable, and that run is its evidence;
// every later unit runs with it frozen. Each unit command carries -count=1
// (lang.EvidenceUnitCmd), so the frozen cache's test results are never
// replayed as evidence, and the pass says so once. A seed that cannot
// share the cache runs every unit as before, and the pass says that too.
func TestCollectUnitEvidenceWarmsOnceThenFreezes(t *testing.T) {
	collect := func(f *unitFixture, run *seedingFake) UnitEvidence {
		return CollectUnitEvidence(context.Background(), run, map[string]string{}, f.p, f.inv, "m", 2, func(int, int) {})
	}
	answer := func(f *unitFixture) {
		f.answer("m/other", sandbox.EnumerateResult{Output: "# suite-exit 0\nmode: set\nm/other/o.go:0,1 4 4\n"})
		f.answer("m/pkg", sandbox.EnumerateResult{Output: "# suite-exit 0\nmode: set\nm/pkg/b.go:0,1 2 2\n"})
	}
	f := newUnitFixture(t, "go", []string{"go", "test", "./..."}, goTwoUnits)
	answer(f)
	run := &seedingFake{unitFakeRunner: f.run, shareCache: true}
	ev := collect(f, run)
	if !ev.Settled() {
		t.Fatalf("ev = %+v, want both units green", ev)
	}
	if run.cacheAsked == nil || run.cacheAsked.Env != "GOCACHE" {
		t.Errorf("Seed was asked to share %+v, want GOCACHE", run.cacheAsked)
	}
	for _, u := range []string{"m/other", "m/pkg"} {
		if !strings.Contains(strings.Join(f.unitCmd(u), " "), "'-count=1'") {
			t.Errorf("unit %s runs %q, with no -count=1", u, f.unitCmd(u))
		}
	}
	want := []string{"warm " + argvKey(f.unitCmd("m/other")), "frozen " + argvKey(f.unitCmd("m/pkg"))}
	if !slices.Equal(run.calls, want) {
		t.Errorf("seeded runs = %q, want the first unit warm and the rest frozen: %q", run.calls, want)
	}
	if n := slices.Index(f.run.ran, argvKey(f.unitCmd("m/other"))); n < 0 || slices.Index(f.run.ran[n+1:], argvKey(f.unitCmd("m/other"))) >= 0 {
		t.Errorf("the first unit ran %v, want exactly once", f.run.ran)
	}
	if other := unitByName(t, ev, "m/other"); other.Err != "" || other.Counts["other/o.go"].Executed == 0 {
		t.Errorf("m/other = %+v, want its warm run's counts as its evidence", other)
	}
	if ev.Changed != "the evidence pass runs each unit with -count=1" {
		t.Errorf("Changed = %q, want only the -count=1 note", ev.Changed)
	}

	// The seed could not share the cache: no warm run, every unit as
	// before, and the pass says so once.
	nf := newUnitFixture(t, "go", []string{"go", "test", "./..."}, goTwoUnits)
	answer(nf)
	norun := &seedingFake{unitFakeRunner: nf.run, unshared: "this bwrap cannot overlay a directory on the filesystem of /tmp"}
	ev = collect(nf, norun)
	if !ev.Settled() {
		t.Fatalf("unshared: ev = %+v, want both units green", ev)
	}
	for _, c := range norun.calls {
		if strings.HasPrefix(c, "warm ") {
			t.Errorf("unshared: a warm run: %q", norun.calls)
		}
	}
	if len(norun.calls) != 2 {
		t.Errorf("unshared: seeded runs = %q, want the 2 units", norun.calls)
	}
	wantChanged := "the evidence pass runs each unit with -count=1; each unit started from an empty GOCACHE (no shared cache: this bwrap cannot overlay a directory on the filesystem of /tmp)"
	if ev.Changed != wantChanged {
		t.Errorf("unshared: Changed = %q, want %q", ev.Changed, wantChanged)
	}

	// A seed refused outright (no overlay at all) shares no cache either,
	// and the pass says that too, beside the fresh-copy sentence.
	rf := newUnitFixture(t, "go", []string{"go", "test", "./..."}, goTwoUnits)
	answer(rf)
	refused := &seedingFake{unitFakeRunner: rf.run, seedErr: &sandbox.NoOverlayError{Backend: "bwrap"}}
	ev = collect(rf, refused)
	wantChanged = "the evidence pass runs each unit with -count=1; units ran on a fresh copy each (this bwrap cannot overlay); each unit started from an empty GOCACHE (no shared cache: this bwrap cannot overlay)"
	if !ev.Settled() || ev.Changed != wantChanged {
		t.Errorf("seed refused: ev.Changed = %q, want %q", ev.Changed, wantChanged)
	}

	// A runner with no shared cache asks for none and never warms.
	vf := newUnitFixture(t, "typescript", []string{"jest"}, "corral-units-cwd /w\n[\"/w/test/alpha.test.ts\",\"/w/test/beta.test.ts\"]\n")
	vf.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	vf.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	jrun := &seedingFake{unitFakeRunner: vf.run, shareCache: true}
	if ev := collect(vf, jrun); !ev.Settled() || jrun.cacheAsked != nil || slices.ContainsFunc(jrun.calls, func(c string) bool { return strings.HasPrefix(c, "warm ") }) {
		t.Errorf("jest: ev = %+v, cache asked %+v, runs %q; want no cache and no warm run", ev, jrun.cacheAsked, jrun.calls)
	}
}

// A runner that can seed is seeded once per pass, after the listing: every
// unit runs through the one seed, which is closed once, after the last
// unit. A runner whose bwrap cannot overlay still runs every unit, on a
// fresh copy each, and the pass says so where it says what else it
// changed (Changed), never in Note, which marks a pass that did not run.
func TestCollectUnitEvidenceSeedsOnce(t *testing.T) {
	collect := func(f *unitFixture, run *seedingFake) UnitEvidence {
		return CollectUnitEvidence(context.Background(), run, map[string]string{}, f.p, f.inv, "/w", 2, func(int, int) {})
	}
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})

	run := &seedingFake{unitFakeRunner: f.run}
	ev := collect(f, run)
	if !ev.Settled() {
		t.Fatalf("ev = %+v, want both units green", ev)
	}
	if run.seeds != 1 || run.closes != 1 {
		t.Errorf("Seed called %d times and Close %d, want 1 and 1", run.seeds, run.closes)
	}
	if run.seededRuns != 2 {
		t.Errorf("%d commands ran through the seed, want the 2 units", run.seededRuns)
	}
	if len(run.ran) != 3 || run.ranAtClose != 3 {
		t.Errorf("%d commands ran, %d before Close, want the listing and 2 units, all before Close", len(run.ran), run.ranAtClose)
	}
	if strings.Contains(ev.Changed, "fresh copy") {
		t.Errorf("Changed = %q on a pass that ran on a seed", ev.Changed)
	}

	nf := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	nf.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	nf.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	norun := &seedingFake{unitFakeRunner: nf.run, seedErr: fmt.Errorf("wrapped: %w", &sandbox.NoOverlayError{Backend: "bwrap"})}
	ev = collect(nf, norun)
	if !ev.Settled() || ev.Note != "" {
		t.Fatalf("without overlay: ev = %+v, want both units green and no Note", ev)
	}
	if norun.seededRuns != 0 || len(norun.ran) != 3 {
		t.Errorf("without overlay: %d seeded runs and %d runs, want 0 and 3", norun.seededRuns, len(norun.ran))
	}
	want := "the evidence pass runs each unit with --pool=threads; units ran on a fresh copy each (this bwrap cannot overlay)"
	if ev.Changed != want {
		t.Errorf("without overlay: Changed = %q, want %q", ev.Changed, want)
	}

	// The sentence names the backend that could not overlay.
	cf := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	cf.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
	cf.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
	ev = collect(cf, &seedingFake{unitFakeRunner: cf.run, seedErr: &sandbox.NoOverlayError{Backend: "container"}})
	if !strings.HasSuffix(ev.Changed, "units ran on a fresh copy each (the container backend cannot overlay)") {
		t.Errorf("container: Changed = %q", ev.Changed)
	}

	// Any other seeding failure stops the pass, and says why; no unit is
	// left looking like it ran.
	bf := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	broken := &seedingFake{unitFakeRunner: bf.run, seedErr: errors.New("no space left on device")}
	ev = collect(bf, broken)
	if ev.Ran || ev.Note != "typescript: the unit pass did not run: the jail could not start it" || !strings.Contains(ev.Said(), "no space left on device") {
		t.Errorf("a failed seed: ev = %+v, want a pass that did not run, saying why on the terminal and recording the class", ev)
	}
	for _, u := range ev.Units {
		if u.Err == "" {
			t.Errorf("unit %s has no Err in a pass that never ran it", u.Unit)
		}
	}
	if len(broken.ran) != 1 {
		t.Errorf("a failed seed ran %d commands, want only the listing", len(broken.ran))
	}
}

// The jail the pass runs on seeds and reads its tree: CollectUnitEvidence
// finds both by type assertion, so a signature drift would silently turn
// off the seed or the version gate's read of node_modules instead of
// failing to build.
func TestTheJailSeedsAndReadsItsTree(t *testing.T) {
	j := adequacy.NewEnumerator(nil, time.Second)
	if _, ok := j.(seedingRunner); !ok {
		t.Error("adequacy's jail is not a seedingRunner")
	}
	if _, ok := j.(TreeReader); !ok {
		t.Error("adequacy's jail is not a TreeReader")
	}
}

// The pass's cache helpers, over hand-built caches: no row shares a cache
// with options or a version gate today, so these are the only tests of
// those branches until one does.
func TestUnitPassCacheHelpers(t *testing.T) {
	args := []string{"--cache", "--cache-dir", sandbox.CacheTarget}
	named := &lang.EvidenceCache{Name: "transform cache", Env: "TC"}
	if got, want := unitsCacheArgs("", named, args), "the evidence pass runs each unit with --cache --cache-dir /corral-cache (a shared transform cache, filled by the first unit and frozen for every later one)"; got != want {
		t.Errorf("unitsCacheArgs with no note = %q, want %q", got, want)
	}
	if got, want := unitsCacheArgs("the evidence pass runs each unit with --pool=threads", named, args), "the evidence pass runs each unit with --pool=threads, and with --cache --cache-dir /corral-cache (a shared transform cache, filled by the first unit and frozen for every later one)"; got != want {
		t.Errorf("unitsCacheArgs with a note = %q, want %q", got, want)
	}

	for _, c := range []struct {
		cache *lang.EvidenceCache
		want  string
	}{
		{named, "transform cache"},
		{&lang.EvidenceCache{Env: "GOCACHE"}, "GOCACHE"},
		{&lang.EvidenceCache{}, "tool cache"},
	} {
		if got := cacheName(c.cache); got != c.want {
			t.Errorf("cacheName(%+v) = %q, want %q", c.cache, got, c.want)
		}
	}

	argv := []string{"tool", "run"}
	read := func(string) ([]byte, error) { return []byte("1.0.0"), nil }
	var gotArgv []string
	reads := 0
	gate := func(why string) func([]string, func(string) ([]byte, error)) string {
		return func(a []string, r func(string) ([]byte, error)) string {
			gotArgv = a
			if b, err := r("x"); err == nil && string(b) == "1.0.0" {
				reads++
			}
			return why
		}
	}
	noGate := &lang.EvidenceCache{Env: "TC"}
	takes := &lang.EvidenceCache{Name: "c", Unsupported: gate("")}
	refuses := &lang.EvidenceCache{Name: "c", Unsupported: gate("tool 0.9 is older than 1.0")}
	if asked, why := askedCache(nil, argv, read); asked != nil || why != "" {
		t.Errorf("askedCache(nil) = %+v, %q; want nil, \"\"", asked, why)
	}
	if asked, why := askedCache(noGate, argv, read); asked != noGate || why != "" {
		t.Errorf("no Unsupported: askedCache = %+v, %q; want the cache, \"\"", asked, why)
	}
	if asked, why := askedCache(takes, argv, read); asked != takes || why != "" {
		t.Errorf("Unsupported gives \"\": askedCache = %+v, %q; want the cache, \"\"", asked, why)
	}
	if asked, why := askedCache(refuses, argv, read); asked != nil || why != "tool 0.9 is older than 1.0" {
		t.Errorf("Unsupported gives a reason: askedCache = %+v, %q; want nil and the reason", asked, why)
	}
	if !slices.Equal(gotArgv, argv) || reads != 2 {
		t.Errorf("Unsupported saw argv %q and read %d times, want %q and 2", gotArgv, reads, argv)
	}
}

// Changed is the note first, then every other sentence in order, however
// the note was extended after a sentence was said: a shared cache's options
// extend the note, and must neither drop nor repeat what was already said.
func TestUnitsChangedKeepsEverySentence(t *testing.T) {
	for _, c := range []struct {
		note string
		said []string
		want string
	}{
		{"", nil, ""},
		{"n", nil, "n"},
		{"", []string{"a", "b"}, "a; b"},
		{"n, and with x", []string{"a", "b"}, "n, and with x; a; b"},
	} {
		if got := unitsChanged(c.note, c.said); got != c.want {
			t.Errorf("unitsChanged(%q, %q) = %q, want %q", c.note, c.said, got, c.want)
		}
	}
}

// treeFileReader names a file by lang.TreeRel's one rule whether or not
// the runner can read further: "./x" is "x", and a path out of the tree is
// refused rather than looked up.
func TestTreeFileReaderOneRule(t *testing.T) {
	files := map[string]string{"node_modules/t/package.json": "seeded"}
	for name, runner := range map[string]commandRunner{
		"seed only":   &unitFakeRunner{},
		"tree reader": readingSeedFake{&seedingFake{unitFakeRunner: &unitFakeRunner{}}, map[string]string{"node_modules/u/package.json": "bound"}},
	} {
		read := treeFileReader(runner, files)
		for _, rel := range []string{"node_modules/t/package.json", "./node_modules/t/package.json", "node_modules//t/package.json"} {
			if b, err := read(rel); err != nil || string(b) != "seeded" {
				t.Errorf("%s: read(%q) = %q, %v; want the seed's file", name, rel, b, err)
			}
		}
		for _, rel := range []string{"../x", "/etc/passwd"} {
			if b, err := read(rel); err == nil {
				t.Errorf("%s: read(%q) = %q, want a refusal", name, rel, b)
			}
		}
		if _, err := read("absent"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: read(absent) err = %v, want fs.ErrNotExist", name, err)
		}
	}
}

// readingSeedFake is a seedingFake that also reads the tree as its jail
// sees it (lang.TreeRel's rule, the seed, then the dependency directories
// mounted into it), as adequacy's jail does: tree is what those
// directories hold.
type readingSeedFake struct {
	*seedingFake
	tree map[string]string
}

func (r readingSeedFake) ReadTreeFile(files map[string]string, rel string) ([]byte, error) {
	clean, ok := lang.TreeRel(rel)
	if !ok {
		return nil, fmt.Errorf("%q is not in the tree", rel)
	}
	if s, ok := files[clean]; ok {
		return []byte(s), nil
	}
	if s, ok := r.tree[clean]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

// answerAsWritten answers unit's one-unit command exactly as the operator's
// command runs it: no evidence-pass flags, no coverage.
func (f *unitFixture) answerAsWritten(unit string, res sandbox.EnumerateResult) {
	f.t.Helper()
	cmd, ok := lang.UnitRunCmd(f.p, f.inv, unit)
	if !ok {
		f.t.Fatalf("no one-unit command for %q", unit)
	}
	f.run.res[argvKey(cmd)] = res
}

// REA's cliJsonPaths.test.ts calls process.chdir(), which vitest's threads
// pool forbids: red in the evidence pass only because the pass ran it with
// --pool=threads (2026-10-30). Run again as the operator's command runs it,
// it passes, so it is not a red unit: the red-baseline retry must not leave
// it out, and its run there measured no coverage, so it reports no evidence
// and runs with every file. A unit red both ways stays red.
func TestUnitPassRechecksARedUnitAsTheOperatorRunsIt(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"vitest", "run"}, vitestTwoUnits)
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: "# suite-exit 1\ncorral-js-coverage: v2\n", ExitCode: 0})
	f.answerAsWritten("test/alpha.test.ts", sandbox.EnumerateResult{ExitCode: 0})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: "# suite-exit 1\ncorral-js-coverage: v2\n", ExitCode: 0})
	f.answerAsWritten("test/beta.test.ts", sandbox.EnumerateResult{ExitCode: 1})

	ev := f.collect(context.Background(), "/w", 1)
	alpha := unitByName(t, ev, "test/alpha.test.ts")
	if strings.HasPrefix(alpha.Err, unitRunFailed) || !strings.Contains(alpha.Err, "green as the operator's command runs it") || !strings.Contains(alpha.Err, "--pool=threads") || alpha.Counts != nil {
		t.Errorf("alpha = %+v, want a non-red reason naming the evidence pass's change, and no evidence", alpha)
	}
	if beta := unitByName(t, ev, "test/beta.test.ts"); !strings.HasPrefix(beta.Err, unitRunFailed) {
		t.Errorf("beta, red both ways, Err = %q, want it still red", beta.Err)
	}
}

// A runner whose evidence pass changes nothing in the command has nothing to
// recheck: a red unit there is red as written, and is not run twice.
func TestUnitPassDoesNotRecheckWhenThePassChangedNothing(t *testing.T) {
	f := newUnitFixture(t, "typescript", []string{"jest"}, "corral-units-cwd /w\n[\"/w/test/alpha.test.ts\",\"/w/test/beta.test.ts\"]\n")
	if lang.EvidenceNote(f.p, f.inv) != "" {
		t.Fatalf("jest's evidence pass changes its command (%q); this control needs a runner whose pass changes nothing", lang.EvidenceNote(f.p, f.inv))
	}
	f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: "# suite-exit 1\n", ExitCode: 0})
	f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: "# suite-exit 1\n", ExitCode: 0})
	f.collect(context.Background(), "/w", 1)
	cmd, _ := lang.UnitRunCmd(f.p, f.inv, "test/alpha.test.ts")
	if f.run.didRun(cmd) {
		t.Error("a red unit was rerun although the evidence pass changed nothing in its command")
	}
}

// A unit's recorded reason quotes no command and no parser text (review of
// R-F2): the secret rides in the operator's command, the host path in what
// the report printed. Each case shows the plant reached the unit's
// TerminalDetail, so the absence from Err is the fix and not a quiet
// fixture.
func TestAUnitsRecordedReasonQuotesNoCommandAndNoParserText(t *testing.T) {
	const (
		secret = "API_KEY=sk-live-PLANTED"
		host   = "/home/operator/private-checkout"
	)
	argv := []string{"vitest", "run", "--reporter=" + secret}
	clean := func(t *testing.T, err string) {
		t.Helper()
		for _, leak := range []string{secret, "sk-live", host, "/home/operator"} {
			if strings.Contains(err, leak) {
				t.Errorf("the recorded Err carries %q: %q", leak, err)
			}
		}
	}
	t.Run("no one-unit command", func(t *testing.T) {
		// A file named like a flag has no one-unit command (fileUnits).
		f := newUnitFixture(t, "typescript", argv, "corral-units-cwd /w\n[{\"file\":\"/w/test/alpha.test.ts\"},{\"file\":\"/w/-x.test.ts\"}]\n")
		f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("7 10 src/x.ts")})
		u := unitByName(t, f.collect(context.Background(), "/w", 1), "-x.test.ts")
		if u.Err != unitNoCommand || !strings.Contains(u.TerminalDetail, secret) {
			t.Fatalf("u = %+v: want %q with the command in TerminalDetail", u, unitNoCommand)
		}
		clean(t, u.Err)
	})
	t.Run("unparseable report", func(t *testing.T) {
		f := newUnitFixture(t, "typescript", argv, vitestTwoUnits)
		f.answer("test/alpha.test.ts", sandbox.EnumerateResult{Output: jsReport("x y " + host + "/" + secret + ".ts")})
		f.answer("test/beta.test.ts", sandbox.EnumerateResult{Output: jsReport("2 10 src/x.ts")})
		u := unitByName(t, f.collect(context.Background(), "/w", 1), "test/alpha.test.ts")
		if !strings.HasPrefix(u.Err, "unparseable: ") || !strings.Contains(u.TerminalDetail, host) {
			t.Fatalf("u = %+v: want an unparseable reason with the parser's text in TerminalDetail", u)
		}
		clean(t, u.Err)
	})
}
