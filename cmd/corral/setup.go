// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/agentsetup"
	"github.com/pdbethke/corralai/internal/statedir"
)

// corral setup — wire corral's MCP server (and skill) into the coding agents
// on this machine, through each agent's own commands, after showing the plan
// and getting a yes. The mechanics live in internal/agentsetup; this file is
// the operator surface: selection, approval, and one honest result line per
// agent. "Verified" is claimed only when execution showed it: the agent's own
// inspect verb lists the server, the skill copy is setup's, and the registered
// binary answered an MCP handshake.

// setupEnv is everything the verb reads from the machine. Tests supply their
// own so nothing touches the real HOME, state directory or agent CLIs.
type setupEnv struct {
	Home     string
	Runner   agentsetup.Runner
	LookPath func(string) (string, error)
	IsTTY    func() bool
	// Exe and OnPath are already symlink-resolved; OnPath is "" when corral
	// is not on the shell's PATH.
	Exe, OnPath, TmpDir string
	Version             string
	// StatePath returns the setup state file; write=true also creates its
	// directory, so a dry run (write=false) leaves nothing behind.
	StatePath func(write bool) (string, error)
	// CheckMCP proves the registered binary answers an MCP handshake.
	CheckMCP func(ctx context.Context, bin string) error
}

const setupUsage = `Usage: corral setup [--client <id>]... [--all-detected] [--dry-run] [--yes] [--skill=false]
       corral setup --uninstall [--client <id>]... [--all-detected] [--dry-run] [--yes]

Registers "corral mcp" as the MCP server "corral" in your coding agents, and
copies corral's skill where the agent reads skills. Nothing is written before
the plan is printed and you approve it.

Agents (ids): claude_code, codex, gemini_cli, cursor, opencode, antigravity,
copilot_cli, vscode.

Selection has no default: name agents with --client (repeatable), take every
installed one with --all-detected, or run on a terminal and pick from the list.

Exit codes:
  0  every selected agent verified, or --dry-run, or nothing to do
  1  the plan was declined, or an agent failed or could not be verified
  2  usage error

Flags:
`

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runSetupWith(args, stdin, stdout, stderr, realSetupEnv)
}

// realSetupEnv resolves the machine's real environment. It runs after flag
// parsing, so `corral setup -h` detects nothing.
func realSetupEnv() (setupEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setupEnv{}, fmt.Errorf("cannot find your home directory: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return setupEnv{}, fmt.Errorf("cannot find this binary: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return setupEnv{}, fmt.Errorf("resolving %s: %w", exe, err)
	}
	onPath, err := resolveOnPath(exec.LookPath, filepath.EvalSymlinks)
	if err != nil {
		return setupEnv{}, err
	}
	return setupEnv{
		Home:     home,
		Runner:   agentsetup.ExecRunner{Timeout: agentsetup.Timeout, Dir: home}, // agent CLIs run in home, never the cwd (see ExecRunner.Dir)
		LookPath: exec.LookPath,
		IsTTY: func() bool {
			fi, err := os.Stdin.Stat()
			return err == nil && fi.Mode()&os.ModeCharDevice != 0
		},
		Exe: exe, OnPath: onPath, TmpDir: os.TempDir(),
		Version: version,
		StatePath: func(write bool) (string, error) {
			if write {
				return statedir.PathForWrite(statedir.SetupState)
			}
			return statedir.Path(statedir.SetupState)
		},
		CheckMCP: agentsetup.CheckMCP,
	}, nil
}

// resolveOnPath finds the corral on the shell's PATH, symlinks resolved. "" with
// no error means it is not on PATH; a corral that IS on PATH but cannot be
// resolved is an error, never "not on PATH".
func resolveOnPath(lookPath, eval func(string) (string, error)) (string, error) {
	p, err := lookPath("corral")
	if err != nil {
		return "", nil
	}
	r, err := eval(p)
	if err != nil {
		return "", fmt.Errorf("corral is on your PATH at %s but that path cannot be resolved: %w", p, err)
	}
	return r, nil
}

