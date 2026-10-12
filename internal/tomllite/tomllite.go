// SPDX-License-Identifier: Elastic-2.0

// Package tomllite is corral's one reader of TOML text: line-based, read-only,
// and deliberately small. corral has no TOML library and reads TOML for two
// narrow questions about files a repository owns — which tables a cargo
// config already declares (before vendoring appends its own), and, next,
// what a Cargo.toml names its package. Every TOML question goes through this
// package so there is one reading of the syntax, never a second parser.
//
// What it does not do: values beyond the simple cases its callers need,
// multi-line strings, or inline tables. Its callers use it only to decide
// whether to refuse or what to name, so a miss falls through to the
// toolchain's own reading of the file, never to a silent merge.
package tomllite

import "strings"

// TablesDeclared is every table a TOML text opens, normalised (segments
// unquoted, spaces dropped): `[source."crates-io"]` and
// `source.crates-io.replace-with = …` both declare source.crates-io. Line-
// based on purpose — it decides only whether to refuse, so a miss costs the
// toolchain's own rejection, never a silent merge.
func TablesDeclared(text string) []string {
	var out []string
	cur := "" // the table the following keys belong to
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(stripComment(line))
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]"):
			cur = normKey(line[2 : len(line)-2]) // an array of tables is never a duplicate
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			cur = normKey(line[1 : len(line)-1])
			out = append(out, cur)
		default:
			k, _, isKV := strings.Cut(line, "=")
			if !isKV {
				continue
			}
			key := normKey(k)
			if cur != "" {
				key = cur + "." + key
			}
			segs := strings.Split(key, ".")
			for n := 2; n < len(segs); n++ { // a dotted key a.b.c opens table a.b (a is opened by its own header or key)
				out = append(out, strings.Join(segs[:n], "."))
			}
		}
	}
	return out
}

// stripComment drops a trailing # comment, ignoring a # inside a quoted
// string.
func stripComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case quote == 0 && r == '#':
			return line[:i]
		}
	}
	return line
}

// normKey normalises a dotted TOML key: each segment trimmed and
// unquoted, rejoined with ".".
func normKey(k string) string {
	segs := strings.Split(k, ".")
	for i, sg := range segs {
		segs[i] = strings.Trim(strings.TrimSpace(sg), `"'`)
	}
	return strings.Join(segs, ".")
}
