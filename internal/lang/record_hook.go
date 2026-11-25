// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/clip"
)

// recordToken is the placeholder a row's record args carry for the record
// file's path; the wrapper substitutes the real path. When it cannot make a
// temporary directory it substitutes /dev/null, or, for a RecordFile with
// DropWhenUnset, leaves the hook's words out, so the run is never changed by
// a hook that could not be set up.
//
// It is a placeholder rather than a path corral picks in Go because the path
// must exist where the command RUNS: inside a jail that is the jail's own
// /tmp, which the process building the command never sees. Only the wrapper,
// running there, can make the directory. The token is a word no project's
// command plausibly carries; a word holding only part of it is passed on
// unchanged.
const recordToken = "__CORRAL_RECORD__"

// recordArgv0 is the $0 the record wrapper's script runs under: the word
// after the script, before the record's format, fallback and suffixes and
// the operator's command.
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
// five positional parameters are corral's constants (the record's format,
// the fallback, `null` or `drop:<from>:<n>`, the record's suffixes joined
// by "/", and the companion file's suffix and text, both empty for none,
// RecordFile), the rest is the command, run as `"$@"` and never spliced
// into the script, so the shell never re-reads a word of it.
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
// becomes `--x=<path>`). The companion's text is rewritten by the same
// function (`__corral_sub`) before the command runs, and written with
// printf '%s', so it too is only ever the subject of a match.
//
// THE GUARANTEE, as pytest's hook: the hook never changes what the run's
// exit code would have been without it.
//
//   - The command runs once, and its exit code is the script's.
//   - If the temporary directory cannot be made (no mktemp, an unwritable
//     temporary directory), or a hook's companion file cannot be written
//     there (Companion), the token becomes /dev/null, one line on stderr
//     says so, and no block is printed: the runner writes its record where
//     nothing keeps it, and its failures are read from what it printed, as
//     they were before this hook existed. /dev/null and not the token's
//     removal, because removing words would leave a flag with no argument
//     (`--log-junit` would take the next word as its file). A hook whose
//     RecordFile says DropWhenUnset instead leaves every word it added out
//     of the command (the `drop:<from>:<n>` fallback word says which), which
//     then is the operator's own: its runner cannot be pointed at /dev/null
//     (see RecordFile).
//   - The block is printed only when a record file exists and is not empty,
//     after a blank line (which keeps the begin marker on a line of its own
//     when the runner's last line had no newline), after the command has
//     exited. The record file is the record path plus the first of the
//     RecordFile's suffixes that names such a file (the path itself when it
//     has none).
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
__corral_fb=$2
__corral_sfx=$3
__corral_cs=$4
__corral_cc=$5
shift 5
__corral_from=0 __corral_n=0 __corral_i=0
case $__corral_fb in
  drop:*:*)
    __corral_from=${__corral_fb#drop:}
    __corral_n=${__corral_from#*:}
    __corral_from=${__corral_from%%:*}
    __corral_fb=drop ;;
esac
__corral_sub() {
  __corral_b=
  while :; do
    case $__corral_a in
      *` + recordToken + `*) __corral_b="$__corral_b${__corral_a%%` + recordToken + `*}$__corral_r"; __corral_a=${__corral_a#*` + recordToken + `} ;;
      *) break ;;
    esac
  done
  __corral_b=$__corral_b$__corral_a
}
__corral_d= __corral_why='make a temporary directory'
if __corral_t=$(mktemp -d 2>/dev/null) && [ -n "$__corral_t" ]; then
  trap 'rm -rf "$__corral_t"' EXIT
  __corral_d=$__corral_t
  __corral_r="$__corral_d/corral-record"
  if [ -n "$__corral_cs" ]; then
    __corral_a=$__corral_cc
    __corral_sub
    case $__corral_r in
      /*[!A-Za-z0-9/._-]*|[!/]*) false ;;
      *) printf '%s' "$__corral_b" > "$__corral_r$__corral_cs" ;;
    esac || {
      rm -rf "$__corral_t"
      __corral_d= __corral_why='write the file its runner reads beside the record'
    }
  fi
fi
if [ -z "$__corral_d" ]; then
  __corral_r=/dev/null
  if [ "$__corral_fb" = drop ]; then
    echo "corral: its record hook could not $__corral_why, so it leaves its record option out of the command and failures are read from what the runner printed" >&2
  else
    echo "corral: its record hook could not $__corral_why, so the record goes to /dev/null and failures are read from what the runner printed" >&2
  fi
fi
for __corral_a do
  shift
  __corral_i=$((__corral_i + 1))
  if [ -z "$__corral_d" ] && [ "$__corral_fb" = drop ] && [ "$__corral_i" -gt "$__corral_from" ] && [ "$__corral_i" -le $((__corral_from + __corral_n)) ]; then
    continue
  fi
  __corral_sub
  set -- "$@" "$__corral_b"
done
"$@"
__corral_rc=$?
__corral_f=
if [ -n "$__corral_d" ]; then
  __corral_l=$__corral_sfx
  while :; do
    __corral_s=${__corral_l%%/*}
    if [ -s "$__corral_r$__corral_s" ]; then
      __corral_f=$__corral_r$__corral_s
      break
    fi
    case $__corral_l in
      */*) __corral_l=${__corral_l#*/} ;;
      *) break ;;
    esac
  done
