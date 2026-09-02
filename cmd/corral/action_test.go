// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
//
// Scope is every documentation-shaped file in the repo, found by walking (see
// docsAdvertisingAnActionRef) — the site included. It used to be a list of
// three filenames, and the site drifted past it.
//
// NOTE ON ORDERING, because this gate makes it load-bearing: a pin can only be
// bumped AFTER its tag is cut. Merge the release content, push the tag, then
// bump the pins in a follow-up. Putting the bump in the release PR fails here,
// correctly.
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

	docs := docsAdvertisingAnActionRef(t, repoRoot)
	// A walk that silently found nothing would pass green forever. The three
	// files this gate was born checking must always be among the scanned set;
	// if they aren't, the walk is broken, not the docs.
	for _, must := range []string{"README.md", "ROADMAP.md", filepath.Join("docs", "corral", "github-action.md")} {
		if _, ok := docs[must]; !ok {
			t.Fatalf("walk did not scan %s — the gate is not looking where it thinks it is", must)
		}
	}

	ref := regexp.MustCompile(`pdbethke/corralai@([A-Za-z0-9._-]+)`)
	for doc, body := range docs {
		for _, m := range ref.FindAllStringSubmatch(body, -1) {
			if !tags[m[1]] {
				t.Errorf("%s advertises %s, but %q is neither an existing tag nor `main` — the snippet does not resolve", doc, m[0], m[1])
			}
		}
	}
}

// docsAdvertisingAnActionRef returns every documentation-shaped file in the
// repo, keyed by its repo-relative path.
//
// This walks rather than reading a hand-maintained list because the list is
// what failed: the gate named README.md, ROADMAP.md and
// docs/corral/github-action.md, while the SAME `uses:` snippet also lived in
// site/src/content/docs/docs/github-action.mdx and
// site/src/components/CiGate.astro — so corralai.dev could advertise an uncut
// tag with CI fully green. Guard the property (no document anywhere names an
// unresolvable ref), not an enumeration of the places it happened to hold.
//
// .go is excluded deliberately: this test's own comment and regex mention
// `pdbethke/corralai@v1` as the historical example, and a gate that flagged its
// own explanation would be unfixable without deleting the explanation.
func docsAdvertisingAnActionRef(t *testing.T, repoRoot string) map[string]string {
	t.Helper()
	docExt := map[string]bool{
		".md": true, ".mdx": true, ".astro": true,
		".html": true, ".yml": true, ".yaml": true, ".txt": true,
	}
	skipDir := map[string]bool{
		".git": true, "node_modules": true, "dist": true, "vendor": true,
		".astro": true, "test-results": true,
	}
	out := map[string]string{}
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !docExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for documentation: %v", repoRoot, err)
	}
	// The walk sees the DISK; the gate is about the REPOSITORY. A contributor's
	// local draft, an uncommitted spec, or anything else .gitignore excludes is
	// not a document this project ships, and grading it turns a private scratch
	// file into a red suite for content that can never reach a user. That is a
	// false failure: it fails locally while CI — which only ever checks out
	// tracked files — stays green, so the two disagree about what "the docs"
	// are. Untracked-but-NOT-ignored files stay in scope on purpose: those are
	// work on its way to a commit, and catching a bad pin before it lands is
	// the whole point of the gate.
	for rel := range gitIgnoredDocs(t, repoRoot, out) {
		delete(out, rel)
	}
	return out
}

// gitIgnoredDocs returns the subset of docs' keys that the repository's ignore
// rules exclude, as a set of repo-relative paths.
//
// One `git check-ignore` process for the whole set rather than one per file:
// the walk finds hundreds of documentation-shaped files, and this runs on every
// invocation of the gate.
//
// FAILURE DIRECTION. When git cannot answer — not a repository, git missing, a
// check-ignore error — this returns nil, so NOTHING is filtered and the gate
// grades everything exactly as it did before this filter existed. That is the
// loud failure: a broken filter makes the gate over-report, which someone
// notices, instead of silently dropping documents and passing green forever.
// The opposite default would make a filter outage indistinguishable from clean
// docs.
func gitIgnoredDocs(t *testing.T, repoRoot string, docs map[string]string) map[string]bool {
	t.Helper()
	if len(docs) == 0 {
		return nil
	}
	rels := make([]string, 0, len(docs))
	for rel := range docs {
		rels = append(rels, rel)
	}
	// -z makes BOTH the stdin paths and the reported ones NUL-delimited, so a
	// path containing a newline cannot split one entry into two.
	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		// check-ignore exits 1 for "no path matched" — a successful answer of
		// "none", not an error. Anything else (128: not a repository) means the
		// question went unanswered; see FAILURE DIRECTION above.
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Logf("git check-ignore in %s: %v — grading every documentation file, ignored ones included", repoRoot, err)
			return nil
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored[filepath.Clean(p)] = true
		}
	}
	return ignored
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
	return runRunCorralStepWithStub(t, runStep, tmp, testCommand, "", extraEnv...)
}

