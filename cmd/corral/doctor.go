// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/agentsetup"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// `corral doctor` answers "will an audit work here?" BEFORE one is paid for.
//
// An audit costs real money and real minutes, and everything that can go wrong
// with the environment goes wrong ONE FAILURE AT A TIME: the sandbox will not
// start, or the toolchain is invisible inside it, or the key for the model you
// assigned is missing, or the file has no paired test, or the suite does not
// pass on unmutated code. Each discovery costs another run, and the first four
// cost money to learn.
//
// Every check here is FREE — no model is called — and they run in the order the
// audit itself would hit them, so the first ✗ is the first thing to fix.
//
// This exists because a single day of using the tool produced six separate
// environment walls (a compiler outside /usr, a devDependency not on PATH, a
// test-path convention the plugin did not know, a read-only dependency cache, a
// missing --repo-dir, and gems the jail could not see). Every one now has a
// clear message; none of them had to be met one at a time.

type checkResult struct {
	name string
	ok   bool
	// detail explains a failure and, wherever possible, names the fix. A check
	// that only says "failed" moves the operator's problem without solving it.
	detail string
	// fatal marks a check whose failure makes every later check meaningless.
	fatal bool
	// info marks a line that reports and never judges: it prints with its own
	// mark, is never counted as a failure, and never moves the exit code.
	info bool
}

// doctorEnv is what the agents section reads from the machine, injected the
// way setup's is so tests never touch the real HOME or run a real agent CLI.
type doctorEnv struct {
	Home     string
	Runner   agentsetup.Runner
	LookPath func(string) (string, error)
}

// doctorAgentEnv resolves the machine's environment for the agents section.
// A variable so the package's tests can substitute it; production never does.
var doctorAgentEnv = realDoctorEnv

