// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

func TestRenderFrame(t *testing.T) {
	raw := `{
	  "missions":[{"id":1,"directive":"Build a Go package 'calc' that parses expressions","status":"running","sprint":2}],
	  "tasks":[
	    {"key":"build","title":"build","role":"builder","status":"claimed","claimed_by":"Cody"},
	    {"key":"test","title":"test","role":"tester","status":"pending"},
	    {"key":"research","title":"research","role":"researcher","status":"done"}
	  ],
	  "active_agents":[
	    {"name":"Cody","role":"builder","status":"working"},
	    {"name":"Shep","role":"scrum","status":"idle"}
	  ],
	  "findings":[
	    {"severity":"high","type":"regression","status":"open"},
	    {"severity":"low","type":"note","status":"addressed"}
	  ],
	  "live_claims":[{"agent":"Cody","path":"calc/parser.go"}],
	  "recent_activity":[{"agent":"Shep","tool":"standup","detail":"1/3 done"}]
	}`
	var s state
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	frame := plain(render(&s, "http://localhost:9019"))
	for _, must := range []string{
		"mission #1", "running", "sprint 2",
		"tasks 1/3", "pending 1 · claimed 1 · done 1",
		"Cody", "builder", "working", "⚿ 1 claim(s)",
		"Shep", "scrum",
		"findings 1 open", "high 1",
		"standup", "1/3 done",
	} {
		if !strings.Contains(frame, must) {
			t.Fatalf("frame missing %q:\n%s", must, frame)
		}
	}
}

func TestRenderEmptyState(t *testing.T) {
	frame := plain(render(&state{}, "http://b"))
	if !strings.Contains(frame, "the herd (0)") || !strings.Contains(frame, "findings 0 open") {
		t.Fatalf("empty state should render calmly:\n%s", frame)
	}
}