fi
if [ -n "$__corral_f" ]; then
  echo
  printf '%s %s\n' "` + recordBegin + `" "$__corral_fmt"
  if cat "$__corral_f"; then
    [ -n "$(tail -c 1 "$__corral_f" 2>/dev/null)" ] && echo
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
	return FileRecordHookAt(args, format, says, RecordFile{})
}

// RecordFile is where a record-file runner writes its record, relative to
// the path its record args name, and what the wrapper does with those args
// when it cannot make a directory for the record. The zero value is the
// PHPUnit shape: the record is the path itself, and the fallback sends it
// to /dev/null.
//
// Suffixes are for a runner that derives its record's name from the path it
// is given. swift-testing is one, and not the same way in every release:
// given `--xunit-output <p>.xml`, Swift 6.4.2 writes `<p>-swift-testing.xml`
// and 6.3.2 writes `<p>.xml` (both measured, capture-runner-fixtures.sh
// swift-xunit-files-*.txt). The record is the record path plus the first
// suffix, in order, that names a file that is not empty, so one row reads
// either release; an empty suffix is the path itself. A suffix is a file
// name's tail: no "/" (the wrapper joins them with it), no space, and none
// of the characters a shell pattern or quote gives meaning to.
//
// DropWhenUnset is for a runner that cannot be pointed at /dev/null. A name
// derived from /dev/null is a file beside it, which a runner may not write,
// and swift-testing 6.4.2 then fails a run that passed ("error: invalid
// access to /dev/null-swift-testing", exit 1); node --test fsyncs its
// reporter's destination, which /dev/null refuses (node 22.23.5: "EINVAL:
// invalid argument, fsync", exit 1 on a run that passed). The hook must
// never change the exit code, so the wrapper instead leaves out every word
// the hook added (its args, all of them, not only the one carrying the
// token), and the command is the operator's own. All of them, because a
// hook's words can depend on each other: node pairs each --test-reporter
// with a --test-reporter-destination by position and refuses a run whose
// counts differ, so leaving out the destination alone would leave its
// reporter unpaired (ERR_INVALID_ARG_VALUE, measured on node 22.23.5).
// Wrap finds the words where ReportCmd put them (the last run of them in
// the command) and hands the wrapper their place.
//
// Companion is a file the wrapper writes before the command runs, for a
// runner that is told where its record goes by a file it reads rather than
// by a word: cargo-nextest takes a config file (`--tool-config-file
// <tool>:<absolute path>`) whose junit path names the record, and no option
// names the record itself. The zero Companion is none.
type RecordFile struct {
	Suffixes      []string
	DropWhenUnset bool
	Companion     Companion
}

