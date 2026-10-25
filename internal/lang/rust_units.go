// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// A Rust test unit is a test TARGET: the one test binary cargo builds for a
// package's library (`lib`), for each binary (`bin:<name>`) and for each
// integration test (`test:<name>`). Doc tests are no unit: they are not a
// binary of their own, and a target-selection flag never runs them.

// cargoTargetFlags select which targets `cargo test` builds and runs. cargo
// runs every target selected, the operator's beside the unit's (measured on
// cargo 1.90: `cargo test -p units-a --test it --lib` ran both), so a unit
// run drops the operator's; the listing, which applied them, keeps them.
var cargoTargetFlags = optionSet(`--lib --bins --bin --examples --example --tests --test --benches --bench --all-targets`)

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
// kind. The listing is read only when cargo reported the build finished and
// succeeded; a build that failed, or output that is not cargo's messages, is
// no listing. A target name two packages share (every crate's `lib`, a
// `tests/it.rs` in each) cannot be run alone: cargo selects targets by name
// across every package the command selects (measured: `cargo test
// --workspace --test it` ran both packages' it), so it is listed marked.
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
		if line == "" {
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

// cargoUnitSelector is the target-selection flag for a unit.
func cargoUnitSelector(unit string) ([]string, bool) {
	if unit == "lib" {
		return []string{"--lib"}, true
	}
	kind, name, ok := strings.Cut(unit, ":")
	if !ok || (kind != "test" && kind != "bin") || !cargoTargetName.MatchString(name) {
		return nil, false
	}
	return []string{"--" + kind, name}, true
}

// cargoUnitCmd runs one target: argv without its own target selection, with
// the unit's, every other option (-p, --workspace, features, profile) and
// the test-binary arguments kept.
func cargoUnitCmd(argv []string, unit string) ([]string, bool) {
	head, opts, tail, ok := cargoTestSplit(argv)
	sel, okSel := cargoUnitSelector(unit)
	if !ok || !okSel {
		return nil, false
	}
	cmd := append(slices.Clone(head), cargoWithoutTargets(opts)...)
	cmd = append(cmd, sel...)
	if tail != nil {
		cmd = append(append(cmd, "--"), tail...)
	}
	return cmd, true
}

// cargoWithoutTargets is opts without cargo's target-selection flags and the
// optional names they read (`--test it`).
func cargoWithoutTargets(opts []string) []string {
	var out []string
	for i := 0; i < len(opts); i++ {
		name, _, hasEq := strings.Cut(opts[i], "=")
		if !cargoTargetFlags[name] {
			out = append(out, opts[i])
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
		hasFlag(opts, "-E", "--filterset", "--filter-expr", "--partition", "-R", "--rerun", "--no-run") {
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
	cmd := append(slices.Clone(pre), "list")
	for i := 0; i < len(opts); i++ {
		a := opts[i]
		name, _, hasEq := strings.Cut(a, "=")
		value, _ := nextestRunOptions.takesValue(name)
		takes := strings.HasPrefix(a, "-") && value && !hasEq && i+1 < len(opts)
		if !nextestRunOnly[name] && (len(name) <= 2 || name[1] == '-' || !nextestRunOnly[name[:2]]) {
			cmd = append(cmd, a)
			if takes {
				cmd = append(cmd, opts[i+1])
			}
		}
		if takes {
			i++
		}
	}
	cmd = append(cmd, "--message-format", "json")
	return append(cmd, tail...), true
}

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

// nextestUnitCmd runs one test binary: argv with `-E 'binary_id(=<id>)'`
// before any "--". nextest ANDs a filterset with the name filters (measured:
// `integrates -E 'binary_id(=units-a)'` ran none of units-a's tests and
// `-E 'binary_id(=units-a)' -- adds_more` ran one), so the operator's
// filters still apply inside the binary, as they did to the listing, which
// listed only binaries with a matching test.
func nextestUnitCmd(argv []string, unit string) ([]string, bool) {
	pre, opts, tail, ok := nextestRunSplit(argv)
	if !ok || !nextestBinaryID.MatchString(unit) {
		return nil, false
	}
	cmd := append(append(slices.Clone(pre), "run"), opts...)
	cmd = append(cmd, "-E", "binary_id(="+unit+")")
	return append(cmd, tail...), true
}
