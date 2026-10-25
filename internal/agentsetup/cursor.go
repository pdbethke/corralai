// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// kv is one member of a JSON object, in file order, with its value left as the
// exact bytes found.
type kv struct {
	key string
	val json.RawMessage
}

// decodeObject splits a JSON object into ordered members. The caller has
// already checked the text is valid JSON.
func decodeObject(raw []byte) ([]kv, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []kv
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, kv{k, v})
	}
	return out, nil
}

func quote(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// detectIndent returns the indent unit the file uses: a tab, or the width of
// the first indented line in spaces; two spaces when it has none.
func detectIndent(old []byte) string {
	lines := strings.Split(string(old), "\n")
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		if l[0] == '\t' {
			return "\t"
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if n > 0 {
			return strings.Repeat(" ", n)
		}
		break
	}
	return "  "
}

// encodeObject writes members one per line at depth, closing at depth-1.
func encodeObject(members []kv, indent string, depth int) string {
	if len(members) == 0 {
		return "{}"
	}
	in := strings.Repeat(indent, depth)
	var b strings.Builder
	b.WriteString("{\n")
	for i, m := range members {
		b.WriteString(in + quote(m.key) + ": " + string(m.val))
		if i < len(members)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat(indent, depth-1) + "}")
	return b.String()
}

func corralEntry(bin, indent string, depth int) (json.RawMessage, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent(strings.Repeat(indent, depth), indent)
	err := e.Encode(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{bin, []string{"mcp"}})
	return bytes.TrimSpace(b.Bytes()), err
}

// parseTop validates old and returns its members; a servers value that is
// present but not an object is an error.
func parseTop(old []byte, key string) (top []kv, servers []kv, si int, err error) {
	if !json.Valid(old) {
		return nil, nil, -1, errors.New("config is not valid JSON (comments are not supported)")
	}
	if top, err = decodeObject(old); err != nil {
		return nil, nil, -1, fmt.Errorf("config: %w", err)
	}
	si = -1
	for i, m := range top {
		if m.key == key {
			si = i
			break
		}
	}
	if si >= 0 {
		if servers, err = decodeObject(top[si].val); err != nil {
			return nil, nil, -1, fmt.Errorf("%q is not a JSON object", key)
		}
	}
	return top, servers, si, nil
}

func finish(top []kv, indent string, old []byte) []byte {
	s := encodeObject(top, indent, 1)
	nl := len(old) == 0 || bytes.HasSuffix(old, []byte("\n"))
	if nl {
		s += "\n"
	}
	return []byte(s)
}

// MergeServerConfig adds or replaces <key>.corral = {"command": bin,
// "args": ["mcp"]} in a JSON config. Every other member, at the top level and
// among the servers, keeps its position and its exact bytes; the file's own
// indent (tab or N spaces) is reused. Text that encoding/json cannot parse
// (JSONC with comments included) or a <key> that is not an object is refused
// and nothing is returned: setup must never rewrite what it could not read.
// Empty old is a new file holding just the server.
func MergeServerConfig(old []byte, key string, bin string) ([]byte, error) {
	if len(bytes.TrimSpace(old)) == 0 {
		old = nil
	}
	indent := "  "
	var top, servers []kv
	si := -1
	if old != nil {
		var err error
		if top, servers, si, err = parseTop(old, key); err != nil {
			return nil, err
		}
		indent = detectIndent(old)
	}
	entry, err := corralEntry(bin, indent, 2)
	if err != nil {
		return nil, err
	}
	replaced := false
	next := servers[:0:0]
	for _, m := range servers {
		if m.key == "corral" {
			if replaced {
				continue // a duplicate key: keep one
			}
			replaced = true
			m.val = entry
		}
		next = append(next, m)
	}
	if !replaced {
		next = append(next, kv{"corral", entry})
	}
	val := json.RawMessage(encodeObject(next, indent, 2))
	if si >= 0 {
		top[si].val = val
	} else {
		top = append(top, kv{key, val})
	}
	return finish(top, indent, old), nil
}

// UnmergeServerConfig deletes only <key>.corral. When nothing is there the
// input is returned unchanged, byte for byte. Invalid input is refused.
func UnmergeServerConfig(old []byte, key string) ([]byte, error) {
	if len(bytes.TrimSpace(old)) == 0 {
		return old, nil
	}
	top, servers, si, err := parseTop(old, key)
	if err != nil {
		return nil, err
	}
	next := servers[:0:0]
	for _, m := range servers {
		if m.key != "corral" {
			next = append(next, m)
		}
	}
	if si < 0 || len(next) == len(servers) {
		return old, nil
	}
	indent := detectIndent(old)
	top[si].val = json.RawMessage(encodeObject(next, indent, 2))
	return finish(top, indent, old), nil
}

// MergeCursorConfig is MergeServerConfig for cursor's ~/.cursor/mcp.json.
func MergeCursorConfig(old []byte, bin string) ([]byte, error) {
	return MergeServerConfig(old, "mcpServers", bin)
}

// UnmergeCursorConfig is UnmergeServerConfig for cursor's ~/.cursor/mcp.json.
func UnmergeCursorConfig(old []byte) ([]byte, error) {
	return UnmergeServerConfig(old, "mcpServers")
}