// runRunCorralStepWithStub is runRunCorralStep with control over what the stub
// `corral` DOES once it has logged its argv. stubTail is appended to the stub
// script, so a caller can make it print a report on stdout and exit non-zero —
// which is what the step's own reporting and exit-status handling have to cope
// with, and what a stub that always exits 0 can never exercise. An empty
// stubTail keeps the original behaviour (log argv, exit 0).
func runRunCorralStepWithStub(t *testing.T, runStep actionStep, tmp, testCommand, stubTail string, extraEnv ...string) (out []byte, runErr error, argv []string) {
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
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"" + argvLog + "\"\n" + stubTail
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
		"TOP=",
		"REPO_OWNER=acme",
		"COMMIT_SHA=deadbeef",
		// The key now travels as MODEL_KEY plus the name of the variable it
		// should become, rather than being hardwired to ANTHROPIC_API_KEY.
		"MODEL_KEY=",
		"MODEL_KEY_ENV=",
		"ANTHROPIC_KEY_IN=",
		"GEMINI_KEY_IN=",
		"OPENAI_KEY_IN=",
		"DERIVE_MODEL=",
		"WRITER_MODEL=",
		"MUTANT_MODEL=",
		"CRITIC_MODEL=",
		"SHADOW_MODEL=",
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

// The --tests map has to reach corral's argv, because the failure mode when it
// doesn't is silent: on a project whose layout filename-pairing can't read
// (routine in JS/TS) the scan finds no candidate, audits nothing, and the gate
// passes GREEN on exactly the change it was installed to inspect.
func TestActionPassesTestsMapOnlyWhenSet(t *testing.T) {
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
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TESTS=corral-tests.json")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		found := false
		for i, a := range full {
			if a == "--tests" && i+1 < len(full) && full[i+1] == "corral-tests.json" {
				found = true
			}
		}
		if !found {
			t.Errorf("want --tests corral-tests.json in corral's argv, got: %v", full)
		}
	})

	t.Run("unset", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TESTS=")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		for _, a := range full {
			if a == "--tests" {
				t.Errorf("tests input was empty; --tests must not be passed at all, got: %v", full)
			}
		}
	})
}

// TestActionPassesTopOnlyWhenSet: an audit costs roughly (mutants × the
// TARGET's whole suite runtime) PER FILE, so cost scales with how many files
// the diff touched — a number the PR author picks, not the workflow author.
// Without a bound, a ten-file PR is a ten-fold job: on a repo whose suite takes
// a minute, that is a job measured in tens of hours and the API spend to match,
// discovered only once it is running. `--top` already exists on the CLI (it is
// named in this action's own reserved-flag list); this exposes it, opt-in and
// omitted-when-empty like --min-kill-rate, so a workflow can put a ceiling on
// what a single PR can cost.
func TestActionPassesTopOnlyWhenSet(t *testing.T) {
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

	if _, ok := a.Inputs["top"]; !ok {
		t.Fatal(`action.yml should declare a "top" input so a workflow can bound what one PR costs`)
	}

	// Same rule as TestActionNamesTheRecordItProduces: the flag must exist and
	// parse on the real command, so this cannot pass on a flag that only looks
	// plausible in YAML.
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--dry-run", "--top", "3",
	}, &out, &errb); code != 0 {
		t.Fatalf("certify --repo rejected --top, the flag the action now passes: exit %d, stderr=%s", code, errb.String())
	}

	t.Run("set", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TOP=3")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		found := false
		for i, a := range full {
			if a == "--top" && i+1 < len(full) && full[i+1] == "3" {
				found = true
			}
		}
		if !found {
			t.Errorf("want --top 3 in corral's argv, got: %v", full)
		}
	})

	t.Run("unset", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TOP=")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		for _, a := range full {
			if a == "--top" {
				t.Errorf("top input was empty; --top must not be passed at all (corral keeps its own default), got: %v", full)
			}
		}
	})
}

// TestActionPassesTimeoutOnlyWhenSet: the per-file budget (`--timeout`,
// default 10m) is a cost guardrail sized for a PR author who did not choose to
// start an hours-long job. It was a CLI-only flag — the README said so — so a
// workflow that WANTS the whole audit (this repo's own self-audit, or anyone
// on faster hardware) had no way to ask for it: on 2026-08-28 the self-audit
// picked cmd/corral/certify_repo.go, ran `go test ./...` per mutant on a
// 4-vCPU runner, and hit the 10m ceiling before a single mutant scored —
// COULD-NOT-GRADE, no number. Raising the ceiling is the operator's call, and
// it needs a door. Same shape as `top`: passed through verbatim when set,
// absent when not, so corral keeps ownership of the default.
func TestActionPassesTimeoutOnlyWhenSet(t *testing.T) {
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

	if _, ok := a.Inputs["timeout"]; !ok {
		t.Fatal(`action.yml should declare a "timeout" input so a workflow can let a whole audit run`)
	}

	// The flag must exist and parse on the real command, with a Go duration.
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--dry-run", "--timeout", "5h30m",
	}, &out, &errb); code != 0 {
		t.Fatalf("certify --repo rejected --timeout, the flag the action now passes: exit %d, stderr=%s", code, errb.String())
	}

	t.Run("set", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TIMEOUT=5h30m")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		found := false
		for i, a := range full {
			if a == "--timeout" && i+1 < len(full) && full[i+1] == "5h30m" {
				found = true
			}
		}
		if !found {
			t.Errorf("want --timeout 5h30m in corral's argv, got: %v", full)
		}
	})

	t.Run("unset", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", "TIMEOUT=")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		full := readFullArgv(tmp)
		for _, a := range full {
			if a == "--timeout" {
				t.Errorf("timeout input was empty; --timeout must not be passed at all (corral keeps its own default), got: %v", full)
			}
		}
	})
}

