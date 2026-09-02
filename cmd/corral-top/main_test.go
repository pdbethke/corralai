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

// TestEnvOrPrefersTheEnvironment covers the resolution behind corral-top's
// --brain and --token: each flag's DEFAULT is the environment variable named
// in its own help text, so `envOr` decides what an operator gets when they
// pass nothing. Getting it backwards would make a set CORRAL_BRAIN silently
// lose to a hardcoded localhost, which is the failure an operator would blame
// on the brain rather than on the client.
//
// Written for the executed-surface manifest: --brain, --token and --interval
// had no test of any kind, in a binary whose only tests rendered frames.
func TestEnvOrPrefersTheEnvironment(t *testing.T) {
	const key = "CORRAL_TOP_ENVOR_TEST"
	t.Setenv(key, "from-env")
	if got := envOr(key, "fallback"); got != "from-env" {
		t.Errorf("envOr with the variable set = %q, want %q", got, "from-env")
	}
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("envOr with an EMPTY variable = %q, want the fallback %q — an empty variable is not a value, and treating it as one hands the brain an empty URL", got, "fallback")
	}
	if got := envOr("CORRAL_TOP_DEFINITELY_UNSET", "fallback"); got != "fallback" {
		t.Errorf("envOr with the variable unset = %q, want %q", got, "fallback")
	}
}
