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
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/lang"
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
	backend := fs.String("jail", "", "sandbox backend (default: auto-detect)")
	mutantModel := fs.String("mutant-model", "", "the mutant-generator model whose credential to check")
	writerModel := fs.String("writer-model", "", "the test-writer model whose credential to check")
	criticModel := fs.String("critic-model", "", "the test-critic model whose credential to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Everything after `--` is the test command, exactly as certify takes it.
	cmd := fs.Args()

	var results []checkResult
	iso, isoErr := sandbox.Resolve(sandbox.Config{Backend: strings.TrimSpace(*backend)})
	results = append(results, checkSandbox(iso, isoErr))

	if isoErr == nil {
		results = append(results, checkToolchain(iso, cmd))
	}
	results = append(results, checkCredentials(*mutantModel, *writerModel, *criticModel)...)
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

// checkToolchain runs the test command's own binary INSIDE the jail. The host
// having it proves nothing: the jail mounts /usr and the dependency dirs, so a
// compiler from snap, asdf, nvm, rustup, pyenv or Homebrew can be on PATH here
// and absent there.
func checkToolchain(iso sandbox.Isolator, cmd []string) checkResult {
	if len(cmd) == 0 {
		return checkResult{name: "toolchain reachable inside the sandbox", ok: true,
			detail: "skipped — no test command given (pass it after `--`)"}
	}
	tool := cmd[0]
	jail := adequacy.NewJail(iso, 60*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pass, err := jail.RunTest(ctx, nil, []string{tool, "--version"})
	if err != nil {
		var snap adequacy.ErrSnapToolchain
		if asSnap(err, &snap) {
			return checkResult{name: "toolchain reachable inside the sandbox", detail: snap.Error()}
		}
		return checkResult{name: "toolchain reachable inside the sandbox", detail: fmt.Sprintf(
			"%q could not run inside the sandbox: %v", tool, err)}
	}
	if !pass {
		// Not every tool supports --version; a non-zero exit is inconclusive,
		// and claiming a failure here would send the operator after a problem
		// that may not exist.
		return checkResult{name: "toolchain reachable inside the sandbox", ok: true,
			detail: fmt.Sprintf("%q is reachable, but `--version` exited non-zero — inconclusive, not a failure: many test runners do not accept that flag", tool)}
	}
	return checkResult{name: "toolchain reachable inside the sandbox", ok: true}
}

// checkCredentials resolves the credential each assigned role needs. A key
// alone does not move providers, so this checks per MODEL rather than asking
// whether any key exists.
func checkCredentials(mutant, writer, critic string) []checkResult {
	roles := []struct{ role, model string }{
		{advpool.RoleMutantGenerator, strings.TrimSpace(mutant)},
		{advpool.RoleTestWriter, strings.TrimSpace(writer)},
		{advpool.RoleTestCritic, strings.TrimSpace(critic)},
	}
	seen := map[string]bool{}
	var out []checkResult
	for _, r := range roles {
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
			out = append(out, checkResult{name: fmt.Sprintf("model for %s", r.role), detail: fmt.Sprintf(
				"no model named. corral has no default models — pass --%s-model <model>, from whichever provider you have a key for",
				map[string]string{advpool.RoleMutantGenerator: "mutant", advpool.RoleTestWriter: "writer"}[r.role])})
			continue
		}
		if strings.EqualFold(r.model, "off") || seen[r.model] {
			continue
		}
		seen[r.model] = true
		name := fmt.Sprintf("credential for %s (%s)", r.role, r.model)
		if _, err := agentbackend.ForModel(r.model); err != nil {
			out = append(out, checkResult{name: name, detail: err.Error()})
			continue
		}
		out = append(out, checkResult{name: name, ok: true})
	}
	return out
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
	for _, cand := range p.TestPaths(code) {
		if _, err := os.Stat(cand.Path); err == nil {
			return checkResult{name: name, ok: true, detail: "found " + cand.Path + " by convention"}
		}
	}
	return checkResult{name: name, detail: fmt.Sprintf(
		"no test found for %q by convention.\nPass --test <path> explicitly. Conventions vary — a JS/TS project using __tests__/ "+
			"or naming tests after behavior rather than after the source file will never pair automatically.", code)}
}

func asSnap(err error, target *adequacy.ErrSnapToolchain) bool {
	return errors.As(err, target)
}