// TestActionRoutesTheKeyToTheNamedProvider: `model-key` was wired into the run
// as ANTHROPIC_API_KEY and nothing else. That is coherent with corral's default
// models (defaultDeriveModel == defaultLocalMutantModel == claude-sonnet-5), so
// it was not broken — but it made the Anthropic path the ONLY reachable one
// through the action, and the config this project actually has evidence for is
// all-Gemini (five replicates, ProvenMissed non-zero in every one). Cost points
// the same way: an audited file is a hours-long run, so the per-call price is
// not a rounding error.
//
// `model-key-env` names the environment variable the key becomes, defaulting to
// ANTHROPIC_API_KEY so every existing caller is unaffected. Those names are
// corral's own credential vocabulary (internal/agentbackend resolves
// GEMINI_API_KEY → GOOGLE_API_KEY → OPENAI_API_KEY, and ANTHROPIC_API_KEY), not
// a new concept invented here.
func TestActionRoutesTheKeyToTheNamedProvider(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	if _, ok := a.Inputs["model-key-env"]; !ok {
		t.Fatal(`action.yml should declare a "model-key-env" input so the key can reach a provider other than Anthropic`)
	}

	// The stub records the environment corral was actually launched with —
	// the only way to see where the key landed, since it travels as env and
	// never as argv.
	envDump := func(tmp string) string {
		b, err := os.ReadFile(filepath.Join(tmp, "env.log"))
		if err != nil {
			t.Fatalf("no env.log written — corral stub was never invoked: %v", err)
		}
		return string(b)
	}
	dumpStub := func(tmp string) string {
		return "env > \"" + filepath.Join(tmp, "env.log") + "\"\n"
	}

	// Replaces a subtest that asserted an unset model-key-env silently became
	// ANTHROPIC_API_KEY. That fallback existed to match corral's own default
	// models; corral has no default models now, so guessing a vendor here
	// would put the operator's key in the wrong variable and surface later as
	// a missing-key error naming a provider they never chose.
	t.Run("model-key with no model-key-env is refused, not guessed", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"MODEL_KEY=sk-secret", "MODEL_KEY_ENV=")
		if runErr == nil {
			t.Fatalf("a model-key with no model-key-env must fail the step rather than pick a vendor:\n%s", out)
		}
		if !strings.Contains(string(out), "model-key-env") {
			t.Errorf("the error must name model-key-env as the missing field; got:\n%s", out)
		}
		// The step must fail BEFORE invoking corral at all — so there is no
		// env.log, and no credential was exported anywhere.
		if _, err := os.Stat(filepath.Join(tmp, "env.log")); !os.IsNotExist(err) {
			t.Errorf("corral must never be invoked when the key's target variable was not named (stat env.log: %v)", err)
		}
	})

	t.Run("routes to Gemini when asked", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"MODEL_KEY=gm-secret", "MODEL_KEY_ENV=GEMINI_API_KEY")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		env := envDump(tmp)
		if !strings.Contains(env, "GEMINI_API_KEY=gm-secret") {
			t.Error("model-key must reach corral as GEMINI_API_KEY when model-key-env names it")
		}
		if strings.Contains(env, "ANTHROPIC_API_KEY=gm-secret") {
			t.Error("a Gemini key must not ALSO be exported as ANTHROPIC_API_KEY — corral would try the wrong vendor with a key that cannot work there, and the failure would name Anthropic")
		}
	})

	t.Run("a key value never reaches the log", func(t *testing.T) {
		tmp := t.TempDir()
		out, _, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"MODEL_KEY=gm-super-secret-value", "MODEL_KEY_ENV=GEMINI_API_KEY")
		if strings.Contains(string(out), "gm-super-secret-value") {
			t.Errorf("the key value was printed by the step — job logs are readable by anyone who can see the run; output:\n%s", out)
		}
	})

	t.Run("a bogus env name fails closed", func(t *testing.T) {
		tmp := t.TempDir()
		// Not an environment variable name. Exporting this would either
		// error deep in the script or, worse, be coerced into setting
		// something unintended.
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"MODEL_KEY=k", "MODEL_KEY_ENV=PATH=/tmp/evil")
		if runErr == nil {
			t.Errorf("model-key-env must be rejected unless it is a plain environment variable name; step exited 0:\n%s", out)
		}
	})
}

