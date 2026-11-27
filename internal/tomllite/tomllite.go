// SPDX-License-Identifier: Elastic-2.0

// Package tomllite is corral's one reader of TOML text: line-based, read-only,
// and deliberately small. corral has no TOML library and reads TOML for a few
// narrow questions about files a repository owns: which tables a cargo
// config already declares (before vendoring appends its own), what a
// Cargo.toml names its package, and whether it declares any dependency. Every TOML question goes through this
// package so there is one reading of the syntax, never a second parser.
//
// What it does not do: values beyond the simple cases its callers need,
// multi-line strings, or inline tables. Its callers use it only to decide
// whether to refuse or what to name, so a miss falls through to the
// toolchain's own reading of the file, never to a silent merge.
package tomllite

import "strings"

// line is one meaningful line of a TOML text as this package reads it: a
// table header (header true, path the table, arrayOfTables for [[...]]) or a
// key, with path the table it belongs to joined to the key and value the raw
// text after '='. Every reader here walks lines, so the syntax is read once.
type line struct {
	path, value           string
	header, arrayOfTables bool
}

// lines is text read line by line: a leading byte-order mark and comments
// stripped, blank lines and lines that are neither a header nor a key =
// value dropped.
func lines(text string) []line {
	var out []line
	cur := ""                                 // the table the following keys belong to
	text = strings.TrimPrefix(text, "\ufeff") // a UTF-8 byte-order mark is not part of the first line
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(stripComment(raw))
		switch {
		case l == "":
			continue
		case strings.HasPrefix(l, "[[") && strings.HasSuffix(l, "]]"):
			cur = normKey(l[2 : len(l)-2])
			out = append(out, line{path: cur, header: true, arrayOfTables: true})
		case strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]"):
			cur = normKey(l[1 : len(l)-1])
			out = append(out, line{path: cur, header: true})
		default:
			k, v, isKV := strings.Cut(l, "=")
			if !isKV {
				continue
			}
			key := normKey(k)
			if cur != "" {
				key = cur + "." + key
			}
			out = append(out, line{path: key, value: strings.TrimSpace(v)})
		}
	}
	return out
}

// TablesDeclared is every table a TOML text opens, normalised (segments
// unquoted, spaces dropped): `[source."crates-io"]` and
// `source.crates-io.replace-with = …` both declare source.crates-io. Line-
// based on purpose — it decides only whether to refuse, so a miss costs the
// toolchain's own rejection, never a silent merge.
func TablesDeclared(text string) []string {
	var out []string
	for _, l := range lines(text) {
		switch {
		case l.arrayOfTables: // an array of tables is never a duplicate
		case l.header:
			out = append(out, l.path)
		default:
			segs := strings.Split(l.path, ".")
			for n := 2; n < len(segs); n++ { // a dotted key a.b.c opens table a.b (a is opened by its own header or key)
				out = append(out, strings.Join(segs[:n], "."))
			}
		}
	}
	return out
}

// Paths is every table header and every key of a TOML text, each as its full
// normalised path (a key under [a] is a.key; a dotted key a.b = … is a.b), in
// the order they appear. A caller asking whether a text declares something
// anywhere, as a table or as a key, reads this.
func Paths(text string) []string {
	var out []string
	for _, l := range lines(text) {
		out = append(out, l.path)
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

// StringValue is the plain quoted-string value of key in table, read with the
// same line-based reading TablesDeclared uses (a dotted key `package.name =`
// and a `[package]` header's `name =` are the same key). ok is false when the
// key is absent or its value is not a single-line quoted string — an inline
// table such as `{ workspace = true }` is not one — so a caller decides what
// "no name" means and never receives a guess.
func StringValue(text, table, key string) (string, bool) {
	want := normKey(table) + "." + normKey(key)
	for _, l := range lines(text) {
		if l.header || l.path != want {
			continue
		}
		v := l.value
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			return v[1 : len(v)-1], true
		}
		return "", false
	}
	return "", false
}
