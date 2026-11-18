// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// pyReportModule is the module name of the pytest plugin corral loads into
// every graded run it can rewrite (pyReportPlugin, by `-p`).
const pyReportModule = "corral_report"

// pyReportArgv0 is the $0 the report wrapper's script runs under: the word
// after the script, before the operator's command.
const pyReportArgv0 = "corral-report"

// The marker lines of a graded pytest run. The plugin writes them to a file
// and the wrapper prints that file after pytest has exited, so they are the
// last lines of the run's output:
//
//	corral-report-begin
//	corral-report failed tests/test_x.py::test_a[a b]
//	corral-report error tests/test_x.py::test_b
//	corral-report-end
//
// A record is the prefix, a kind, and the node id running to the END OF THE
// LINE: an id may contain spaces (a parametrized `test_x[a b]`), which is
// what the printed `FAILED <id> - <message>` line cannot say, since nothing
// there marks where the id stops. `failed` is a test that failed in its call
// phase (pytest's FAILED), `error` one that failed in setup or teardown, or a
// collector that failed (pytest's ERROR).
//
// begin is written when the plugin is configured and end when the session
// finishes, so a reader can tell three things apart: no block (the plugin
// never ran: a command corral could not rewrite, output recorded before this
// existed, or a begin line the output cap cut off), a begin with no end (the
// session never finished, or a write failed: not a complete account), and a
// complete block, which names every failure pytest reported, possibly none.
const (
	pyReportBegin  = "corral-report-begin"
	pyReportEnd    = "corral-report-end"
	pyReportRecord = "corral-report "
	pyReportFailed = "failed"
	pyReportError  = "error"
)

// pyReportPlugin is the pytest plugin a graded run loads (-p corral_report,
// from a temporary directory the wrapper puts LAST on PYTHONPATH). It records
// each failing test from pytest's own reports, which is what pytest's
// terminal summary is printed from, so no report setting of the project's
// (-r, --no-summary, -p no:terminal) can hide it.
//
// It may change only what corral can read, never what passes or fails:
//
//   - with no CORRAL_REPORT in the environment it registers nothing;
//   - the path is read once, at configure, so a test that empties os.environ
//     cannot make a later hook raise;
//   - a write that fails (a full disk) is swallowed, and nothing more is
//     written, the end line included: the block is then incomplete and a
//     reader ignores it;
//   - on a pytest-xdist worker (config.workerinput) it registers nothing: the
//     controller is handed every worker's test report and records each once.
//     pytest-xdist was not installed where this was written, so that half
//     has not been run.
//
// Hooks are methods of an object registered at configure rather than module
// functions, so a pytest session a test starts inside its own process
// (pytester's in-process runs), which loads no -p of corral's, records
// nothing into the outer run's file.
const pyReportPlugin = `import os


class _Recorder:
    def __init__(self, path):
        self.path = path
        self.ok = True

    def write(self, line):
        if not self.ok:
            return
        try:
            with open(self.path, "a", encoding="utf-8", errors="backslashreplace") as out:
                out.write(line + "\n")
        except OSError:
            self.ok = False

    def pytest_runtest_logreport(self, report):
        if report.failed:
            kind = "failed" if report.when == "call" else "error"
            self.write("corral-report " + kind + " " + report.nodeid)

    def pytest_collectreport(self, report):
        if report.failed:
            self.write("corral-report error " + report.nodeid)

    def pytest_sessionfinish(self):
        self.write("corral-report-end")


def pytest_configure(config):
    if hasattr(config, "workerinput"):
        return
    path = os.environ.get("CORRAL_REPORT")
    if not path:
        return
    recorder = _Recorder(path)
    recorder.write("corral-report-begin")
    if recorder.ok:
        config.pluginmanager.register(recorder, "corral-report-recorder")
`