// TestActionAcceptsSeveralProviderKeysAtOnce: one key is not enough, and the
// reason is structural rather than a matter of taste.
//
// CheckDecorrelation (internal/advpool/driver.go) REJECTS a run whose
// test-critic and test-writer share a model — a critic judging tests written by
// its own model is the same failure mode grading its own homework. So a real
// audit needs at least two models, and the natural way to get genuine
// independence is two VENDORS. A single-key input cannot express that, and its
// only escape is `critic-model: "off"` — turning off corral's independence
// check because the plumbing could not carry a second credential.
//
// The keys are separate named inputs rather than one parsed blob: GitHub masks
// each secret it knows about independently, and splitting credentials out of a
// combined string is a place to get that wrong.
func TestActionAcceptsSeveralProviderKeysAtOnce(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	for _, in := range []string{"anthropic-key", "gemini-key", "openai-key"} {
		if _, ok := a.Inputs[in]; !ok {
			t.Fatalf("action.yml should declare a %q input — without independent per-provider keys a cross-vendor writer/critic pair cannot be configured, and the only way out is disabling the critic", in)
		}
	}

	envDump := func(tmp string) string {
		b, err := os.ReadFile(filepath.Join(tmp, "env.log"))
		if err != nil {
			t.Fatalf("no env.log written — corral stub was never invoked: %v", err)
		}
		return string(b)
	}
	dumpStub := func(tmp string) string {
		return "env > \"" + filepath.Join(tmp, "env.log") + "\"\n"
	}

	t.Run("two vendors reach corral together", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"GEMINI_KEY_IN=gm-secret", "ANTHROPIC_KEY_IN=sk-ant-secret",
			"WRITER_MODEL=gemini-3.6-flash", "CRITIC_MODEL=claude-haiku-4-5")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		env := envDump(tmp)
		if !strings.Contains(env, "GEMINI_API_KEY=gm-secret") {
			t.Error("gemini-key must reach corral as GEMINI_API_KEY")
		}
		if !strings.Contains(env, "ANTHROPIC_API_KEY=sk-ant-secret") {
			t.Error("anthropic-key must reach corral as ANTHROPIC_API_KEY — this is the whole point: a cross-vendor writer/critic pair needs BOTH present in the same run")
		}
	})

	t.Run("openai key maps to its canonical name", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"OPENAI_KEY_IN=oa-secret")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		if !strings.Contains(envDump(tmp), "OPENAI_API_KEY=oa-secret") {
			t.Error("openai-key must reach corral as OPENAI_API_KEY")
		}
	})

	t.Run("an unset key is not exported as empty", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"GEMINI_KEY_IN=gm-secret")
		if runErr != nil {
			t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
		}
		// An empty ANTHROPIC_API_KEY= exported over an inherited one would
		// BLANK a credential the runner legitimately had — the audit then
		// fails on a missing key that was actually present.
		if strings.Contains(envDump(tmp), "ANTHROPIC_API_KEY=\n") {
			t.Error("an unset provider key must not be exported as an empty variable — that blanks a credential the environment may already carry")
		}
	})

	t.Run("no key value reaches the log", func(t *testing.T) {
		tmp := t.TempDir()
		out, _, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"GEMINI_KEY_IN=gm-leak-canary", "ANTHROPIC_KEY_IN=ant-leak-canary",
			"OPENAI_KEY_IN=oa-leak-canary")
		for _, canary := range []string{"gm-leak-canary", "ant-leak-canary", "oa-leak-canary"} {
			if strings.Contains(string(out), canary) {
				t.Errorf("key value %q was printed by the step — job logs are readable by anyone who can see the run; output:\n%s", canary, out)
			}
		}
	})

	// Two inputs that both name the same variable with DIFFERENT values have
	// no correct answer, and a silent precedence rule is how the wrong key
	// gets used with no sign of it. Refuse instead.
	t.Run("a contradictory pair fails closed", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"GEMINI_KEY_IN=one-value",
			"MODEL_KEY=a-different-value", "MODEL_KEY_ENV=GEMINI_API_KEY")
		if runErr == nil {
			t.Errorf("gemini-key and model-key/model-key-env both set GEMINI_API_KEY to different values; the step must refuse rather than silently pick one:\n%s", out)
		}
	})

	// The same pair agreeing is not a conflict — it is just redundant, and
	// failing on it would break a caller who set both harmlessly.
	t.Run("an agreeing pair is allowed", func(t *testing.T) {
		tmp := t.TempDir()
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true", dumpStub(tmp),
			"GEMINI_KEY_IN=same-value",
			"MODEL_KEY=same-value", "MODEL_KEY_ENV=GEMINI_API_KEY")
		if runErr != nil {
			t.Fatalf("two inputs naming the same variable with the SAME value is redundant, not contradictory, and must be allowed: %v\n%s", runErr, out)
		}
	})
}

