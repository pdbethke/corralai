// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// A Rust test unit is a test TARGET: the one test binary cargo builds for a
// package's library, for each binary and for each integration test. cargo
// test names one `<package>/lib`, `<package>/bin:<name>`,
// `<package>/test:<name>`; nextest names one by its own binary id. Doc tests
// are no unit: they are not a binary of their own, and a target-selection
// flag never runs them.

// cargoTargetFlags select which targets `cargo test` builds and runs. cargo
// runs every target selected, the operator's beside the unit's (measured on
// cargo 1.90: `cargo test -p units-a --test it --lib` ran both), so a unit
// run drops the operator's; the listing, which applied them, keeps them.
var cargoTargetFlags = optionSet(`--lib --bins --bin --examples --example --tests --test --benches --bench --all-targets`)

// cargoPackageFlags select which packages `cargo test` tests. A unit run
// replaces them with `-p <its package>`: -p does not narrow --workspace, it
// adds to it (measured on cargo 1.90: `cargo test --workspace -p units-a
// --lib` ran both packages' libs), and a target name two packages share is
// selected in each (`cargo test --workspace --test it` ran both its).
var cargoPackageFlags = optionSet(`--workspace --all -p --package --exclude`)

// cargoTestSplit reads a `cargo test` command: the words through `test`
// (head), cargo's options after it, and the test-binary arguments after
// "--" (tail, without the "--"). ok=false when it has no unit form: a
// TESTNAME, a filter, --skip or --ignored after "--" (each can leave a
// target with no test to run, which passes), --list or --bench there (no
// test runs), --doc or --no-run, or an option the tables do not list before
// a bare word.
func cargoTestSplit(argv []string) (head, opts, tail []string, ok bool) {
	i := wordIndex(argv, "cargo")
	if i < 0 || cmdIsShellWrapped(argv) {
		return nil, nil, nil, false
	}
	j := i + 1
	if j < len(argv) && strings.HasPrefix(argv[j], "+") {
		j++ // a rustup toolchain, `cargo +nightly test`
	}
	if j >= len(argv) || argv[j] != "test" {
		return nil, nil, nil, false
	}
	head = argv[:j+1]
	rest := argv[j+1:]
	if k := slices.Index(rest, "--"); k >= 0 {
		rest, tail = rest[:k], rest[k+1:]
	}
	kept, names, ok := splitFileArgs(rest, true, cargoTestOptions.takesValue)
	if !ok || len(names) > 0 || hasFlag(kept, "--doc", "--no-run") {
		return nil, nil, nil, false
	}
	for k := 0; k < len(tail); k++ {
		a := tail[k]
		name, _, hasEq := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "-") || name == "--skip" || name == "--ignored" || name == "--list" || name == "--bench" {
			return nil, nil, nil, false
		}
		if hasEq {
			continue
		}
		value, known := libtestOptions.takesValue(a)
		bareNext := k+1 < len(tail) && !strings.HasPrefix(tail[k+1], "-")
		switch {
		case !known && bareNext, value && !bareNext:
			return nil, nil, nil, false
		case value:
			k++
		}
	}
	return head, kept, tail, true
}

// cargoListUnits builds every test target argv selects without running one,
// reporting each as cargo's JSON message. The operator's own
// --message-format is replaced and the test-binary arguments dropped, since
// nothing runs.
func cargoListUnits(argv []string) ([]string, bool) {
	head, opts, _, ok := cargoTestSplit(argv)
	if !ok {
		return nil, false
	}
	cmd := append(slices.Clone(head), withoutFlag(opts, "--message-format")...)
	return append(cmd, "--no-run", "--message-format=json"), true
}

