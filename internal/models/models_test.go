// SPDX-License-Identifier: Elastic-2.0

package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoEntries = `{
  "fast":  {"provider": "google", "model": "gemini-3.6-flash"},
  "local": {"provider": "ollama", "model": "qwen3.5:9b-q8_0", "endpoint": "http://127.0.0.1:11434"}
}`

func writeRepoRegistry(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".corral")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A repo with no .corral/models.json has no registry, and that is not an
// error: the registry is additive and every existing run must keep working.
func TestLoadNoRegistryIsNotAnError(t *testing.T) {
	t.Setenv(EnvInline, "")
	t.Setenv(EnvFile, "")
	reg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg != nil {
		t.Fatalf("Load = %+v, want nil registry", reg)
	}
	// A nil registry answers every lookup with "not an alias".
	if _, ok := reg.Lookup("fast"); ok {
		t.Errorf("nil registry resolved an alias")
	}
}

func TestLoadFromRepoFile(t *testing.T) {
	t.Setenv(EnvInline, "")
	t.Setenv(EnvFile, "")
	reg, err := Load(writeRepoRegistry(t, twoEntries))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := reg.Lookup("fast")
	if !ok {
		t.Fatalf("alias fast not found; aliases = %v", reg.Aliases())
	}
	if e.Provider != "google" || e.Model != "gemini-3.6-flash" || e.Endpoint != "" {
		t.Errorf("entry = %+v", e)
	}
	l, ok := reg.Lookup("local")
	if !ok || l.Endpoint != "http://127.0.0.1:11434" || !l.IsLocal() {
		t.Errorf("local entry = %+v ok=%v", l, ok)
	}
	if !strings.Contains(reg.Source, filepath.Join(".corral", "models.json")) {
		t.Errorf("Source = %q, want the repo file path", reg.Source)
	}
}

