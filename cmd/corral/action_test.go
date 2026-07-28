// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// actionFetchLine returns the `git fetch` command action.yml actually ships,
// read from the file itself rather than restated here — a test that asserted
// a copy of the command would keep passing while the shipped action broke.
func actionFetchLine(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.Contains(ln, "git fetch") {
			return strings.TrimSpace(ln)
		}
	}
	t.Fatal("action.yml has no `git fetch` line")
	return ""
}

// runGit runs a git command in dir, skipping the test when git itself is
// unusable in this environment.
func runGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=corral", "GIT_AUTHOR_EMAIL=corral@example.com",
		"GIT_COMMITTER_NAME=corral", "GIT_COMMITTER_EMAIL=corral@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(t, dir, args...)
	if err != nil {
		t.Skipf("git %v in %s unusable here: %v: %s", args, dir, err, out)
	}
	return out
}

// actionPRFixture builds the shape a real pull_request run has: an origin
// repo whose `main` has ADVANCED since the fork point, and a checkout made
// the way actions/checkout makes it — a single-ref refspec covering only the
// PR branch, so `refs/remotes/origin/main` does not exist locally.
func actionPRFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	if err := os.MkdirAll(origin, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "fork point")
	mustGit(t, origin, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(origin, "changed.txt"), []byte("pr\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "the PR's change")
	// main advances after the fork point — the normal case, and the one that
	// makes a truncated base ancestry fatal.
	mustGit(t, origin, "checkout", "main")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base moved on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "base moves on")

	// actions/checkout's shape: a fresh repo with a SINGLE-ref refspec.
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "init", "--initial-branch=feature", ".")
	mustGit(t, work, "remote", "add", "origin", origin)
	mustGit(t, work, "config", "remote.origin.fetch", "+refs/heads/feature:refs/remotes/origin/feature")
	mustGit(t, work, "fetch", "--no-tags", "--prune", "origin", "+refs/heads/feature:refs/remotes/origin/feature")
	mustGit(t, work, "checkout", "feature")
	return work
}

// TestActionBaseFetchMakesTheDiffBaseUsable runs the EXACT `git fetch`
// command action.yml ships, against a checkout shaped the way
// actions/checkout shapes one, and then asks git the exact question
// --diff-base asks (`<base>...HEAD`, three dots, against the merge base).
//
// Before the fix the shipped line was `git fetch --no-tags --depth=1 origin
// "$GITHUB_BASE_REF"`, which fails two independent ways, both reproduced
// here in one fixture:
//
//	(a) a bare `origin main` refspec updates only FETCH_HEAD; it writes
//	    refs/remotes/origin/main only if the remote's configured refspec
//	    covers it, and actions/checkout configures a single-ref refspec, so
//	    `origin/main` does not exist → "unknown revision".
//	(b) --depth=1 writes .git/shallow and truncates the base's ancestry,
//	    destroying the merge base the documented `fetch-depth: 0` exists to
//	    provide → "no merge base".
func TestActionBaseFetchMakesTheDiffBaseUsable(t *testing.T) {
	work := actionPRFixture(t)

	line := actionFetchLine(t)
	// The line is shell, with "$GITHUB_BASE_REF" in it; run it as shell with
	// that variable set, exactly as the action's `run:` block does.
	cmd := exec.Command("bash", "-c", "set -euo pipefail; "+line)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("action.yml's fetch failed: %v\n%s", err, out)
	}

	if out, err := runGit(t, work, "rev-parse", "--verify", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("the action's fetch did not create refs/remotes/origin/main, so --diff-base origin/main cannot resolve: %v\n%s", err, out)
	}
	out, err := runGit(t, work, "diff", "--name-only", "origin/main...HEAD")
	if err != nil {
		t.Fatalf("`git diff origin/main...HEAD` (what --diff-base runs) failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "changed.txt") {
		t.Errorf("the three-dot diff should name the PR's own file; got:\n%s", out)
	}
	if strings.Contains(out, "base.txt") {
		t.Errorf("the three-dot diff must exclude files that only moved on the base branch; got:\n%s", out)
	}
}