// resolveSetupBinary decides which corral path agents will launch. exe and
// onPath are symlink-resolved by the caller; onPath is "" when corral is not
// on PATH. A binary under tmpDir (a `go run` build) is refused because it
// disappears; a PATH corral that is a different file is refused because the
// operator's shell and the agents would run different programs.
func resolveSetupBinary(exe, onPath, tmpDir string) (string, error) {
	if tmpDir != "" {
		// exe arrives symlink-resolved, so compare against the resolved temp
		// dir too (macOS: /var/folders is /private/var/folders).
		if r, err := filepath.EvalSymlinks(tmpDir); err == nil {
			tmpDir = r
		}
		if rel, err := filepath.Rel(filepath.Clean(tmpDir), exe); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return "", fmt.Errorf("%s is under the temp directory %s, so it will not survive: this looks like `go run`; build or install corral (go install ./cmd/corral) and run that", exe, tmpDir)
		}
	}
	if onPath != "" && onPath != exe {
		return "", fmt.Errorf("the corral on your PATH is %s but this is %s; run setup from the one you want agents to use (they would otherwise disagree with your shell)", onPath, exe)
	}
	return exe, nil
}

type setupFlags struct {
	clients     multiFlag
	allDetected bool
	dryRun, yes bool
	skill       bool
	uninstall   bool
}

func agentIDs() []string {
	var ids []string
	for _, a := range agentsetup.Agents() {
		ids = append(ids, a.ID)
	}
	return ids
}