// Precedence: inline env beats the env file, which beats the repo file.
func TestLoadPrecedence(t *testing.T) {
	root := writeRepoRegistry(t, `{"seat": {"provider": "google", "model": "from-repo-file"}}`)
	envFile := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(envFile, []byte(`{"seat": {"provider": "google", "model": "from-env-file"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvInline, "")
	t.Setenv(EnvFile, envFile)
	reg, err := Load(root)
	if err != nil {
		t.Fatalf("Load (env file): %v", err)
	}
	if e, _ := reg.Lookup("seat"); e.Model != "from-env-file" {
		t.Errorf("env file did not win: %+v", e)
	}

	t.Setenv(EnvInline, `{"seat": {"provider": "google", "model": "from-inline"}}`)
	reg, err = Load(root)
	if err != nil {
		t.Fatalf("Load (inline): %v", err)
	}
	if e, _ := reg.Lookup("seat"); e.Model != "from-inline" {
		t.Errorf("inline did not win: %+v", e)
	}
}

func TestLoadRefusesMissingEnvFile(t *testing.T) {
	t.Setenv(EnvInline, "")
	t.Setenv(EnvFile, filepath.Join(t.TempDir(), "nope.json"))
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load = nil error for a missing CORRALAI_MODELS_FILE, want a refusal")
	}
}

// The five refusals. Each must name the fix, not just the fault.
func TestLoadRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"malformed json", `{"fast": {`, []string{"not valid JSON"}},
		{"alias named default", `{"default": {"provider": "google", "model": "gemini-3.6-flash"}}`, []string{"default", "no default models", "rename"}},
		{"missing provider", `{"fast": {"model": "gemini-3.6-flash"}}`, []string{"provider"}},
		{"missing model", `{"fast": {"provider": "google"}}`, []string{"model"}},
		{"endpoint on a hosted provider", `{"fast": {"provider": "google", "model": "gemini-3.6-flash", "endpoint": "http://127.0.0.1:11434"}}`, []string{"endpoint", "google"}},
		{"no endpoint on a local provider", `{"l": {"provider": "ollama", "model": "qwen3.5:9b-q8_0"}}`, []string{"endpoint", "ollama"}},
		{"unknown provider", `{"fast": {"provider": "acme", "model": "x"}}`, []string{"acme", "provider"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvInline, tc.body)
			t.Setenv(EnvFile, "")
			_, err := Load(t.TempDir())
			if err == nil {
				t.Fatalf("Load(%s) = nil error, want a refusal", tc.body)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}

func TestLookupUnknownAliasIsNotAnError(t *testing.T) {
	// An unknown value is NOT a refusal at the registry: the seat flags accept
	// a concrete model name too, so "not an alias" is an ordinary answer.
	t.Setenv(EnvInline, twoEntries)
	t.Setenv(EnvFile, "")
	reg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := reg.Lookup("gemini-3.6-flash"); ok {
		t.Errorf("a concrete model name resolved as an alias")
	}
	if got := reg.Aliases(); len(got) != 2 || got[0] != "fast" || got[1] != "local" {
		t.Errorf("Aliases = %v, want sorted [fast local]", got)
	}
}

// The unknown-alias refusal message the seat flags reuse must list what IS
// known, so the operator can fix it from the message alone.
func TestUnknownAliasErrorListsKnownAliases(t *testing.T) {
	t.Setenv(EnvInline, twoEntries)
	t.Setenv(EnvFile, "")
	reg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = reg.UnknownAliasErr("writer-model", "strong")
	if err == nil {
		t.Fatal("UnknownAliasErr = nil, want a refusal")
	}
	for _, w := range []string{"strong", "fast", "local", "--writer-model"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

func TestParseRejectsNonObjectEntry(t *testing.T) {
	if _, err := Parse([]byte(`{"fast": "gemini-3.6-flash"}`), "inline"); err == nil {
		t.Fatal("Parse = nil error for a string entry, want a refusal")
	}
}

// STRICT MODE. Off (the default), an unknown value is the concrete model name
// the flags have always accepted — which lets a MISTYPED alias through as a
// bogus model that dies at the seat, hours into a paid run. On, it is refused.
func TestStrictModeIsOffByDefaultAndReadFromTheDocument(t *testing.T) {
	t.Setenv(EnvFile, "")
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"absent", `{"fast": {"provider":"google","model":"gemini-3.6-flash"}}`, false},
		{"false", `{"strict": false, "fast": {"provider":"google","model":"gemini-3.6-flash"}}`, false},
		{"true", `{"strict": true, "fast": {"provider":"google","model":"gemini-3.6-flash"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvInline, tc.body)
			reg, err := Load(t.TempDir())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if reg.Strict != tc.want {
				t.Errorf("Strict = %v, want %v", reg.Strict, tc.want)
			}
			// The reserved key is never an alias, and never counts as a model.
			if _, ok := reg.Lookup(StrictKey); ok {
				t.Errorf("%q resolved as an alias", StrictKey)
			}
			if reg.Len() != 1 {
				t.Errorf("Len = %d, want 1 (the reserved key is not a model)", reg.Len())
			}
		})
	}
}

// A registry trying to DECLARE a model under the reserved key is refused by
// name — a key that meant two things depending on its value would be worse
// than either meaning.
func TestReservedStrictKeyCannotBeAnAlias(t *testing.T) {
	t.Setenv(EnvFile, "")
	t.Setenv(EnvInline, `{"strict": {"provider":"google","model":"gemini-3.6-flash"}}`)
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Load = nil error for an alias named by the reserved key, want a refusal")
	}
	for _, w := range []string{"strict", "reserved", "rename"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

// The strict refusal must be actionable on its own: what was typed, what is
// declared, and the three ways out.
func TestStrictUnknownAliasErrorIsActionable(t *testing.T) {
	t.Setenv(EnvFile, "")
	t.Setenv(EnvInline, `{"strict": true, "fast": {"provider":"google","model":"gemini-3.6-flash"}}`)
	reg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	msg := reg.UnknownAliasErr("writer-model", "nope").Error()
	for _, w := range []string{"nope", "fast", "STRICT", "--writer-model"} {
		if !strings.Contains(msg, w) {
			t.Errorf("error %q does not name %q", msg, w)
		}
	}
}

// TestAuditedRepoCannotPickItsAuditors pins the router review's first
// finding: the registry rewrites seat values in place and was read from the
// repository under audit, so a change under review could ship a
// .corral/models.json that sent the writer seat (and the source it is
// shown) to a host of its choosing, or re-pointed a concrete name the
// operator typed at a retired model. Two rules close it: an alias may not
// be spelled like a concrete model, and on a CI runner the checkout's own
// registry is ignored.
func TestAuditedRepoCannotPickItsAuditors(t *testing.T) {
	t.Setenv(EnvInline, "")
	t.Setenv(EnvFile, "")
	for _, alias := range []string{"claude-sonnet-5", "gemini-3.6-flash", "google/gemini-3.6-flash", "GPT-5", "o3-mini"} {
		doc := `{"` + alias + `": {"provider": "ollama", "model": "evil:latest", "endpoint": "http://attacker.example:11434"}}`
		if _, err := Parse([]byte(doc), "hostile"); err == nil {
			t.Errorf("alias %q accepted — an operator who typed that exact model would be redirected", alias)
		} else if !strings.Contains(err.Error(), "spelled like a concrete model name") {
			t.Errorf("alias %q refused for the wrong reason: %v", alias, err)
		}
	}
	// A purpose-named alias is still fine.
	if _, err := Parse([]byte(`{"fast": {"provider": "google", "model": "gemini-3.6-flash"}}`), "ok"); err != nil {
		t.Errorf("a purpose-named alias was refused: %v", err)
	}

	root := writeRepoRegistry(t, twoEntries)
	t.Setenv("GITHUB_ACTIONS", "true")
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 0 {
		t.Errorf("on a runner the checkout's registry was loaded: %v", reg.Aliases())
	}
	if !RepoLocalIgnored() || !RepoLocalExists(root) {
		t.Error("the caller cannot tell the file was ignored")
	}
	// The operator's own file still applies on a runner.
	t.Setenv(EnvFile, filepath.Join(root, RepoRelPath))
	reg, err = Load(root)
	if err != nil || reg.Len() == 0 {
		t.Errorf("CORRALAI_MODELS_FILE ignored on a runner: reg=%v err=%v", reg, err)
	}
}