// TestActionNamesTheRecordItProduces: a run's report header is
// "Repo adequacy — <owner>/<repo> @ <commit>". The action passed no --commit
// and `--repo .`, so EmitConfig.Repo was filepath.Base(".") = "." and the
// header read `local/. @ (no commit given)` — a signed, published record that
// names nothing. This asserts the shipped invocation carries the identity
// GitHub already knows, and — separately — that certify --repo really accepts
// those flag names, so the assertion cannot pass on a flag that doesn't exist.
func TestActionNamesTheRecordItProduces(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		"--commit", "github.sha",
		"--owner", "github.repository_owner",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("action.yml does not pass %q: the record it publishes names nothing", want)
		}
	}

	// The flags must exist and parse on the real command, not just look
	// plausible in YAML.
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--dry-run",
		"--commit", "deadbeef", "--owner", "pdbethke",
	}, &out, &errb); code != 0 {
		t.Fatalf("certify --repo rejected the flags the action passes: exit %d, stderr=%s", code, errb.String())
	}
}

// TestDocsNeverAdvertiseAnUncutActionTag: the docs showed
// `uses: pdbethke/corralai@v1`, and no v1 tag exists (the repo's tags are
// v0.1.0 and v0.2.0) — a copy-pasteable snippet that cannot resolve. The
// project's rule is that documentation describes what exists, so every
// `pdbethke/corralai@<ref>` in the docs must name a ref that resolves: a
// branch that exists, or a tag that has actually been cut.
func TestDocsNeverAdvertiseAnUncutActionTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := filepath.Join("..", "..")
	tagsOut, err := runGit(t, repoRoot, "tag", "-l")
	if err != nil {
		t.Skipf("git tag: %v: %s", err, tagsOut)
	}
	if strings.TrimSpace(tagsOut) == "" {
		t.Skip("no tags in this clone (shallow/tagless); cannot tell a cut tag from an uncut one")
	}
	tags := map[string]bool{}
	for _, tg := range strings.Fields(tagsOut) {
		tags[tg] = true
	}
	// Branches are resolvable refs too; `main` is where the action lands.
	tags["main"] = true

	ref := regexp.MustCompile(`pdbethke/corralai@([A-Za-z0-9._-]+)`)
	for _, doc := range []string{"README.md", "ROADMAP.md", filepath.Join("docs", "corral", "github-action.md")} {
		b, rerr := os.ReadFile(filepath.Join(repoRoot, doc))
		if rerr != nil {
			t.Fatalf("reading %s: %v", doc, rerr)
		}
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			if !tags[m[1]] {
				t.Errorf("%s advertises %s, but %q is neither an existing tag nor `main` — the snippet does not resolve", doc, m[0], m[1])
			}
		}
	}
}

// actionStep is a single composite step within action.yml's `runs.steps`.
type actionStep struct {
	Name  string            `yaml:"name"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`
	Run   string            `yaml:"run"`
}

// actionYAML is a minimal typed view of action.yml, enough to inspect its
// inputs and composite steps without hand-parsing YAML.
type actionYAML struct {
	Inputs map[string]struct {
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
		Default     string `yaml:"default"`
	} `yaml:"inputs"`
	Runs struct {
		Using string       `yaml:"using"`
		Steps []actionStep `yaml:"steps"`
	} `yaml:"runs"`
}

func loadActionYAML(t *testing.T) actionYAML {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	var a actionYAML
	if err := yaml.Unmarshal(b, &a); err != nil {
		t.Fatalf("parsing action.yml: %v", err)
	}
	return a
}

// TestActionRunScriptsHaveNoInlineExpressions is the fence for defect 2:
// GitHub expands every `${{ }}` textually into the `run:` script BEFORE bash
// ever sees the line, so any input value containing shell metacharacters
// executes. Every value the action needs inside a script must travel through
// `env:` (which GitHub also expands, but into an environment variable value,
// never into script text) and be referenced as an ordinary quoted shell
// variable instead.
func TestActionRunScriptsHaveNoInlineExpressions(t *testing.T) {
	a := loadActionYAML(t)
	if len(a.Runs.Steps) == 0 {
		t.Fatal("action.yml has no composite steps")
	}
	for _, step := range a.Runs.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("step %q run script contains a `${{ }}` expression — GitHub expands this into the script text before bash runs it, so a value with shell metacharacters executes as code. Move it into env: and reference it as a shell variable instead. Script:\n%s", step.Name, step.Run)
		}
	}
}

