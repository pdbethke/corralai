// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// nextestHookWords are the words the cargo-nextest row's hook adds, after
// the row's report flags.
var nextestHookWords = []string{"--tool-config-file", "corral:" + recordToken + ".toml"}

// nextestFlags are the cargo-nextest row's ReportArgs.
var nextestFlags = []string{"--status-level", "pass", "--final-status-level", "flaky"}

// nextestWrapper is the record wrapper's head for the cargo-nextest row:
// fallback drop:<from>:2 (the hook's two words after the command's first
// <from>), the record at <path>.xml, the companion at <path>.toml.
func nextestWrapper(from string) []string {
	return []string{"sh", "-c", recordScript, recordArgv0, recordJUnit, "drop:" + from + ":2", ".xml", ".toml", nextestRecordHook.companion()}
}

// companion is the companion text the hook hands its wrapper.
func (h *ReportHook) companion() string {
	w := h.Wrap([]string{"x"})
	return w[8]
}

// Every `cargo nextest run` corral can rewrite is hooked: the report flags,
// then --tool-config-file naming the companion, before the runner's own
// `--`; the binary called directly and the `r` alias too, and a command a
// killer or a deselection derived from the operator's. What is not a run
// (`list`, `archive`) and a command with a --tool-config-file of its own
// keep the report flags alone; `cargo test` gets nothing from this row.
func TestNextestHookWrapsANextestRun(t *testing.T) {
	p, _ := ByName("rust")
	for _, c := range []struct {
		operator, inner []string
		from            string
	}{
		{[]string{"cargo", "nextest", "run"}, slices.Concat([]string{"cargo", "nextest", "run"}, nextestFlags, nextestHookWords), "7"},
		{[]string{"cargo", "nextest", "run", "-p", "x", "--", "--exact", "a"}, slices.Concat([]string{"cargo", "nextest", "run", "-p", "x"}, nextestFlags, nextestHookWords, []string{"--", "--exact", "a"}), "9"},
		{[]string{"cargo-nextest", "nextest", "run", "-j", "1"}, slices.Concat([]string{"cargo-nextest", "nextest", "run", "-j", "1"}, nextestFlags, nextestHookWords), "9"},
		{[]string{"cargo", "nextest", "r"}, slices.Concat([]string{"cargo", "nextest", "r"}, nextestFlags, nextestHookWords), "7"},
	} {
		got, ok := ReportCmd(p, Cmd(c.operator), c.operator)
		if want := append(nextestWrapper(c.from), c.inner...); !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the record wrapper around %q", c.operator, got[min(len(got), 9):], ok, c.inner)
		}
	}
	for _, operator := range [][]string{
		{"cargo", "nextest", "list"},
		{"cargo", "nextest", "archive", "--archive-file", "a.tar.zst"},
		{"cargo", "nextest", "run", "--tool-config-file", "mine:/abs/mine.toml"},
		{"cargo", "nextest", "run", "--tool-config-file=mine:/abs/mine.toml"},
	} {
		got, ok := ReportCmd(p, Cmd(operator), operator)
		if want := slices.Concat(operator, nextestFlags); !ok || !slices.Equal(got, want) {
			t.Errorf("ReportCmd(%q) = %q ok=%v, want the report flags alone, no hook", operator, got, ok)
		}
	}
	if got, ok := ReportCmd(p, Cmd([]string{"cargo", "test"}), []string{"cargo", "test"}); ok || !slices.Equal(got, []string{"cargo", "test"}) {
		t.Errorf("ReportCmd(cargo test) = %q ok=%v, want it unchanged", got, ok)
	}
	operator := []string{"cargo", "nextest", "run"}
	killer, ok := nextestKillerCmd(operator, "fail-cargo tests::a_fails")
	if !ok {
		t.Fatal("no killer command")
	}
	if got, ok := ReportCmd(p, Cmd(killer), killer); !ok || !slices.Equal(got[9:], slices.Concat(killer, nextestFlags, nextestHookWords)) {
		t.Errorf("killer command graded as %q ok=%v, want it hooked", got[min(len(got), 9):], ok)
	}
}

