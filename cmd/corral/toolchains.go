// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// toolchainReadBound bounds each version command. A version read is a
// courtesy to the record: it must never stall the audit it describes, so a
// command that has not answered by then is recorded as not read.
//
// A var, not a const, only so a test driving a whole `certify --repo` can
// shorten it: newLocalExecutor reads it at construction.
var toolchainReadBound = 30 * time.Second

// toolchainReadBudget bounds the whole read, every language and tool of it
// together: the per-command bound alone would let ten tools hold the end of an
// audit for five minutes.
const toolchainReadBudget = 60 * time.Second

// errBudgetRan is the cause recorded when the overall budget expires.
var errBudgetRan = errors.New("the version read's overall budget ran out")

// notRead is the value recorded for a toolchain that could not be read. The
// record says so rather than omit the key: a missing key reads as "nothing
// was asked", and this run asked.
func notRead(reason string) string { return "not read: " + reason }

// toolchainVersions reads, inside the jail the unit evidence pass already
// builds (scanRunner: one seeded jail per language, so the version recorded
// is the one that graded and the seed is not built again), the version of the
// language's interpreter or compiler and of the test runner the run uses.
// Keys are the tool (the binary's name, or the runner row's id); values are
// the first line of the command's output, trimmed, or "not read: <reason>".
// Each command runs once, under toolchainBound; a failure of any kind is
// recorded for that tool and never returned, because the audit has no use for
// an error here.
func (l *localExecutor) toolchainVersions(ctx context.Context, langName string) map[string]string {
	out := map[string]string{}
	plug, ok := lang.ByName(langName)
	if !ok {
		return out
	}
	run, files, why := l.versionJail(langName, plug)
	read := func(key string, cmd []string) {
		if why != "" {
			out[key] = notRead(why)
			return
		}
		out[key] = l.readVersion(ctx, run, files, key, cmd)
	}
	if cmd := plug.VersionCmd(); len(cmd) > 0 {
		read(filepath.Base(cmd[0]), cmd)
	}
	// The runner is the one the run resolved for this language: the same
	// command and --runner that recordedRunner reads.
	argv := l.checkArgv
	if len(argv) == 0 {
		argv = plug.TestCmd()
	}
	inv, rerr := lang.Resolve(plug, l.repoDir, argv, l.runnerFor(langName))
	if rerr != nil {
		out["runner"] = notRead("no runner row resolved for this command")
		return out
	}
	row, found := lang.RunnerFor(plug, inv)
	if !found {
		out["runner"] = notRead("no runner row resolved for this command")
		return out
	}
	if row.VersionCmd == nil {
		out[row.ID] = notRead(fmt.Sprintf("the %s row has no version command", row.ID))
		return out
	}
	cmd, shown := row.VersionCmd(argv)
	if !shown {
		out[row.ID] = notRead(fmt.Sprintf("the command does not show the %s binary", row.ID))
		return out
	}
	read(row.ID, cmd)
	return out
}

// versionJail is the runner a version is read in: a test's fake when one is
// set, else the scan's jail for the language (or, on the workspace substrate,
// the checkout's own runner). When there is none it returns why, as a fixed
// reason: the error that stopped it can carry host paths and the
// operator's command, and what it returns is published.
func (l *localExecutor) versionJail(langName string, plug lang.Plugin) (coverageRunner, map[string]string, string) {
	if l.toolchainRunner != nil {
		return l.toolchainRunner, map[string]string{}, ""
	}
	// Only a jail the run ALREADY prepared: building a repo seed here would
	// spend unbounded time after the audit. The workspace substrate seeds
	// nothing, so scanRunner is cheap there.
	if l.substrate != substrateWorkspace {
		if l.seeds == nil {
			return nil, nil, fmt.Sprintf("no jail was prepared for %s in this run", langName)
		}
		if _, ok := l.seeds.peek(langName); !ok {
			return nil, nil, fmt.Sprintf("no jail was prepared for %s in this run", langName)
		}
	}
	run, files, err := versionScanRunner(l, langName, plug)
	if err != nil {
		return nil, nil, fmt.Sprintf("the jail for %s could not be prepared", langName)
	}
	return run, files, ""
}

// versionScanRunner is the scan's own runner for a version read. A package
// var, the seam collectSelectionEvidence uses, so a test of a whole
// `certify --repo` can stand in for the version commands without a real
// toolchain.
var versionScanRunner = func(l *localExecutor, langName string, plug lang.Plugin) (coverageRunner, map[string]string, error) {
	return l.scanRunner(langName, plug, toolchainMaxOutput, l.toolchainBound)
}

// toolchainMaxOutput bounds what one version command may print.
const toolchainMaxOutput = 64 << 10