func runSetupWith(args []string, stdin io.Reader, stdout, stderr io.Writer, envFn func() (setupEnv, error)) int {
	fs := flag.NewFlagSet("corral setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f setupFlags
	fs.Var(&f.clients, "client", "an agent id to wire (repeatable): "+strings.Join(agentIDs(), ", "))
	fs.BoolVar(&f.allDetected, "all-detected", false, "select every agent whose CLI is installed")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the plan and exit 0 without asking or writing")
	fs.BoolVar(&f.yes, "yes", false, "apply the plan without the prompt (for scripts)")
	fs.BoolVar(&f.skill, "skill", true, "also copy corral's skill; --skill=false omits it")
	fs.BoolVar(&f.uninstall, "uninstall", false, "remove only what setup added (registrations, its skill copies)")
	fs.Usage = func() {
		fmt.Fprint(stderr, setupUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "corral setup: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	skillSet := false
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "skill" {
			skillSet = true
		}
	})
	if f.uninstall && skillSet && !f.skill {
		fmt.Fprintln(stderr, "corral setup: --skill=false means nothing with --uninstall (uninstall removes setup's skill copies); drop it")
		return 2
	}
	valid := map[string]bool{}
	for _, id := range agentIDs() {
		valid[id] = true
	}
	for _, c := range f.clients {
		if !valid[c] {
			fmt.Fprintf(stderr, "corral setup: unknown --client %q; the agents are: %s\n", c, strings.Join(agentIDs(), ", "))
			return 2
		}
	}
	env, err := envFn()
	if err != nil {
		fmt.Fprintf(stderr, "corral setup: %v\n", err)
		return 1
	}
	interactive := len(f.clients) == 0 && !f.allDetected
	if interactive && !env.IsTTY() {
		fmt.Fprintln(stderr, "corral setup: nothing selected and no terminal to ask on; pass --client <id> (repeatable) or --all-detected")
		return 2
	}

	verb := "corral setup"
	bin := ""
	if !f.uninstall {
		if bin, err = resolveSetupBinary(env.Exe, env.OnPath, env.TmpDir); err != nil {
			fmt.Fprintf(stderr, "corral setup: %v\n", err)
			return 1
		}
		if env.OnPath == "" {
			fmt.Fprintf(stdout, "corral is not on your shell's PATH; agents will launch %s\n", bin)
		}
	}

	ctx, cancel := setupContext(stderr)
	defer cancel()
	readLine := ctxLineReader(ctx, bufio.NewReader(stdin))
	interrupted := func() int {
		fmt.Fprintln(stdout, "interrupted; nothing was written")
		return 1
	}

	statuses := agentsetup.Detect(ctx, env.Runner, env.Home, env.LookPath)
	if ctx.Err() != nil {
		return interrupted()
	}
	var selected []string
	if interactive {
		selected, err = pickAgents(readLine, stdout, statuses)
		if ctx.Err() != nil {
			return interrupted()
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", verb, err)
			return 2
		}
	} else {
		selected = selectFlagged(statuses, f)
	}
	if len(selected) == 0 {
		fmt.Fprintln(stdout, "nothing selected; nothing to do")
		return 0
	}

	readPath, err := env.StatePath(false)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return 1
	}
	st, err := agentsetup.LoadState(readPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return 1
	}

	var plan agentsetup.Plan
	if f.uninstall {
		plan = agentsetup.BuildUninstallPlan(statuses, selected, st, env.Home)
	} else {
		plan, err = agentsetup.BuildSetupPlan(statuses, selected, agentsetup.SetupOptions{Bin: bin, Skill: f.skill, Home: env.Home, Version: env.Version})
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", verb, err)
			return 2
		}
	}
	fmt.Fprintln(stdout, "Plan:")
	plan.Render(stdout)
	if f.dryRun {
		fmt.Fprintln(stdout, "dry run: nothing was written")
		return 0
	}

	mutating := mutatingAgents(plan)
	if len(mutating) > 0 && !f.yes {
		fmt.Fprint(stdout, "Apply this plan? [y/N] ")
		line, _ := readLine()
		if ctx.Err() != nil {
			fmt.Fprintln(stdout)
			return interrupted()
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "declined; nothing was written")
			return 1
		}
	}

	results := map[string]agentsetup.Result{}
	statePath := ""
	if len(mutating) > 0 {
		statePath, err = env.StatePath(true)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", verb, err)
			return 1
		}
		var applied agentsetup.Plan
		for _, a := range plan.Actions {
			if mutating[a.AgentID] {
				applied.Actions = append(applied.Actions, a)
			}
		}
		for _, r := range agentsetup.Apply(ctx, env.Runner, applied, env.Home, statePath, bin, env.Version, &st) {
			results[r.AgentID] = r
		}
	}

	// The re-read only reads, so it runs even after an interrupt (bounded by
	// the agents' Timeout; a second Ctrl-C still exits at once): without it
	// the result lines could not say what an interrupted run did.
	readCtx := context.WithoutCancel(ctx)
	after := agentsetup.Detect(readCtx, env.Runner, env.Home, env.LookPath)
	var mcpErr error
	mcpChecked := false
	if !f.uninstall && len(mutating) > 0 {
		mcpChecked, mcpErr = true, env.CheckMCP(readCtx, bin)
	}
	fmt.Fprintln(stdout)
	ok := true
	for _, id := range selected {
		line, good := resultLine(id, f, bin, plan, mutating[id], results[id], statusOf(after, id), mcpChecked, mcpErr)
		fmt.Fprintln(stdout, line)
		ok = ok && good
	}
	for _, id := range selected {
		if a, found := agentsetup.ByID(id); found && mutating[id] && results[id].OK && !f.uninstall {
			fmt.Fprintf(stdout, "%s: %s\n", a.Name, a.Restart)
		}
	}
	if statePath != "" {
		fmt.Fprintf(stdout, "setup's record of what it added: %s\n", statePath)
	}
	if !ok {
		return 1
	}
	return 0
}

// selectFlagged is the non-interactive selection: --client ids and, with
// --all-detected, every installed agent, in table order.
func selectFlagged(st []agentsetup.Status, f setupFlags) []string {
	want := map[string]bool{}
	for _, c := range f.clients {
		want[c] = true
	}
	var out []string
	for _, s := range st {
		if want[s.Agent.ID] || (f.allDetected && s.Installed) {
			out = append(out, s.Agent.ID)
		}
	}
	return out
}

func wired(s agentsetup.Status) bool { return s.Registered || s.ViaPlugin }