// pyReportScript is the wrapper a graded pytest run goes through. It holds
// only corral's own text: the operator's command is its positional
// parameters, run as `"$@"` and never spliced into the script (the rule
// coverageRunAndReduce follows), so the shell never re-reads a word of it
// and a space, a quote or a `$` in one reaches pytest as it was written.
//
//   - The temporary directory holds the plugin and the file it writes, and is
//     removed when the script exits. A run killed outright (a timeout's
//     SIGKILL) leaves it behind: inside a jail it goes with the jail's /tmp,
//     on --substrate workspace it stays in the runner's temporary directory.
//   - It goes at the END of PYTHONPATH. pyPlugin.TreeEnv puts a pool tree at
//     the front so the tree imports its own copy of the project, and the
//     operator's own entries follow it; a directory in front of either could
//     win an import from them.
//   - pytest's exit code is the script's, whatever it was. Setting up can
//     fail (no mktemp, a full /tmp): then the script says so on stderr and
//     exits 70 without running pytest, since a command carrying
//     `-p corral_report` with no such module would fail for a reason nobody
//     could read. The baseline on unmutated code goes through this same
//     script, so a host where it cannot be set up fails its baseline and
//     grades nothing, rather than grading every fault as caught.
//   - The blank line before the block keeps the begin marker on a line of
//     its own when pytest's last line had no newline.
const pyReportScript = `d=$(mktemp -d) || { echo 'corral: mktemp -d failed, so the pytest failure hook could not be set up; the tests were not run' >&2; exit 70; }
trap 'rm -rf "$d"' EXIT
cat > "$d/` + pyReportModule + `.py" <<'CORRAL_PY' || { echo 'corral: could not write the pytest failure hook; the tests were not run' >&2; exit 70; }
` + pyReportPlugin + `CORRAL_PY
CORRAL_REPORT="$d/report" PYTHONPATH="${PYTHONPATH:+$PYTHONPATH:}$d" "$@"
rc=$?
[ -f "$d/report" ] && { echo; cat "$d/report"; }
exit "$rc"
`

// pyReportHook is the pytest row's ReportHook: cmd run through
// pyReportScript with the plugin loaded by `-p`, which goes where the row's
// ReportArgs go (optsEnd: before pytest's own `--`, after which every word
// is a path).
var pyReportHook = &ReportHook{
	Wrap: func(cmd []string, optsEnd int) []string {
		out := make([]string, 0, len(cmd)+6)
		out = append(out, "sh", "-c", pyReportScript, pyReportArgv0)
		out = append(out, cmd[:optsEnd]...)
		out = append(out, "-p", pyReportModule)
		return append(out, cmd[optsEnd:]...)
	},
	Says: "corral runs every graded run through `sh -c` with a pytest plugin of its own loaded (-p " + pyReportModule +
		", from a temporary directory put last on PYTHONPATH), and reads which test failed from what the plugin records, " +
		"whatever the project's report settings print (what passes and fails is unchanged)",
}

// pyReportRecord is one failure the plugin recorded.
type pyReportRec struct{ kind, id string }

// pyReportBlock is the records of the LAST complete marker block in output
// (the wrapper prints it after everything pytest printed), and whether there
// is one. A begin with no end after it is not a block: see pyReportBegin.
// Lines are matched whole, with only a trailing carriage return dropped, so
// an id keeps every space it has.
func pyReportBlock(output string) (recs []pyReportRec, ok bool) {
	var cur []pyReportRec
	open := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == pyReportBegin:
			cur, open = nil, true
		case line == pyReportEnd:
			if open {
				recs, ok, open = cur, true, false
			}
		case open:
			rest, isRec := strings.CutPrefix(line, pyReportRecord)
			if !isRec {
				continue
			}
			kind, id, found := strings.Cut(rest, " ")
			if !found || id == "" || (kind != pyReportFailed && kind != pyReportError) {
				continue
			}
			cur = append(cur, pyReportRec{kind: kind, id: id})
		}
	}
	return recs, ok
}

// pyReportedFirstFailure is pyFirstFailure's answer from a complete block:
// the first record that names a test (an id with "::"; a collector that
// failed names only a file), an error prefixed `error:` as the printed ERROR
// line is. "" when the block names none: pytest reported no failing test,
// and that is the block's answer, not a reason to read the printed lines.
func pyReportedFirstFailure(recs []pyReportRec) string {
	for _, r := range recs {
		if !strings.Contains(r.id, "::") {
			continue
		}
		if r.kind == pyReportError {
			return "error:" + r.id
		}
		return r.id
	}
	return ""
}

// pyReportedFailedTests is pyFailedTests' answer from a complete block: the
// ids that failed in their call phase, each once, in report order. Errors
// are left out, as the printed reader leaves ERROR lines out.
func pyReportedFailedTests(recs []pyReportRec) []string {
	var out []string
	for _, r := range recs {
		if r.kind == pyReportFailed && !slices.Contains(out, r.id) {
			out = append(out, r.id)
		}
	}
	return out
}
