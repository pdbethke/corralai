// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// A test UNIT is the smallest thing a runner runs on its own: a test file
// for the JavaScript runners. Evidence pairing runs each unit the operator's
// command collects once, under coverage, to learn which unit executes which
// source file. The units are the runner's own listing of the operator's
// command, never a walk of the tree, so tests the operator excluded are never
// run.

// unitsCwdMarker leads ListUnitsCmd's output: the working directory the
// listing ran in, as the shell prelude prints it. jest and vitest list
// absolute paths, and a unit is named relative to the repository, which is
// the command's working directory — inside the jail as on the host. The
// directory is printed physically (`pwd -P`) because jest and vite resolve
// symlinks in the paths they list.
const unitsCwdMarker = "corral-units-cwd "

// unitsPrelude prints the marker line and then execs the listing command,
// passed as the shell's positional parameters, so no word of it is ever
// re-read by the shell.
const unitsPrelude = `printf 'corral-units-cwd %s\n' "$(pwd -P)" && exec "$@"`

// ListUnitsCmd is the command that lists the units argv collects, from its
// runner, under the working-directory prelude ParseUnitList reads.
func ListUnitsCmd(p Plugin, inv Invocation) ([]string, bool) {
	r, ok := RunnerFor(p, inv)
	if !ok || r.ListUnits == nil || r.ParseUnits == nil {
		return nil, false
	}
	cmd, ok := inv.shape(p, r.ListUnits)
	if !ok {
		return nil, false
	}
	return append([]string{"sh", "-c", unitsPrelude, "corral-units"}, cmd...), true
}

// ParseUnitList reads ListUnitsCmd's stdout into repository-relative unit
// names, sorted and deduplicated; nil when the output does not open with the
// working-directory line or the runner's listing cannot be read. An absolute
// path outside the working directory names no repository file and is left
// out. A unit the runner cannot run without also running another listed one
// (UnitCoruns) stays in the list, marked: UnitRunCmd refuses it and UnitName
// is its name.
func ParseUnitList(p Plugin, inv Invocation, stdout string) []string {
	r, ok := RunnerFor(p, inv)
	if !ok || r.ParseUnits == nil {
		return nil
	}
	first, rest, _ := strings.Cut(stdout, "\n")
	cwd, ok := strings.CutPrefix(strings.TrimRight(first, "\r"), unitsCwdMarker)
	if !ok || !filepath.IsAbs(cwd) {
		return nil
	}
	listed := r.ParseUnits(rest)
	if listed == nil {
		return nil
	}
	units := []string{}
	for _, u := range listed {
		if filepath.IsAbs(u) {
			rel, err := filepath.Rel(cwd, u)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			u = rel
		}
		u = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(u)), "./")
		// A relative unit is held to cwd as an absolute one is above: one
		// that climbs out of it is no unit of this repository.
		if u == ".." || strings.HasPrefix(u, "../") {
			continue
		}
		if u != "" && u != "." {
			units = append(units, u)
		}
	}
	slices.Sort(units)
	units = slices.Compact(units)
	if r.UnitCoruns == nil {
		return units
	}
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u
		for _, other := range units {
			if other != u && r.UnitCoruns(u, other) {
				out[i] = markNotAlone(u)
				break
			}
		}
	}
	return out
}

// UnitNoCoverageReason is argv's runner's UnitNoCoverage: why one of its
// unit runs whose coverage names no project file gave none, or "".
func UnitNoCoverageReason(p Plugin, inv Invocation) string {
	r, ok := RunnerFor(p, inv)
	if !ok {
		return ""
	}
	return r.UnitNoCoverage
}

// EvidenceUnitCmd is unitCmd (a UnitRunCmd result) as the unit evidence
// pass runs it: with the runner's evidence-only flags (Runner.EvidenceArgs),
// or unchanged when its row has none. Grading never calls it.
func EvidenceUnitCmd(p Plugin, inv Invocation, unitCmd []string) []string {
	r, ok := RunnerFor(p, inv)
	if !ok || r.EvidenceArgs == nil {
		return unitCmd
	}
	return r.EvidenceArgs(slices.Clone(unitCmd))
}

