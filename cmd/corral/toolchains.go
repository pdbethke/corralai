// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// toolchainReadBound bounds each version command. A version read is a
// courtesy to the record: it must never stall the audit it describes, so a
// command that has not answered by then is recorded as not read.
const toolchainReadBound = 30 * time.Second

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
	run, files, err := l.versionJail(langName, plug)
	read := func(key string, cmd []string) {
		if err != nil {
			out[key] = notRead(err.Error())
			return
		}
		out[key] = l.readVersion(ctx, run, files, cmd)
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
		return out
	}
	row, found := lang.RunnerFor(plug, inv)
	if !found {
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
// the checkout's own runner).
func (l *localExecutor) versionJail(langName string, plug lang.Plugin) (coverageRunner, map[string]string, error) {
	if l.toolchainRunner != nil {
		return l.toolchainRunner, map[string]string{}, nil
	}
	return l.scanRunner(langName, plug, toolchainMaxOutput, l.toolchainBound)
}

// toolchainMaxOutput bounds what one version command may print.
const toolchainMaxOutput = 64 << 10

// readVersion runs one version command under the bound and reduces its output
// to the recorded value. The run happens on its own goroutine so that a
// runner which ignores its context still cannot hold the audit past the
// bound; the goroutine is abandoned in that case.
func (l *localExecutor) readVersion(ctx context.Context, run coverageRunner, files map[string]string, cmd []string) string {
	if l.toolchainBound <= 0 {
		return notRead("no bound was set for reading versions")
	}
	rctx, cancel := context.WithTimeout(ctx, l.toolchainBound)
	defer cancel()
	type answer struct {
		out string
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		o, e := run.Enumerate(rctx, files, cmd)
		ch <- answer{o, e}
	}()
	select {
	case a := <-ch:
		if a.err != nil {
			return notRead(oneLine(a.err.Error()))
		}
		if v := lang.FirstLine(a.out); v != "" {
			return v
		}
		return notRead("the command printed nothing")
	case <-rctx.Done():
		return notRead(fmt.Sprintf("%s did not answer within %s", strings.Join(cmd, " "), l.toolchainBound))
	}
}

// oneLine is s's first line, so a multi-line error cannot spread over the
// record.
func oneLine(s string) string { return lang.FirstLine(s) }

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
