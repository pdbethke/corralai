// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
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