func realDoctorEnv() (doctorEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorEnv{}, fmt.Errorf("cannot find your home directory: %w", err)
	}
	return doctorEnv{
		Home:     home,
		Runner:   agentsetup.ExecRunner{Timeout: agentsetup.Timeout, Dir: home}, // agent CLIs run in home, never the cwd (see ExecRunner.Dir)
		LookPath: exec.LookPath,
	}, nil
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	code := fs.String("code", "", "the source file you intend to audit (optional, enables the test-pairing check; doctor does not run your suite on unmutated code — run it by hand first)")
	test := fs.String("test", "", "its test file (optional; otherwise inferred from the language's convention)")
	backend := fs.String("jail", "", "sandbox backend (default: auto-detect). \"container\" needs CORRALAI_EXEC_IMAGE set to a toolchain image, e.g. CORRALAI_EXEC_IMAGE=python:3.12-bookworm")
	mutantModel := fs.String("mutant-model", "", "the mutant-generator model whose credential to check")
	writerModel := fs.String("writer-model", "", "the test-writer model whose credential to check")
	criticModel := fs.String("critic-model", "", "the test-critic model whose credential to check")
	shadow := registerShadowSeatFlags(fs, shadowSeatHelp{
		model:       "the challenger generator model, if the run will name one — it needs a credential too",
		pool:        "the challenger generator POOL a certify run will draw from (comma-separated). doctor draws nothing: it checks the pool exactly as certify does before its draw — at least two members, no duplicates, every member resolvable and holding a credential, not combined with --shadow-model",
		writerModel: "the challenger WRITER model, if the run will name one — doctor checks its credential too",
		writerPool:  "the challenger WRITER pool a certify run will draw from. doctor draws nothing: it checks the pool exactly as --shadow-pool's is checked",
		seed:        "the --shadow-seed a certify run will replay. doctor draws nothing: it checks the seed parses and that a pool is named for it to replay",
	})
	deriveModel := fs.String("derive-model", "", "the goal-derivation model a `certify --repo` run will name, if any. Naming it makes doctor SPEND ONE small real derive call through certify's own deriver, on a tiny built-in file with an obvious property, and fail the check when the seat errors or returns an empty reply; nothing is called unless this is named")
	repoDir := fs.String("repo", ".", "the repository the run will audit — where its .corral/models.json registry is read from, exactly as certify reads it")
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	// Every member of every challenger pool, checked exactly as certify checks
	// it before its draw — but never drawn: doctor answers "would this run
	// work", and a pool works only if EVERY member could be the one drawn.
	// Run before the registry resolves the seats, as certify runs it.
	poolNamed := strings.TrimSpace(*shadow.pool) != "" || strings.TrimSpace(*shadow.writerPool) != ""
	poolErr := validateShadowPools("corral doctor", *repoDir, shadow, nil, true, stderr)

	// The model registry, resolved exactly as certify resolves it — doctor
	// exists to answer "would this run work", and it can only answer that
	// about the CONCRETE model a seat would really use. Its refusals are the
	// registry's, so a broken declaration is caught here for free.
	seatReg, regErr := resolveSeatRegistry("corral doctor", *repoDir,
		certifySeats(deriveModel, mutantModel, writerModel, criticModel, shadow.model, shadow.writerModel), stderr)
	if regErr != nil {
		fmt.Fprintf(stderr, "corral doctor: %v\n", regErr)
		return 2
	}

	// Everything after `--` is the test command, exactly as certify takes it.
	cmd := fs.Args()

	var results []checkResult
	// First, not beside the herd: it reads no sandbox, so the sandbox's fatal
	// failure must not hide it — a pool with a bad member is a refusal at
	// certify's door whatever the host can run.
	if poolErr != nil {
		results = append(results, checkResult{name: "shadow pool", detail: poolErr.Error()})
	} else if poolNamed {
		results = append(results, checkResult{name: "shadow pool", ok: true})
	}
	iso, isoErr := sandbox.Resolve(sandbox.Config{Backend: strings.TrimSpace(*backend)})
	results = append(results, checkSandbox(iso, isoErr))

	if isoErr == nil {
		// The language, when --code names one, so the rehearsal asks that
		// language's jail-only requirements too; nil asks none.
		var plug lang.Plugin
		if c := strings.TrimSpace(*code); c != "" {
			plug, _ = lang.Detect(c)
		}
		results = append(results, checkToolchain(iso, cmd, nil, plug))
	}
	results = append(results, checkHerd(*mutantModel, *writerModel, *criticModel, *shadow.model, *shadow.writerModel, seatReg)...)
	derive := checkDeriveSeat(*deriveModel, seatReg.deriveEndpoint())
	results = append(results, derive...)
	if strings.TrimSpace(*code) != "" {
		results = append(results, checkPairing(*code, *test))
	}

	failed := 0
	for _, r := range results {
		if !printCheck(stdout, r, &failed) {
			break
		}
	}
	// The one check that spends, run only when the seat was named and its
	// static checks passed (a missing CLI or key is reported as that, not as
	// a failed call). It runs AFTER the other checks have printed and is
	// announced before the call, so a fatal check above can never hide a
	// call that was spent (review I1, ruling R25).
	if model := strings.TrimSpace(*deriveModel); model != "" && checksPassed(derive) {
		fmt.Fprintf(stdout, "  spending one derive call on %s…\n", model)
		printCheck(stdout, probeDeriveSeat(model, seatReg.deriveEndpoint()), &failed)
	}
	printAgentsSection(stdout)
	if failed > 0 {
		fmt.Fprintf(stdout, "\n%d check(s) failed — fix these before spending a run.\n", failed)
		return 1
	}
	fmt.Fprintln(stdout, "\nAll checks passed — the environment is not what will stop an audit here.")
	fmt.Fprintln(stdout, "Two things doctor does NOT check, because both need a real seeded workspace:")
	fmt.Fprintln(stdout, "  - whether your suite passes on UNMUTATED code inside the sandbox (the most common way an audit dies);")
	fmt.Fprintln(stdout, "  - whether a multi-file project needs --repo-dir (it does — the bare --code form seeds only that file and its test).")
	fmt.Fprintln(stdout, "`certify --local` reports the first as COULD-NOT-GRADE with the runner's own output.")
	return 0
}

