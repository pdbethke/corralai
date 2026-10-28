// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

// vitestFilters are vitest's name filters, in both spellings it accepts.
var vitestFilters = []string{"-t", "--testNamePattern", "--test-name-pattern"}

// vitestShortFlags are vitest 3.2.7's other single-letter options (version,
// root, config, update, watch, help). Its option parser (cac, over mri)
// reads a bundle letter by letter and hands the next word to the last:
// `vitest run -ut 'a one'` ran 1 of the file's 2 tests (measured), a name
// filter hasFlag cannot see.
const vitestShortFlags = "vrcuwh"

// vitestNarrowable is narrowable for a vitest command, refusing a name
// filter set inside a bundle of short options after the vitest word too.
func vitestNarrowable(argv []string) bool {
	i := max(vitestIndex(argv), 0)
	return narrowable(argv, vitestFilters...) && !shortBundleSets(argv[i:], vitestShortFlags, "t")
}

// vitestIndex is the position of the word that is vitest in argv: the vitest
// binary, or its entry script run by a host interpreter
// (`node ./node_modules/vitest/vitest.mjs`, the shape AGENTS.md gives for the
// jail, where node_modules/.bin is not on PATH). -1 when there is none.
func vitestIndex(argv []string) int {
	if i := wordIndex(argv, "vitest"); i >= 0 {
		return i
	}
	return slices.IndexFunc(argv, func(a string) bool {
		b := filepath.Base(a)
		return b == "vitest.mjs" || b == "vitest.js"
	})
}

// vitestWithMode is argv with vitest's subcommand set to mode: an existing
// run/watch/dev replaced, else mode inserted after the vitest word.
func vitestWithMode(argv []string, mode string) ([]string, bool) {
	i := vitestIndex(argv)
	if i < 0 || !vitestNarrowable(argv) {
		return nil, false
	}
	cmd := slices.Clone(argv)
	if i+1 < len(cmd) && (cmd[i+1] == "run" || cmd[i+1] == "watch" || cmd[i+1] == "dev") {
		cmd[i+1] = mode
		return cmd, true
	}
	return slices.Insert(cmd, i+1, mode), true
}

// vitestListCmd is vitest's own `list` subcommand over the test file: it
// collects without running.
func vitestListCmd(argv []string, testPath string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "list")
	if !ok {
		return nil, false
	}
	return append(cmd, testPath), true
}

// vitestParseList reads `list`'s `<file> > <title> > <title>` lines. A title
// that itself contains " > " cannot be told from a describe boundary; its id
// then names titles that do not exist, and its one-test run selects nothing,
// which is never credited.
func vitestParseList(stdout string) []string {
	var ids []string
	for _, line := range strings.Split(stdout, "\n") {
		parts := strings.Split(strings.TrimSpace(line), " > ")
		if len(parts) < 2 {
			continue
		}
		ids = append(ids, joinTitles(parts[1:]))
	}
	return ids
}

// vitestSingleCmd runs the one test whose full name is id's, always under
// `run`: a bare vitest watches when it has a terminal.
func vitestSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "run")
	if id == "" || !ok {
		return nil, false
	}
	return append(cmd, "-t", "^"+jsRegexQuote(fullName(id))+"$", testPath), true
}

// vitestListUnits is vitest's own `list --filesOnly` over argv: the test
// files argv collects, collected and not run. --json comes last so it is
// read as a flag, not as the file to write the list to, and prints each file
// as an absolute path (verified on vitest 3.2.7; the plain-text form is
// relative to the configured root, which need not be the working directory).
func vitestListUnits(argv []string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "list")
	if !ok {
		return nil, false
	}
	return append(cmd, "--filesOnly", "--json"), true
}

// vitestParseUnits reads `list --filesOnly --json`'s array of files. A file
// in two projects is listed twice; ParseUnitList keeps it once.
func vitestParseUnits(stdout string) []string {
	var files []struct {
		File string `json:"file"`
	}
	if !decodeArray(stdout, &files) {
		return nil
	}
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f.File)
	}
	return paths
}

// vitestUnitsCmd runs the test files: argv under `run`, without its shard
// (withoutFlag), with the operator's file filters replaced by the units,
// since vitest runs the files ANY filter matches (so several units are
// several filters, each a unit's path; vitestUnitCoruns says which other
// listed file a unit's filter also matches). A bare word after a flag
// written without "=" may be that flag's value or a filter, and vitest's
// options are too many to tell, so such a command has no unit form.
func vitestUnitsCmd(argv []string, units []string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "run")
	if !fileUnits(units) || !ok {
		return nil, false
	}
	i := vitestIndex(cmd) + 2 // past the vitest word and `run`
	flags, ok := dropFileArgs(withoutFlag(cmd[i:], "--shard"), true, func(string) (bool, bool) { return false, false })
	if !ok {
		return nil, false
	}
	return append(append(slices.Clone(cmd[:i]), flags...), units...), true
}