// findStepContaining returns the first composite step whose run script
// contains needle, failing the test if none does.
func findStepContaining(t *testing.T, a actionYAML, needle string) actionStep {
	t.Helper()
	for _, step := range a.Runs.Steps {
		if strings.Contains(step.Run, needle) {
			return step
		}
	}
	t.Fatalf("no step in action.yml has a run script containing %q", needle)
	panic("unreachable")
}

// TestActionInstallsCorralOntoPATH is the fence for defect 1: action.yml
// must not assume `corral` is already on the runner's PATH. It must install
// it itself, via the Go toolchain already on the runner (not
// actions/setup-go, which would silently swap the toolchain the audited
// project's own tests run under), into a private GOBIN, then publish that
// directory onto PATH for later steps via $GITHUB_PATH.
func TestActionInstallsCorralOntoPATH(t *testing.T) {
	a := loadActionYAML(t)
	install := findStepContaining(t, a, "go install")

	if !strings.Contains(install.Run, "github.com/pdbethke/corralai/cmd/corral@") {
		t.Errorf("install step does not `go install` the corral module path declared in go.mod; got:\n%s", install.Run)
	}
	if !strings.Contains(install.Run, "GITHUB_PATH") {
		t.Error("install step does not append its GOBIN to $GITHUB_PATH — later steps still would not find `corral` on PATH")
	}
	if !strings.Contains(install.Run, "RUNNER_TEMP") {
		t.Error("install step should install into a private GOBIN under $RUNNER_TEMP, not pollute the runner's default GOBIN/GOPATH")
	}
	// Mentioning actions/setup-go in a diagnostic message (as remediation
	// advice when `go` is missing) is fine; actually using it as a step
	// would replace the runner's Go toolchain, corrupting the toolchain the
	// audited project's own tests run under, so it must never appear as a
	// step invocation.
	for _, step := range a.Runs.Steps {
		if strings.Contains(step.Run, "uses: actions/setup-go") || strings.HasPrefix(strings.TrimSpace(step.Name), "actions/setup-go") {
			t.Error("action.yml must not use actions/setup-go as a step")
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "actions/setup-go@") {
		t.Error("action.yml must not depend on the actions/setup-go action")
	}

	cv, ok := a.Inputs["corral-version"]
	if !ok {
		t.Fatal(`action.yml should declare a "corral-version" input to override the installed ref`)
	}
	if cv.Default != "" {
		t.Errorf(`"corral-version" should default to "" (meaning: use the action's own ref), got %q`, cv.Default)
	}
}

// TestActionInstallStepFailsClearlyWithoutGo runs the real install script
// with every directory that has a `go` binary stripped from PATH. Before a
// preflight check, a runner without Go would die on a bare "go: command not
// found" from deep inside the script — this asserts the step instead fails
// fast with a message that tells the operator what to do about it.
func TestActionInstallStepFailsClearlyWithoutGo(t *testing.T) {
	a := loadActionYAML(t)
	install := findStepContaining(t, a, "go install")

	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not available in this test environment")
	}
	goDir := filepath.Dir(goPath)

	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || dir == goDir {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(dir, "go")); statErr == nil {
			continue
		}
		kept = append(kept, dir)
	}

	tmp := t.TempDir()
	envPairs := []string{
		"PATH=" + strings.Join(kept, string(os.PathListSeparator)),
		"RUNNER_TEMP=" + tmp,
		"GITHUB_PATH=" + filepath.Join(tmp, "github_path"),
		"CORRAL_VERSION=",
		"ACTION_REF=",
	}

	cmd := exec.Command("bash", "-c", install.Run)
	cmd.Env = envPairs
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("install step should fail when `go` is not on PATH; it exited 0:\n%s", out)
	}
	lower := strings.ToLower(string(out))
	if !strings.Contains(lower, "setup-go") {
		t.Errorf("install step's failure message should name actions/setup-go as the fix; got:\n%s", out)
	}
}