// The companion is the nextest config the hook writes, and it is what the
// row's doc comment says: the default profile's junit path, absolute, and
// nothing else, since a project's own report inherits any other key.
func TestNextestHookCompanionSetsTheDefaultProfilesJUnitPath(t *testing.T) {
	want := "# corral's failure hook: nextest's JUnit record of this run, outside the project.\n" +
		"[profile.default.junit]\npath = '" + recordToken + ".xml'\n"
	if got := nextestRecordHook.companion(); got != want {
		t.Errorf("companion = %q, want %q", got, want)
	}
}

// nextestTiming is a status line's duration, which differs between runs.
var nextestTiming = regexp.MustCompile(`\[ *[0-9.]+s\]|\[> *[0-9.]+s\]| in [0-9.]+s$`)

// nextestPrinted is nextest's output with its durations taken out, and
// cargo's build lines (`Compiling`, `Finished`), which say whether the
// first of the two runs had to build.
func nextestPrinted(s string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "Compiling ") || strings.HasPrefix(t, "Finished ") {
			continue
		}
		lines = append(lines, nextestTiming.ReplaceAllString(l, ""))
	}
	return strings.Join(lines, "\n")
}

// What nextest prints with the hook is what it prints without it (the
// row's report flags alone), on a failing run and a passing one, durations
// aside (cargo-nextest 0.9.148, -j 1 so the order is the same).
func TestNextestHookLeavesThePrintedOutputAlone(t *testing.T) {
	for hooked, plain := range map[string]string{
		"nextest-hook-fail.txt": "nextest-hook-unhooked.txt",
		"nextest-hook-edge.txt": "nextest-hook-edge-unhooked.txt",
		"nextest-hook-pass.txt": "nextest-hook-unhooked-pass.txt",
	} {
		h := nextestPrinted(printedPart(t, hooked))
		u := nextestPrinted(readFixture(t, plain))
		if h != u {
			t.Errorf("%s: printed output differs with the hook:\n--- hooked\n%s\n--- unhooked\n%s", hooked, h, u)
		}
	}
}

// The ids nextest's JUnit record gives are the ids its printed FAIL lines
// give on the same run, `<binary id> <test>`, and every one is an id the
// killer and deselect commands take. On fail/cargo the two name the same
// tests. On fail/nextest-edge the record names three more: a test that
// failed every retry (`TRY 2 FAIL`), one that aborted (`SIGABRT`) and one
// nextest timed out (`TIMEOUT`), none of which the printed reader takes;
// the one that passed on its retry is neither's failure and both runs'
// flaky test. The binary ids are the library's, an integration test's and
// a binary's.
func TestNextestRecordIDsEqualPrintedIDs(t *testing.T) {
	for _, c := range []struct {
		fixture        string
		record, extra  []string
		flaky          []string
		printedIsWhole bool
	}{
		{"nextest-hook-fail.txt", []string{"fail-cargo tests::a_fails", "fail-cargo tests::b_fails"}, nil, nil, true},
		{"nextest-hook-edge.txt",
			[]string{"nextest-edge tests::aborts", "nextest-edge tests::fails_every_try", "nextest-edge tests::lib_fails", "nextest-edge tests::times_out", "nextest-edge::it it_fails", "nextest-edge::bin/edge-bin tests::bin_fails"},
			[]string{"nextest-edge tests::aborts", "nextest-edge tests::fails_every_try", "nextest-edge tests::times_out"},
			[]string{"nextest-edge tests::passes_on_retry"}, false},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			out := readFixture(t, c.fixture)
			rec, ok := nextestRecordedFailures(out)
			if !ok {
				t.Fatalf("no record read from %s", c.fixture)
			}
			if !slices.Equal(sorted(rec), sorted(c.record)) {
				t.Errorf("record = %q, want %q", rec, c.record)
			}
			printed := rustFailedTests(PrintedOutput(out))
			var missing []string
			for _, id := range rec {
				if !slices.Contains(printed, id) {
					missing = append(missing, id)
				}
			}
			if !slices.Equal(sorted(missing), sorted(c.extra)) {
				t.Errorf("record ids the printed lines lack = %q, want %q", missing, c.extra)
			}
			for _, id := range printed {
				if !slices.Contains(rec, id) {
					t.Errorf("printed id %q is not in the record", id)
				}
			}
			if got := nextestFailedTests(out); !slices.Equal(got, rec) {
				t.Errorf("nextestFailedTests = %q, want the record's %q (it already holds every printed id)", got, rec)
			}
			if got := nextestFlakyTests([]byte(out)); !slices.Equal(got, c.flaky) {
				t.Errorf("flaky = %q, want %q", got, c.flaky)
			}
			operator := []string{"cargo", "nextest", "run"}
			for _, id := range rec {
				if _, ok := nextestKillerCmd(operator, id); !ok {
					t.Errorf("nextestKillerCmd refuses the recorded id %q", id)
				}
			}
			if _, ok := nextestDeselectCmd(operator, rec); !ok {
				t.Errorf("nextestDeselectCmd refuses the recorded ids %q", rec)
			}
		})
	}
}

