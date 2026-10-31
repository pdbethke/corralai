// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// What a run executed under, as the ledger records it: the test command
// (redacted), the runner row it was read as, and the settings. The entry is
// signed and, for a public repository, published, so this file is the one
// door every such value passes through.

const redactedValue = "<redacted>"

// The recorded command is an ALLOWLIST (ruling R-T5). It is signed into an
// entry on a public branch, and a denylist of secret-looking names cannot be
// closed: a credential rides in a URL (https://u:p@h), a header
// (-H "Authorization: Bearer x"), a short flag (-psecret), a name nobody
// listed (DB_PASS, AUTH, PWD) or a quoted value holding a metacharacter. So
// a token is recorded verbatim only when it is provably safe, and every
// other one becomes "<redacted>" in its own position, which keeps the
// command's shape. When in doubt, redact.

// launcherWords are interpreters and launchers a command is run through.
var launcherWords = map[string]bool{
	"node": true, "python": true, "python3": true, "npm": true, "npx": true, "pnpm": true, "yarn": true,
	"bun": true, "go": true, "cargo": true, "bundle": true, "ruby": true, "php": true, "composer": true,
	"sh": true, "bash": true, "env": true,
}

// subcommandWords are the fixed subcommand words recorded as written.
var subcommandWords = map[string]bool{"run": true, "test": true, "exec": true, "-m": true}

// shortFlagsKept are the single-dash flags longer than one letter that are
// recorded: a word like -psecret is a short flag with its value attached
// and cannot be told from one, so single-dash flags are kept only when they
// are one letter or listed here.
var shortFlagsKept = map[string]bool{
	"race": true, "run": true, "count": true, "short": true, "timeout": true, "cover": true,
	"bench": true, "json": true, "parallel": true, "failfast": true,
}