// runRunCorralStep runs the real "run corral" step's script (extracted from
// action.yml) with TEST_COMMAND set to testCommand, a stub `corral` on PATH
// that logs the argv it receives, and the other env vars the step needs.
// tmp is the caller's scratch dir, so a caller that needs to plant a marker
// file path inside testCommand (to prove it was or wasn't executed) can do
// so before calling. Returns the step's combined output, its error (nil on
// exit 0), and the test-command's own argv — everything the stub `corral`
// received after the `--` separator, not corral's own --repo/--owner/etc
// flags — or nil if the stub was never run.
//
// extraEnv entries (each "NAME=value") are appended after the defaults
// below, so a caller can override one (e.g. "MIN_KILL_RATE=0.8") without
// this function growing a dedicated parameter per input — later entries win
// when os/exec.Cmd.Env has a duplicate key.
func runRunCorralStep(t *testing.T, runStep actionStep, tmp, testCommand string, extraEnv ...string) (out []byte, runErr error, argv []string) {
	t.Helper()
	stubDir := filepath.Join(tmp, "stubbin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvLog := filepath.Join(tmp, "argv.log")
	// NUL-delimited, not newline-delimited: a newline-joined log has the
	// exact ambiguity this whole fix is about — it cannot distinguish "no
	// trailing empty argument" from "one trailing empty argument", since
	// both would just be a trailing newline. NUL cannot appear in argv at
	// all, so it is an unambiguous separator.
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"" + argvLog + "\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "corral"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "-c", runStep.Run)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+":"+os.Getenv("PATH"),
		"TEST_COMMAND="+testCommand,
		"DIFF_BASE=",
		"GOALS=",
		"MIN_KILL_RATE=",
		"REPO_OWNER=acme",
		"COMMIT_SHA=deadbeef",
		"ANTHROPIC_API_KEY=",
		"GITHUB_WORKSPACE="+workspace,
		// Make sure a real pull_request base-ref fetch path isn't
		// accidentally exercised in these unit tests.
		"GITHUB_BASE_REF=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, runErr = cmd.CombinedOutput()

	if argvBytes, err := os.ReadFile(argvLog); err == nil {
		// Exactly one NUL terminates each field, including the last one, so
		// trim exactly one trailing NUL (the final field's own terminator) —
		// never TrimRight, which would also eat a legitimately empty final
		// argument's terminator and reintroduce the very ambiguity NUL
		// delimiting exists to avoid.
		content := strings.TrimSuffix(string(argvBytes), "\x00")
		var full []string
		if content != "" {
			full = strings.Split(content, "\x00")
		}
		// The stub received corral's ENTIRE argv (its own --repo/--owner/etc
		// flags too); only what follows "--" is the test-command's own
		// argv, which is what every caller here actually wants to assert on.
		argv = []string{}
		for i, a := range full {
			if a == "--" {
				argv = append(argv, full[i+1:]...)
				break
			}
		}
	}
	return out, runErr, argv
}

// TestActionPassesMinKillRateOnlyWhenSet proves the min-kill-rate input is
// threaded onto corral's own argv as --min-kill-rate exactly when the input
// is non-empty, and omitted (not passed as an empty string) when it isn't —
// matching how --diff-base and --goals already behave, and keeping the flag
// genuinely opt-in end to end, not just at the corral CLI layer.
func TestActionPassesMinKillRateOnlyWhenSet(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	readFullArgv := func(tmp string) []string {
		t.Helper()
		argvBytes, err := os.ReadFile(filepath.Join(tmp, "argv.log"))
		if err != nil {
			t.Fatalf("no argv.log written — corral stub was never invoked: %v", err)
		}
		content := strings.TrimSuffix(string(argvBytes), "\x00")
		if content == "" {
			return nil
		}
		return strings.Split(content, "\x00")
	}

	t.Run("set", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "MIN_KILL_RATE=0.8")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		found := false
		for i, a := range full {
			if a == "--min-kill-rate" && i+1 < len(full) && full[i+1] == "0.8" {
				found = true
			}
		}
		if !found {
			t.Errorf("want --min-kill-rate 0.8 in corral's argv, got: %v", full)
		}
	})

	t.Run("unset", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "MIN_KILL_RATE=")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		for _, a := range full {
			if a == "--min-kill-rate" {
				t.Errorf("min-kill-rate input was empty; --min-kill-rate must not be passed at all, got: %v", full)
			}
		}
	})
}

