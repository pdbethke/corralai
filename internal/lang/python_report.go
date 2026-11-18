// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/pdbethke/corralai/internal/clip"
)

// pyReportModule is the module name of the pytest plugin corral loads into
// every graded run it wraps (pyReportPlugin, through PYTEST_PLUGINS). It is
// a name a project is unlikely to own. A project that does own it shadows
// the plugin (under `python -m pytest` the working directory is ahead of
// PYTHONPATH): the plugin then never loads, which the wrapper's fallback
// handles like any other way of not loading.
const pyReportModule = "_corral_pytest_report_hook"

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
//
// WHAT A TEST CAN FORGE. These are lines in the run's output, and a test can
// print anything: it could always print a `FAILED <id>` line, and it can
// print a block. Nothing here makes that impossible. The reader takes the
// last complete block, which the wrapper prints after pytest has exited, and
// trusts no block at all when the last begin has no end (a test that prints
// a block and kills the process leaves that shape). The defence against a
// forged name is what it was before this existed, and is not in this file: a
// kill is credited to a named test only when that test, run alone, fails on
// the fault and passes on the clean code (adequacy's confirmation).
const (
	pyReportBegin  = "corral-report-begin"
	pyReportEnd    = "corral-report-end"
	pyReportRecord = "corral-report "
	pyReportFailed = "failed"
	pyReportError  = "error"
)

// pyReportPlugin is the pytest plugin a graded run loads (named in
// PYTEST_PLUGINS, from a temporary directory the wrapper puts LAST on
// PYTHONPATH). It records each failing test from pytest's own reports, which
// is what pytest's terminal summary is printed from, so no report setting of
// the project's (-r, --no-summary, -p no:terminal) can hide it.
//
// Everything it writes goes into its OWN directory (the directory of its
// file), so no path is exported in the environment for a test to read:
//
//   - `loaded`, at import. The wrapper reads it to tell a run in which the
//     plugin never loaded (and which it then repeats without the hook) from
//     one in which it did.
//   - `owner`, at import, created exclusively: the first process to import
//     the plugin is the one that records. PYTEST_PLUGINS is inherited, so a
//     pytest a test starts as a subprocess imports the plugin too; it is not
//     the owner and records nothing, where it would otherwise put its own
//     (often deliberately failing) tests into the outer run's record.
//   - `report`, created exclusively at the first configure with the begin
//     line. A second session configured in the same process (pytester's
//     in-process runs) finds it there and registers nothing.
//
// It may change only what corral can read, never what passes or fails:
//
//   - every write is guarded: one that fails (a full disk) is swallowed and
//     nothing more is written, the end line included, so the block is
//     incomplete and a reader ignores it;
//   - on a pytest-xdist worker (config.workerinput, and not the owner) it
//     registers nothing: the controller is handed every worker's test report
//     and records each once. pytest-xdist was not installed where this was
//     written, so that half has not been run.
//
// What it does change, and a suite asserting on its own environment could
// see: PYTEST_PLUGINS and PYTHONPATH each gain an entry (PYTHONPATH is set
// where the operator had none), one more plugin is registered, and one more
// module is in sys.modules.
const pyReportPlugin = `import os

_dir = os.path.dirname(os.path.abspath(__file__))


def _claim(name, text):
    try:
        with open(os.path.join(_dir, name), "x", encoding="utf-8") as out:
            out.write(text)
        return True
    except OSError:
        return False


_claim("loaded", "")
_owner = _claim("owner", str(os.getpid()))


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
    if not _owner or hasattr(config, "workerinput"):
        return
    if _claim("report", "corral-report-begin\n"):
        config.pluginmanager.register(_Recorder(os.path.join(_dir, "report")), "corral-report-recorder")
`

