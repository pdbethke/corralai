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