// TestActionTestCommandWordSplitNotEvaluated is the fence for the one
// deliberate exception to defect 2: `test-command` must still split into
// argv words, but it must never be handed to bash as script text. This runs
// the real "run corral" step's script with a test-command containing `;`,
// backticks and `$( )`, and checks (a) none of the embedded commands
// actually ran, and (b) the literal text arrived as argv on a stub `corral`.
func TestActionTestCommandWordSplitNotEvaluated(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")
	if strings.Contains(runStep.Run, "${{") {
		t.Fatal("the run-corral step still inlines a ${{ }} expression; fix defect 2 first")
	}

	tmp := t.TempDir()
	pwned := filepath.Join(tmp, "pwned")
	malicious := "go test ./...; touch " + pwned + "; echo $(touch " + pwned + ")"

	out, runErr, argv := runRunCorralStep(t, runStep, tmp, malicious)
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}

	if _, statErr := os.Stat(pwned); statErr == nil {
		t.Fatal("the malicious test-command was executed by the shell (the injected `touch` ran) — word-splitting alone is not protecting this")
	}

	joined := strings.Join(argv, "\x1f")
	if !strings.Contains(joined, "touch") || !strings.Contains(joined, pwned) {
		t.Errorf("expected the test-command's literal text (including the embedded `touch` and its path) to arrive as argv words after `--`; got argv=%v", argv)
	}
}

// TestActionTestCommandStillSplitsAnOrdinaryCommand pins the ordinary case
// the whole mechanism exists for: "go test ./..." must still arrive as
// exactly three argv words, the same as it did before any hardening.
func TestActionTestCommandStillSplitsAnOrdinaryCommand(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")
	tmp := t.TempDir()

	out, runErr, argv := runRunCorralStep(t, runStep, tmp, "go test ./...")
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}
	want := []string{"go", "test", "./..."}
	if strings.Join(argv, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("test-command %q: got argv=%v, want=%v", "go test ./...", argv, want)
	}
}

// TestActionTestCommandPreservesQuotedWords is the fence for the regression
// a naive `read -ra` word split introduces: a real shell honours quoting, so
// `pytest -k "not slow"` is two flags plus one argument, "not slow" — an
// entirely ordinary pytest invocation. Splitting on whitespace alone turns
// the quotes into literal characters and breaks the quoted argument into
// two words, silently sending pytest a filter expression it never asked
// for. The parser must honour shell-style quoting without being a shell
// (never evaluating `$( )`, backticks, `;`, or redirection — that's
// TestActionTestCommandWordSplitNotEvaluated's job).
func TestActionTestCommandPreservesQuotedWords(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")
	tmp := t.TempDir()

	testCommand := `pytest -k "not slow"`
	out, runErr, argv := runRunCorralStep(t, runStep, tmp, testCommand)
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}
	want := []string{"pytest", "-k", "not slow"}
	if strings.Join(argv, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("test-command %q should preserve the quoted argument as one word (the way a real shell parses it); got argv=%v, want=%v", testCommand, argv, want)
	}
}

// TestActionTestCommandUnmatchedQuoteFailsClosed: a test-command that can't
// be parsed as shell-style words (an unterminated quote) must fail the step
// loudly, not silently misparse into some other argv and audit a command
// the operator never asked for.
func TestActionTestCommandUnmatchedQuoteFailsClosed(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")
	tmp := t.TempDir()

	out, runErr, argv := runRunCorralStep(t, runStep, tmp, `go test "unterminated`)
	if runErr == nil {
		t.Fatalf("an unmatched quote in test-command should fail the step, not silently audit something else; output:\n%s", out)
	}
	if len(argv) > 0 {
		t.Errorf("corral should never have been invoked when test-command could not be parsed; got argv=%v, output:\n%s", argv, out)
	}
}

// TestActionTestCommandRejectsEmbeddedNewline is the fence for a
// multi-line test-command (e.g. a YAML block scalar, `test-command: |`)
// being silently truncated to its first line. `read` returns success after
// consuming one line, so a naive split would leave `set -e` with nothing to
// catch — the step would exit 0 having quietly graded a different, partial
// command than the one written. It must fail instead, with a message that
// says why.
func TestActionTestCommandRejectsEmbeddedNewline(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")
	tmp := t.TempDir()

	multiline := "go test ./...\necho this-should-never-run-alone"
	out, runErr, argv := runRunCorralStep(t, runStep, tmp, multiline)
	if runErr == nil {
		t.Fatalf("a multi-line test-command should fail the step instead of silently running only its first line; output:\n%s", out)
	}
	if len(argv) > 0 {
		t.Errorf("corral should never have been invoked on a multi-line test-command; got argv=%v, output:\n%s", argv, out)
	}
}

