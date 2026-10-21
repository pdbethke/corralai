// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
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
		Jail:        "bwrap",
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
	one := `{"name":"a","upstream":"o/a","fork":"f/a","branch":"main","path":"p","test_command":["go","test","./..."],"substrate":"jail","jail":"bwrap",` +
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
	p := writeTargets(t, `{"targets":[{"name":"zeta"}]}`)
	_, err := LoadTargets(p)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("an incomplete target must fail the load with ErrInvalid: got %v", err)
	}
	if !strings.Contains(err.Error(), `target "zeta": upstream is required`) {
		t.Errorf("error must name the target and the first missing key: %v", err)
	}
}

func TestValidateRefusesWhitespaceOnlyStrings(t *testing.T) {
	tg := validTarget()
	tg.Seats.Mutant = "  \t"
	if err := tg.Validate(); err == nil || !strings.Contains(err.Error(), "seats.mutant") {
		t.Errorf("whitespace-only seat: got %v", err)
	}
}

func TestLoadTargetsRefusesTrailingData(t *testing.T) {
	p := writeTargets(t, `{"targets":[]} {"targets":[]}`)
	if _, err := LoadTargets(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("trailing data: got %v", err)
	}
}

func TestShippedTargetsFileLoads(t *testing.T) {
	if _, err := LoadTargets("targets.json"); err != nil {
		t.Fatalf("targets.json: %v", err)
	}
}

func TestValidateRefusesForkEqualToUpstream(t *testing.T) {
	for _, fork := range []string{"Kludex/starlette", "kludex/STARLETTE"} {
		tg := validTarget()
		tg.Fork = fork
		err := tg.Validate()
		if err == nil || !strings.Contains(err.Error(), "fork") {
			t.Errorf("fork %q: err %v", fork, err)
		}
	}
}

// A targets file named relatively (`-targets targets.json`) has "." as its
// directory, so joining onto it left the goals path relative, and certify,
// which runs from the campaign directory, could not open it. The first real
// run (urfave/cli, 2026-10-21) died on exactly that.
func TestGoalsPathIsAbsoluteForARelativeTargetsFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := goalsPath("goals/cli.json", "targets.json")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "goals", "cli.json")
	if got != want {
		t.Fatalf("goalsPath = %q, want %q", got, want)
	}
	if got, _ := goalsPath("/abs/goals.json", "targets.json"); got != "/abs/goals.json" {
		t.Fatalf("an absolute goals path must pass through unchanged, got %q", got)
	}
	if got, _ := goalsPath("", "targets.json"); got != "" {
		t.Fatalf("no goals must stay no goals (derive), got %q", got)
	}
}

// The jail substrate runs in a named sandbox, never a defaulted one: bwrap,
// or a container whose image carries the project's toolchain. The image is
// required exactly when the jail is a container, and the workspace
// substrate, which runs no sandbox, takes neither.
func TestValidateJailAndImage(t *testing.T) {
	ok := func(f func(*Target)) Target { x := validTarget(); f(&x); return x }
	for name, tc := range map[string]struct {
		t    Target
		want string // "" = valid
	}{
		"bwrap":                   {ok(func(x *Target) {}), ""},
		"container with image":    {ok(func(x *Target) { x.Jail, x.Image = "container", "corral-campaign/starlette:1" }), ""},
		"workspace, no jail":      {ok(func(x *Target) { x.Substrate, x.Jail = "workspace", "" }), ""},
		"jail missing":            {ok(func(x *Target) { x.Jail = "" }), "jail"},
		"unknown jail":            {ok(func(x *Target) { x.Jail = "chroot" }), "chroot"},
		"container, no image":     {ok(func(x *Target) { x.Jail = "container" }), "image"},
		"image without container": {ok(func(x *Target) { x.Image = "py:1" }), "image"},
		"workspace with jail":     {ok(func(x *Target) { x.Substrate = "workspace" }), "workspace"},
	} {
		err := tc.t.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: Validate = %v, want an error naming %q", name, err, tc.want)
		}
	}
}