// TestActionPassesRoleModelsOnlyWhenSet: corral routes each ROLE to its own
// model (--derive-model / --writer-model / --mutant-model / --critic-model),
// and the action exposed none of them — so the all-Gemini configuration this
// project's evidence comes from was unreachable through it. A key alone is not
// enough: with only the key swapped, the model NAMES are still Claude ones
// pointed at Google's endpoint.
//
// Each mirrors the CLI flag exactly, opt-in and omitted-when-empty like
// --min-kill-rate and --top, so corral keeps ownership of every default.
func TestActionPassesRoleModelsOnlyWhenSet(t *testing.T) {
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

	roles := []struct {
		input string // action.yml input name
		env   string // the env var the run step reads it through
		flag  string // corral's own flag
		value string
	}{
		{"derive-model", "DERIVE_MODEL", "--derive-model", "gemini-3.6-flash"},
		{"writer-model", "WRITER_MODEL", "--writer-model", "gemini-3.6-flash"},
		{"mutant-model", "MUTANT_MODEL", "--mutant-model", "gemini-3.6-flash"},
		{"critic-model", "CRITIC_MODEL", "--critic-model", "off"},
		{"shadow-model", "SHADOW_MODEL", "--shadow-model", "off"},
	}

	for _, r := range roles {
		t.Run(r.input, func(t *testing.T) {
			if _, ok := a.Inputs[r.input]; !ok {
				t.Fatalf("action.yml should declare a %q input — without it corral's own role routing is unreachable through the action", r.input)
			}

			// The flag must exist and parse on the real command, so this
			// cannot pass on a flag that only looks plausible in YAML.
			var out, errb bytes.Buffer
			if code := runCertifyRepo([]string{
				"--repo", t.TempDir(), "--dry-run", r.flag, r.value,
			}, &out, &errb); code != 0 {
				t.Fatalf("certify --repo rejected %s: exit %d, stderr=%s", r.flag, code, errb.String())
			}

			t.Run("set", func(t *testing.T) {
				tmp := t.TempDir()
				stepOut, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", r.env+"="+r.value)
				if runErr != nil {
					t.Fatalf("run-corral step failed: %v\n%s", runErr, stepOut)
				}
				full := readFullArgv(tmp)
				found := false
				for i, arg := range full {
					if arg == r.flag && i+1 < len(full) && full[i+1] == r.value {
						found = true
					}
				}
				if !found {
					t.Errorf("want %s %s in corral's argv, got: %v", r.flag, r.value, full)
				}
			})

			t.Run("unset", func(t *testing.T) {
				tmp := t.TempDir()
				stepOut, runErr, _ := runRunCorralStep(t, runStep, tmp, "true", r.env+"=")
				if runErr != nil {
					t.Fatalf("run-corral step failed: %v\n%s", runErr, stepOut)
				}
				for _, arg := range readFullArgv(tmp) {
					if arg == r.flag {
						t.Errorf("%s input was empty; %s must not be passed at all, leaving corral's own default in charge", r.input, r.flag)
					}
				}
			})
		})
	}
}

// selfAuditWorkflow is a typed view of .github/workflows/self-audit.yml, enough
// to inspect what can start a paid audit.
type selfAuditWorkflow struct {
	On struct {
		PullRequest *struct {
			Types []string `yaml:"types"`
			Paths []string `yaml:"paths"`
		} `yaml:"pull_request"`
		PullRequestTarget *struct{} `yaml:"pull_request_target"`
		WorkflowDispatch  *struct{} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If string `yaml:"if"`
	} `yaml:"jobs"`
}

// TestSelfAuditNeverSpendsOnAStrangersPullRequest guards a MONEY property, not
// a code one: an audit is hours of runner time and hours of model calls, paid
// by this repository's owner.
//
// GitHub already withholds secrets from fork pull requests, so a fork audit
// skips today. That is a platform default doing the work silently — nothing in
// the workflow says it, so nothing stops a later edit from removing it. The
// specific way it gets removed is reaching for `pull_request_target` because
// "fork PRs skip": that trigger runs with secrets while checking out the PR's
// code, which is how a stranger's pull request gets to both spend and READ this
// repo's API key. The correct answer is that fork PRs should skip.
//
// The `audit` label is the second half: opt-in per pull request, so no PR —
// including ours — starts a two-hour paid job just by existing.
func TestSelfAuditNeverSpendsOnAStrangersPullRequest(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "self-audit.yml"))
	if err != nil {
		t.Fatalf("reading self-audit.yml: %v", err)
	}
	var wf selfAuditWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parsing self-audit.yml: %v", err)
	}

	if wf.On.PullRequestTarget != nil {
		t.Error("self-audit.yml must never trigger on pull_request_target: it exposes this repo's secrets to code from a fork, so a stranger's PR could both spend and read the API key")
	}

	job, ok := wf.Jobs["audit"]
	if !ok {
		t.Fatal("self-audit.yml has no `audit` job")
	}
	if strings.TrimSpace(job.If) == "" {
		t.Fatal("the audit job has no `if:` — anything that triggers the workflow starts a paid, hours-long run")
	}
	// Checked as properties of the condition rather than as an exact string,
	// so the guard can be rewritten but not dropped.
	if !strings.Contains(job.If, "head.repo.full_name") || !strings.Contains(job.If, "github.repository") {
		t.Errorf("the audit job's `if:` must compare the PR's head repo against this repository, so a fork PR cannot spend this repo's money; got: %s", job.If)
	}
	if !strings.Contains(job.If, "labels") {
		t.Errorf("the audit job's `if:` must require an opt-in label, so no pull request starts a paid run merely by existing; got: %s", job.If)
	}

	// A label opt-in that cannot be applied to an already-open PR is not an
	// opt-in: without `labeled`, adding the label starts nothing and the only
	// way to trigger an audit is to push again.
	if wf.On.PullRequest == nil {
		t.Fatal("self-audit.yml should still trigger on pull_request")
	}
	hasLabeled := false
	for _, ty := range wf.On.PullRequest.Types {
		if ty == "labeled" {
			hasLabeled = true
		}
	}
	if !hasLabeled {
		t.Errorf("pull_request types must include `labeled`, or applying the opt-in label to an open PR would not start the audit; got: %v", wf.On.PullRequest.Types)
	}
}

