// SPDX-License-Identifier: Elastic-2.0

// Command campaign drives corral's proven-contributions campaign: it audits a
// named list of upstream repositories with the real `corral certify --repo`.
//
// It lives in scripts/ and not cmd/ ON PURPOSE, for the reason ledgersite
// does: scripts/gen-cli-docs.sh publishes a CLI reference page for every
// cmd/*/ binary, and this is internal tooling no user runs.
//
// The targets file (targets.json) names every value of every audit and
// defaults none; see Target.
//
//	campaign -targets targets.json list
//	campaign -targets targets.json run <name>
//	campaign -targets /abs/targets.json install-timer <name> \
//	    --bin /abs/campaign --corral /abs/corral \
//	    --path /home/me/.local/bin:/usr/bin:/bin --on-calendar 'Mon *-*-* 03:00'
//
// `run` needs CORRAL_CAMPAIGN_DIR, the directory holding one folder per target
// plus _corral-ledger/. `run` holds an exclusive lock on
// $CORRAL_CAMPAIGN_DIR/.lock for its whole length, so two audits never overlap
// (a second one exits 1). `install-timer` only writes systemd user units and
// prints the enable command; it never runs systemctl. Exit 2 means the invocation or the targets file is
// wrong; exit 1 means a run failed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// lockCampaign takes an exclusive, non-blocking flock on
// <campaignDir>/.lock for the length of a run. Audits are long and metered; a
// second one started by a timer that fired while the first still ran would
// double the spend and race on the same worktrees. The kernel drops the lock
// when the process dies, so a crash never leaves a stale one.
func lockCampaign(campaignDir string) (unlock func(), err error) {
	p := filepath.Join(campaignDir, ".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the campaign's own directory
	if err != nil {
		return nil, fmt.Errorf("opening the run lock %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor fits in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another campaign run is in progress (%s is locked)", p)
		}
		return nil, fmt.Errorf("locking %s: %w", p, err)
	}
	return func() { _ = f.Close() }, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli(ctx, os.Args[1:], os.Getenv, execRunner{echo: os.Stderr}, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// cli is main with its inputs passed in, so the exit-code rules are tested.
func cli(ctx context.Context, args []string, getenv func(string) string, r Runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("campaign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetsPath := fs.String("targets", "", "the targets file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintln(stderr, "campaign:", msg)
		return 2
	}
	rest := fs.Args()
	if *targetsPath == "" {
		return usage("-targets is required")
	}
	if len(rest) == 0 {
		return usage("a command is required: list | run <name>")
	}
	var campaignDir string
	switch rest[0] {
	case "list":
	case "install-timer":
		if len(rest) < 2 {
			return usage("usage: install-timer <name> --bin <abs> --corral <abs> --path <PATH value> --on-calendar <spec>")
		}
		campaignDir = getenv("CORRAL_CAMPAIGN_DIR")
		if campaignDir == "" {
			return usage("CORRAL_CAMPAIGN_DIR is required: the unit must carry it")
		}
	case "run":
		if len(rest) != 2 {
			return usage("usage: run <name>")
		}
		campaignDir = getenv("CORRAL_CAMPAIGN_DIR")
		if campaignDir == "" {
			return usage("CORRAL_CAMPAIGN_DIR is required: the directory holding one folder per target")
		}
	default:
		return usage(fmt.Sprintf("unknown command %q: list | run <name> | install-timer <name>", rest[0]))
	}

	var binPath, corralPath, unitPath, onCalendar string
	if rest[0] == "install-timer" {
		tfs := flag.NewFlagSet("install-timer", flag.ContinueOnError)
		tfs.SetOutput(stderr)
		bin := tfs.String("bin", "", "absolute path of the built campaign binary (required)")
		corral := tfs.String("corral", "", "absolute path of the corral binary (required)")
		pathEnv := tfs.String("path", "", "the unit's PATH: absolute directories joined by ':' (required)")
		onCal := tfs.String("on-calendar", "", "the timer's systemd calendar spec (required)")
		if err := tfs.Parse(rest[2:]); err != nil {
			return 2
		}
		if tfs.NArg() != 0 {
			return usage("usage: install-timer <name> --bin <abs> --corral <abs> --path <PATH value> --on-calendar <spec>")
		}
		for _, p := range [][2]string{{"--bin", *bin}, {"--corral", *corral}, {"-targets", *targetsPath}} {
			if !filepath.IsAbs(p[1]) {
				return usage(p[0] + " is required and must be an absolute path")
			}
			fi, err := os.Stat(p[1])
			if err != nil {
				return usage(p[0] + ": " + err.Error())
			}
			if p[0] != "-targets" && (!fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0) {
				return usage(p[0] + ": " + p[1] + " is not an executable regular file")
			}
		}
		if !filepath.IsAbs(campaignDir) {
			return usage("CORRAL_CAMPAIGN_DIR must be an absolute path")
		}
		if x := getenv("XDG_CONFIG_HOME"); x != "" && !filepath.IsAbs(x) {
			return usage("XDG_CONFIG_HOME must be an absolute path when set")
		}
		if err := checkPathEnv(*pathEnv); err != nil {
			return usage(err.Error())
		}
		if err := checkOnCalendar(*onCal); err != nil {
			return usage(err.Error())
		}
		binPath, corralPath, unitPath, onCalendar = *bin, *corral, *pathEnv, *onCal
	}

	ts, err := LoadTargets(*targetsPath)
	if err != nil {
		fmt.Fprintln(stderr, "campaign:", err)
		if errors.Is(err, ErrInvalid) {
			return 2 // the file is wrong: name what to fix, as for a missing flag
		}
		return 1 // could not read it at all
	}
	if rest[0] == "list" {
		for _, t := range ts {
			fmt.Fprintln(stdout, t.Name)
		}
		return 0
	}
	if rest[0] == "install-timer" {
		found := false
		for _, t := range ts {
			found = found || t.Name == rest[1]
		}
		if !found {
			return usage(fmt.Sprintf("no target named %q (see: list)", rest[1]))
		}
		dir, err := unitDir(getenv)
		if err == nil {
			err = InstallTimer(dir, binPath, corralPath, campaignDir, *targetsPath, unitPath, onCalendar, rest[1], stdout)
		}
		if err != nil {
			fmt.Fprintln(stderr, "campaign:", err)
			return 1
		}
		return 0
	}
	for _, t := range ts {
		if t.Name != rest[1] {
			continue
		}
		// goals are named relative to the targets file; the audit runs
		// elsewhere, so hand it an absolute path.
		if t.Goals != "" && !filepath.IsAbs(t.Goals) {
			t.Goals = filepath.Join(filepath.Dir(*targetsPath), t.Goals)
		}
		unlock, err := lockCampaign(campaignDir)
		if err != nil {
			fmt.Fprintln(stderr, "campaign:", err)
			return 1
		}
		defer unlock()
		if err := RunTarget(ctx, r, t, campaignDir, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "campaign:", err)
			return 1
		}
		return 0
	}
	return usage(fmt.Sprintf("no target named %q (see: list)", rest[1]))
}