// cargoParseUnits reads cargo's JSON messages: each compiler-artifact built
// as a test (profile.test) with an executable is a target, named by its
// package and kind. The listing is read only when cargo reported the build
// finished and succeeded; a build that failed, a JSON line that is not one of
// cargo's messages, or a package id it cannot name is no listing. Lines that
// are not JSON objects are cargo's stderr on the same stream, and skipped. Two package ids
// with one name (a member and a dependency of the same name from another
// source) cannot be told apart by -p, so such a unit is listed marked.
// Example and bench targets, built as tests only under --examples,
// --benches or --all-targets, are not units of this row.
func cargoParseUnits(stdout string) []string {
	type message struct {
		Reason    string `json:"reason"`
		PackageID string `json:"package_id"`
		Target    struct {
			Kind []string `json:"kind"`
			Name string   `json:"name"`
		} `json:"target"`
		Profile struct {
			Test bool `json:"test"`
		} `json:"profile"`
		Executable *string `json:"executable"`
		Success    *bool   `json:"success"`
	}
	packages := map[string]map[string]bool{}
	finished := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		// A line that is not a JSON object is cargo's stderr (Compiling,
		// Finished, a warning): the jail returns both streams as one
		// (capture cargo-units-jail.txt). It names no target, and the
		// listing still needs cargo's own build-finished message to count.
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m message
		if json.Unmarshal([]byte(line), &m) != nil {
			return nil
		}
		switch m.Reason {
		case "build-finished":
			if m.Success == nil || !*m.Success {
				return nil
			}
			finished = true
		case "compiler-artifact":
			if !m.Profile.Test || m.Executable == nil {
				continue
			}
			if u := cargoUnitName(m.Target.Kind, m.Target.Name); u != "" {
				pkg, ok := cargoPackageName(m.PackageID)
				if !ok {
					return nil
				}
				u = pkg + "/" + u
				if packages[u] == nil {
					packages[u] = map[string]bool{}
				}
				packages[u][m.PackageID] = true
			}
		}
	}
	if !finished {
		return nil
	}
	units := []string{}
	for u, pkgs := range packages {
		if len(pkgs) > 1 {
			u = markNotAlone(u)
		}
		units = append(units, u)
	}
	sort.Strings(units)
	return units
}

// cargoUnitName is the unit for a target of the kinds cargo reports, or ""
// for a kind that is no unit here. Every library crate type is the one
// --lib target.
func cargoUnitName(kinds []string, name string) string {
	for _, k := range kinds {
		switch k {
		case "lib", "rlib", "dylib", "cdylib", "staticlib", "proc-macro":
			return "lib"
		case "test", "bin":
			if cargoTargetName.MatchString(name) {
				return k + ":" + name
			}
		}
	}
	return ""
}

var cargoTargetName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// cargoPackageName is the package name in a cargo package id: the current
// spec form `<source>#<name>@<version>`, the same form with the name left
// out when it is the source path's last element (`path+file:///w/same#0.1.0`,
// measured on cargo 1.90), or the older `<name> <version> (<source>)`.
func cargoPackageName(id string) (string, bool) {
	var name string
	if src, frag, ok := strings.Cut(id, "#"); ok {
		if n, _, ok := strings.Cut(frag, "@"); ok {
			name = n
		} else {
			name = src[strings.LastIndex(src, "/")+1:]
		}
	} else if f := strings.Fields(id); len(f) == 3 {
		name = f[0]
	}
	return name, cargoTargetName.MatchString(name)
}

// cargoUnitParts splits a unit into its package and its target-selection
// flags (`--lib`, `--test it`, `--bin x`).
func cargoUnitParts(unit string) (pkg string, target []string, ok bool) {
	pkg, t, ok := strings.Cut(unit, "/")
	if !ok || !cargoTargetName.MatchString(pkg) {
		return "", nil, false
	}
	if t == "lib" {
		return pkg, []string{"--lib"}, true
	}
	kind, name, ok := strings.Cut(t, ":")
	if !ok || (kind != "test" && kind != "bin") || !cargoTargetName.MatchString(name) {
		return "", nil, false
	}
	return pkg, []string{"--" + kind, name}, true
}

// cargoUnitsSelector is the package and target-selection flags for the
// units: each package once (`-p`), then each target once. cargo selects
// targets ACROSS the packages it is given, so the flags select the cross
// product of the units' packages and targets; cargoUnitsCoruns names what
// that adds.
func cargoUnitsSelector(units []string) ([]string, bool) {
	if !unitSet(units) {
		return nil, false
	}
	var pkgs, targets []string
	for _, u := range units {
		pkg, target, ok := cargoUnitParts(u)
		if !ok {
			return nil, false
		}
		if !slices.Contains(pkgs, pkg) {
			pkgs = append(pkgs, pkg)
		}
		if t := strings.Join(target, " "); !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
	}
	var sel []string
	for _, p := range pkgs {
		sel = append(sel, "-p", p)
	}
	for _, t := range targets {
		sel = append(sel, strings.Fields(t)...)
	}
	return sel, true
}

// cargoUnitsCmd runs targets of packages: argv with its own package and
// target selection replaced by the units', every other option (features,
// profile, target dir) and the test-binary arguments kept.
func cargoUnitsCmd(argv []string, units []string) ([]string, bool) {
	head, opts, tail, ok := cargoTestSplit(argv)
	sel, okSel := cargoUnitsSelector(units)
	if !ok || !okSel {
		return nil, false
	}
	cmd := append(slices.Clone(head), cargoWithout(opts, cargoTargetFlags, cargoPackageFlags)...)
	cmd = append(cmd, sel...)
	if tail != nil {
		cmd = append(append(cmd, "--"), tail...)
	}
	return cmd, true
}