// sampleRepoReport is the shape printRepoReport actually emits, used by the
// step-summary tests below. It is a sample rather than the real renderer's
// output on purpose: these tests are about the ACTION's handling of whatever
// corral prints, so they must not start failing when the report's own wording
// changes.
const sampleRepoReport = `
Repo adequacy — acme/widget @ deadbeef
  kill rate 0.47 over 2 audited file(s) (22% of 9 candidates)
  weakest files:
    0.33  internal/pack/pack.go (6 survivor(s), 2 proven missed)
    0.61  internal/pack/wire.go (3 survivor(s), 0 proven missed)
`

// stubPrintingReport builds a stub-`corral` tail that prints report on stdout
// and exits with status.
func stubPrintingReport(report string, status int) string {
	return "cat <<'CORRAL_EOF'\n" + report + "CORRAL_EOF\n" + fmt.Sprintf("exit %d\n", status)
}

// readStepSummary returns the contents of the summary file the step was told
// to write, or "" if the step never created it.
func readStepSummary(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading step summary: %v", err)
	}
	return string(b)
}

// TestActionExitStatusSurvivesTheSummaryPipe is the load-bearing test of the
// whole step-summary feature, and the reason it is written before the feature.
//
// The obvious way to capture corral's report for the summary is
// `corral ... | tee "$report"`. Under bash's default (no pipefail) a pipeline's
// status is its LAST command's — tee's — which is 0 essentially always. So a
// --min-kill-rate failure, corral's entire merge gate, would exit 0 and the PR
// would go green on exactly the change the gate was installed to block. That is
// a silent no-gate, the failure mode this project has already shipped three
// times through three different doors, and it is invisible in every green run.
//
// The status must therefore be corral's own, whatever the reporting does around
// it — asserted here for both a failing and a passing corral, because a step
// that simply propagated "always fail" would satisfy the first half alone.
func TestActionExitStatusSurvivesTheSummaryPipe(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	t.Run("corral fails", func(t *testing.T) {
		tmp := t.TempDir()
		summary := filepath.Join(tmp, "summary.md")
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
			stubPrintingReport(sampleRepoReport, 7),
			"GITHUB_STEP_SUMMARY="+summary)
		if runErr == nil {
			t.Fatalf("corral exited 7 (e.g. a --min-kill-rate failure) but the step exited 0 — the merge gate is silently disabled. Output:\n%s", out)
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() != 7 {
			t.Errorf("step should exit with corral's own status 7, got %d — a rewritten status makes corral's exit codes unreadable to the caller. Output:\n%s", exitErr.ExitCode(), out)
		}
		// The report must still reach the summary on the failing path: a
		// red X whose reason was discarded is the exact problem this
		// feature exists to fix.
		if got := readStepSummary(t, summary); !strings.Contains(got, "kill rate 0.47") {
			t.Errorf("corral's report must reach the step summary even when corral fails — that is the run whose reason the reader most needs; summary was:\n%s", got)
		}
	})

	t.Run("corral passes", func(t *testing.T) {
		tmp := t.TempDir()
		summary := filepath.Join(tmp, "summary.md")
		out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
			stubPrintingReport(sampleRepoReport, 0),
			"GITHUB_STEP_SUMMARY="+summary)
		if runErr != nil {
			t.Fatalf("corral exited 0 but the step failed: %v\n%s", runErr, out)
		}
	})
}

// TestActionWritesTheReportToStepSummary: corral's report is the product. Left
// only on stdout it lands in a collapsed job log nobody opens, so a run that
// proved a real gap and a run that found nothing look identical from the PR
// page. The report goes to $GITHUB_STEP_SUMMARY, which needs no `permissions:`
// block and works on fork PRs, where a PR-comment token does not exist.
//
// Asserted verbatim, line for line: printRepoReport carries a dozen branches of
// deliberately-worded honesty (NOT AUDITED, DID NOT FINISH, WRITER FAILED, TEST
// UNSOUND). Anything that re-renders or summarises it in the action would be a
// second renderer free to drift from the first, and the drift would always be
// in the direction of looking cleaner than the run was.
func TestActionWritesTheReportToStepSummary(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	tmp := t.TempDir()
	summary := filepath.Join(tmp, "summary.md")
	out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
		stubPrintingReport(sampleRepoReport, 0),
		"GITHUB_STEP_SUMMARY="+summary)
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}

	got := readStepSummary(t, summary)
	if got == "" {
		t.Fatal("nothing was written to $GITHUB_STEP_SUMMARY — corral's report reaches only the job log, so the PR page shows a bare pass/fail with no reason")
	}
	for _, line := range strings.Split(strings.TrimSpace(sampleRepoReport), "\n") {
		if !strings.Contains(got, line) {
			t.Errorf("step summary is missing report line %q — the report must be reproduced verbatim, not re-rendered; summary was:\n%s", line, got)
		}
	}
	// stdout must keep the report too: the job log is what an operator
	// re-reads when a summary is truncated or the run is inspected via the API.
	if !strings.Contains(string(out), "kill rate 0.47") {
		t.Errorf("the report must still reach stdout as well as the summary; step output was:\n%s", out)
	}
}

