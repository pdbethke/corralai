// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
)

// A Go test unit is a PACKAGE: `go test` builds one test binary per package,
// and a package is what it runs on its own. The units are the packages with
// test files among those the operator's package patterns match, as `go list`
// resolves those patterns under the operator's build context.

// goUnitTemplate prints one line for EVERY package the patterns match: its
// import path, marked as a unit, as no unit (no test files), or as a package
// go could not load. A listing in which go printed nothing is then never read
// as a module with no tests: an empty stdout is what a go that failed before
// listing anything (no go.mod, a bad flag) leaves behind.
const goUnitTemplate = `{{if .Error}}error {{.ImportPath}}{{else if or .TestGoFiles .XTestGoFiles}}unit {{.ImportPath}}{{else}}none {{.ImportPath}}{{end}}`

// goListContext are the go test options that change which files a package
// is built from (build tags, the module graph, the toolchain), and so which
// packages have test files: the listing passes them to go list. Every other
// option — test flags, output flags — means nothing to go list (-json would
// even replace the template).
var goListContext = optionSet(`C tags mod modfile overlay race msan asan compiler`)

// goUnitFilters select tests inside a package (-run, -skip), list instead of
// running (-list), or build without running (-c). Under any of them a package
// can run no test and pass, so the command has no unit form.
var goUnitFilters = optionSet(`run skip list c`)

// goTestArgs splits the arguments after `go test` into its options, kept in
// order with their values, and its package patterns; at is where the first
// pattern stood among the options (len(opts) when there was none). ok=false
// when the command cannot be read or narrowed: an option go test does not
// list before a bare word, -args (whatever follows goes to the test binary,
// and CoverageCmd refuses it too: goTestShaped), "--", a .go file argument
// (go test then builds one package from the named files), or a name filter.
func goTestArgs(args []string) (opts, patterns []string, at int, ok bool) {
	at = -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return nil, nil, 0, false
		}
		if !strings.HasPrefix(a, "-") {
			if strings.HasSuffix(a, ".go") {
				return nil, nil, 0, false
			}
			if at < 0 {
				at = len(opts)
			}
			patterns = append(patterns, a)
			continue
		}
		name, _, hasEq := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "test."), "=")
		if name == "args" || goUnitFilters[name] {
			return nil, nil, 0, false
		}
		opts = append(opts, a)
		if hasEq {
			continue
		}
		value, known := goTestOption(a)
		bareNext := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
		switch {
		case !known && bareNext, value && !bareNext:
			return nil, nil, 0, false
		case value:
			i++
			opts = append(opts, args[i])
		}
	}
	if at < 0 {
		at = len(opts)
	}
	return opts, patterns, at, true
}

// goListUnitsCmd is `go list` over the patterns, under the options of opts
// that change which files a package holds. With no pattern, go list, like
// go test, means the package in the working directory.
func goListUnitsCmd(goBin string, opts, patterns []string) []string {
	cmd := []string{goBin, "list"}
	for i := 0; i < len(opts); i++ {
		name, _, hasEq := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(opts[i], "-"), "-"), "=")
		value, _ := goTestOption(opts[i])
		takes := value && !hasEq
		if goListContext[name] {
			cmd = append(cmd, opts[i])
			if takes {
				cmd = append(cmd, opts[i+1])
			}
		}
		if takes {
			i++
		}
	}
	return append(append(cmd, "-e", "-f", goUnitTemplate), patterns...)
}

// goWithPackage is opts with the one import path standing where the
// operator's patterns stood.
func goWithPackage(opts []string, at int, pkg string) []string {
	out := slices.Clone(opts[:at])
	out = append(out, pkg)
	return append(out, opts[at:]...)
}

func goTestListUnits(argv []string) ([]string, bool) {
	if !goTestCmd(argv) || cmdIsShellWrapped(argv) {
		return nil, false
	}
	opts, patterns, _, ok := goTestArgs(argv[2:])
	if !ok {
		return nil, false
	}
	return goListUnitsCmd(argv[0], opts, patterns), true
}

