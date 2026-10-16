// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Invocation is a test command as the runner table reads it. It is resolved
// ONCE per run (Resolve), where the repository is on disk, and carried with
// the run: the driver that later asks for a one-test command has no
// repository directory and must not re-read files.
//
// Argv is always what runs. Resolution only decides which row answers and
// how that row's arguments reach the runner; corral never substitutes the
// script's command for `npm test`.
type Invocation struct {
	Argv []string
	// Runner is the row ID an operator named with --runner; "" = detect.
	Runner string
	// Script is the package script's words when Argv runs one (`npm test`
	// reads package.json scripts.test), nil otherwise. Non-nil and EMPTY
	// means Argv runs a package script corral did not read — no manifest, a
	// script of several commands, a launcher flag pointing at another
	// package — so nothing may be appended to it.
	Script []string
	// Pass is what the launcher needs before extra arguments to reach the
	// script ("--" for npm, pnpm and composer; "" for yarn), and Narrow
	// whether the script is one simple command they can be appended to.
	Pass   string
	Narrow bool
	// From says how the row was chosen, for the scan line.
	From string
}

// Cmd is a bare command with nothing resolved: the runner table answers for
// it exactly as it answered for argv alone before Invocation existed.
func Cmd(argv []string) Invocation { return Invocation{Argv: argv} }

// WithArgv is inv with a different command run — the per-mutant command a
// scorer builds from the operator's — keeping what was resolved about it.
func (inv Invocation) WithArgv(argv []string) Invocation {
	inv.Argv = argv
	return inv
}

// detectWords are the words a row is detected from: the script's when argv
// runs one, else argv's.
func (inv Invocation) detectWords() []string {
	if inv.Script != nil {
		return inv.Script
	}
	return inv.Argv
}

// launcher is how one package manager runs a named script and hands it
// extra arguments. The separators were measured (scripts/capture-runner-
// fixtures.sh, testdata/runners/PROVENANCE.txt): npm 10.9 and pnpm 9.15 take
// them after "--" (pnpm rejects a bare runner flag as its own unknown
// option), yarn 1.22 takes them directly, Composer 2.9 after "--".
type launcher struct {
	manifest, key string   // the file and the field that holds scripts
	pass          string   // separator before extra arguments
	testWords     []string // subcommands that mean the "test" script
	runWords      []string // subcommands that take a script name next
	bareName      bool     // `<launcher> <name>` runs a script by name
}

var launchers = map[string]launcher{
	"npm":      {"package.json", "scripts", "--", []string{"test", "t"}, []string{"run", "run-script"}, false},
	"pnpm":     {"package.json", "scripts", "--", []string{"test", "t"}, []string{"run", "run-script"}, true},
	"yarn":     {"package.json", "scripts", "", []string{"test"}, []string{"run"}, true},
	"composer": {"composer.json", "scripts", "--", []string{"test"}, []string{"run-script", "run"}, true},
}

// ScriptLauncher is one package manager whose scripts the runner table reads,
// for the support page: the manifest it reads and the separator before extra
// arguments ("" = none).
type ScriptLauncher struct{ Name, Manifest, Pass string }

// ScriptLaunchers is every launcher Resolve reads, by name.
func ScriptLaunchers() []ScriptLauncher {
	var out []ScriptLauncher
	for _, name := range slices.Sorted(maps.Keys(launchers)) {
		out = append(out, ScriptLauncher{name, launchers[name].manifest, launchers[name].pass})
	}
	return out
}

// Resolve reads the package script argv runs, if it runs one, and applies an
// operator's --runner. Nothing is executed. A script that cannot be read —
// no manifest, no such script, malformed JSON — leaves Script nil, and the
// row is detected from argv alone as before. The only error is a --runner
// that names no row of p's language.
func Resolve(p Plugin, repoDir string, argv []string, override string) (Invocation, error) {
	inv := Cmd(argv)
	if override != "" {
		if _, err := RunnerByID(p, override); err != nil {
			return Invocation{}, err
		}
		inv.Runner, inv.From = override, "--runner"
	}
	if len(argv) == 0 {
		return inv, nil
	}
	if _, isLauncher := launchers[filepath.Base(argv[0])]; !isLauncher {
		return inv, nil
	}
	l, name, ok := launchedScript(repoDir, argv)
	if !ok {
		// A launcher running something corral cannot name as a script.
		// `yarn jest` names its runner itself and is read as before;
		// anything else (`composer --working-dir=pkg test`, `pnpm --filter
		// web test`) runs a script corral has not read.
		if _, named := namedRunner(p, argv); !named {
			inv.Script = []string{}
		}
		return inv, nil
	}
	inv.Script = []string{} // until it is read
	if launcherRedirected(argv) {
		return inv, nil
	}
	src, ok := readScript(filepath.Join(repoDir, l.manifest), l.key, name)
	if !ok {
		return inv, nil
	}
	words, ok := splitScript(src)
	if !ok || len(words) == 0 {
		return inv, nil
	}
	inv.Script, inv.Pass = words, l.pass
	inv.Narrow = scriptIsSimple(src) && !strings.HasPrefix(src, "@")
	if inv.From == "" {
		inv.From = l.manifest + " scripts." + name
	}
	return inv, nil
}

// launcherRedirects are launcher options that run a script from ANOTHER
// package's manifest than the one at the repository root (npm and pnpm
// workspaces, yarn's --cwd, composer's --working-dir): the root's script is
// not the one that runs.
var launcherRedirects = []string{
	"--workspace", "-w", "--workspaces", "-ws", "--prefix", "--include-workspace-root",
	"--cwd", "--filter", "-F", "--recursive", "-r", "--dir", "-C", "--workspace-root",
	"--working-dir", "-d",
}

