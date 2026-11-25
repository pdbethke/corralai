// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"

	"github.com/pdbethke/corralai/internal/clip"
)

// recordToken is the placeholder a row's record args carry for the record
// file's path; the wrapper substitutes the real path (or /dev/null when it
// cannot make a temporary directory, so the run is never changed by a
// hook that could not be set up).
//
// It is a placeholder rather than a path corral picks in Go because the path
// must exist where the command RUNS: inside a jail that is the jail's own
// /tmp, which the process building the command never sees. Only the wrapper,
// running there, can make the directory. The token is a word no project's
// command plausibly carries; a word holding only part of it is passed on
// unchanged.
const recordToken = "__CORRAL_RECORD__"

// recordArgv0 is the $0 the record wrapper's script runs under: the word
// after the script, before the record's format and the operator's command.
const recordArgv0 = "corral-record"

// The marker lines around a runner's record of its run, printed by
// recordScript after the runner has exited:
//
//	corral-record-begin junit
//	<?xml version="1.0"?>
//	<testsuites>…</testsuites>
//	corral-record-end
//
// The begin line names the record's format, so one reader can tell a JUnit
// XML from a jest JSON without knowing which row produced the run. The body
// is the file as the runner wrote it, every line, with a final newline
// supplied by the wrapper when the file had none (so the end marker is a
// line of its own).
//
// WHAT A TEST CAN FORGE is what it can forge under pytest's hook (see
// python_report.go): a test can print a block, or a line equal to a marker
// that ends up inside a record (a JUnit file carries a test's captured
// output). The reader takes the LAST complete block, which the wrapper prints
// after the runner has exited, trusts none when the last begin has no end,
// and the defence against a forged name is unchanged and elsewhere: a kill is
// credited to a named test only when that test, run alone, fails on the fault
// and passes on the clean code (adequacy's confirmation). In particular, when
// the wrapper prints no block of its own (an empty or missing record, the
// /dev/null fallback), a block a TEST printed is the last complete block and
// is read as genuine: a body is never proof that corral wrote it, and nothing
// that reads one may treat it as such.
const (
	recordBegin = "corral-record-begin"
	recordEnd   = "corral-record-end"
)

// recordScript is the wrapper a graded run of a record-file runner goes
// through. Like pyReportScript it holds only corral's own text: its first
// positional parameter is the record's format (corral's constant), the rest
// is the command, run as `"$@"` and never spliced into the script, so the
// shell never re-reads a word of it.
//
// The token is replaced word by word with the `for a do shift; …; set --
// "$@" "$a"; done` rotation: `for` expands the word list once, before the
// loop, so each pass takes the first word off and puts its rewritten form on
// the end, and after one pass per word the list is the command again, in
// order. A word is rewritten only through parameter expansion whose PATTERN
// is the token (letters and underscores, no glob character), so the word
// itself is only ever the subject of a match: a space, quote, `$` or `*` in
// it is never read by the shell, and a word without the token is put back
// exactly as it was. Every occurrence in a word is replaced (`--x=TOKEN`
// becomes `--x=<path>`).
//
// THE GUARANTEE, as pytest's hook: the hook never changes what the run's
// exit code would have been without it.
//
//   - The command runs once, and its exit code is the script's.
//   - If the temporary directory cannot be made (no mktemp, an unwritable
//     temporary directory), the token becomes /dev/null, one line on stderr
//     says so, and no block is printed: the runner writes its record where
//     nothing keeps it, and its failures are read from what it printed, as
//     they were before this hook existed. /dev/null and not the token's
//     removal, because removing words would leave a flag with no argument
//     (`--log-junit` would take the next word as its file).
//   - The block is printed only when the record file exists and is not
//     empty, after a blank line (which keeps the begin marker on a line of
//     its own when the runner's last line had no newline), after the command
//     has exited.
//
// The record file is `corral-record` inside a directory made for this run
// alone (mktemp -d), so it cannot collide with a file the project writes, and
// the directory is removed when the script exits. A run killed outright (a
// timeout's SIGKILL) leaves it behind: inside a jail it goes with the jail's
// /tmp, on --substrate workspace it stays in the runner's temporary
// directory. `tail -c 1` decides whether the record ended with a newline; a
// shell without it supplies none, the end marker then shares the record's
// last line, and the block is incomplete, so it is not read: a missing tool
// costs the record, never the run. For the same reason the end marker is
// printed only when cat succeeded: a record it could not read, or read only
// part of (an I/O error mid-file), leaves a begin with no end, which the
// reader does not trust, where a closed block would pass a truncated or empty
// body for the runner's whole account (and an empty one reads as "no test
// failed").
//
// Every variable the script sets is prefixed `__corral_`. POSIX sh keeps a
// variable it imported from the environment exported, so assigning a plain
// name (`d`, `rc`) the operator's environment happens to export would change
// what the runner inherits; the prefix is a name no project plausibly
// exports.
const recordScript = `__corral_fmt=$1
shift
if __corral_d=$(mktemp -d 2>/dev/null) && [ -n "$__corral_d" ]; then
  trap 'rm -rf "$__corral_d"' EXIT
  __corral_r="$__corral_d/corral-record"
else
  __corral_d=
  __corral_r=/dev/null
  echo 'corral: its record hook could not make a temporary directory, so the record goes to /dev/null and failures are read from what the runner printed' >&2
fi
for __corral_a do
  shift
  __corral_b=
  while :; do
    case $__corral_a in
      *` + recordToken + `*) __corral_b="$__corral_b${__corral_a%%` + recordToken + `*}$__corral_r"; __corral_a=${__corral_a#*` + recordToken + `} ;;
      *) break ;;
    esac
  done
  set -- "$@" "$__corral_b$__corral_a"
done
"$@"
__corral_rc=$?
if [ -n "$__corral_d" ] && [ -s "$__corral_r" ]; then
  echo
  printf '%s %s\n' "` + recordBegin + `" "$__corral_fmt"
  if cat "$__corral_r"; then
    [ -n "$(tail -c 1 "$__corral_r" 2>/dev/null)" ] && echo
    echo "` + recordEnd + `"
  fi
fi
exit "$__corral_rc"
`