// TestActionSummaryFenceIsNotBrokenByReportContent: the report is preformatted
// text and has to sit in a code fence to survive markdown rendering (leading
// spaces in the "weakest files" list are load-bearing). A fixed ``` fence is
// closed early by any line in the report that itself starts with three
// backticks — from that point the rest of the report renders as markdown, and
// the honesty lines quietly lose their formatting or vanish into a heading.
// Whatever fence is used must be longer than the longest backtick run in the
// content it wraps.
func TestActionSummaryFenceIsNotBrokenByReportContent(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	hostile := sampleRepoReport + "```\n  NOT AUDITED: 1 source file(s) could not be paired\n"

	tmp := t.TempDir()
	summary := filepath.Join(tmp, "summary.md")
	out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
		stubPrintingReport(hostile, 0),
		"GITHUB_STEP_SUMMARY="+summary)
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}

	got := readStepSummary(t, summary)
	if !strings.Contains(got, "NOT AUDITED") {
		t.Fatalf("the line after an embedded fence was lost from the summary:\n%s", got)
	}
	longestInReport := 0
	for _, run := range regexp.MustCompile("`+").FindAllString(hostile, -1) {
		if len(run) > longestInReport {
			longestInReport = len(run)
		}
	}
	fenceOK := false
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") && strings.Trim(trimmed, "`") == "" && len(trimmed) > longestInReport {
			fenceOK = true
			break
		}
	}
	if !fenceOK {
		t.Errorf("summary must wrap the report in a fence longer than the longest backtick run in the report itself (%d backticks), or the report closes the fence early and the rest renders as markdown; summary was:\n%s", longestInReport, got)
	}
}

// TestActionSummaryTruncationIsAnnounced: GitHub rejects a step summary over
// 1MiB outright, so an unbounded write can lose the whole report rather than
// its tail. Bounding it is right; bounding it SILENTLY is not — a reader cannot
// tell a short report from a cut-off one, and this action already refuses to
// silently truncate a multi-line test-command for exactly that reason.
func TestActionSummaryTruncationIsAnnounced(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	huge := sampleRepoReport + strings.Repeat("    0.10  internal/pack/generated_file.go (9 survivor(s), 1 proven missed)\n", 40000)

	tmp := t.TempDir()
	summary := filepath.Join(tmp, "summary.md")
	out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
		stubPrintingReport(huge, 0),
		"GITHUB_STEP_SUMMARY="+summary)
	if runErr != nil {
		t.Fatalf("run-corral step failed: %v\n%s", runErr, out)
	}

	got := readStepSummary(t, summary)
	const limit = 1 << 20
	if len(got) >= limit {
		t.Errorf("step summary is %d bytes, at or over GitHub's %d-byte limit — GitHub rejects it and the whole report is lost", len(got), limit)
	}
	if !strings.Contains(strings.ToLower(got), "truncat") {
		t.Errorf("an oversized report was cut down without saying so — a reader cannot distinguish a short report from a cut-off one; summary ended:\n%s", got[max(0, len(got)-400):])
	}
	// The head of the report — the verdict line itself — is what must
	// survive truncation, not just any 1MiB of it.
	if !strings.Contains(got, "kill rate 0.47") {
		t.Error("truncation dropped the verdict line; the head of the report must be what survives")
	}
}

// TestActionRunsOutsideActionsWithoutAStepSummary: $GITHUB_STEP_SUMMARY is set
// by the Actions runner and by nothing else. The step is also run by `act`, by
// self-hosted setups, and by this repo's own tests; an unset variable must not
// take the audit down with it. Reporting is a side channel — never the reason a
// gate fails to run.
func TestActionRunsOutsideActionsWithoutAStepSummary(t *testing.T) {
	a := loadActionYAML(t)
	runStep := findStepContaining(t, a, "certify --repo")

	tmp := t.TempDir()
	out, runErr, _ := runRunCorralStepWithStub(t, runStep, tmp, "true",
		stubPrintingReport(sampleRepoReport, 0),
		"GITHUB_STEP_SUMMARY=")
	if runErr != nil {
		t.Fatalf("step must survive an unset $GITHUB_STEP_SUMMARY (act, self-hosted, local runs): %v\n%s", runErr, out)
	}
	if !strings.Contains(string(out), "kill rate 0.47") {
		t.Errorf("the report must still reach stdout when there is no summary to write to; got:\n%s", out)
	}
}

// TestDocsWalkSkipsGitIgnoredFiles pins the scope fix: the gate grades the
// REPOSITORY, not the disk.
//
// The bug this replaces was a false FAILURE, which is why it needs its own
// test rather than a line in the gate above. docsAdvertisingAnActionRef walked
// the filesystem, so a local `docs/launch/…` draft or an ignored spec carrying
// a `pdbethke/corralai@latest` snippet turned every developer's `go test ./...`
// red for a file that is not in the repository and never will be — while CI,
// which only ever sees tracked files, stayed green. A gate whose verdict
// depends on what happens to be lying in the working tree is not grading the
// property it claims to grade.
//
// Both directions are asserted here, because dropping ignored files is only
// safe if the gate still sees everything it is supposed to: an ignored doc is
// skipped, and a tracked one plus an untracked-but-not-ignored one are BOTH
// still graded — the latter is a doc on its way to a commit, exactly when
// catching a bad pin is most useful.
func TestDocsWalkSkipsGitIgnoredFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	if out, err := runGit(t, root, "init", "-q", "."); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(".gitignore", "scratch/\n")
	write("README.md", "uses: pdbethke/corralai@main\n")
	write("scratch/draft.md", "uses: pdbethke/corralai@latest\n")
	write("docs/new.md", "uses: pdbethke/corralai@main\n")
	if out, err := runGit(t, root, "add", ".gitignore", "README.md"); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	docs := docsAdvertisingAnActionRef(t, root)

	if _, found := docs[filepath.Join("scratch", "draft.md")]; found {
		t.Errorf("scratch/draft.md is gitignored but was graded — the gate is reading the disk, not the repository")
	}
	for _, must := range []string{"README.md", filepath.Join("docs", "new.md")} {
		if _, found := docs[must]; !found {
			t.Errorf("%s is in the repository (tracked or merely untracked) but was NOT graded — the ignore filter is dropping documents it must keep", must)
		}
	}
}

// docGateSelector is the `go test -run` pattern CI uses to run the
// documentation gates on a change that touches ONLY documentation.
//
// It is duplicated, deliberately, in .github/workflows/deploy.yml — and
// TestDocsGatesRunOnDocsOnlyChanges below asserts the two agree, so the
// duplication cannot rot.
const docGateSelector = "^TestDocs"

// TestDocsGatesRunOnDocsOnlyChanges is the gate on the gates.
//
// THE BUG IT EXISTS TO PREVENT, which shipped and cost two stale tags:
// deploy.yml classifies a change as docs-only when every changed file matches
// `\.md$`, and then skips `go test ./...`. But the documentation gates ARE Go
// tests. So a Markdown-only pull request was precisely the pull request on
// which the pin-freshness gate never ran — the gate was neither missing nor
// ignored, it was structurally unreachable by the one class of change it
// exists to police. Documentation drifted past two releases underneath it.
//
// The fix is a step in deploy.yml that runs `-run "^TestDocs"` with no `if:`
// guard. That step selects tests BY NAME, which is the hand-maintained list
// this repo keeps getting burned by, so both halves of it are guarded here:
//
//  1. Every test that walks the documentation must be named for the selector.
//     Add a doc gate called TestActionRefsResolve and CI silently stops
//     running it; this test fails instead.
//  2. deploy.yml must still contain the selector. Delete or rename the step
//     and this test fails, rather than the gate going quiet.
//
// What it deliberately does NOT assert is the absence of an `if:` on that
// step — YAML structure is not worth hand-parsing here, and every failure
// mode above is already covered. Read the step's own comment before changing
// its condition.
func TestDocsGatesRunOnDocsOnlyChanges(t *testing.T) {
	const walker = "docsAdvertisingAnActionRef"
	wantPrefix := strings.TrimPrefix(docGateSelector, "^")

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing this package's tests: %v", err)
	}

	found := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				calls := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == walker {
						calls = true
					}
					return !calls
				})
				if !calls {
					continue
				}
				found++
				if !strings.HasPrefix(fn.Name.Name, wantPrefix) {
					t.Errorf("%s walks the documentation but is named %q — CI selects the doc gates with `-run %q`, so this test would never run on a docs-only pull request. Rename it to start with %q.",
						walker, fn.Name.Name, docGateSelector, wantPrefix)
				}
			}
		}
	}
	// A walk that found nothing would pass green forever — the same failure
	// docsAdvertisingAnActionRef's own scan-set check guards against.
	if found == 0 {
		t.Fatalf("found no test calling %s — this gate is not looking where it thinks it is", walker)
	}

	wf := filepath.Join("..", "..", ".github", "workflows", "deploy.yml")
	b, rerr := os.ReadFile(wf)
	if rerr != nil {
		t.Fatalf("reading %s: %v", wf, rerr)
	}
	if !strings.Contains(string(b), docGateSelector) {
		t.Errorf("deploy.yml no longer mentions %q — the documentation gates are only reachable on a docs-only change through that step, so removing or renaming it takes them off those pull requests silently", docGateSelector)
	}
}