// EvidenceCache is a tool cache the unit evidence pass shares among its
// units (Runner.EvidenceCache). The pass warms it with its first unit, run
// once with the cache writable, and every later unit sees it FROZEN: a
// copy-on-write overlay whose writes are discarded with the run, so no
// unit's run can change what another unit reads. It lives at a fixed path
// inside the jail. Env, when set, is the environment variable the jail
// points at that path (GOCACHE); Args, when set, is the words a unit
// command takes to use the cache at dir, appended after EvidenceArgs'. A
// pass that cannot share the cache gives neither, and each unit starts
// from an empty cache, as every jailed run did before.
//
// Name is what the pass calls the cache when it says it was not shared;
// Env stands in when Name is "". Unsupported, when set, is why the tool
// the operator's argv runs would not take Args — read from the tree as the
// jail sees it (read: the seed, then the dependency directories mounted
// into it), never learned by running the tool and reading an error — or ""
// when it takes them. A cache it gives a reason for is not shared, and the
// pass says why.
type EvidenceCache struct {
	Name        string
	Env         string
	Args        func(dir string) []string
	Unsupported func(argv []string, read func(rel string) ([]byte, error)) string
}

// EvidenceCacheFor is argv's runner's EvidenceCache, or nil when the row
// shares none.
func EvidenceCacheFor(p Plugin, inv Invocation) *EvidenceCache {
	r, ok := RunnerFor(p, inv)
	if !ok {
		return nil
	}
	return r.EvidenceCache
}

// EvidenceNote is one sentence naming what the evidence pass changed in
// argv's command, for the scan line and for a unit failure's reason (the
// operator's own run of a unit may pass where the evidence pass's does
// not): the words EvidenceArgs adds, and the operator's words they replace.
// It compares meaning, not spelling: `--pool threads` and `--pool=threads`
// are the same choice, so a command already carrying the pass's flags gets
// "" — as does a row with no evidence flags.
func EvidenceNote(p Plugin, inv Invocation) string {
	r, ok := RunnerFor(p, inv)
	if !ok || r.EvidenceArgs == nil {
		return ""
	}
	added, replaced := evidenceChange(inv.Argv, r.EvidenceArgs(slices.Clone(inv.Argv)))
	if len(added) == 0 {
		return ""
	}
	note := "the evidence pass runs each unit with " + strings.Join(added, " ")
	if len(replaced) > 0 {
		note += " in place of " + strings.Join(replaced, " ")
	}
	return note
}

// evidenceChange is what turning before into after added and replaced, by
// meaning. A flag the change removed, followed by a word the change also
// removed, is one option written in two words (`--pool forks`, go's
// `-count 3`), so it is compared as `--pool=forks` and named as the
// operator wrote it.
func evidenceChange(before, after []string) (added, replaced []string) {
	kept := map[string]bool{}
	for _, w := range after {
		kept[w] = true
	}
	type option struct{ written, meaning string }
	var opts []option
	for i := 0; i < len(before); i++ {
		w := before[i]
		if strings.HasPrefix(w, "-") && w != "-" && w != "--" && !strings.Contains(w, "=") && !kept[w] &&
			i+1 < len(before) && !strings.HasPrefix(before[i+1], "-") && !kept[before[i+1]] {
			opts = append(opts, option{w + " " + before[i+1], w + "=" + before[i+1]})
			i++
			continue
		}
		opts = append(opts, option{w, w})
	}
	had := map[string]bool{}
	for _, o := range opts {
		had[o.meaning] = true
		if !kept[o.meaning] {
			replaced = append(replaced, o.written)
		}
	}
	for _, w := range after {
		// Only an option is a change the operator is told of. A row adds
		// a bare word or "--" only to keep its command's meaning — the
		// "--" and ./... gotestsum's row writes for a command with no go
		// test arguments, which bare gotestsum already runs — and each
		// unit command names its own unit there anyway, so naming those
		// words would say the pass ran something it did not. A row adds
		// its options as single words (`--pool=threads`, `-count=1`).
		if !had[w] && strings.HasPrefix(w, "-") && w != "--" {
			added = append(added, w)
		}
	}
	return added, replaced
}