// renderPickList prints the eight agents numbered with their ticks. A tick
// means "already wired" (the starting selection); newly detected agents stay
// unticked until the person ticks them.
func renderPickList(w io.Writer, st []agentsetup.Status, ticked []bool) {
	for i, s := range st {
		box := "[ ]"
		if ticked[i] {
			box = "[x]"
		}
		line := fmt.Sprintf("  %d. %s %s", i+1, box, s.Agent.Name)
		if !s.Installed {
			line += " (not installed)"
		}
		if !s.Agent.Proven {
			line += " (not yet proven)"
		}
		fmt.Fprintln(w, line)
	}
}

// ctxLineReader reads one line per call from in, and returns ctx's error as
// soon as ctx is done, so an interrupt at a prompt is answered at once rather
// than after the person presses Enter. A read still blocked when ctx ends is
// abandoned with its goroutine; setup is exiting by then.
func ctxLineReader(ctx context.Context, in *bufio.Reader) func() (string, error) {
	return func() (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		type line struct {
			s   string
			err error
		}
		got := make(chan line, 1)
		go func() {
			s, err := in.ReadString('\n')
			got <- line{s, err}
		}()
		select {
		case l := <-got:
			return l.s, l.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// pickAgents asks which agents to act on: numbers toggle and the list is
// shown again; an empty line (or the end of input) accepts.
func pickAgents(readLine func() (string, error), w io.Writer, st []agentsetup.Status) ([]string, error) {
	ticked := make([]bool, len(st))
	for i, s := range st {
		ticked[i] = wired(s)
	}
	for {
		fmt.Fprintln(w, "Coding agents ([x] = already wired):")
		renderPickList(w, st, ticked)
		fmt.Fprint(w, "Type numbers to toggle (e.g. 1 3), Enter to accept: ")
		line, err := readLine()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		toggles := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\r' || r == '\n' })
		next := append([]bool(nil), ticked...)
		for _, t := range toggles {
			n, aerr := strconv.Atoi(t)
			if aerr != nil || n < 1 || n > len(st) {
				return nil, fmt.Errorf("%q is not a number from 1 to %d", t, len(st))
			}
			next[n-1] = !next[n-1]
		}
		ticked = next
		if len(toggles) > 0 && err == nil {
			continue
		}
		var out []string
		for i, s := range st {
			if ticked[i] {
				out = append(out, s.Agent.ID)
			}
		}
		return out, nil
	}
}

// mutatingAgents are the agents whose plan changes something. An agent whose
// actions are only noop or skip-skill needs no approval and no Apply: nothing
// would be written, so not even the state file is touched.
func mutatingAgents(p agentsetup.Plan) map[string]bool {
	m := map[string]bool{}
	for _, a := range p.Actions {
		if a.Kind != agentsetup.ActionNoop && a.Kind != agentsetup.ActionSkipSkill {
			m[a.AgentID] = true
		}
	}
	return m
}

func statusOf(st []agentsetup.Status, id string) agentsetup.Status {
	for _, s := range st {
		if s.Agent.ID == id {
			return s
		}
	}
	return agentsetup.Status{}
}

func notesOf(p agentsetup.Plan, id string, kinds ...agentsetup.ActionKind) []string {
	var out []string
	for _, a := range p.Actions {
		if a.AgentID != id {
			continue
		}
		for _, k := range kinds {
			if a.Kind == k {
				out = append(out, strings.TrimSpace(strings.ReplaceAll(a.Note, "(not yet proven)", "")))
			}
		}
	}
	return out
}

// noopsSettled reports whether the agent has noop actions and every one is a
// satisfied end state rather than a blocked one.
func noopsSettled(p agentsetup.Plan, id string) bool {
	n := 0
	for _, a := range p.Actions {
		if a.AgentID == id && a.Kind == agentsetup.ActionNoop {
			n++
			if !a.Settled {
				return false
			}
		}
	}
	return n > 0
}

func hasKind(p agentsetup.Plan, id string, k agentsetup.ActionKind) bool {
	for _, a := range p.Actions {
		if a.AgentID == id && a.Kind == k {
			return true
		}
	}
	return false
}

// resultLine builds the one line an agent gets after the run, and whether that
// agent counts as verified. Every claim in it comes from the re-detect run
// after applying, never from the fact that the commands exited 0.
func resultLine(id string, f setupFlags, bin string, plan agentsetup.Plan, mutated bool, res agentsetup.Result, now agentsetup.Status, mcpChecked bool, mcpErr error) (string, bool) {
	a, _ := agentsetup.ByID(id)
	suffix := ""
	if !a.Proven {
		suffix = " (not yet proven)"
	}
	if !mutated {
		// Only noop actions: say what the plan said, never "registered". The
		// exit code comes from the plan's own Settled marker, not from its words.
		note := strings.Join(notesOf(plan, id, agentsetup.ActionNoop), "; ")
		return fmt.Sprintf("%s: %s%s", a.Name, note, suffix), noopsSettled(plan, id)
	}
	if !res.OK {
		detail := strings.TrimSpace(res.Detail)
		return fmt.Sprintf("%s: FAILED: %s%s", a.Name, detail, suffix), false
	}
	var parts []string
	good := true
	fail := func(s string) { parts = append(parts, s); good = false }
	if f.uninstall {
		if hasKind(plan, id, agentsetup.ActionRemove) || hasKind(plan, id, agentsetup.ActionUnwriteFile) {
			switch {
			case now.Unknown != "":
				fail("could not confirm removal: " + now.Unknown)
			case now.Registered:
				fail("still registered")
			default:
				parts = append(parts, "unregistered")
			}
		} else if hasKind(plan, id, agentsetup.ActionForget) {
			switch {
			case now.Unknown != "":
				fail("could not confirm it is unregistered: " + now.Unknown)
			case now.Registered:
				fail("registered again (" + strings.Join(now.Command, " ") + ") after setup dropped its record")
			default:
				parts = append(parts, "already unregistered, setup's record dropped")
			}
		} else {
			parts = append(parts, "registration left alone")
		}
		if hasKind(plan, id, agentsetup.ActionRemoveSkill) {
			if now.Skill == "absent" {
				parts = append(parts, "skill removed")
			} else {
				fail(fmt.Sprintf("skill left in place (%s)", now.Skill))
			}
		}
		if good {
			return fmt.Sprintf("%s: removed (%s)%s", a.Name, strings.Join(parts, ", "), suffix), true
		}
		return fmt.Sprintf("%s: NOT verified: %s%s", a.Name, strings.Join(parts, ", "), suffix), false
	}
	switch {
	case now.Unknown != "":
		fail("registration unreadable: " + now.Unknown)
	case now.Registered && agentsetup.SameCommand(now.Command, bin):
		parts = append(parts, fmt.Sprintf("registered (inspect shows %s mcp)", bin))
	case now.Registered:
		fail(fmt.Sprintf("registered but inspect shows %q", strings.Join(now.Command, " ")))
	default:
		fail("NOT registered (inspect does not list corral)")
	}
	switch {
	case a.SkillMode == agentsetup.SkillNone:
		parts = append(parts, "skill n/a")
	case !f.skill:
		parts = append(parts, "skill skipped (--skill=false)")
	case hasKind(plan, id, agentsetup.ActionWriteSkill):
		if now.Skill == "managed" {
			parts = append(parts, "skill managed")
		} else {
			fail(fmt.Sprintf("skill not managed (state %q)", now.Skill))
		}
	default:
		skipped := strings.Join(notesOf(plan, id, agentsetup.ActionSkipSkill), "; ")
		if skipped == "" {
			skipped = "shared with another agent"
		}
		parts = append(parts, "skill skipped ("+skipped+")")
	}
	if mcpErr != nil {
		fail("mcp did NOT answer: " + mcpErr.Error())
	} else if mcpChecked {
		parts = append(parts, "mcp answered")
	}
	if good {
		return fmt.Sprintf("%s: verified: %s%s", a.Name, strings.Join(parts, ", "), suffix), true
	}
	return fmt.Sprintf("%s: NOT verified: %s%s", a.Name, strings.Join(parts, ", "), suffix), false
}