// FileRecordHook is the ReportHook of a runner that writes its own record
// of the run to a file named on its command line. args returns the words to
// insert, carrying recordToken where the path goes; they are inserted at
// runnerOptionsEnd (ReportCmd's placement rule). format names the record's
// format for its reader ("junit", "jest-json", "rspec-json").
//
// Such a runner needs no plugin, and its record is the runner's own account
// of every test it ran, which no report setting of the project's reaches.
// The args are part of the hook, not of the row's ReportArgs, because they
// are worth nothing without the wrapper: where the hook is not applied (on
// Windows, or a command its row declines) they are not added, since the
// token would reach the runner as a file name.
func FileRecordHook(args func() []string, format string, says string) *ReportHook {
	return &ReportHook{
		Args: args,
		Wrap: func(cmd []string) []string {
			return append([]string{"sh", "-c", recordScript, recordArgv0, format}, cmd...)
		},
		Says: says,
	}
}

// RecordBlock is the last COMPLETE `corral-record-begin <format>` …
// `corral-record-end` block in out: its format and body. ok is false when
// there is none, or the last begin has no end (a run cut short, or a test
// that printed a begin line) — the reader then falls back to printed lines.
// A begin line naming no format is not a begin. The body's lines are the
// record's, each with a trailing carriage return dropped, joined by "\n",
// with no final newline.
func RecordBlock(out string) (format, body string, ok bool) {
	return lastBlock(out, func(line string) (string, bool) {
		f, found := strings.CutPrefix(line, recordBegin+" ")
		return f, found && f != ""
	}, recordEnd)
}

// withoutRecordBlock is out with the block RecordBlock reads (begin and end
// lines included) taken out, so a reader of the runner's printed lines does
// not read the record's lines as printed ones; out itself when RecordBlock
// finds no block.
func withoutRecordBlock(out string) string {
	if _, _, ok := RecordBlock(out); !ok {
		return out
	}
	lines := strings.Split(out, "\n")
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSuffix(lines[i], "\r") == recordEnd {
			end = i
			break
		}
	}
	for j := end - 1; j >= 0; j-- {
		if f, found := strings.CutPrefix(strings.TrimSuffix(lines[j], "\r"), recordBegin+" "); found && f != "" {
			return strings.Join(append(lines[:j:j], lines[end+1:]...), "\n")
		}
	}
	return out
}

// lastCompleteBlock is the one "last complete block between begin and end"
// rule; pytest's reader and RecordBlock both use it.
//
// The rule, for every marker pair: lines are matched whole, a trailing
// carriage return dropped. A begin starts a block, discarding one still
// open; an end closes the open block, and is ignored when none is open. The
// answer is the last block closed, and there is none when the LAST begin has
// no end, an earlier complete block included: the record is then incomplete,
// and an earlier block is something the tests printed. An elision line (a
// capture that kept both ends of an over-long output, clip.Elision) voids
// everything before it and any block it falls inside: a block with its begin
// in the kept head and its end in the kept tail has lost lines.
func lastCompleteBlock(out, begin, end string) (body string, ok bool) {
	_, body, ok = lastBlock(out, func(line string) (string, bool) { return "", line == begin }, end)
	return body, ok
}

// lastBlock is lastCompleteBlock with a begin line that may carry an
// argument (RecordBlock's format), which isBegin returns. The two share it
// so the rule is written once.
func lastBlock(out string, isBegin func(line string) (arg string, ok bool), end string) (arg, body string, ok bool) {
	var cur []string
	var curArg string
	open := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if clip.IsElision(line) {
			arg, body, ok, cur, open = "", "", false, nil, false
			continue
		}
		if a, isB := isBegin(line); isB {
			cur, curArg, open = nil, a, true
			continue
		}
		if line == end {
			if open {
				arg, body, ok, open = curArg, strings.Join(cur, "\n"), true, false
			}
			continue
		}
		if open {
			cur = append(cur, line)
		}
	}
	if open {
		return "", "", false
	}
	return arg, body, ok
}
