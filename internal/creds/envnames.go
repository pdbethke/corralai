// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"sort"
	"strings"
)

// SecretShapedEnvNames returns, sorted and de-duplicated, the NAMES of the
// variables in environ (os.Environ form, "KEY=value") that look like
// secrets and hold a non-blank value: every CanonicalNames entry, plus any
// name matching *_API_KEY, *_TOKEN, *_SECRET, *_SECRET_*, *_PASSWORD,
// AWS_*, GH_TOKEN or GITHUB_TOKEN (compared upper-cased, so a lowercase
// motherduck_token counts). It never returns a value.
//
// It exists for the macOS jail's warning (named gap C1): code under audit
// there can read the startup environment of any non-Apple process the
// operator owns, corral's included, so corral names what it is carrying.
// It is a heuristic over names, deliberately broad: a false positive costs
// one word in a warning, a false negative a key the operator never hears of.
func SecretShapedEnvNames(environ []string) []string {
	canonical := map[string]bool{}
	for _, n := range CanonicalNames {
		canonical[n] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || strings.TrimSpace(value) == "" || seen[name] {
			continue
		}
		if canonical[name] || secretShapedName(strings.ToUpper(name)) {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func secretShapedName(n string) bool {
	switch {
	case n == "GH_TOKEN", n == "GITHUB_TOKEN":
		return true
	case strings.HasPrefix(n, "AWS_"):
		return true
	case strings.HasSuffix(n, "_API_KEY"), strings.HasSuffix(n, "_TOKEN"),
		strings.HasSuffix(n, "_SECRET"), strings.HasSuffix(n, "_PASSWORD"):
		return true
	case strings.Contains(n, "_SECRET_"):
		return true
	}
	return false
}