// UnitName is a listed unit without the mark ParseUnitList gives one that
// cannot run alone.
func UnitName(unit string) string { return listedName(unit) }

// UnitRunCmd is argv's runner narrowed to the one listed unit; ok=false when
// the runner has no unit form for argv, or the unit cannot run alone.
func UnitRunCmd(p Plugin, inv Invocation, unit string) ([]string, bool) {
	r, ok := RunnerFor(p, inv)
	if !ok || r.UnitCmd == nil || unit == "" || unit != listedName(unit) {
		return nil, false
	}
	return inv.shape(p, func(w []string) ([]string, bool) { return r.UnitCmd(w, unit) })
}

// UnitsRunCmd is argv's runner narrowed to exactly the listed units, shaped
// as UnitRunCmd is; ok=false when the runner has no multi-unit form for
// argv, or a unit cannot run alone. A run of several units can still collect
// one nobody named: the row's UnitsCoruns says so.
func UnitsRunCmd(p Plugin, inv Invocation, units []string) ([]string, bool) {
	r, ok := RunnerFor(p, inv)
	if !ok || r.UnitsCmd == nil || slices.ContainsFunc(units, func(u string) bool { return u != listedName(u) }) {
		return nil, false
	}
	return inv.shape(p, func(w []string) ([]string, bool) { return r.UnitsCmd(w, units) })
}

// unitSet reports whether units is a set a multi-unit command can take: at
// least one, no empty name, none twice. An empty list is never "every unit",
// and a name twice is refused rather than run twice (PHPUnit runs a file it
// is given twice twice: capture phpunit-args-union).
func unitSet(units []string) bool {
	if len(units) == 0 || slices.Contains(units, "") {
		return false
	}
	seen := make(map[string]bool, len(units))
	for _, u := range units {
		if seen[u] {
			return false
		}
		seen[u] = true
	}
	return true
}

// oneUnit is a row's UnitCmd built from its UnitsCmd, so the one-unit and
// multi-unit forms cannot drift: UnitCmd(argv, u) is UnitsCmd(argv, [u]),
// refusals included.
func oneUnit(units func(argv, units []string) ([]string, bool)) func([]string, string) ([]string, bool) {
	return func(argv []string, unit string) ([]string, bool) { return units(argv, []string{unit}) }
}

// eachUnitCoruns is a row's UnitsCoruns for a runner whose multi-unit
// selection is the union of its one-unit selections: other is collected when
// nobody named it and one named unit's selection also takes it.
func eachUnitCoruns(coruns func(unit, other string) bool) func([]string, string) bool {
	return func(units []string, other string) bool {
		if slices.Contains(units, other) {
			return false
		}
		return slices.ContainsFunc(units, func(u string) bool { return coruns(u, other) })
	}
}

// repoRelative is testFile as a unit name: repository-relative, slash-
// separated, cleaned; ok=false for no file, an absolute path, or one that
// climbs out of the repository, as ParseUnitList drops such a listed unit.
func repoRelative(testFile string) (string, bool) {
	if testFile == "" || filepath.IsAbs(testFile) || path.IsAbs(filepath.ToSlash(testFile)) {
		return "", false
	}
	f := path.Clean(filepath.ToSlash(testFile))
	if f == "." || f == ".." || strings.HasPrefix(f, "../") {
		return "", false
	}
	return f, true
}