// cargoUnitsCoruns: a run of units also runs every other target of the
// cross product cargoUnitsSelector selects — one of the units' packages
// and one of their targets (measured on cargo 1.90: units-a/lib with
// units-b/test:it ran units-a's it and units-b's lib as well, capture
// cargo-units-multi-coruns). An other that cannot be read as a unit is
// reported as collected: nothing shows it is not.
func cargoUnitsCoruns(units []string, other string) bool {
	if slices.Contains(units, other) {
		return false
	}
	opkg, otarget, ok := cargoUnitParts(other)
	if !ok {
		return true
	}
	pkgIn, targetIn := false, false
	for _, u := range units {
		pkg, target, ok := cargoUnitParts(u)
		if !ok {
			return true
		}
		pkgIn = pkgIn || pkg == opkg
		targetIn = targetIn || slices.Equal(target, otarget)
	}
	return pkgIn && targetIn
}

// rustUnitTarget places a test file in its package's targets: the package
// that owns it (owningCrateDir), and the integration-test target a
// tests/<name>.rs or tests/<name>/main.rs is, or the library for a file
// under src/ when the package has src/lib.rs. A binary's source (src/main.rs,
// src/bin/), a module below tests/, a bench or an example is no unit here,
// and neither is a target cargo was told about in Cargo.toml rather than
// finding it by its place, which this reads no further than the place.
func rustUnitTarget(repoRoot, testFile string) (pkg, kind, name string, ok bool) {
	f, ok := repoRelative(testFile)
	if !ok || repoRoot == "" {
		return "", "", "", false
	}
	pkg, dir, err := owningCrateDir(repoRoot, f)
	if err != nil {
		return "", "", "", false
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", "", "", false
	}
	rel, err := filepath.Rel(dir, filepath.Join(root, filepath.FromSlash(f)))
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case len(parts) == 2 && parts[0] == "tests" && strings.HasSuffix(parts[1], ".rs"):
		name = strings.TrimSuffix(parts[1], ".rs")
	case len(parts) == 3 && parts[0] == "tests" && parts[2] == "main.rs":
		name = parts[1]
	case parts[0] == "src" && len(parts) > 1 && rel != filepath.Join("src", "main.rs") && parts[1] != "bin":
		if _, err := os.Stat(filepath.Join(dir, "src", "lib.rs")); err != nil {
			return "", "", "", false
		}
		return pkg, "lib", "", true
	default:
		return "", "", "", false
	}
	return pkg, "test", name, cargoTargetName.MatchString(name)
}

// cargoUnitForFile is rustUnitTarget in cargo test's unit spelling.
func cargoUnitForFile(repoRoot, testFile string) (string, bool) {
	pkg, kind, name, ok := rustUnitTarget(repoRoot, testFile)
	switch {
	case !ok:
		return "", false
	case kind == "lib":
		return pkg + "/lib", true
	}
	return pkg + "/test:" + name, true
}

// nextestUnitForFile is rustUnitTarget as nextest's binary id: the library
// is the package's own id, an integration test `<package>::<name>`.
func nextestUnitForFile(repoRoot, testFile string) (string, bool) {
	pkg, kind, name, ok := rustUnitTarget(repoRoot, testFile)
	if !ok {
		return "", false
	}
	id := pkg
	if kind == "test" {
		id += "::" + name
	}
	return id, nextestBinaryID.MatchString(id)
}

// cargoWithout is opts without the flags of the sets and the values they
// read (`--test it`, `-p a`, `-pa`, `--exclude=b`).
func cargoWithout(opts []string, sets ...map[string]bool) []string {
	in := func(name string) bool {
		return slices.ContainsFunc(sets, func(s map[string]bool) bool { return s[name] })
	}
	var out []string
	for i := 0; i < len(opts); i++ {
		a := opts[i]
		name, _, hasEq := strings.Cut(a, "=")
		if len(a) > 2 && a[0] == '-' && a[1] != '-' && in(a[:2]) {
			continue // a short flag with its value attached
		}
		if !in(name) {
			out = append(out, a)
			continue
		}
		if value, _ := cargoTestOptions.takesValue(name); value && !hasEq && i+1 < len(opts) && !strings.HasPrefix(opts[i+1], "-") {
			i++
		}
	}
	return out
}

