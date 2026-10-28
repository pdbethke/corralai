// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"errors"
	"os"
	"path"
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

// goWithPackages is opts with the import paths standing where the
// operator's patterns stood.
func goWithPackages(opts []string, at int, pkgs []string) []string {
	out := slices.Clone(opts[:at])
	out = append(out, pkgs...)
	return append(out, opts[at:]...)
}

// goPackageUnits reports whether units is a set of import paths go list
// could have printed.
func goPackageUnits(units []string) bool {
	return unitSet(units) && !slices.ContainsFunc(units, func(u string) bool { return !goImportPath(u) })
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

// goTestUnitsCmd is argv with its package patterns replaced by the
// packages; every option stays where it was. go test runs exactly the
// packages it is named, one test binary each, so a run of several is exact.
func goTestUnitsCmd(argv []string, units []string) ([]string, bool) {
	if !goTestCmd(argv) || cmdIsShellWrapped(argv) || !goPackageUnits(units) {
		return nil, false
	}
	opts, _, at, ok := goTestArgs(argv[2:])
	if !ok {
		return nil, false
	}
	return append(slices.Clone(argv[:2]), goWithPackages(opts, at, units)...), true
}

// goEvidenceCache is the go rows' EvidenceCache: one GOCACHE for every unit
// of an evidence pass, warmed by its first unit and frozen for the rest.
var goEvidenceCache = &EvidenceCache{Env: "GOCACHE"}

// goEvidenceArgs is go test arguments (after `go test`, or after
// gotestsum's "--") with -count=1 as their last word, in place of any
// -count the operator gave; nil when go's own parsing cannot read them.
//
// -count=1 is CORRECTNESS, not speed. The pass shares one build cache among
// its units (goEvidenceCache), and that cache also holds test RESULTS: go
// test replays a cached result, coverage profile included, instead of
// running the test, whenever its inputs match one it has recorded. Every
// jailed run used to start from an empty GOCACHE, so a result was never
// replayed; with a shared one it could be, and a replayed result is
// evidence of a run that did not happen in this pass. -count=1 is go's own
// switch for "never use a cached result" (go help testflag), and it
// changes nothing else: the build cache is still used.
func goEvidenceArgs(args []string) ([]string, bool) {
	opts, patterns, at, ok := goTestArgs(args)
	if !ok {
		return nil, false
	}
	var kept []string
	keptAt := 0
	for i := 0; i < len(opts); i++ {
		if i == at {
			keptAt = len(kept)
		}
		name, _, hasEq := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(opts[i], "-"), "-"), "test."), "=")
		if name == "count" {
			if !hasEq {
				i++ // its value, which goTestArgs kept as the next word
			}
			continue
		}
		kept = append(kept, opts[i])
	}
	if at >= len(opts) {
		keptAt = len(kept)
	}
	return append(goWithPackages(kept, keptAt, patterns), "-count=1"), true
}

// goTestEvidenceArgs is the go-test row's EvidenceArgs: goEvidenceArgs over
// the words after `go test`. A command go's parsing refuses is returned
// unchanged; no unit command is one (goTestUnitsCmd is built by the same
// parsing), so this is only ever the operator's argv, for EvidenceNote.
func goTestEvidenceArgs(unitCmd []string) []string {
	if !goTestCmd(unitCmd) || cmdIsShellWrapped(unitCmd) {
		return unitCmd
	}
	args, ok := goEvidenceArgs(unitCmd[2:])
	if !ok {
		return unitCmd
	}
	return append(slices.Clone(unitCmd[:2]), args...)
}

