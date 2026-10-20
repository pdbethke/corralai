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

// fixedPath is appended after the corral binary's directory so git and gh stay
// findable; a systemd user unit's default PATH is not the operator's shell's.
const fixedPath = "/usr/local/bin:/usr/bin:/bin"

// RenderUnits returns the oneshot service and the weekly timer for one target.
// Every path must be absolute: a unit runs with no working directory the
// operator chose, so a relative path would resolve against nothing meaningful.
//
// The service has no Restart=: a failed audit waits for next week rather than
// retrying and spending again (the run itself never retries either).
// Persistent=true makes a missed week (machine off) run at the next start or
// wake; the randomized delay spreads the start time within the hour.
func RenderUnits(binPath, corralPath, campaignDir, targetsPath, name string) (service, timer string, err error) {
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
		if i := strings.IndexAny(p[1], unitUnsafe); i >= 0 || strings.IndexFunc(p[1], unicode.IsSpace) >= 0 {
			return "", "", fmt.Errorf("%s %q contains whitespace or one of %s, which systemd would split or expand in an unquoted Environment=/ExecStart= value; use a path without them", p[0], p[1], unitUnsafe)
		}
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
Environment=PATH=%[3]s:%[4]s
ExecStart=%[5]s -targets %[6]s run %[1]s
`, name, campaignDir, filepath.Dir(corralPath), fixedPath, binPath, targetsPath)
	timer = fmt.Sprintf(`# SPDX-License-Identifier: Elastic-2.0
[Unit]
Description=Weekly corral campaign audit: %[1]s

[Timer]
OnCalendar=weekly
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
`, name)
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
func InstallTimer(dir, binPath, corralPath, campaignDir, targetsPath, name string, stdout io.Writer) error {
	svc, tim, err := RenderUnits(binPath, corralPath, campaignDir, targetsPath, name)
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, i := range toWrite {
		if err := os.WriteFile(files[i].path, []byte(files[i].body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", files[i].path)
	}
	fmt.Fprintf(stdout, "to enable, run:\n  systemctl --user enable --now corral-campaign-%s.timer\n", name)
	return nil
}