// sorted is a sorted copy of s.
func sorted(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// The row reads the record through its own readers: FirstFailure is the
// record's first id, FailedTests every id, on the hooked capture.
func TestNextestRowReadsTheRecord(t *testing.T) {
	p, _ := ByName("rust")
	inv := Cmd([]string{"cargo", "nextest", "run"})
	r, ok := RunnerFor(p, inv)
	if !ok || r.ID != "cargo-nextest" {
		t.Fatalf("RunnerFor = %q, want cargo-nextest", r.ID)
	}
	out := readFixture(t, "nextest-hook-edge.txt")
	want, _ := nextestRecordedFailures(out)
	if got := FailureParserFor(p, inv).FirstFailure([]byte(out)); got != want[0] {
		t.Errorf("FirstFailure = %q, want the record's first, %q", got, want[0])
	}
	if got := r.FailedTests(out); !slices.Equal(got, want) {
		t.Errorf("FailedTests = %q, want %q", got, want)
	}
}

// With no record (the wrapper could not set up, or the project's own junit
// path won, so nextest wrote none of corral's), the failures are the
// printed lines', as before the hook.
func TestNextestFallsBackToPrintedLinesWithoutARecord(t *testing.T) {
	out := readFixture(t, "nextest-hook-fallback.txt")
	if strings.Contains(out, recordBegin) {
		t.Fatalf("the fallback capture holds a record block")
	}
	if !strings.Contains(out, "could not make a temporary directory, so it leaves its record option out") {
		t.Errorf("the fallback is not said:\n%s", out)
	}
	if got, want := nextestFailedTests(out), rustFailedTests(out); len(want) == 0 || !slices.Equal(got, want) {
		t.Errorf("nextestFailedTests = %q, want the printed %q", got, want)
	}
}

// The scan line says what the hook does, and what it can leave behind.
func TestRunDisclosuresSayWhenTheNextestHookWraps(t *testing.T) {
	p, _ := ByName("rust")
	got := RunDisclosures(p, t.TempDir(), Cmd([]string{"cargo", "nextest", "run"}))
	if !slices.ContainsFunc(got, func(l string) bool {
		return strings.HasPrefix(l, "cargo nextest: corral runs every graded run through `sh -c` and adds `--tool-config-file corral:<a temporary file>`") &&
			strings.Contains(l, "empty, where the run without corral's hook would not")
	}) {
		t.Errorf("RunDisclosures = %q, want the hook's sentence", got)
	}
}

// The hooked captures are made with the command the row builds, taken from
// scripts/reportcmd, and the script holds the checks the comments here and
// in nextest_record.go cite: the exit code kept, the fallback, and the
// binding rule that a graded run writes no project file the run without
// the hook would not (nextest-wrote-*: what each run added to the
// checkout, files and directories).
func TestNextestHookCaptureIsTheRowsCommand(t *testing.T) {
	p, _ := ByName("rust")
	operator := []string{"cargo", "nextest", "run", "-j", "1"}
	got, ok := ReportCmd(p, Cmd(operator), operator)
	if want := slices.Concat(nextestWrapper("9"), operator, nextestFlags, nextestHookWords); !ok || !slices.Equal(got, want) {
		t.Fatalf("ReportCmd(%q) = %q ok=%v", operator, got, ok)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, line := range []string{
		"reporthook NEXTESTHOOK rust cargo nextest run -j 1",
		`(cd fail/cargo && nextestfails nextest-hook-fail.txt "${NEXTESTHOOK[@]}")`,
		`(cd fail/cargo && fails nextest-hook-unhooked.txt cargo nextest run -j 1 --status-level pass --final-status-level flaky)`,
		`(cd fail/nextest-edge && nextestfails nextest-hook-edge.txt "${NEXTESTHOOK[@]}")`,
		`(cd fail/nextest-edge && fails nextest-hook-edge-unhooked.txt cargo nextest run -j 1 --status-level pass --final-status-level flaky)`,
		`(cd units/cargo && nextestfails nextest-hook-pass.txt "${NEXTESTHOOK[@]}")`,
		`(cd units/cargo && fails nextest-hook-unhooked-pass.txt cargo nextest run -j 1 --status-level pass --final-status-level flaky)`,
		`want nextest-hook-exit-fail "100 100"`,
		`want nextest-hook-exit-pass "0 0"`,
		`nextestfails nextest-hook-fallback.txt env TMPDIR="$DIR/no-such-dir" "${NEXTESTHOOK[@]}"`,
		`want nextest-hook-fallback-exit 100`,
		"want nextest-wrote-none-flagged ",
		"want nextest-wrote-none-hooked ",
		"want nextest-wrote-theirs-flagged ",
		"want nextest-wrote-theirs-hooked ",
		"want nextest-wrote-profile-flagged ",
		"want nextest-wrote-profile-hooked ",
		"want nextest-wrote-profile-env-flagged ",
		"want nextest-wrote-profile-env-hooked ",
		"want nextest-wrote-profile-nojunit-flagged ",
		"want nextest-wrote-profile-nojunit-hooked ",
		"want nextest-wrote-profile-default-hooked ",
		"want nextest-wrote-none-config-hooked ",
		"want cargo-test-junit-format-refused-on-stable 101",
		"want cargo-test-json-format-refused-on-stable 101",
		"want cargo-test-logfile-keeps-the-last-binary \"failed it_fails\"",
		"want nextest-wrote-config-file-flagged ",
		"want nextest-wrote-config-file-hooked ",
		"want nextest-wrote-store-flagged ",
		"want nextest-wrote-store-hooked ",
		"want nextest-wrote-absolute-flagged ",
		"want nextest-wrote-absolute-hooked ",
		"want nextest-theirs-same-content same",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("the capture script has no %q", line)
		}
	}
	for _, f := range []string{"nextest-hook-fail.txt", "nextest-hook-edge.txt", "nextest-hook-pass.txt"} {
		if out := readFixture(t, f); !strings.Contains(out, "\n"+recordEnd) {
			t.Errorf("%s: no complete record block", f)
		}
	}
}

// The rows left without a failure hook say why in their Note, from a run
// the capture script holds (cargo test: libtest's record formats refused on
// stable and its --logfile rewritten by each test binary; mocha: one
// reporter per run; minitest: no record option in its --help).
func TestRowsWithoutAFailureHookSayWhy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, c := range []struct {
		id, note string
		wants    []string
	}{
		{"cargo-test", "no failure hook: libtest writes a record of the run only in its json and junit formats, which stable refuses",
			[]string{"want cargo-test-junit-format-refused-on-stable 101", "want cargo-test-json-format-refused-on-stable 101", `want cargo-test-logfile-keeps-the-last-binary "failed it_fails"`}},
		{"mocha", "no failure hook: mocha runs one reporter, the last one named", []string{`want mocha-one-reporter-per-run "2 0"`}},
		{"minitest", "no failure hook: no option minitest 5.20.0 lists", []string{"want minitest-no-record-option 0"}},
	} {
		i := slices.IndexFunc(runners, func(r Runner) bool { return r.ID == c.id })
		if i < 0 {
			t.Fatalf("no row %s", c.id)
		}
		if r := runners[i]; r.ReportHook != nil || !strings.Contains(r.Note, c.note) {
			t.Errorf("%s: hook %v, Note %q, want no hook and %q", c.id, r.ReportHook != nil, r.Note, c.note)
		}
		for _, w := range c.wants {
			if !strings.Contains(script, w) {
				t.Errorf("the capture script has no %q", w)
			}
		}
	}
}
