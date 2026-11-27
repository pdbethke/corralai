// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
)

// jestRunCache is the jest row's EvidenceCache: one cache directory that a
// pass's runs share, warmed by its first run and frozen for the rest, as go's
// GOCACHE and cargo's target directory are. jest reads no environment
// variable for it (jest-config's getCacheDirectory is os.tmpdir() plus
// "jest", and the sandbox gives each run its own /tmp), so the command names
// it: Args puts --cacheDirectory=<dir> on each command, and a directory named
// on the command line is the one jest uses, over a config's cacheDirectory
// (measured on jest 30.5.2: with package.json's jest.cacheDirectory set, the
// command line's directory was filled and the config's was not made).
//
// What jest keeps there (jest 30.5.2, read from @jest/transform,
// jest-haste-map and @jest/test-sequencer, and listed after a run) is three
// things, and none of them is a test's result:
//
//   - jest-transform-cache-*: each file's transformed code, under a key that
//     hashes the file's bytes (or the transformer's own getCacheKey, which
//     for babel-jest hashes them too), so a mutated file is transformed
//     again. A project transformer whose getCacheKey ignores the file's
//     bytes would hand a mutant the original's code, and the mutant would be
//     graded as the original (a survivor, never a false kill); jest's own
//     runs on the developer's machine would serve it the same way.
//   - haste-map-*: each file's module metadata, reused for a file whose mtime
//     has not changed. Every run's workspace is written after the warming run
//     ends (runInJailCache takes the cache first), so its files are newer
//     than the map, and are read again.
//   - perf-cache-*: each test file's last duration and whether it failed,
//     which orders the files: the failed and the slow run first. Only
//     --onlyFailures selects by it, and jestCacheUnsupported refuses that.
var jestRunCache = &EvidenceCache{
	Name:        "jest cache directory",
	Args:        func(dir string) []string { return []string{"--cacheDirectory=" + dir} },
	Unsupported: jestCacheUnsupported,
}

// jestOwnCacheOptions name the cache in a command: its directory, or whether
// jest uses one, in both spellings jest's option parser (yargs) accepts.
var jestOwnCacheOptions = []string{"--cacheDirectory", "--cache-directory", "--cache", "--no-cache"}

// jestSelectingOptions choose the tests to run from something a shared cache
// or the tree's history holds, not from the command: --onlyFailures reads the
// perf cache, and on a warm one a mutant's run read the warming run's passes
// and ran no test ("No failed test found.", exit 1, or 0 under
// --passWithNoTests, while the same mutant's plain run failed its test; jest
// 30.5.2, in the jail and outside it). The rest choose by the haste map's dependency graph
// or by version control, which no audited run is graded by.
var jestSelectingOptions = []string{
	"--onlyFailures", "--only-failures", "--onlyChanged", "--only-changed",
	"--lastCommit", "--last-commit", "--changedSince", "--changed-since",
	"--changedFilesWithAncestor", "--changed-files-with-ancestor",
	"--findRelatedTests", "--find-related-tests",
}

// jestCacheUnsupported is jestRunCache's Unsupported: why argv's jest would
// not share the cache, or "" when it would. Only words after jest's own count.
func jestCacheUnsupported(argv []string, _ func(rel string) ([]byte, error)) string {
	at := jestIndex(argv)
	if at < 0 {
		return ""
	}
	words := argv[at+1:]
	switch {
	case hasFlag(words, jestOwnCacheOptions...):
		return "the command sets jest's cache itself"
	case hasFlag(words, jestSelectingOptions...) || shortBundleSets(words, jestShortFlags, "fo"):
		return "the command chooses its tests from jest's records of earlier runs or from version control"
	}
	return ""
}

// jestNoTestOptions make jest run no test: it lists, prints its config or
// clears its cache, and exits.
var jestNoTestOptions = []string{"--listTests", "--list-tests", "--showConfig", "--show-config", "--clearCache", "--clear-cache"}

// jestRunCacheArgs is the jest row's RunCacheArgs. jest keeps no test result
// it could serve in place of a run (jestRunCache), so a command that runs
// tests already forbids one, and is returned as it stands, RunCacheRewritten
// in RunCacheCmd's terms; RunCacheCmd adds the cache's own words. A command
// that runs no test is RunCacheNoTest. A shell wrapper, a package script or
// any command whose jest is not a word of its own is RunCacheUnreadable, as
// the go and cargo rows treat what they cannot read; so is a command with
// "--", after which an appended option would be a test path pattern, and a
// watch mode, which never ends.
func jestRunCacheArgs(cmd []string) ([]string, RunCacheRewrite) {
	at := jestIndex(cmd)
	if at < 0 || cmdIsShellWrapped(cmd) || slices.Contains(cmd, "--") {
		return cmd, RunCacheUnreadable
	}
	words := cmd[at+1:]
	switch {
	case hasFlag(words, jestNoTestOptions...):
		return cmd, RunCacheNoTest
	case hasFlag(words, "--watch", "--watchAll", "--watch-all"):
		return cmd, RunCacheUnreadable
	}
	return cmd, RunCacheRewritten
}