// nextestRunSplit reads a `cargo nextest run` command: the words before
// `run`, its options and name filters after it, and the "--" tail (with the
// "--"). ok=false when it has no unit form: a filterset (-E), since nextest
// ORs filtersets and a unit's own would add to it rather than narrow
// (measured on cargo-nextest 0.9.148: `-E 'binary_id(=units-a)' -E
// 'test(negates)'` ran units-b's test too); --partition, which divides the
// TESTS, so a partition of one binary is a different set; --rerun, which
// replays a recorded run; --no-run; or an option the table does not list
// before a bare word.
func nextestRunSplit(argv []string) (pre, opts, tail []string, ok bool) {
	if cmdIsShellWrapped(argv) {
		return nil, nil, nil, false
	}
	head, tail := splitAtDashDash(argv)
	i := slices.Index(head, "run")
	if i < 0 || wordIndex(head[:i], "nextest") < 0 && wordIndex(head[:i], "cargo-nextest") < 0 {
		return nil, nil, nil, false
	}
	opts = head[i+1:]
	if _, ok := dropFileArgs(opts, true, nextestRunOptions.takesValue); !ok ||
		hasFlag(opts, "-E", "--filterset", "--filter-expr", "--partition", "-R", "--rerun", "--no-run") ||
		shortBundleSets(opts, nextestShortFlags, "ER") {
		return nil, nil, nil, false
	}
	return head[:i], opts, tail, true
}

// nextestListUnits is nextest's own `list` over the run's selection, name
// filters and tail included: it reports, per test binary, whether each test
// matches them (filter-match). The run-only options, which `list` refuses,
// are dropped.
func nextestListUnits(argv []string) ([]string, bool) {
	pre, opts, tail, ok := nextestRunSplit(argv)
	if !ok {
		return nil, false
	}
	cmd := append(append(slices.Clone(pre), "list"), nextestListArgs(opts)...)
	cmd = append(cmd, "--message-format", "json")
	return append(cmd, tail...), true
}

// nextestListArgs is a run's options and name filters without the run-only
// options `cargo nextest list` refuses (and their values): every one of
// nextestRunOnly was refused by list on cargo-nextest 0.9.148 (capture
// nextest-list-refuses-every-run-only), so a listing that kept one listed
// nothing.
func nextestListArgs(opts []string) []string {
	var out []string
	for i := 0; i < len(opts); i++ {
		a := opts[i]
		name, _, hasEq := strings.Cut(a, "=")
		value, _ := nextestRunOptions.takesValue(name)
		takes := strings.HasPrefix(a, "-") && value && !hasEq && i+1 < len(opts)
		if !nextestRunOnly[name] && (len(name) <= 2 || name[1] == '-' || !nextestRunOnly[name[:2]]) {
			out = append(out, a)
			if takes {
				out = append(out, opts[i+1])
			}
		}
		if takes {
			i++
		}
	}
	return out
}

// nextestShortFlags are nextest run's short options that take no value,
// which clap lets a bundle carry before one that does (`-rEtest(x)`).
const nextestShortFlags = "vhr"

// nextestParseUnits reads `list --message-format json`: each test binary
// with a test the run's filters match is a unit, named by its binary id
// (`units-a`, `units-a::it`, `units-a::bin/units-a`), which is unique across
// a workspace. A binary none of whose tests match is not run by the
// operator's command and is left out.
func nextestParseUnits(stdout string) []string {
	var rep struct {
		Suites *map[string]struct {
			Testcases map[string]struct {
				FilterMatch struct {
					Status string `json:"status"`
				} `json:"filter-match"`
			} `json:"testcases"`
		} `json:"rust-suites"`
	}
	if !decodeReport(stdout, &rep) || rep.Suites == nil {
		return nil
	}
	units := []string{}
	for id, s := range *rep.Suites {
		for _, tc := range s.Testcases {
			if tc.FilterMatch.Status == "matches" {
				units = append(units, id)
				break
			}
		}
	}
	sort.Strings(units)
	return units
}

var nextestBinaryID = regexp.MustCompile(`^[A-Za-z0-9_-]+(::[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)?)?$`)

// nextestUnitsCmd runs test binaries: argv with one `-E 'binary_id(=<id>)'`
// per unit before any "--". nextest ORs filtersets (capture
// nextest-filtersets-union), so the run is the union of the binaries and
// nothing else (capture nextest-units-multi). nextest ANDs a filterset with the name filters (measured:
// `integrates -E 'binary_id(=units-a)'` ran none of units-a's tests and
// `-E 'binary_id(=units-a)' -- adds_more` ran one), so the operator's
// filters still apply inside the binary, as they did to the listing, which
// listed only binaries with a matching test.
func nextestUnitsCmd(argv []string, units []string) ([]string, bool) {
	pre, opts, tail, ok := nextestRunSplit(argv)
	if !ok || !unitSet(units) || slices.ContainsFunc(units, func(u string) bool { return !nextestBinaryID.MatchString(u) }) {
		return nil, false
	}
	cmd := append(append(slices.Clone(pre), "run"), opts...)
	for _, u := range units {
		cmd = append(cmd, "-E", "binary_id(="+u+")")
	}
	return append(cmd, tail...), true
}