// readVersion runs one version command under the bound and reduces its output
// to the recorded value. The run happens on its own goroutine so that a
// runner which ignores its context still cannot hold the audit past the
// bound; the goroutine is abandoned in that case.
//
// A reason it records names the tool (key: a binary's base name or a runner
// row's id) and never the command or an error's text. The command is the
// operator's own up to the runner word, env assignments and flags included,
// and an error names argv[0] and host paths; the record is published, and
// the test command's redaction (recordedTestCommand) does not reach here.
// So each failure is one fixed reason per class.
//
// Only an exit of 0 is a version. The jail's Enumerate returns no error for a
// non-zero exit, so a failing `pytest --version` (a conftest ImportError)
// would otherwise have its first line recorded as pytest's version; the exit
// code comes from EnumerateDetailed, and a runner without it reads nothing.
func (l *localExecutor) readVersion(ctx context.Context, run coverageRunner, files map[string]string, key string, cmd []string) string {
	if l.toolchainBound <= 0 {
		return notRead("no bound was set for reading versions")
	}
	if ctx.Err() != nil {
		return notRead(budgetReason(ctx))
	}
	detailed, ok := run.(detailedCoverageRunner)
	if !ok {
		return notRead(fmt.Sprintf("the runner for the %s version command reports no exit code", key))
	}
	rctx, cancel := context.WithTimeout(ctx, l.toolchainBound)
	defer cancel()
	type answer struct {
		res sandbox.EnumerateResult
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		r, e := detailed.EnumerateDetailed(rctx, files, cmd)
		ch <- answer{r, e}
	}()
	select {
	case a := <-ch:
		if a.err != nil {
			if ctx.Err() != nil {
				return notRead(budgetReason(ctx))
			}
			return notRead(l.versionErrorReason(rctx, key, a.err))
		}
		if a.res.ExitCode != 0 {
			return notRead(fmt.Sprintf("the %s version command exited %d", key, a.res.ExitCode))
		}
		if v := lang.FirstLine(a.res.Output); v != "" {
			return v
		}
		return notRead(fmt.Sprintf("the %s version command printed nothing", key))
	case <-rctx.Done():
		if ctx.Err() != nil {
			return notRead(budgetReason(ctx))
		}
		return notRead(l.versionTimeoutReason(key))
	}
}

// versionErrorReason is the fixed reason for an error a version command
// returned, by its class; its text is never used (readVersion's comment).
func (l *localExecutor) versionErrorReason(rctx context.Context, key string, err error) string {
	var snap adequacy.ErrSnapToolchain
	var unbindable adequacy.ErrUnbindableToolchain
	switch {
	case errors.Is(err, adequacy.ErrTestTimeout), errors.Is(err, context.DeadlineExceeded), rctx.Err() != nil:
		return l.versionTimeoutReason(key)
	case errors.As(err, &snap):
		return fmt.Sprintf("the %s binary resolves to a snap, which cannot run in the sandbox", key)
	case errors.As(err, &unbindable):
		return fmt.Sprintf("the %s binary lives where the sandbox may not bind it", key)
	default:
		return fmt.Sprintf("the %s version command could not be run", key)
	}
}

// versionTimeoutReason is the reason for a version command the bound cut off.
func (l *localExecutor) versionTimeoutReason(key string) string {
	return fmt.Sprintf("the %s version command did not answer within %s", key, l.toolchainBound)
}

// budgetReason says why a read was cut off by its parent context: the overall
// budget, or the operator's interrupt.
func budgetReason(ctx context.Context) string {
	if c := context.Cause(ctx); errors.Is(c, errBudgetRan) {
		return c.Error()
	}
	return "interrupted"
}

// toolchainsJSON is ScanRow.Toolchains: the toolchains of every language the
// scan's sources are in, as {"<language>":{"<tool>":"<version>"}} JSON text
// (a ScanRow is compared whole, so no map). "" when there is no executor or
// no language to read.
func (l *localExecutor) toolchainsJSON(ctx context.Context, sources []string) string {
	if l == nil {
		return ""
	}
	langs := preflightLanguages(sources)
	names := make([]string, 0, len(langs))
	for n := range langs {
		names = append(names, n)
	}
	sort.Strings(names)
	// ONE budget for the whole read, under the run's own (signal) context.
	ctx, cancel := context.WithTimeoutCause(ctx, l.toolchainBudget, errBudgetRan)
	defer cancel()
	rec := map[string]map[string]string{}
	for _, n := range names {
		if v := l.toolchainVersions(ctx, n); len(v) > 0 {
			rec[n] = v
		}
	}
	if len(rec) == 0 {
		return ""
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(b)
}

// localToolchainsNotRead is ScanRow.Toolchains for a `certify --local` run.
// --local builds its jail inside buildJailWiring, per substrate and per file,
// with no command runner to ask a version of without restructuring it, so it
// reads none; the field says so for the language instead of being absent.
func localToolchainsNotRead(langName string) string {
	if langName == "" {
		return ""
	}
	b, err := json.Marshal(map[string]map[string]string{
		langName: {"toolchains": notRead("certify --local does not read toolchain versions yet")},
	})
	if err != nil {
		return ""
	}
	return string(b)
}