// TestActionTestCommandEmptyFailsClosed is the fence for a door that has
// reopened four times now, each through a different input shape — and the
// first three "fixes" each guarded a shape instead of the actual invariant,
// so each was walked around by the next shape:
//
//   - Pre-fix, `read -ra A <<< ""` gave a zero-length argv, so corral saw
//     `--` with nothing after it and silently fell back to the language's
//     stock test command.
//   - The xargs-based parser instead produces a ONE-element array holding
//     "" for a bare-empty TEST_COMMAND (GNU xargs runs its command once
//     even on empty input without -r).
//   - A literal empty-quoted value (`test-command: '""'` or `"”"`) is not
//     whitespace, so it survived a whitespace-only pre-parse check, then
//     was reduced to a single zero-length word by xargs's own quote
//     removal — the same mechanism that makes `pytest -k "not slow"` work.
//   - A guard written as "reject argv of length 0, or length 1 with an
//     empty element" is STILL a shape special-case, not the actual
//     invariant, and `'"" ""'`, `"” ”"`, and `'"" pytest'` all walk
//     straight through it: two (or more) elements, first one empty.
//     `certify_repo.go` honours any non-empty argv as an explicit test
//     command and execs argv[0] — empty — for every candidate.
//
// The actual invariant, stated once, is about what corral NEEDS rather
// than what the input looked like: there must be at least one argv
// element, and the FIRST one — the program name — must not be empty.
// Nothing else about the argv's shape matters; a trailing or
// middle empty argument elsewhere is completely legitimate (see
// TestActionTestCommandPreservesEmptyArguments). That is what the
// post-parse guard in action.yml checks, and it is what subsumes every
// shape above plus any nobody has constructed yet.
func TestActionTestCommandEmptyFailsClosed(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	for name, testCommand := range map[string]string{
		"empty":                                "",
		"whitespace only":                      "   \t  ",
		"literal double-quoted empty":          `""`,
		"literal single-quoted empty":          `''`,
		"whitespace around a quoted empty":     `  ""  `,
		"two empty double-quoted words":        `"" ""`,
		"two empty single-quoted words":        `'' ''`,
		"empty program name, real second word": `"" pytest`,
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			out, runErr, argv := runRunCorralStep(t, runStep, tmp, testCommand)
			if runErr == nil {
				t.Fatalf("test-command %q should fail the step (required, and must not silently fall back to a guessed command); output:\n%s", testCommand, out)
			}
			if len(argv) > 0 {
				t.Errorf("corral should never have been invoked for test-command %q; got argv=%v, output:\n%s", testCommand, argv, out)
			}
			if !strings.Contains(strings.ToLower(string(out)), "test-command") {
				t.Errorf("failure message should name test-command as the problem; got:\n%s", out)
			}
		})
	}
}

// TestActionTestCommandPreservesEmptyArguments is the fence for the second
// manifestation of the same root cause: $( ) command substitution strips
// trailing newlines, so a NAIVE fix that captures xargs's newline-delimited
// output through `$( )` silently drops a trailing empty argument —
// `pytest ""` would arrive as a single word `[pytest]` instead of two,
// and `pytest -k ""` would hand pytest a `-k` flag with nothing after it.
// A NUL-delimited round trip (`xargs ... printf '%s\0'` into
// `mapfile -d ”`) must preserve empty arguments in every position:
// leading, middle, and trailing.
func TestActionTestCommandPreservesEmptyArguments(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	cases := []struct {
		name        string
		testCommand string
		want        []string
	}{
		{"trailing empty argument", `pytest ""`, []string{"pytest", ""}},
		{"empty flag value", `pytest -k ""`, []string{"pytest", "-k", ""}},
		{"empty flag value followed by another word", `pytest -k "" -x`, []string{"pytest", "-k", "", "-x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			out, runErr, argv := runRunCorralStep(t, runStep, tmp, tc.testCommand)
			if runErr != nil {
				t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
			}
			if strings.Join(argv, "\x1f") != strings.Join(tc.want, "\x1f") {
				t.Errorf("test-command %q: got argv=%#v, want=%#v", tc.testCommand, argv, tc.want)
			}
		})
	}
}