// printCheck prints one result and counts a failure; false means a fatal
// failure, after which later checks are skipped.
func printCheck(stdout io.Writer, r checkResult, failed *int) bool {
	mark := "ok  "
	if r.info {
		mark = "info"
	} else if !r.ok {
		mark = "FAIL"
		*failed++
	}
	fmt.Fprintf(stdout, "  [%s] %s\n", mark, r.name)
	if r.detail != "" {
		for _, line := range strings.Split(strings.TrimRight(r.detail, "\n"), "\n") {
			fmt.Fprintf(stdout, "         %s\n", line)
		}
	}
	if !r.info && !r.ok && r.fatal {
		fmt.Fprintln(stdout, "         (later checks skipped — this one has to pass first)")
		return false
	}
	return true
}

// printAgentsSection reports each installed coding agent's corral wiring. It
// is informational only: doctor answers "would an audit run work", and agent
// wiring is not an audit prerequisite. Detection is already concurrent, so the
// section costs at most one inspect timeout.
func printAgentsSection(stdout io.Writer) {
	fmt.Fprintln(stdout, "\nCoding agents (informational — never affects the result above):")
	var res []checkResult
	env, err := doctorAgentEnv()
	if err != nil {
		res = []checkResult{{name: "agents not inspected: " + err.Error(), info: true}}
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		res = checkAgents(agentsetup.Detect(ctx, env.Runner, env.Home, env.LookPath))
	}
	failed := 0
	for _, r := range res {
		printCheck(stdout, r, &failed)
	}
	fmt.Fprintln(stdout, "  `corral setup` wires agents.")
}

// checkAgents is one informational line per INSTALLED agent; an agent whose CLI
// is absent prints nothing. The wording and the reading of Status match what
// `corral setup` shows: wired is Registered or ViaPlugin, and an inspect that
// could not answer is "unknown", never "not registered".
func checkAgents(st []agentsetup.Status) []checkResult {
	var out []checkResult
	for _, s := range st {
		if !s.Installed {
			continue
		}
		var wiring string
		switch {
		case s.Registered:
			wiring = "registered"
			if len(s.Command) > 0 {
				wiring += " (" + strings.Join(s.Command, " ") + ")"
			}
		case s.ViaPlugin:
			wiring = "wired by the corral plugin"
		case s.Unknown != "":
			wiring = "unknown (" + strings.Join(strings.Fields(s.Unknown), " ") + ")"
		default:
			wiring = "not registered"
		}
		line := fmt.Sprintf("%s: %s; skill %s", s.Agent.Name, wiring, s.Skill)
		if !s.Agent.Proven {
			line += " (not yet proven)"
		}
		out = append(out, checkResult{name: line, info: true})
	}
	if len(out) == 0 {
		out = append(out, checkResult{name: "no coding agent CLI found on PATH", info: true})
	}
	return out
}

func checkSandbox(iso sandbox.Isolator, err error) checkResult {
	if err != nil {
		return checkResult{name: "sandbox starts", fatal: true, detail: fmt.Sprintf(
			"%v\ncorral refuses to run your tests against live mutations unsandboxed, so this is not optional.\n"+
				"On Linux install bubblewrap (apt install bubblewrap); or use --jail container with docker/podman.", err)}
	}
	_ = iso
	return checkResult{name: "sandbox starts", ok: true}
}

