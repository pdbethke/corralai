// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// phpunitListCmd is PHPUnit's own `--list-tests`, which collects without
// running.
func phpunitListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, "--filter") {
		return nil, false
	}
	return append(slices.Clone(argv), "--list-tests", testPath), true
}

// phpunitParseList reads `--list-tests`' ` - Ns\Class::method` lines into
// `Class::method`, the form TestNamesInSource gives the critic. A data-set
// suffix (`"name"` or `#0`) is dropped and the method listed once, since one
// run of the method runs every data set. The namespace is dropped after
// de-duplicating, so two classes sharing a short name in two namespaces are
// listed twice and ResolveListedTest leaves the name unresolved.
func phpunitParseList(stdout string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		fq, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
		if !ok {
			continue
		}
		class, method, ok := strings.Cut(fq, "::")
		if !ok || class == "" {
			continue
		}
		if i := strings.IndexAny(method, `"#`); i >= 0 {
			method = method[:i]
		}
		if method == "" || seen[class+"::"+method] {
			continue
		}
		seen[class+"::"+method] = true
		ids = append(ids, class[strings.LastIndex(class, `\`)+1:]+"::"+method)
	}
	return ids
}

// phpunitSingleCmd runs one test method, every data set of it, in whatever
// namespace its class is: PHPUnit matches --filter against
// `Ns\Class::method with data set "name"`.
func phpunitSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	class, method, ok := strings.Cut(id, "::")
	if !ok || class == "" || method == "" || !narrowable(argv, "--filter") {
		return nil, false
	}
	filter := `/(?:^|\\)` + regexQuoter.Replace(class) + "::" + regexQuoter.Replace(method) + `(?: with data set .*)?$/`
	return append(slices.Clone(argv), "--filter", filter, testPath), true
}

// phpunitListUnits is PHPUnit's own `--list-test-files` over argv: the test
// files the command collects, after its configuration's suites, the
// operator's arguments and every group or filter option (measured on
// PHPUnit 11.5.57: under --group slow only the file with a slow test was
// listed, and under --exclude-group slow a file whose only test is slow was
// not).
func phpunitListUnits(argv []string) ([]string, bool) {
	if _, _, ok := phpunitUnitArgs(argv); !ok {
		return nil, false
	}
	return append(slices.Clone(argv), "--list-test-files"), true
}

// phpunitParseUnits reads the ` - <path>` lines under `Available test
// files:`; with no such heading, PHPUnit listed nothing (an error, another
// mode), which is no listing.
func phpunitParseUnits(stdout string) []string {
	var files []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if files == nil {
			if line == "Available test files:" {
				files = []string{}
			}
			continue
		}
		if f, ok := strings.CutPrefix(line, " - "); ok && f != "" {
			files = append(files, f)
		}
	}
	return files
}

// phpunitUnitCmd runs one test file: argv with the operator's file and
// directory arguments replaced by it. PHPUnit runs every argument it is
// given (measured: `phpunit tests tests/ATest.php` ran ATest twice beside
// BTest, 5 tests), and a file argument takes the place of the configured
// suites, --testsuite included (measured: `--testsuite other tests/ATest.php`
// ran ATest), so the options can all stay.
func phpunitUnitCmd(argv []string, unit string) ([]string, bool) {
	head, kept, ok := phpunitUnitArgs(argv)
	if !ok || unit == "" || strings.HasPrefix(unit, "-") {
		return nil, false
	}
	return append(append(slices.Clone(head), kept...), unit), true
}

// phpunitUnitArgs splits argv at its phpunit word into the words up to it
// and the options after it, without the file arguments. PHPUnit reads
// options after a file (measured: `tests/ATest.php --group slow` ran one
// test).
func phpunitUnitArgs(argv []string) (head, kept []string, ok bool) {
	i := wordIndex(argv, "phpunit")
	if i < 0 || !narrowable(argv, "--filter") {
		return nil, nil, false
	}
	kept, ok = dropFileArgs(argv[i+1:], true, phpunitOptions.takesValue)
	return argv[:i+1], kept, ok
}