// pyReportScript is the wrapper a graded pytest run goes through. It holds
// only corral's own text: the operator's command is its positional
// parameters, run as `"$@"` and never spliced into the script (the rule
// coverageRunAndReduce follows), so the shell never re-reads a word of it
// and a space, a quote or a `$` in one reaches pytest as it was written. The
// plugin is loaded through the environment (PYTEST_PLUGINS, appended to the
// operator's own), so those words are the same with and without the hook.
//
// THE GUARANTEE is that the hook failing never changes what the run's exit
// code would have been without it, and so never fails a baseline:
//
//   - if the hook cannot be set up (no mktemp, an unwritable temporary
//     directory), the command is run exactly as it would run without the
//     hook, and its exit code is the script's;
//   - if the hooked run exits non-zero and the plugin never recorded that it
//     loaded, the command is run once more without the hook's environment,
//     one line says so, and THAT run's exit code is the script's. This is
//     what covers a command that does not carry the environment to pytest
//     (python -I or -E, `env -u PYTHONPATH`, a container, tox), where pytest
//     fails at start-up on a plugin it cannot import, and a project that
//     owns a module of the plugin's name;
//   - when the plugin loaded, the run is never repeated: its exit code is
//     pytest's own.
//
// A repeated run's output follows the first one's, and carries no block, so
// its failures are read from pytest's printed lines.
//
// The rest:
//
//   - The temporary directory holds the plugin and the files it writes, and
//     is removed when the script exits. A run killed outright (a timeout's
//     SIGKILL) leaves it behind: inside a jail it goes with the jail's /tmp,
//     on --substrate workspace it stays in the runner's temporary directory.
//   - It goes at the END of PYTHONPATH. pyPlugin.TreeEnv puts a pool tree at
//     the front so the tree imports its own copy of the project, and the
//     operator's own entries follow it; a directory in front of either could
//     win an import from them.
//   - The blank line before the block keeps the begin marker on a line of
//     its own when pytest's last line had no newline.
const pyReportScript = `if d=$(mktemp -d 2>/dev/null) && cat > "$d/` + pyReportModule + `.py" 2>/dev/null <<'CORRAL_PY'
` + pyReportPlugin + `CORRAL_PY
then
  trap 'rm -rf "$d"' EXIT
  PYTHONPATH="${PYTHONPATH:+$PYTHONPATH:}$d" PYTEST_PLUGINS="${PYTEST_PLUGINS:+$PYTEST_PLUGINS,}` + pyReportModule + `" "$@"
  rc=$?
  if [ "$rc" -ne 0 ] && [ ! -f "$d/loaded" ]; then
    echo
    echo "corral: its pytest failure hook did not load in the run above (exit $rc), so the command is run again without the hook; the result is the run below"
    "$@"
    exit $?
  fi
  [ -f "$d/report" ] && { echo; cat "$d/report"; }
  exit "$rc"
fi
[ -n "$d" ] && rm -rf "$d"
echo 'corral: its pytest failure hook could not be set up (mktemp -d, or writing into the directory it made, failed), so the command is run without the hook' >&2
"$@"
`

// pyReportHook is the pytest row's ReportHook: cmd run through
// pyReportScript, its words unchanged.
var pyReportHook = &ReportHook{
	Wrap: func(cmd []string) []string {
		return append([]string{"sh", "-c", pyReportScript, pyReportArgv0}, cmd...)
	},
	Wraps: pyReportWraps,
	// The qualifier is deliberate: the run's environment does differ, and a
	// suite that asserts on its own environment (PYTHONPATH, PYTEST_PLUGINS,
	// the registered plugins, sys.modules) could tell. "What passes and
	// fails is unchanged" would be a claim about every suite, which nobody
	// ran.
	Says: "corral runs every graded run through `sh -c` with a pytest plugin of its own loaded (named in PYTEST_PLUGINS, " +
		"from a temporary directory put last on PYTHONPATH, which is set where you had none), and reads which test failed from what the plugin records, " +
		"whatever the project's report settings print; a run the plugin does not load in is repeated without it, so its result is the command's own. " +
		"A suite that asserts on PYTHONPATH, PYTEST_PLUGINS or the plugins registered could behave differently",
}

// pyReportWraps reports whether a pytest command named at word index at is
// worth wrapping: false for a launcher that plainly builds its own
// environment, in which the plugin could not load.
//
// THIS LIST IS A COST SAVING, NOT THE CORRECTNESS GUARANTEE. A command it
// misses is still graded right, by the wrapper's fallback (pyReportScript):
// the hooked run fails at start-up, and the command is run again without the
// hook. That costs a failed start and a repeat on every graded run, which is
// what this avoids for the shapes known to need it, so the list need not be
// complete: tox and nox (their own virtualenv and environment), docker and
// podman (a container sees neither the temporary directory nor the
// variables), and a Python interpreter given -I or -E, which ignores
// PYTHONPATH. A command it refuses keeps the report flags and nothing else.
func pyReportWraps(cmd []string, at int) bool {
	python := false
	for _, w := range cmd[:at] {
		base := filepath.Base(w)
		switch base {
		case "tox", "nox", "docker", "podman":
			return false
		}
		if strings.HasPrefix(base, "python") || strings.HasPrefix(base, "pypy") {
			python = true
			continue
		}
		// A short-option cluster of the interpreter's holding I or E
		// (-I, -E, -sE): isolated mode, or ignore the PYTHON* variables.
		if python && strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.ContainsAny(w[1:], "IE") {
			return false
		}
	}
	return true
}

// pyReportRecord is one failure the plugin recorded.
type pyReportRec struct{ kind, id string }

// pyReportBlock is the records of the LAST complete marker block in output
// (the wrapper prints it after everything pytest printed), and whether there
// is one. A begin with no end after it is not a block, and when the LAST
// begin has no end there is no block at all, an earlier complete one
// included: the record is incomplete, and an earlier block is then something
// the tests printed (see "WHAT A TEST CAN FORGE" above). An elision line (a
// capture that kept both ends of an over-long output, clip.Elision) voids
// everything before it and any block it falls inside: a block with its begin
// in the kept head and its end in the kept tail has lost records, and is not
// a block. Lines are matched
// whole, with only a trailing carriage return dropped, so an id keeps every
// space it has.
func pyReportBlock(output string) (recs []pyReportRec, ok bool) {
	var cur []pyReportRec
	open := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case clip.IsElision(line):
			// Part of the run's output is not in this string. Nothing before
			// this line can be the last block, and a block open here has lost
			// records: neither is trusted. A block wholly after it still is.
			recs, ok, cur, open = nil, false, nil, false
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
	if open {
		return nil, false
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
