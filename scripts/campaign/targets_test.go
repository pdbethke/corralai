// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validTarget is a target that passes Validate; each test breaks exactly one
// thing in a copy of it, so a failure names the rule under test.
func validTarget() Target {
	return Target{
		Name:        "starlette",
		Upstream:    "Kludex/starlette",
		Fork:        "pdbethke/starlette",
		Branch:      "main",
		Path:        "starlette",
		Goals:       "",
		TestCommand: []string{"python", "-m", "pytest", "tests"},
		Substrate:   "jail",
		Seats: Seats{
			Mutant:       "gemini-3.6-pro",
			Writer:       "claude-code:claude-sonnet-5",
			ShadowWriter: "codex",
			Critic:       "gemini-3.6-flash",
			Derive:       "gemini-3.6-pro",
		},
		MaxTokens: 2000000,
		Timeout:   "90m",
	}
}

func writeTargets(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "targets.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTargetsRefusesEachMissingField(t *testing.T) {
	cases := []struct {
		key   string
		blank func(*Target)
	}{
		{"name", func(x *Target) { x.Name = "" }},
		{"upstream", func(x *Target) { x.Upstream = "" }},
		{"fork", func(x *Target) { x.Fork = "" }},
		{"branch", func(x *Target) { x.Branch = "" }},
		{"path", func(x *Target) { x.Path = "" }},
		{"test_command", func(x *Target) { x.TestCommand = nil }},
		{"substrate", func(x *Target) { x.Substrate = "" }},
		{"mutant", func(x *Target) { x.Seats.Mutant = "" }},
		{"writer", func(x *Target) { x.Seats.Writer = "" }},
		{"shadow_writer", func(x *Target) { x.Seats.ShadowWriter = "" }},
		{"critic", func(x *Target) { x.Seats.Critic = "" }},
		{"max_tokens", func(x *Target) { x.MaxTokens = 0 }},
		{"timeout", func(x *Target) { x.Timeout = "" }},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			tg := validTarget()
			c.blank(&tg)
			err := tg.Validate()
			if err == nil {
				t.Fatalf("blank %s: Validate returned nil", c.key)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Errorf("error %q does not name %q", err, c.key)
			}
		})
	}
	if err := validTarget().Validate(); err != nil {
		t.Fatalf("the base target must be valid: %v", err)
	}
}

func TestValidateRefusesUnknownSubstrateAndBadTimeout(t *testing.T) {
	tg := validTarget()
	tg.Substrate = "docker"
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "substrate") {
		t.Errorf("unknown substrate: got %v", err)
	}
	tg = validTarget()
	tg.Timeout = "ninety minutes"
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("unparseable timeout: got %v", err)
	}
}

func TestValidateRequiresDeriveOnlyWithoutGoals(t *testing.T) {
	tg := validTarget()
	tg.Goals, tg.Seats.Derive = "", ""
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "derive") {
		t.Errorf("no goals and no derive: got %v", err)
	}
	tg.Goals = "goals/starlette.json"
	if err := tg.Validate(); err != nil {
		t.Errorf("goals set, derive empty: got %v", err)
	}
}

func TestValidateRefusesWriterEqualCritic(t *testing.T) {
	tg := validTarget()
	tg.Seats.Critic = tg.Seats.Writer
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "critic") {
		t.Errorf("writer == critic: got %v", err)
	}
}

func TestValidateRefusesWriterEqualShadowWriter(t *testing.T) {
	tg := validTarget()
	tg.Seats.ShadowWriter = tg.Seats.Writer
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "shadow_writer") {
		t.Errorf("writer == shadow_writer: got %v", err)
	}
}

func TestLoadTargetsRefusesDuplicateNames(t *testing.T) {
	one := `{"name":"a","upstream":"o/a","fork":"f/a","branch":"main","path":"p","test_command":["go","test","./..."],"substrate":"jail",` +
		`"seats":{"mutant":"m","writer":"w","shadow_writer":"s","critic":"c","derive":"d"},"max_tokens":1,"timeout":"1h"}`
	p := writeTargets(t, `{"targets":[`+one+`,`+one+`]}`)
	if _, err := LoadTargets(p); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate names: got %v", err)
	}
	p = writeTargets(t, `{"targets":[`+one+`]}`)
	got, err := LoadTargets(p)
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Errorf("single target: got %v, %v", got, err)
	}
}

func TestLoadTargetsRefusesUnknownFields(t *testing.T) {
	p := writeTargets(t, `{"targets":[{"name":"a","testcomand":["go","test"]}]}`)
	if _, err := LoadTargets(p); err == nil || !strings.Contains(err.Error(), "testcomand") {
		t.Errorf("misspelled key must be an error naming it: got %v", err)
	}
}

func TestLoadTargetsValidatesEveryTarget(t *testing.T) {
	p := writeTargets(t, `{"targets":[{"name":"a"}]}`)
	if _, err := LoadTargets(p); err == nil || !strings.Contains(err.Error(), "a") {
		t.Errorf("an incomplete target must fail the load: got %v", err)
	}
}

func TestShippedTargetsFileLoads(t *testing.T) {
	if _, err := LoadTargets("targets.json"); err != nil {
		t.Fatalf("targets.json: %v", err)
	}
}