// checkToolchain runs the test command's own binary INSIDE the SAME jail
// configuration `certify --local` would actually score with — same builder
// (newRunJail), same binds, same env, same seeded-files rule (none, for a
// bare toolchain probe: neither this check nor a bare/no-`--repo-dir` real
// run seeds anything beyond the run's own workspace). The host having the
// tool proves nothing: the jail mounts /usr and the dependency dirs, so a
// compiler from snap, asdf, nvm, rustup, pyenv or Homebrew can be on PATH
// here and absent there — and a relative path like `.venv/bin/python`,
// resolved against the OPERATOR's cwd for the toolchain-bind heuristic, is
// resolved against the JAIL's own fresh, empty workspace when it actually
// runs, and is not there at all.
//
// depBinds mirrors whatever the real run would bind read-only (nil in bare
// `--code`/`--test` mode, the auto-detected node_modules/vendor/.venv/...
// set under `--repo-dir` — see certify --local's own newRunJail call), so a
// probe run under `--repo-dir` sees exactly the dependency dirs the audit
// itself will see.
//
// plug, when the language is known (doctor's --code), is asked its
// jail-only requirements first (lang.JailPreflight) — the same call certify's
// door makes for every jailed run, so the rehearsal refuses what the run
// would. nil (no --code) asks none: a `--version` probe is all doctor can do
// without knowing the language.
func checkToolchain(iso sandbox.Isolator, cmd []string, depBinds []adequacy.DepBind, plug lang.Plugin) checkResult {
	if len(cmd) == 0 {
		return checkResult{name: "toolchain reachable inside the sandbox", ok: true,
			detail: "skipped — no test command given (pass it after `--`)"}
	}
	if plug != nil {
		if err := lang.JailPreflight(plug, cmd); err != nil {
			return checkResult{name: "toolchain reachable inside the sandbox", detail: err.Error()}
		}
	}
	tool := cmd[0]
	jail := newRunJail(iso, 60*time.Second, depBinds)
	// newRunJail's concrete jail (adequacy.NewJail's bwrapJail) always
	// implements VerboseJail too — asserted here rather than widening
	// Jail's own narrow interface, which exists to keep every OTHER caller
	// (the scorer, which only ever needs pass/fail) honest about that.
	vjail, ok := jail.(adequacy.VerboseJail)
	if !ok {
		return checkResult{name: "toolchain reachable inside the sandbox", detail: "internal: jail does not support verbose output"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pass, out, err := vjail.RunTestVerbose(ctx, nil, []string{tool, "--version"})
	if err != nil {
		var snap adequacy.ErrSnapToolchain
		if asSnap(err, &snap) {
			return checkResult{name: "toolchain reachable inside the sandbox", detail: snap.Error()}
		}
		var unbindable adequacy.ErrUnbindableToolchain
		if errors.As(err, &unbindable) {
			return checkResult{name: "toolchain reachable inside the sandbox", detail: unbindable.Error()}
		}
		return checkResult{name: "toolchain reachable inside the sandbox", detail: fmt.Sprintf(
			"%q could not run inside the sandbox: %v", tool, err)}
	}
	if !pass {
		// The run this check preflights would hit the SAME jail with the
		// SAME command and die the same way — so "the shell could not even
		// find the binary" (exit 127, sh's own "<tool>: not found") is a
		// hard FAIL, never "inconclusive". Any OTHER non-zero exit is a
		// distinct case: the tool WAS found and ran, it just doesn't accept
		// --version — that one stays inconclusive, since claiming a failure
		// there would send the operator after a problem that may not exist.
		if toolNotFoundInJail(out, tool) {
			parity := "the run itself will hit this exact wall — same jail, same binds, same env — and die grading nothing."
			if len(depBinds) == 0 {
				parity = "a bare `--local` run (no --repo-dir) will hit this exact wall — same jail, same binds, same env — and die grading nothing.\n" +
					"a `certify --local --repo-dir` run additionally binds your project's dependency dirs (.venv/node_modules/vendor/...) read-only, so this failure may not apply there — rerun doctor with the same flags to be sure."
			}
			return checkResult{name: "toolchain reachable inside the sandbox", detail: fmt.Sprintf(
				"%s\n%s\n"+
					"the jail cannot see %s — for a repo with a virtualenv use `certify --repo --substrate workspace`, or bake the toolchain into CORRALAI_EXEC_IMAGE.",
				strings.TrimSpace(out), parity, toolchainDirHint(tool))}
		}
		return checkResult{name: "toolchain reachable inside the sandbox", ok: true,
			detail: fmt.Sprintf("%q is reachable, but `--version` exited non-zero — inconclusive, not a failure: many test runners do not accept that flag", tool)}
	}
	return checkResult{name: "toolchain reachable inside the sandbox", ok: true}
}

// toolNotFoundInJail reports whether out is the JAIL SHELL's own "no such
// command" complaint (sh: 1: <tool>: not found, or the container backend's
// "No such file or directory") rather than the probed tool's own non-zero
// exit — the exact failure mode that killed the run this check exists to
// catch: the toolchain was on the operator's host but invisible inside the
// jail's fresh workspace, which is a hard failure, not a tool that merely
// rejects `--version`.
func toolNotFoundInJail(out, tool string) bool {
	return strings.Contains(out, tool+": not found") ||
		(strings.Contains(out, "No such file or directory") && strings.Contains(out, tool))
}

// toolchainDirHint names the thing to blame in the fix hint: the parent
// directory of a relative toolchain path (".venv/bin/python" -> ".venv"),
// since that is what the jail actually failed to see — or the bare command
// name when there is no directory to name (a PATH-relative "pytest").
func toolchainDirHint(tool string) string {
	tool = strings.TrimSpace(tool)
	if i := strings.IndexByte(tool, '/'); i >= 0 {
		return tool[:i]
	}
	return tool
}

// checkHerd answers "would certify accept this herd" by asking the SAME
// preflight certify runs (resolveAuditRoles): decorrelation, the credential
// each named seat needs under the operator's MODEL_BACKEND, the challenger
// seat, the registry's placements. doctor used to run its own version —
// ForModel per model, no MODEL_BACKEND, models de-duplicated before
// decorrelation, no shadow seat — and disagreed with certify in both
// directions: an all-local herd doctor called FAIL that certify accepted, a
// pinned gateway doctor demanded the vendor's key for that certify did not,
// and a writer==critic herd doctor passed that certify refused. A doctor
// that contradicts the run it is a rehearsal for is worse than none.
//
// The unnamed-seat findings stay doctor's own, because certify's refusal
// for those is one message about the run and doctor's job is one finding
// per seat.
func checkHerd(mutant, writer, critic, shadow, shadowWriter string, reg *seatResolution) []checkResult {
	var out []checkResult
	named := true
	for _, r := range []struct{ role, flag, model string }{
		{advpool.RoleMutantGenerator, "mutant", strings.TrimSpace(mutant)},
		{advpool.RoleTestWriter, "writer", strings.TrimSpace(writer)},
		{advpool.RoleTestCritic, "critic", strings.TrimSpace(critic)},
	} {
		// An UNNAMED seat is the finding, not an excuse to check a model the
		// operator never chose. doctor used to substitute claude-sonnet-5 here
		// and then report a credential failure for it — telling someone with a
		// Gemini key to go get an Anthropic one. corral has no default models;
		// the honest check is whether this seat has been filled at all.
		//
		// The critic is exempt: it is advisory, "off" is a legitimate answer,
		// and unnamed is treated the same as off.
		if r.model == "" {
			if r.role == advpool.RoleTestCritic {
				// Off is a choice, and certify requires it typed: an
				// unnamed critic is refused there too.
				named = false
				out = append(out, checkResult{name: fmt.Sprintf("model for %s", r.role), detail: fmt.Sprintf(
					"no model named. corral has no default models — pass --%s-model <model> (it must differ from the writer's), or --%s-model off to run without the advisory critic", r.flag, r.flag)})
				continue
			}
			named = false
			out = append(out, checkResult{name: fmt.Sprintf("model for %s", r.role), detail: fmt.Sprintf(
				"no model named. corral has no default models — pass --%s-model <model>, from whichever provider you have a key for", r.flag)})
		}
	}
	if !named {
		return out
	}
	in := localAuditInput{
		cmdName:     "corral doctor",
		mutantModel: mutant, writerModel: writer, criticModel: critic, shadowModel: shadow, shadowWriterModel: shadowWriter,
	}
	if reg != nil {
		in.seatProviders = reg.providers
		in.localEndpoints = reg.endpoints
	}
	name := fmt.Sprintf("herd accepted by certify's own preflight (mutant-generator=%s, test-writer=%s, test-critic=%s)",
		strings.TrimSpace(mutant), strings.TrimSpace(writer), advpool.ResolveOptionalModel(critic, "(off)"))
	// The per-seat lines print whatever the preflight says: a missing binary
	// fails the preflight too, and its one line names only the first seat it
	// met, while these name every seat and its binary.
	seats := agentSeatChecks(mutant, writer, critic, shadow, shadowWriter)
	if _, err := resolveAuditRoles(in, io.Discard); err != nil {
		out = append(out, checkResult{name: name, detail: err.Error()})
		return append(out, seats...)
	}
	out = append(out, checkResult{name: name, ok: true})
	return append(out, seats...)
}

// agentSeatChecks is one line per distinct AGENT seat the herd names. What
// doctor can say for free is exactly three things: the resolver built the seat
// (the definition expanded, which the preflight above already required), that
// the binary is on PATH, the version the binary prints, and — said out loud — that
// the login is NOT verified. An agent's subscription login can only be proved
// by spending a call, and doctor spends none; printing "ok" for it would be a
// claim nothing executed, the one thing this project refuses to make.
func agentSeatChecks(specs ...string) []checkResult {
	var out []checkResult
	seen := map[string]bool{}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		name, _, ok := agentbackend.AgentSeat(spec)
		if !ok || seen[spec] {
			continue
		}
		seen[spec] = true
		if err := agentbackend.AgentBinary(name); errors.Is(err, agentbackend.ErrBinaryNotChecked) {
			out = append(out, checkResult{
				name:   "agent seat " + spec,
				ok:     true,
				detail: err.Error() + ", definition ok, login not verified (no call is spent)",
			})
			continue
		} else if err != nil {
			out = append(out, checkResult{name: "agent seat " + spec, detail: err.Error()})
			continue
		}
		out = append(out, checkResult{
			name: "agent seat " + spec,
			ok:   true,
			detail: fmt.Sprintf("binary ok (%s), definition ok, login not verified (no call is spent)",
				orDefault(agentbackend.AgentVersion(name), "version not reported")),
		})
	}
	return out
}

// checkDeriveSeat rehearses the derive seat the same way certify --repo
// constructs it (newLLMDeriver): the credential under the operator's pin,
// or the registry's daemon. Skipped when no derive model is named — a
// certify --local run has no such seat, and --goals replaces it on --repo.
func checkDeriveSeat(model, endpoint string) []checkResult {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if isAgentSeat(model) {
		// An agent seat has no credential to check: it has a binary, and gets
		// the same line every other agent seat gets. Only when that line is
		// not a failure is the seat itself built (an endpoint placement can
		// still refuse it), so one missing CLI reads as one problem.
		lines := agentSeatChecks(model)
		for i := range lines {
			lines[i].name += " (derive seat)"
		}
		if len(lines) == 1 && !lines[0].ok {
			return lines
		}
		if _, err := newLLMDeriver(model, endpoint); err != nil {
			lines = append(lines, checkResult{name: "derive seat (" + model + ")", detail: err.Error()})
		}
		return lines
	}
	name := "credential for the derive seat (" + model + ")"
	if _, err := newLLMDeriver(model, endpoint); err != nil {
		return []checkResult{{name: name, detail: err.Error()}}
	}
	return []checkResult{{name: name, ok: true}}
}

// checksPassed reports whether every result is a pass (or information).
func checksPassed(rs []checkResult) bool {
	for _, r := range rs {
		if !r.ok && !r.info {
			return false
		}
	}
	return true
}

// doctorDeriver builds the probe's deriver: newLLMDeriver, certify's own
// derive seat, behind a seam so the probe's outcomes are testable without a
// real call.
var doctorDeriver = newLLMDeriver

// deriveProbeCandidate and deriveProbeSource are the probe's fixed input: a
// tiny file whose correctness property is obvious, so a working seat has
// something to say about it.
var deriveProbeCandidate = reposcan.Candidate{Path: "probe/clamp.go", Lang: "go"}

const deriveProbeSource = "package probe\n\n// Clamp returns v limited to the closed range [lo, hi].\nfunc Clamp(v, lo, hi int) int {\n\tif v < lo {\n\t\treturn lo\n\t}\n\tif v > hi {\n\t\treturn hi\n\t}\n\treturn v\n}\n"

// probeDeriveSeat makes ONE real derive call through certify's own deriver
// (ruling R25) and reports what came back: a goal (its first line), an
// explicit NONE, an empty reply, or the seat's error, with the tokens the
// seat reported or "not metered". An empty reply or an error fails: either
// one is a seat that produced nothing, which a certify run turns into
// derive-failed files (and once turned into "ungoaled — purely
// declarative", B6). It spends one call, and says so.
func probeDeriveSeat(model, endpoint string) checkResult {
	name := "derive seat answers (" + model + ") — spends one call"
	d, err := doctorDeriver(model, endpoint)
	if err != nil {
		return checkResult{name: name, detail: err.Error()}
	}
	text, ok, derr := d.Derive(context.Background(), deriveProbeCandidate, deriveProbeSource)
	spent := "spent one call; the seat did not report its token use (not metered)"
	if r, isReporter := d.(usageReporter); isReporter {
		for _, row := range r.modelCallRows() {
			if row.UnmeteredCalls == 0 {
				spent = fmt.Sprintf("spent one call: %d input / %d output tokens", row.InputTokens, row.OutputTokens)
			}
		}
	}
	switch {
	case derr != nil:
		return checkResult{name: name, detail: derr.Error()}
	case !ok:
		return checkResult{name: name, ok: true, detail: "answered NONE (no testable property) for a file with an obvious one; a certify run would exclude such files as ungoaled\n" + spent}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return checkResult{name: name, ok: true, detail: "goal: " + strings.TrimSpace(first) + "\n" + spent}
}

// checkPairing answers the question that silently produces a useless run: does
// the file corral is asked to audit have a test it can find?
func checkPairing(code, test string) checkResult {
	name := "code file pairs to a test"
	p, ok := lang.Detect(code)
	if !ok {
		return checkResult{name: name, detail: fmt.Sprintf(
			"no language plugin claims %q — corral infers the language from the extension", code)}
	}
	// The same refusal every audit door makes before any jail, store or
	// spend, asked through the SAME function certify --local's door asks
	// (auditLanding), so doctor cannot pass a file certify then refuses. A
	// file that pairs but cannot be audited is not "[ok]". Asked twice,
	// because the answer depends on the invocation: with the repository
	// (--repo-dir, rooted at the working directory as the pairing below is)
	// and without it (single-file). A file only the first admits — a Rust
	// file whose test lands in the test file it declares — is [ok], and the
	// detail says the bare form refuses it and why.
	_, landing := auditLanding(localAuditInput{codePath: code, repoDir: "."})
	needsRepoDir := ""
	if landing == nil {
		if _, bare := auditLanding(localAuditInput{codePath: code}); bare != nil {
			needsRepoDir = fmt.Sprintf(" — audit it with --repo-dir: without it, certify --local refuses it (%v)", bare)
		}
	}
	// Called only when landing != nil.
	paired := func(how string) checkResult {
		return checkResult{name: name, detail: fmt.Sprintf("pairs to %s, but cannot be audited yet: %v", how, landing)}
	}
	if t := strings.TrimSpace(test); t != "" {
		if _, err := os.Stat(t); err != nil {
			return checkResult{name: name, detail: fmt.Sprintf("--test %q does not exist", t)}
		}
		if landing != nil {
			return paired(t + " (--test)")
		}
		return checkResult{name: name, ok: true, detail: strings.TrimPrefix(needsRepoDir, " — ")}
	}
	// Rooted at the working directory, as certify --local and
	// --adversarial resolve an operator's --code: that is also the root a
	// file's own declared test path is held to (pairing.Sources.Root).
	res, ferr := reposcan.FindTest(p, ".", code)
	if ferr != nil {
		return checkResult{name: name, detail: fmt.Sprintf("searching for a test for %q: %v", code, ferr)}
	}
	if res.Found {
		if res.ViaSearch {
			if landing != nil {
				return paired(res.Path + " (search)")
			}
			return checkResult{name: name, ok: true, detail: "paired by search: " + res.Path + needsRepoDir}
		}
		if landing != nil {
			return paired(res.Path + " (" + res.Label + ")")
		}
		return checkResult{name: name, ok: true, detail: "found " + res.Path + " by convention (" + res.Label + ")" + needsRepoDir}
	}
	detail := fmt.Sprintf("no test found for %q by convention. ", code) + whereItLooked(res)
	if landing != nil {
		// Naming --test or a convention would send the operator after a
		// test that cannot help: the refusal is about the code path.
		return checkResult{name: name, detail: detail + fmt.Sprintf("It could not be audited yet either way: %v", landing)}
	}
	detail += "Pass --test <path> explicitly. Conventions vary — a JS/TS project using __tests__/ " +
		"or naming tests after behavior rather than after the source file will never pair automatically."
	return checkResult{name: name, detail: detail}
}

func asSnap(err error, target *adequacy.ErrSnapToolchain) bool {
	return errors.As(err, target)
}
