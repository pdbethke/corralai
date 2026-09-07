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
//
// INJECTIVE: a name or value carrying the form's own delimiters (`,` `=`)
// or the escape (`\`) is escaped, so two different maps never render the
// same bytes. It was not: a test-writer named
// `claude-sonnet-5,test-writer-shadow=claude-opus-5` rendered identically
// to a map that also named a challenger writer, and KeyInputs.CacheKey
// served one herd's verdict for the other (review 28c4ae555cbc#R2, Claude
// Code reviewing, Codex verifying). Escaping rather than length-prefixing
// keeps every key and record that never carried a delimiter byte-identical
// — nothing already cached or recorded changes form — and only the
// ambiguous strings, which had to change, do.
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
		parts = append(parts, kvEscape(k)+"="+kvEscape(m[k]))
	}
	return strings.Join(parts, ",")
}

var kvEscaper = strings.NewReplacer(`\`, `\\`, ",", `\,`, "=", `\=`)

// kvEscape makes a name or value safe inside CanonicalKV's form.
func kvEscape(s string) string { return kvEscaper.Replace(s) }