// launcherRedirected reports whether argv carries a launcher redirect before
// its "--" (after it, the words are the script's).
func launcherRedirected(argv []string) bool {
	head := argv[1:]
	if i := slices.Index(head, "--"); i >= 0 {
		head = head[:i]
	}
	return hasFlag(head, launcherRedirects...)
}

// launchedScript is the launcher argv runs and the script name it runs.
func launchedScript(repoDir string, argv []string) (launcher, string, bool) {
	if len(argv) < 2 {
		return launcher{}, "", false
	}
	l, ok := launchers[filepath.Base(argv[0])]
	if !ok {
		return launcher{}, "", false
	}
	switch sub := argv[1]; {
	case slices.Contains(l.testWords, sub):
		return l, "test", true
	case slices.Contains(l.runWords, sub):
		if len(argv) < 3 || strings.HasPrefix(argv[2], "-") {
			return launcher{}, "", false
		}
		return l, argv[2], true
	case l.bareName && !strings.HasPrefix(sub, "-"):
		// `yarn unit` runs the script named unit only when there is one;
		// otherwise it is one of the launcher's own commands.
		if _, ok := readScript(filepath.Join(repoDir, l.manifest), l.key, sub); ok {
			return l, sub, true
		}
	}
	return launcher{}, "", false
}

// readScript is scripts.<name> from a manifest: a string, or (Composer) an
// array holding exactly one string. Composer's array of several commands
// runs them in turn, which is a compound script; it is read as unreadable
// here rather than as its first command.
func readScript(path, key, name string) (string, bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- the audited repository's own manifest, read only
	if err != nil {
		return "", false
	}
	// Only the scripts object is decoded: a manifest's other top-level
	// values are strings, booleans and arrays ("name", "private"), which a
	// decode of the whole file into objects refuses.
	var top map[string]json.RawMessage
	var scripts map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil || json.Unmarshal(top[key], &scripts) != nil {
		return "", false
	}
	raw, ok := scripts[name]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, s != ""
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) == 1 {
		return arr[0], arr[0] != ""
	}
	return "", false
}

// scriptIsSimple reports whether a script is one command that arguments
// appended to its end reach. npm and Composer append extra arguments to the
// END of the script string, so in `jest && eslint .` a --bail would reach
// eslint. Anything that could chain, pipe, redirect or substitute is refused,
// even inside quotes: a refused script loses capabilities, a misread one
// hands a flag to the wrong program.
func scriptIsSimple(s string) bool {
	return !strings.ContainsAny(s, ";|&<>`\n") && !strings.Contains(s, "$(")
}

// splitScript splits a script into words, honouring single and double
// quotes and nothing else (no expansions). Unbalanced quotes split nothing.
func splitScript(s string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	in, quote, inWord := false, byte(0), false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in && c == quote:
			in = false
		case in:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			in, quote, inWord = true, c, true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if in {
		return nil, false
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

// shape builds a row's command for inv: on argv as before when there is no
// script; on the script's words when there is one, passed through the
// launcher. Through a launcher the row must only APPEND to the script — its
// command must start with the script's words exactly — since only appended
// words reach it; vitest's `list`, which rewrites the subcommand, is refused.
// Under --runner the row must also be one the command words name: a row an
// operator chose for `make test` can read its output, but nothing says its
// arguments would reach it.
func (inv Invocation) shape(p Plugin, row func([]string) ([]string, bool)) ([]string, bool) {
	words := inv.detectWords()
	if inv.Script != nil && !inv.Narrow {
		return nil, false
	}
	// Through a script, or under --runner, the row must be one the words
	// name: a script running pest or `php artisan test` is no phpunit, and
	// phpunit's --list-tests must not reach it just because phpunit is
	// PHP's Default row.
	if inv.Script != nil || inv.Runner != "" {
		if r, named := namedRunner(p, words); !named || (inv.Runner != "" && r.ID != inv.Runner) {
			return nil, false
		}
	}
	out, ok := row(words)
	if !ok || inv.Script == nil {
		return out, ok
	}
	if len(out) < len(words) || !slices.Equal(out[:len(words)], words) {
		return nil, false
	}
	return inv.passThrough(out[len(words):]), true
}

// passThrough is argv with extra arguments handed on to its script: after
// the launcher's separator, unless argv already carries it (`npm test --
// --ci`), in which case they follow.
func (inv Invocation) passThrough(extra []string) []string {
	cmd := slices.Clone(inv.Argv)
	if inv.Pass != "" && !slices.Contains(cmd, inv.Pass) {
		cmd = append(cmd, inv.Pass)
	}
	return append(cmd, extra...)
}

// Describe is what the scan line says about how the runner was found: ""
// when nothing was resolved (the command named its runner, or no row did).
func (inv Invocation) Describe(p Plugin) string {
	if inv.From == "" && (inv.Script == nil || len(inv.Script) > 0) {
		return ""
	}
	r, _ := RunnerFor(p, inv)
	cmd := strings.Join(inv.Argv, " ")
	if inv.Script != nil && len(inv.Script) == 0 {
		return cmd + " runs a package script corral did not read, so it adds no arguments to it (no fail-fast, no test list, no one-test runs)"
	}
	if inv.Runner == "" {
		if _, named := namedRunner(p, inv.Script); !named {
			return cmd + " runs `" + strings.Join(inv.Script, " ") + "` (" + inv.From + "): no runner corral recognises in it, so it is graded by exit code with no runner-specific steps; --runner names one"
		}
	}
	line := cmd + " → " + r.Name + " (" + inv.From + ")"
	if inv.Script != nil && !inv.Narrow {
		line += "; the script is not one command, so corral adds no arguments to it: no fail-fast, no test list, no one-test runs"
	}
	return line
}