// goTestUnitCmd is argv with its package patterns replaced by the one
// package; every option stays where it was.
func goTestUnitCmd(argv []string, unit string) ([]string, bool) {
	if !goTestCmd(argv) || cmdIsShellWrapped(argv) || !goImportPath(unit) {
		return nil, false
	}
	opts, _, at, ok := goTestArgs(argv[2:])
	if !ok {
		return nil, false
	}
	return append(slices.Clone(argv[:2]), goWithPackage(opts, at, unit)...), true
}

// goParseUnits reads goUnitTemplate's lines. A package go could not load, or
// a line the template cannot have printed, makes the listing unreadable
// (nil); so does a listing of no package at all.
func goParseUnits(stdout string) []string {
	units := []string{}
	n := 0
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		kind, path, ok := strings.Cut(line, " ")
		if !ok || !goImportPath(path) {
			return nil
		}
		switch kind {
		case "unit":
			units = append(units, path)
		case "none":
		default:
			return nil
		}
		n++
	}
	if n == 0 {
		return nil
	}
	return units
}

// goImportPath reports whether s can be an import path go list printed: no
// space, not a flag, not a relative or absolute file path.
func goImportPath(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t") && !strings.HasPrefix(s, "-") &&
		!strings.HasPrefix(s, ".") && !filepath.IsAbs(s)
}

// gotestsumSplit splits a gotestsum command into the words up to and
// including its own options (head) and the go test arguments after "--".
// gotestsum runs `go test -json ./...` when it is given no go test argument
// at all (measured on v1.13.0 with --debug: bare `gotestsum` ran
// `go test -json ./...`, `gotestsum -- -v` ran `go test -json -v`), so with
// none the patterns are ./... . A bare word among gotestsum's own options is
// a go test argument too (`gotestsum ./a` ran `go test -json ./a`), which the
// row refuses rather than read; so it does --packages, which names the
// packages outside the go test arguments, and --raw-command and --watch,
// under which gotestsum does not run `go test <args>` once.
func gotestsumSplit(argv []string) (head, goArgs []string, ok bool) {
	i := wordIndex(argv, "gotestsum")
	if i < 0 || cmdIsShellWrapped(argv) {
		return nil, nil, false
	}
	own := argv[i+1:]
	if j := slices.Index(own, "--"); j >= 0 {
		own, goArgs = own[:j], own[j+1:]
	}
	for k := 0; k < len(own); k++ {
		a := own[k]
		if !strings.HasPrefix(a, "-") {
			return nil, nil, false
		}
		name, _, hasEq := strings.Cut(a, "=")
		if name == "--packages" || name == "--raw-command" || name == "--watch" {
			return nil, nil, false
		}
		if hasEq {
			continue
		}
		value, known := gotestsumOptions.takesValue(a)
		bareNext := k+1 < len(own) && !strings.HasPrefix(own[k+1], "-")
		switch {
		case !known && bareNext, value && !bareNext:
			return nil, nil, false
		case value:
			k++
		}
	}
	return argv[:i+1+len(own)], goArgs, true
}

func gotestsumListUnits(argv []string) ([]string, bool) {
	_, goArgs, ok := gotestsumSplit(argv)
	if !ok {
		return nil, false
	}
	if len(goArgs) == 0 {
		return goListUnitsCmd("go", nil, []string{"./..."}), true
	}
	opts, patterns, _, ok := goTestArgs(goArgs)
	if !ok {
		return nil, false
	}
	return goListUnitsCmd("go", opts, patterns), true
}

// gotestsumUnitCmd keeps gotestsum's own part and replaces the go test
// patterns after "--" with the one package, adding the "--" when the
// command had none.
func gotestsumUnitCmd(argv []string, unit string) ([]string, bool) {
	head, goArgs, ok := gotestsumSplit(argv)
	if !ok || !goImportPath(unit) {
		return nil, false
	}
	opts, _, at, ok := goTestArgs(goArgs)
	if !ok {
		return nil, false
	}
	return append(append(slices.Clone(head), "--"), goWithPackage(opts, at, unit)...), true
}
