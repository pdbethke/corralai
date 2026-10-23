// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"encoding/json"
	"strings"
	"testing"
)

const cursorIn = `{
    "other": {"keep":   [1,2,  3]},
    "mcpServers": {
        "x": {"command": "x",   "args": ["a"]},
        "corral": {"command": "/old", "args": ["mcp"]},
        "z": 1
    },
    "tail": true
}
`

func TestMergeCursorPreservesOtherServers(t *testing.T) {
	out, err := MergeCursorConfig([]byte(cursorIn), "/new/corral")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, keep := range []string{`"other": {"keep":   [1,2,  3]}`, `"x": {"command": "x",   "args": ["a"]}`, `"z": 1`, `"tail": true`} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q in\n%s", keep, s)
		}
	}
	var doc struct {
		S map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	var c struct {
		Command string
		Args    []string
	}
	if err := json.Unmarshal(doc.S["corral"], &c); err != nil || c.Command != "/new/corral" || len(c.Args) != 1 || c.Args[0] != "mcp" {
		t.Errorf("corral = %s (%v)", doc.S["corral"], err)
	}
	if strings.Index(s, `"other"`) > strings.Index(s, `"mcpServers"`) || strings.Index(s, `"x"`) > strings.Index(s, `"corral"`) || strings.Index(s, `"corral"`) > strings.Index(s, `"z"`) {
		t.Errorf("order changed:\n%s", s)
	}
	if !strings.Contains(s, "\n    \"mcpServers\"") {
		t.Errorf("indent not reused:\n%s", s)
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	a, _ := MergeCursorConfig([]byte(cursorIn), "/new/corral")
	b, err := MergeCursorConfig(a, "/new/corral")
	if err != nil || string(a) != string(b) {
		t.Fatalf("not idempotent: %v\n%s\n---\n%s", err, a, b)
	}
}

func TestMergeNewFileAndTabIndent(t *testing.T) {
	out, err := MergeCursorConfig(nil, "/c")
	if err != nil || !json.Valid(out) || !strings.Contains(string(out), "\n  \"mcpServers\"") {
		t.Fatalf("%v\n%s", err, out)
	}
	tab, err := MergeServerConfig([]byte("{\n\t\"servers\": {}\n}\n"), "servers", "/c")
	if err != nil || !strings.Contains(string(tab), "\n\t\t\"corral\": {\n\t\t\t\"command\"") {
		t.Fatalf("%v\n%s", err, tab)
	}
}

func TestMergeCursorRefusesInvalid(t *testing.T) {
	for _, in := range []string{`{not json`, `{"mcpServers": []}`, `{"mcpServers": null}`, `[]`, "{\n// c\n\"mcpServers\":{}}"} {
		out, err := MergeCursorConfig([]byte(in), "/c")
		if err == nil || out != nil {
			t.Errorf("%q: out=%q err=%v", in, out, err)
		}
	}
}

func TestUnmergeCursorRemovesOnlyCorral(t *testing.T) {
	out, err := UnmergeCursorConfig([]byte(cursorIn))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "corral") || !strings.Contains(s, `"x": {"command": "x",   "args": ["a"]}`) || !strings.Contains(s, `"z": 1`) || !strings.Contains(s, `"tail": true`) {
		t.Errorf("\n%s", s)
	}
	same, err := UnmergeCursorConfig([]byte(`{"mcpServers":{"x":{}}}`))
	if err != nil || string(same) != `{"mcpServers":{"x":{}}}` {
		t.Errorf("no-op changed bytes: %q %v", same, err)
	}
	if _, err := UnmergeCursorConfig([]byte(`{bad`)); err == nil {
		t.Error("accepted invalid")
	}
}