// vitestUnitCoruns: vitest selects the files whose path, relative to its
// root, contains a filter — the filter as given, or made relative to that
// root — ignoring case (vitest 3.2.7's filterFiles). The root may sit below
// the working directory the unit is named from, where the filter shrinks to
// as little as the base name (measured: `vitest run --root units/collide
// units/collide/a.vitest.test.mjs` also ran xa.vitest.test.mjs), so unit
// coruns other whenever other contains unit's base name.
func vitestUnitCoruns(unit, other string) bool {
	return unit != other && strings.Contains(strings.ToLower(other), strings.ToLower(filepath.Base(unit)))
}

// vitestEvidenceArgs runs a unit in the threads pool for the evidence pass
// (Runner.EvidenceArgs says why the forks pool cannot be used). The
// operator's own --pool, in either spelling, is removed and --pool=threads
// goes last, so the flag is the command's one pool choice and the last
// word whatever vitest makes of a repeated option. CLI --pool overrides a
// project's configured pool (REA's forks projects ran threads with it on
// vitest 5.0.3). --poolOptions and every other flag are left as written.
func vitestEvidenceArgs(unitCmd []string) []string {
	return append(withoutFlag(unitCmd, "--pool"), "--pool=threads")
}

// vitestEvidenceCache is vitest's row's EvidenceCache: one transform cache
// (fsModuleCache) for every unit of an evidence pass, warmed by the first
// unit and frozen for the rest, so a later unit reads the modules the
// first one already transformed instead of transforming them again.
// vitest keys each entry on the module's absolute path and content and on
// the project's root, plugins, config files and NODE_ENV (vitest 5.0.3,
// dist/chunks/index.DpLw24bj.js:7290-7340), so a unit whose file differs
// never reads another's entry for it; every unit of a pass runs over one
// seed at one path, so the paths match.
var vitestEvidenceCache = &EvidenceCache{
	Name:        "vitest transform cache",
	Args:        func(dir string) []string { return []string{"--fsModuleCache", "--fsModuleCachePath", dir} },
	Unsupported: vitestCacheUnsupported,
}

// vitestCacheFloor is the oldest vitest known to take --fsModuleCache and
// --fsModuleCachePath on its command line. 5.0.3 lists both as top-level
// CLI options (dist/chunks/cac.DfDGTQ9W.js:1160-1162) that override a
// project's config (PROJECT_CLI_OVERRIDES, dist/chunks/index.DpLw24bj.js:
// 12462-12463), and says they "were promoted to the top-level" from
// `experimental` (index.DpLw24bj.js:14779-14780). 4.1.5 has fsModuleCache
// only as experimental.fsModuleCache, with no CLI form of the path
// (dist/chunks/cac.*.js:1211-1212). 5.0.0 to 5.0.2 were not read, so the
// floor is the oldest version that was: 5.0.3.
const vitestCacheFloor = "5.0.3"

// vitestCacheUnsupported is why the vitest argv runs would not take the
// cache's options, "" when it would: its version, read from the
// package.json beside the entry script the command names
// (node_modules/vitest/vitest.mjs), or node_modules/vitest's for a bare
// vitest word. A version it cannot read is a reason, not a guess.
func vitestCacheUnsupported(argv []string, read func(rel string) ([]byte, error)) string {
	rel, ok := vitestPackageJSON(argv)
	if !ok {
		return "the command's vitest is outside the tree, so its version could not be read"
	}
	data, err := read(rel)
	if err != nil {
		return fmt.Sprintf("could not read %s, so the installed vitest's version is unknown: %v", rel, err)
	}
	var pkg struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Sprintf("could not parse %s, so the installed vitest's version is unknown: %v", rel, err)
	}
	if pkg.Version == nil || *pkg.Version == "" {
		return rel + " names no version, so the installed vitest's version is unknown"
	}
	v := *pkg.Version
	if !semver.IsValid("v" + v) {
		return fmt.Sprintf("%s gives version %q, which is not a version corral can compare", rel, v)
	}
	if semver.Compare("v"+v, "v"+vitestCacheFloor) < 0 {
		return fmt.Sprintf("vitest %s (%s) is older than %s, the oldest vitest known to take --fsModuleCache --fsModuleCachePath", v, rel, vitestCacheFloor)
	}
	return ""
}

// vitestPackageJSON is the tree-relative package.json of the vitest argv
// runs; ok=false when the command names one outside the tree.
func vitestPackageJSON(argv []string) (string, bool) {
	const root = "node_modules/vitest/package.json"
	i := vitestIndex(argv)
	if i < 0 {
		return root, true
	}
	a := path.Clean(filepath.ToSlash(argv[i]))
	var rel string
	switch b := path.Base(a); {
	case b == "vitest.mjs" || b == "vitest.js":
		rel = path.Join(path.Dir(a), "package.json")
	case strings.HasSuffix(a, "node_modules/.bin/vitest"):
		rel = strings.TrimSuffix(a, ".bin/vitest") + "vitest/package.json"
	default:
		return root, true
	}
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