// Companion is a file a record-file hook writes in its own directory before
// the command runs: at the record path plus Suffix, holding Content with
// every recordToken replaced by the record path, exactly as the row gives
// it otherwise. A hook's args name it the same way, by the token plus
// Suffix (cargo-nextest's `corral:<token>.toml`).
//
// THE SAME GUARANTEE as the record's. Content is corral's own constant
// text, handed to the wrapper as a positional parameter and written with
// printf, so the shell reads none of it; no word of the operator's command
// ever reaches it. The file goes in the directory made for this run alone
// and is removed with it. Because Content is a file in the runner's own
// syntax (TOML for nextest), the record's path must be one that syntax
// carries as it stands: the wrapper writes the companion only when the
// path is absolute and made of letters, digits and `/._-`, which a
// temporary directory's path is unless TMPDIR names an unusual one. Any
// other path, or a companion it could not write, is a hook that could not
// be set up, handled as when mktemp fails: the directory is removed, the
// hook's words are left out (a Companion requires DropWhenUnset, since a
// /dev/null fallback would name a companion that was never written), one
// line on stderr says so, and the run is the operator's command.
type Companion struct {
	Suffix  string
	Content string
}

// recordSuffix is what a RecordFile suffix may hold.
var recordSuffix = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)

// FileRecordHookAt is FileRecordHook for a runner whose record's file, or
// whose fallback, is not the zero RecordFile's. It panics on a suffix
// recordSuffix refuses: every caller is a row of the runner table, built
// when the package loads, so a bad one fails every test.
//
// The wrapper's fallback word is `null`, or for DropWhenUnset
// `drop:<from>:<n>`: the hook's n words start after the command's first
// <from> words. A command that does not carry the hook's words (Wrap
// called on its own, not through ReportCmd) is `drop:0:0`, which leaves
// out nothing, so it runs as given.
func FileRecordHookAt(args func() []string, format, says string, f RecordFile) *ReportHook {
	for _, sfx := range f.Suffixes {
		if !recordSuffix.MatchString(sfx) {
			panic("lang: record suffix " + strconv.Quote(sfx) + " is not a file name's tail")
		}
	}
	if c := f.Companion; c != (Companion{}) {
		records := f.Suffixes
		if len(records) == 0 {
			records = []string{""} // the record is the path itself
		}
		switch {
		case !f.DropWhenUnset:
			panic("lang: a record hook with a companion file must drop its words on fallback")
		case c.Suffix == "" || !recordSuffix.MatchString(c.Suffix):
			panic("lang: companion suffix " + strconv.Quote(c.Suffix) + " is not a file name's tail")
		case slices.Contains(records, c.Suffix):
			panic("lang: companion suffix " + strconv.Quote(c.Suffix) + " is a record's: the record would be read from the companion")
		}
	}
	suffixes := strings.Join(f.Suffixes, "/")
	return &ReportHook{
		Args: args,
		Wrap: func(cmd []string) []string {
			fallback := "null"
			if f.DropWhenUnset {
				from, n := hookWordsAt(cmd, args())
				fallback = "drop:" + strconv.Itoa(from) + ":" + strconv.Itoa(n)
			}
			return append([]string{"sh", "-c", recordScript, recordArgv0, format, fallback, suffixes, f.Companion.Suffix, f.Companion.Content}, cmd...)
		},
		Says: says,
	}
}

// hookWordsAt is where the last run of words sits in cmd, and its length;
// 0, 0 when cmd does not carry them (or there are none).
func hookWordsAt(cmd, words []string) (from, n int) {
	if len(words) == 0 {
		return 0, 0
	}
	for i := len(cmd) - len(words); i >= 0; i-- {
		if slices.Equal(cmd[i:i+len(words)], words) {
			return i, len(words)
		}
	}
	return 0, 0
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