// gotestsumEvidenceArgs is the gotestsum row's EvidenceArgs: goEvidenceArgs
// over the go test arguments after its "--", adding the "--" when the
// command had none. gotestsum with no go test argument runs ./... (see
// gotestsumSplit), so that is what the added arguments name.
func gotestsumEvidenceArgs(unitCmd []string) []string {
	head, goArgs, ok := gotestsumSplit(unitCmd)
	if !ok {
		return unitCmd
	}
	if len(goArgs) == 0 {
		goArgs = []string{"./..."}
	}
	args, ok := goEvidenceArgs(goArgs)
	if !ok {
		return unitCmd
	}
	return append(append(slices.Clone(head), "--"), args...)
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

// gotestsumUnitsCmd keeps gotestsum's own part and replaces the go test
// patterns after "--" with the packages, adding the "--" when the command
// had none.
func gotestsumUnitsCmd(argv []string, units []string) ([]string, bool) {
	head, goArgs, ok := gotestsumSplit(argv)
	if !ok || !goPackageUnits(units) {
		return nil, false
	}
	opts, _, at, ok := goTestArgs(goArgs)
	if !ok {
		return nil, false
	}
	return append(append(slices.Clone(head), "--"), goWithPackages(opts, at, units)...), true
}

// goUnitForFile is the package a Go test file belongs to, by the import
// path go list prints for it (goImportPathOf over the repository's go.mod
// files): the unit is named exactly as a listed one, so a test written into
// a listed package joins that unit rather than naming it twice, and one
// written into a package with no test file yet (which no listing named) is
// still a package go test runs. Only a _test.go file is a test file.
func goUnitForFile(repoRoot, testFile string) (string, bool) {
	f, ok := repoRelative(testFile)
	if !ok || repoRoot == "" || !strings.HasSuffix(f, "_test.go") {
		return "", false
	}
	read := func(name string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(name))) // #nosec G304 -- a go.mod at a repository-relative path under the repository being audited
		return string(b), err == nil
	}
	pkg, ok := GoImportPathOf(read, path.Dir(f))
	return pkg, ok && goImportPath(pkg)
}

// GoImportPathOf is the import path of the package in dir (repository-
// relative, slash-separated): the nearest go.mod at or above it names the
// module, and dir's place below that go.mod the rest. read returns a
// repository file's text; ok is false when no go.mod above dir declares a
// module. It is the one rule for it: unit evidence reads the scan's files
// through it, and goUnitForFile the checkout on disk.
func GoImportPathOf(read func(name string) (string, bool), dir string) (string, bool) {
	under := func(d, rel string) string {
		if d == "." || d == "" {
			return rel
		}
		return d + "/" + rel
	}
	for d := dir; ; d = path.Dir(d) {
		if text, ok := read(under(d, "go.mod")); ok {
			mod, err := GoModuleDirective(text)
			if err != nil {
				return "", false
			}
			if d == dir {
				return mod, true
			}
			return mod + "/" + strings.TrimPrefix(dir, under(d, "")), true
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}

// GoModuleDirective is the module path a go.mod's text declares, or the
// error that says why there is none ("no module directive", "a module
// directive with no path"; a caller prefixes the file's name and "has").
// Both legal spellings an earlier prefix-cut mis-parsed are handled: a
// quoted path (`module "example.com/x"`, and its backquoted variant) and a
// trailing `//` comment (`module example.com/x // v2`), plus tab separation.
// It is the one parser for that directive: the coverage pre-flight reads
// go.mod from disk through it, and unit evidence reads it from the scan's
// files, to turn a package import path into the directory it names.
func GoModuleDirective(gomod string) (string, error) {
	for _, line := range strings.Split(gomod, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !ok {
			continue
		}
		// "module" must be its own token: `moduleX ...` is not the
		// directive, while `module\tpath` is.
		if rest != "" && !strings.ContainsAny(rest[:1], " \t") {
			continue
		}
		// A `//` comment may trail the directive; everything after it is
		// commentary, not part of the path.
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = rest[:i]
		}
		rest = strings.TrimSpace(rest)
		// go.mod tokens may be quoted (") or raw-quoted (`).
		if len(rest) >= 2 {
			if (rest[0] == '"' && rest[len(rest)-1] == '"') || (rest[0] == '`' && rest[len(rest)-1] == '`') {
				rest = rest[1 : len(rest)-1]
			}
		}
		if rest == "" {
			return "", errors.New("a module directive with no path")
		}
		return rest, nil
	}
	return "", errors.New("no module directive")
}