// fileUnits is unitSet for units appended as bare words: none may start
// with "-", which the runner would read as an option.
func fileUnits(units []string) bool {
	return unitSet(units) && !slices.ContainsFunc(units, func(u string) bool { return strings.HasPrefix(u, "-") })
}

// fileUnitForFile is UnitForFile for a runner whose unit IS a test file
// (jest, vitest, node, rspec, PHPUnit): the file itself, named as the
// listing names one.
func fileUnitForFile(_, testFile string) (string, bool) { return repoRelative(testFile) }

// decodeArray decodes the JSON array a runner prints on stdout into v,
// starting at the first line that begins with "[" — decodeReport's rule for
// a launcher's banner lines, for a report that is an array.
func decodeArray(stdout string, v any) bool {
	i := 0
	for line := range strings.Lines(stdout) {
		if strings.HasPrefix(line, "[") {
			return json.NewDecoder(strings.NewReader(stdout[i:])).Decode(v) == nil
		}
		i += len(line)
	}
	return false
}

// withoutFlag is argv without the flag names, in either spelling (`--x=v`,
// or `--x v` when the next word is not a flag). A one-unit run drops what
// the listing already applied and what would narrow the unit itself: a
// shard of one file can be empty, and an empty run exits 0 (measured on
// node 22.23.4: `node --test --test-shard=2/2 a.node.test.js` ran no test
// and passed).
func withoutFlag(argv []string, names ...string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if slices.Contains(names, a) {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
			}
			continue
		}
		if f, _, ok := strings.Cut(a, "="); ok && slices.Contains(names, f) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// dropFileArgs is args (the words after a runner's name and subcommand)
// without its file arguments, for a row that replaces the operator's files
// with one unit. takesValue says, for a flag written without "=", whether
// the runner reads the next word as its value (value) and whether it knows
// the flag at all (known). A bare word after a flag the row cannot place
// could be either a value or a file, so the whole command is refused, as is
// "--" and, unless flagsAfterFiles, a flag after the first file (node hands
// those to the test files rather than reading them).
func dropFileArgs(args []string, flagsAfterFiles bool, takesValue func(flag string) (value, known bool)) ([]string, bool) {
	kept, _, ok := splitFileArgs(args, flagsAfterFiles, takesValue)
	return kept, ok
}

// splitFileArgs is dropFileArgs that also returns the file arguments it
// dropped, for a row that must look at them (rspec refuses a file with a
// line number).
func splitFileArgs(args []string, flagsAfterFiles bool, takesValue func(flag string) (value, known bool)) (kept, files []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return nil, nil, false
		case !strings.HasPrefix(a, "-") || a == "-":
			files = append(files, a)
			continue
		case len(files) > 0 && !flagsAfterFiles:
			return nil, nil, false
		}
		kept = append(kept, a)
		bareNext := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
		if strings.Contains(a, "=") {
			continue
		}
		value, known := takesValue(a)
		switch {
		case !known && bareNext, value && !bareNext && i+1 < len(args):
			return nil, nil, false
		case value && bareNext:
			i++
			kept = append(kept, args[i])
		}
	}
	return kept, files, true
}

// shortBundleSets reports whether a single-dash word of argv sets one of the
// short options in refused, written attached or bundled: `-Etest(x)`, or
// `-rE` after short options that take no value (flags). A short option
// that takes a value ends the bundle, the rest being its value. hasFlag
// sees only `-E` and `-E=x`, and an attached filter it misses is ORed with a
// unit's own (measured on cargo-nextest 0.9.148: `'-Etest(negates)' -E
// 'binary_id(=nx::it)'` ran two binaries' tests).
func shortBundleSets(argv []string, flags, refused string) bool {
	for _, a := range argv {
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		for _, c := range a[1:] {
			if strings.ContainsRune(refused, c) {
				return true
			}
			if !strings.ContainsRune(flags, c) {
				break
			}
		}
	}
	return false
}
