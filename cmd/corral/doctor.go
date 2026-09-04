// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
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
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	code := fs.String("code", "", "the source file you intend to audit (optional, enables the pairing and baseline checks)")
	test := fs.String("test", "", "its test file (optional; otherwise inferred from the language's convention)")
	backend := fs.String("jail", "", "sandbox backend (default: auto-detect). \"container\" needs CORRALAI_EXEC_IMAGE set to a toolchain image, e.g. CORRALAI_EXEC_IMAGE=python:3.12-bookworm")
	mutantModel := fs.String("mutant-model", "", "the mutant-generator model whose credential to check")
	writerModel := fs.String("writer-model", "", "the test-writer model whose credential to check")
	criticModel := fs.String("critic-model", "", "the test-critic model whose credential to check")
	shadowModel := fs.String("shadow-model", "", "the challenger generator model, if the run will name one — it needs a credential too")
	deriveModel := fs.String("derive-model", "", "the goal-derivation model a `certify --repo` run will name, if any")
	repoDir := fs.String("repo", ".", "the repository the run will audit — where its .corral/models.json registry is read from, exactly as certify reads it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// The model registry, resolved exactly as certify resolves it — doctor
	// exists to answer "would this run work", and it can only answer that
	// about the CONCRETE model a seat would really use. Its refusals are the
	// registry's, so a broken declaration is caught here for free.
	seatReg, regErr := resolveSeatRegistry("corral doctor", *repoDir,
		certifySeats(deriveModel, mutantModel, writerModel, criticModel, shadowModel, nil), stderr)
	if regErr != nil {
		fmt.Fprintf(stderr, "corral doctor: %v\n", regErr)
		return 2
	}

	// Everything after `--` is the test command, exactly as certify takes it.
	cmd := fs.Args()

	var results []checkResult
	iso, isoErr := sandbox.Resolve(sandbox.Config{Backend: strings.TrimSpace(*backend)})
	results = append(results, checkSandbox(iso, isoErr))

	if isoErr == nil {
		results = append(results, checkToolchain(iso, cmd, nil))
	}
	results = append(results, checkHerd(*mutantModel, *writerModel, *criticModel, *shadowModel, seatReg)...)
	results = append(results, checkDeriveSeat(*deriveModel, seatReg.deriveEndpoint())...)
	if strings.TrimSpace(*code) != "" {
		results = append(results, checkPairing(*code, *test))
	}

	failed := 0
	for _, r := range results {
		mark := "ok  "
		if !r.ok {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(stdout, "  [%s] %s\n", mark, r.name)
		if r.detail != "" {
			for _, line := range strings.Split(strings.TrimRight(r.detail, "\n"), "\n") {
				fmt.Fprintf(stdout, "         %s\n", line)
			}
		}
		if !r.ok && r.fatal {
			fmt.Fprintln(stdout, "         (later checks skipped — this one has to pass first)")
			break
		}
	}
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
func checkToolchain(iso sandbox.Isolator, cmd []string, depBinds []adequacy.DepBind) checkResult {
	if len(cmd) == 0 {
		return checkResult{name: "toolchain reachable inside the sandbox", ok: true,
			detail: "skipped — no test command given (pass it after `--`)"}
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
func checkHerd(mutant, writer, critic, shadow string, reg *seatResolution) []checkResult {
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
				out = append(out, checkResult{name: fmt.Sprintf("model for %s", r.role), ok: true,
					detail: "not named — the critic is advisory and may be left off"})
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
		mutantModel: mutant, writerModel: writer, criticModel: critic, shadowModel: shadow,
	}
	if reg != nil {
		in.seatProviders = reg.providers
		in.localEndpoints = reg.endpoints
	}
	name := fmt.Sprintf("herd accepted by certify's own preflight (mutant-generator=%s, test-writer=%s, test-critic=%s)",
		strings.TrimSpace(mutant), strings.TrimSpace(writer), advpool.ResolveOptionalModel(critic, "(off)"))
	if _, err := resolveAuditRoles(in, io.Discard); err != nil {
		out = append(out, checkResult{name: name, detail: err.Error()})
		return out
	}
	return append(out, checkResult{name: name, ok: true})
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
	name := "credential for the derive seat (" + model + ")"
	if _, err := newLLMDeriver(model, endpoint); err != nil {
		return []checkResult{{name: name, detail: err.Error()}}
	}
	return []checkResult{{name: name, ok: true}}
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
	if t := strings.TrimSpace(test); t != "" {
		if _, err := os.Stat(t); err != nil {
			return checkResult{name: name, detail: fmt.Sprintf("--test %q does not exist", t)}
		}
		return checkResult{name: name, ok: true}
	}
	res, ferr := reposcan.FindTest(p, "", code)
	if ferr != nil {
		return checkResult{name: name, detail: fmt.Sprintf("searching for a test for %q: %v", code, ferr)}
	}
	if res.Found {
		if res.ViaSearch {
			return checkResult{name: name, ok: true, detail: "paired by search: " + res.Path}
		}
		return checkResult{name: name, ok: true, detail: "found " + res.Path + " by convention"}
	}
	detail := fmt.Sprintf("no test found for %q by convention. Looked for:\n", code)
	for _, t := range res.Tried {
		detail += "  " + t + "\n"
	}
	if len(res.Roots) > 0 {
		detail += fmt.Sprintf("and searched %s recursively for a matching basename\n", strings.Join(res.Roots, ", "))
	}
	detail += "Pass --test <path> explicitly. Conventions vary — a JS/TS project using __tests__/ " +
		"or naming tests after behavior rather than after the source file will never pair automatically."
	return checkResult{name: name, detail: detail}
}

func asSnap(err error, target *adequacy.ErrSnapToolchain) bool {
	return errors.As(err, target)
}
