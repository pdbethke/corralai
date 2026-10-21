// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// unitNameRE is what a target name may be inside a unit file name and an
// ExecStart line: a plain identifier, so no path separator, space or
// systemd specifier ("%") can reach either.
var unitNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// unitUnsafe are the non-space characters systemd treats specially in an
// unquoted unit value: % is a specifier, $ an expansion, the rest quoting.
const unitUnsafe = "%\"'\\$"

// checkUnitValue refuses a value systemd would split or expand in an unquoted
// Environment=/ExecStart= line.
func checkUnitValue(label, v string) error {
	if i := strings.IndexAny(v, unitUnsafe); i >= 0 || strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s %q contains whitespace or one of %s, which systemd would split or expand in an unquoted Environment=/ExecStart= value; use a path without them", label, v, unitUnsafe)
	}
	return nil
}

// checkPathEnv validates a PATH value for the unit: every component absolute
// and free of the characters checkUnitValue refuses. The unit's PATH is exactly
// this value; nothing is appended, because a baked-in directory list would be
// one machine's layout imposed on the rest (the subscription seats' claude and
// codex live wherever the operator installed them, often ~/.local/bin).
func checkPathEnv(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("--path is required: the PATH value for the unit (absolute directories joined by ':'), including where git, gh, claude and codex live")
	}
	if err := checkUnitValue("--path", v); err != nil {
		return err
	}
	for _, c := range strings.Split(v, ":") {
		if !filepath.IsAbs(c) {
			return fmt.Errorf("--path %q: component %q must be an absolute directory", v, c)
		}
	}
	return nil
}

// checkOnCalendar validates the timer's schedule. Spaces are legal in a
// calendar spec ("Mon *-*-* 03:00"); control characters and the characters
// systemd expands are not.
func checkOnCalendar(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("--on-calendar is required: a systemd calendar spec, different for each target so audits do not overlap")
	}
	if strings.ContainsAny(v, unitUnsafe) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return fmt.Errorf("--on-calendar %q contains a control character or one of %s", v, unitUnsafe)
	}
	return nil
}

// RenderUnits returns the oneshot service and the timer for one target.
// Every path must be absolute: a unit runs with no working directory the
// operator chose, so a relative path would resolve against nothing meaningful.
//
// The service has no Restart=: a failed audit waits for next week rather than
// retrying and spending again (the run itself never retries either).
// onCalendar is required and not defaulted: one default would fire every
// target at the same moment, and the audits are long and metered. (`run` also
// takes a lock, so overlap is refused even if the schedules are not apart.)
// Persistent=true makes a missed run (machine off) run at the next start or
// wake; the randomized delay spreads the start time within the hour.
func RenderUnits(binPath, corralPath, campaignDir, targetsPath, pathEnv, onCalendar, name string) (service, timer string, err error) {
	for _, p := range [][2]string{
		{"campaign binary", binPath}, {"corral binary", corralPath},
		{"campaign dir", campaignDir}, {"targets file", targetsPath},
	} {
		if !filepath.IsAbs(p[1]) {
			return "", "", fmt.Errorf("%s %q must be an absolute path", p[0], p[1])
		}
	}
	for _, p := range [][2]string{
		{"campaign binary", binPath}, {"corral binary", corralPath},
		{"campaign dir", campaignDir}, {"targets file", targetsPath},
	} {
		if err := checkUnitValue(p[0], p[1]); err != nil {
			return "", "", err
		}
	}
	if err := checkPathEnv(pathEnv); err != nil {
		return "", "", err
	}
	if err := checkOnCalendar(onCalendar); err != nil {
		return "", "", err
	}
	if !unitNameRE.MatchString(name) {
		return "", "", fmt.Errorf("target name %q is not usable in a unit name", name)
	}
	service = fmt.Sprintf(`# SPDX-License-Identifier: Elastic-2.0
[Unit]
Description=corral proven-contributions campaign: audit %[1]s

[Service]
Type=oneshot
Environment=CORRAL_CAMPAIGN_DIR=%[2]s
Environment=PATH=%[3]s
ExecStart=%[4]s -targets %[5]s run %[1]s
`, name, campaignDir, pathEnv, binPath, targetsPath)
	timer = fmt.Sprintf(`# SPDX-License-Identifier: Elastic-2.0
[Unit]
Description=Scheduled corral campaign audit: %[1]s

[Timer]
OnCalendar=%[2]s
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
`, name, onCalendar)
	return service, timer, nil
}

// unitDir is where systemd user units live.
func unitDir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	h := getenv("HOME")
	if h == "" {
		return "", errors.New("neither XDG_CONFIG_HOME nor HOME is set")
	}
	return filepath.Join(h, ".config", "systemd", "user"), nil
}

// InstallTimer writes the two unit files and prints the enable command. It
// never runs systemctl: enabling a timer is the operator's act, and this
// program's command allowlist has no systemctl in it. An existing unit with
// different content is refused rather than overwritten, checked for both
// files before either is written.
func InstallTimer(dir, binPath, corralPath, campaignDir, targetsPath, pathEnv, onCalendar, name string, stdout io.Writer) error {
	svc, tim, err := RenderUnits(binPath, corralPath, campaignDir, targetsPath, pathEnv, onCalendar, name)
	if err != nil {
		return err
	}
	files := []struct {
		path, body string
	}{
		{filepath.Join(dir, "corral-campaign-"+name+".service"), svc},
		{filepath.Join(dir, "corral-campaign-"+name+".timer"), tim},
	}
	var toWrite []int
	for i, f := range files {
		old, err := os.ReadFile(f.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			toWrite = append(toWrite, i)
		case err != nil:
			return err
		case !bytes.Equal(old, []byte(f.body)):
			return fmt.Errorf("%s exists and differs; remove or reconcile it first", f.path)
		default:
			fmt.Fprintf(stdout, "%s unchanged\n", f.path)
		}
	}
	if len(toWrite) > 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for _, i := range toWrite {
		if err := os.WriteFile(files[i].path, []byte(files[i].body), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", files[i].path)
	}
	fmt.Fprintf(stdout, "to enable, run:\n  systemctl --user enable --now corral-campaign-%s.timer\n", name)
	return nil
}
