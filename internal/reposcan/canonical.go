// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"sort"
	"strings"
)

// CanonicalKV flattens a set of name/value pairs into one stable string:
// sorted by name, `name=value`, comma-joined, empty values omitted.
//
// It is shared by KeyInputs.ModelSet and KeyInputs.AuditConfig rather than
// spelled once per caller, because the two must agree forever: these strings
// are hashed into a verdict's content address, so a serialization that
// differed between them — or drifted between releases — would silently change
// every key and invalidate every cached verdict without anyone deciding to.
//
// An empty value is omitted rather than written as `name=`, so "role not set"
// and "role absent" cannot key differently for the same audit.
func CanonicalKV(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}