var (
	flagRe       = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*$`)
	flagValueRe  = regexp.MustCompile(`^(--?[A-Za-z][A-Za-z0-9-]*)=(.*)$`)
	assignmentRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	plainWordRe  = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)
)

// runnerWords are the words the runner table names (lang.Runner.Words).
func runnerWords() map[string]bool {
	m := map[string]bool{}
	for _, r := range lang.Runners() {
		for _, w := range r.Words {
			m[w] = true
		}
	}
	return m
}

// repoPathExists reports whether tok names something that exists in the
// repository at repoRoot: a path relative to it that does not escape it. A
// token ending in "/..." (go's package wildcard) counts when its prefix is
// an existing directory, or "." itself. With no repoRoot nothing exists.
func repoPathExists(repoRoot, tok string) bool {
	if repoRoot == "" || tok == "" || filepath.IsAbs(tok) {
		return false
	}
	t := strings.TrimSuffix(tok, "/...")
	if t == "" {
		t = "."
	}
	if t == "..." {
		return false
	}
	// The token is recorded as written, so what is tested must BE what is
	// written: no ".." component at all (a/../b names b yet records a), and
	// the token equal to its own cleaned form, bar a "./" prefix and one
	// trailing slash.
	for _, part := range strings.Split(t, "/") {
		if part == ".." {
			return false
		}
	}
	c := filepath.Clean(t)
	if c != strings.TrimSuffix(strings.TrimPrefix(t, "./"), "/") && !(t == "." || t == "./") {
		return false
	}
	_, err := os.Lstat(filepath.Join(repoRoot, c)) // #nosec G703 -- tok is relative, has no ".." component and equals its own cleaned form (checked above), so the join cannot leave repoRoot; Lstat only tests existence
	return err == nil
}

// keepToken is the value part of one token: verbatim when provably safe,
// "<redacted>" otherwise. first marks argv[0], recorded by basename.
func keepToken(tok, repoRoot string, words map[string]bool, first bool) string {
	switch {
	case tok == "--":
		return tok
	case first && plainWordRe.MatchString(tok):
		return filepath.Base(tok)
	case plainWordRe.MatchString(tok) && !strings.Contains(tok, "/") && (launcherWords[tok] || words[tok] || subcommandWords[tok]):
		return tok
	case subcommandWords[tok]:
		return tok
	case flagAllowed(tok):
		return tok
	case repoPathExists(repoRoot, tok):
		return tok
	}
	return redactedValue
}

// flagAllowed is the one rule for a flag's NAME, with or without a value
// attached: a long flag, a one-letter flag, or a listed single-dash flag.
func flagAllowed(name string) bool {
	if !flagRe.MatchString(name) {
		return false
	}
	bare := strings.TrimLeft(name, "-")
	return strings.HasPrefix(name, "--") || len(bare) == 1 || shortFlagsKept[bare]
}

// redactToken is keepToken extended to the two attached-value shapes.
// --flag=value keeps the flag only when its name passes flagAllowed, the
// same rule as a flag with no value (so -ps3cr3tpw=x is not a flag but a
// secret). NAME=value keeps NAME only in assignment position (assign: argv[0],
// directly after env, or a leading word of a script command); anywhere else
// an "=" is part of some value (padded base64 ends in "="), and the whole
// token is redacted. The value of either is redacted unless it is itself a
// path that exists in the repository.
func redactToken(tok, repoRoot string, words map[string]bool, first, assign bool) string {
	if m := flagValueRe.FindStringSubmatch(tok); m != nil {
		if flagAllowed(m[1]) {
			return m[1] + "=" + valueOf(m[2], repoRoot)
		}
		return redactedValue
	}
	if m := assignmentRe.FindStringSubmatch(tok); m != nil && assign {
		return m[1] + "=" + valueOf(m[2], repoRoot)
	}
	return keepToken(tok, repoRoot, words, first)
}

func valueOf(v, repoRoot string) string {
	if repoPathExists(repoRoot, v) {
		return v
	}
	return redactedValue
}

// redactCommand returns argv with every token that is not provably safe
// replaced by "<redacted>" in place, leaving argv untouched. Safe is one of:
// argv[0] (by basename) or a launcher word; a word the runner table names or
// one of run, test, exec, -m; a flag with no attached value; a path that
// exists in the repository at repoRoot (or a "/..." wildcard over one); and
// an `sh -c` script, whose every word is judged by the same rules and
// rejoined, or the whole script redacted when it cannot be read.
func redactCommand(argv []string, repoRoot string) []string {
	words := runnerWords()
	out := make([]string, len(argv))
	shell := len(argv) > 0 && cmdShellName(argv[0])
	script := -1
	if shell {
		for i, a := range argv {
			if a == "-c" && i+1 < len(argv) {
				script = i + 1
				break
			}
		}
	}
	// assign: a NAME=value here is an environment assignment (argv[0], or
	// directly after env, through any run of assignments).
	assign := true
	for i, a := range argv {
		if i == script {
			out[i] = redactScript(a, repoRoot, words)
			continue
		}
		out[i] = redactToken(a, repoRoot, words, i == 0, assign)
		assign = assign && (assignmentRe.MatchString(a) || filepath.Base(a) == "env")
	}
	return out
}

func cmdShellName(a string) bool {
	switch filepath.Base(a) {
	case "sh", "bash", "zsh", "dash":
		return true
	}
	return false
}

// redactScript judges every word of an `sh -c` script and rejoins the
// commands with " ; " (the operators and redirections are not recorded).
func redactScript(script, repoRoot string, words map[string]bool) string {
	cmds, ok := lang.ShellScriptCommands(script)
	if !ok {
		return redactedValue
	}
	var parts []string
	for _, c := range cmds {
		ws := make([]string, len(c))
		assign := true // leading words of a command may be assignments
		for i, w := range c {
			ws[i] = redactToken(w, repoRoot, words, false, assign)
			assign = assign && (assignmentRe.MatchString(w) || filepath.Base(w) == "env")
		}
		parts = append(parts, strings.Join(ws, " "))
	}
	return strings.Join(parts, " ; ")
}

// shellQuoted joins argv as one command, an argument that needs it
// single-quoted.
func shellQuoted(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if quotePlainRe.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

var quotePlainRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// testCommandText is the recorded text of a test command: argv redacted and
// shell-joined. "" for no command.
func testCommandText(argv []string, repoRoot string) string {
	if len(argv) == 0 {
		return ""
	}
	return shellQuoted(redactCommand(argv, repoRoot))
}

// recordedRunner is the runner-table row ID argv is read as under plug: the
// operator's --runner, else the row the command names, else the language's
// default (lang.Resolve, the resolution the evidence pass itself makes).
// "" when plug is nil or no row resolves.
func recordedRunner(plug lang.Plugin, repoDir string, argv []string, runnerFlag string) string {
	if plug == nil {
		return ""
	}
	inv, err := lang.Resolve(plug, repoDir, argv, runnerFlag)
	if err != nil {
		return ""
	}
	r, ok := lang.RunnerFor(plug, inv)
	if !ok {
		return ""
	}
	return r.ID
}

// runSettings are the conditions a run executed under, taken from the values
// the run used, not the flags as typed. Every field is optional because a
// run records only the settings it has: a zero number or empty string is
// ABSENT (no default is invented for a setting the run did not use), and a
// boolean is a pointer so that false, a real value, is recorded.
type runSettings struct {
	Timeout, EvidenceTimeout time.Duration
	SeedLimitBytes           int64
	// Swarm is the resolved swarm width; ScanWorkers the files audited at
	// once, UnitWorkers the units run at once, MutantConcurrency the mutants
	// scored at once within a file, PerFileSwarm the workspace substrate's
	// per-file role width.
	Swarm, ScanWorkers, UnitWorkers, MutantConcurrency, PerFileSwarm int
	BaselineRuns                                                     int
	MaxTokens                                                        int64
	WholeSuite, NoFailFast                                           *bool
	NoGoalCache, NoVerdictCache, NoSelectionCache                    *bool
	WriterMode, Jail, RunnerFlag                                     string
}

func boolPtrOf(b bool) *bool { return &b }

// runSettingsJSON is ScanRow.Settings: the settings as JSON text (a ScanRow
// is compared whole, so no map), "" when the run recorded none. One line per
// key, so a new flag is one line here.
func runSettingsJSON(s runSettings) string {
	m := map[string]any{}
	dur := func(k string, d time.Duration) {
		if d > 0 {
			m[k] = d.String()
		}
	}
	num := func(k string, n int64) {
		if n > 0 {
			m[k] = n
		}
	}
	flag := func(k string, b *bool) {
		if b != nil {
			m[k] = *b
		}
	}
	str := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	dur("timeout", s.Timeout)
	dur("evidence_timeout", s.EvidenceTimeout)
	num("seed_limit_mib", s.SeedLimitBytes>>20)
	num("swarm", int64(s.Swarm))
	num("scan_workers", int64(s.ScanWorkers))
	num("unit_workers", int64(s.UnitWorkers))
	num("mutant_concurrency", int64(s.MutantConcurrency))
	num("per_file_swarm", int64(s.PerFileSwarm))
	num("baseline_runs", int64(s.BaselineRuns))
	num("max_tokens", s.MaxTokens)
	flag("whole_suite", s.WholeSuite)
	flag("no_fail_fast", s.NoFailFast)
	flag("no_goal_cache", s.NoGoalCache)
	flag("no_verdict_cache", s.NoVerdictCache)
	flag("no_selection_cache", s.NoSelectionCache)
	str("writer_mode", s.WriterMode)
	str("jail", s.Jail)
	str("runner_flag", s.RunnerFlag)
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// seedLimitUsed is the jail seed limit the run used: the workspace substrate
// builds no seed, so there is none to record (0 is absent).
func seedLimitUsed(substrate string, limit int64) int64 {
	if substrate == substrateWorkspace {
		return 0
	}
	return limit
}
