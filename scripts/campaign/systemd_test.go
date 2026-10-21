// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	testPath = "/home/me/.local/bin:/usr/bin:/bin"
	testCal  = "Mon *-*-* 03:00:00"
)

func unitInputs(t *testing.T) (bin, corral, campaignDir, targets string) {
	t.Helper()
	d := t.TempDir()
	bin = filepath.Join(d, "bin", "campaign")
	corral = filepath.Join(d, "cbin", "corral")
	for _, p := range []string{bin, corral} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targets = filepath.Join(d, "targets.json")
	if err := os.WriteFile(targets, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	return bin, corral, filepath.Join(d, "campaign-dir"), targets
}

func TestTimerUnitIsPersistentAndScheduled(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	_, timer, err := RenderUnits(bin, corral, dir, tg, testPath, testCal, "starlette")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OnCalendar=" + testCal, "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer lacks %q:\n%s", want, timer)
		}
	}
	m := regexp.MustCompile(`(?m)^RandomizedDelaySec=(\S+)$`).FindStringSubmatch(timer)
	if m == nil || m[1] == "0" || m[1] == "0s" {
		t.Errorf("RandomizedDelaySec must be present and non-zero, got %v", m)
	}
}

func TestServiceUnitUsesAbsolutePathsOnly(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	svc, _, err := RenderUnits(bin, corral, dir, tg, testPath, testCal, "starlette")
	if err != nil {
		t.Fatal(err)
	}
	wantExec := "ExecStart=" + bin + " -targets " + tg + " run starlette"
	for _, want := range []string{
		wantExec,
		"Environment=CORRAL_CAMPAIGN_DIR=" + dir,
		"Environment=PATH=" + testPath,
		"Type=oneshot",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	if strings.Contains(svc, "Restart=") {
		t.Errorf("a failed run waits for next week; no Restart=:\n%s", svc)
	}
	for _, rel := range [][4]string{
		{"campaign", corral, dir, tg}, {bin, "corral", dir, tg},
		{bin, corral, "dir", tg}, {bin, corral, dir, "targets.json"},
	} {
		if _, _, err := RenderUnits(rel[0], rel[1], rel[2], rel[3], testPath, testCal, "x"); err == nil {
			t.Errorf("relative input %v must be refused", rel)
		}
	}
	if _, _, err := RenderUnits(bin, corral, dir, tg, testPath, testCal, "../evil"); err == nil {
		t.Error("a name that is not a plain identifier must be refused")
	}
}

func TestRenderUnitsRefusesPathsSystemdWouldMisread(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	for _, bad := range []string{" ", "\t", "%", "\"", "'", "\\", "$"} {
		for i, field := range []string{"campaign binary", "corral binary", "campaign dir", "targets file"} {
			in := [4]string{bin, corral, dir, tg}
			in[i] += "x" + bad + "y"
			_, _, err := RenderUnits(in[0], in[1], in[2], in[3], testPath, testCal, "n")
			if err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), fmt.Sprintf("%q", in[i])) {
				t.Errorf("%s with %q: want an error naming the field and path, got %v", field, bad, err)
			}
		}
	}
}

func installArgs(bin, corral, tg, name string) []string {
	return []string{"-targets", tg, "install-timer", name, "--bin", bin, "--corral", corral, "--path", testPath, "--on-calendar", testCal}
}

func copyTargets(t *testing.T, path string) {
	t.Helper()
	b, err := json.Marshal(map[string][]Target{"targets": {validTarget()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func firstTarget(t *testing.T, path string) string {
	t.Helper()
	ts, err := LoadTargets(path)
	if err != nil || len(ts) == 0 {
		t.Fatalf("load: %v", err)
	}
	return ts[0].Name
}

func TestInstallTimerWritesUnitsAndPrintsEnableCommand(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	copyTargets(t, tg)
	name := firstTarget(t, tg)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	env := func(k string) string {
		switch k {
		case "CORRAL_CAMPAIGN_DIR":
			return dir
		case "XDG_CONFIG_HOME":
			return xdg
		}
		return ""
	}
	r := &recRunner{}
	var out, errb bytes.Buffer
	code := cli(context.Background(), installArgs(bin, corral, tg, name), env, r, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(r.argvs) != 0 {
		t.Errorf("install-timer must run nothing, ran %v", r.argvs)
	}
	for _, ext := range []string{".service", ".timer"} {
		p := filepath.Join(xdg, "systemd", "user", "corral-campaign-"+name+ext)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("unit not written: %v", err)
		}
	}
	want := "systemctl --user enable --now corral-campaign-" + name + ".timer"
	if !strings.Contains(out.String(), want) {
		t.Errorf("must print %q, got %q", want, out.String())
	}

	// a second identical install is a no-op; a changed one is refused.
	out.Reset()
	if code := cli(context.Background(), installArgs(bin, corral, tg, name), env, r, &out, &errb); code != 0 || !strings.Contains(out.String(), "unchanged") {
		t.Errorf("identical reinstall: exit %d out %q", code, out.String())
	}
	svc := filepath.Join(xdg, "systemd", "user", "corral-campaign-"+name+".service")
	if err := os.WriteFile(svc, []byte("hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := cli(context.Background(), installArgs(bin, corral, tg, name), env, r, &out, &errb); code != 1 {
		t.Errorf("differing unit must be refused with exit 1, got %d", code)
	}
	if b, _ := os.ReadFile(svc); string(b) != "hand edited\n" {
		t.Error("existing unit was overwritten")
	}
}

func TestInstallTimerRefusesBadInvocations(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	copyTargets(t, tg)
	name := firstTarget(t, tg)
	xdg := t.TempDir()
	env := func(k string) string {
		if k == "CORRAL_CAMPAIGN_DIR" {
			return dir
		}
		if k == "XDG_CONFIG_HOME" {
			return xdg
		}
		return ""
	}
	nonExec := filepath.Join(t.TempDir(), "corral")
	if err := os.WriteFile(nonExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no --bin":              {"-targets", tg, "install-timer", name, "--corral", corral},
		"no --corral":           {"-targets", tg, "install-timer", name, "--bin", bin},
		"missing bin":           installArgs(bin+"-nope", corral, tg, name),
		"unknown name":          installArgs(bin, corral, tg, "nope"),
		"relative bin":          installArgs("campaign", corral, tg, name),
		"bin is a directory":    installArgs(filepath.Dir(bin), corral, tg, name),
		"corral not executable": installArgs(bin, nonExec, tg, name),
		"no name":               {"-targets", tg, "install-timer"},
		"relative -targets":     installArgs(bin, corral, "targets.json", name),
	}
	for label, e := range map[string][2]string{
		"relative XDG_CONFIG_HOME":     {"XDG_CONFIG_HOME", "rel/cfg"},
		"relative CORRAL_CAMPAIGN_DIR": {"CORRAL_CAMPAIGN_DIR", "rel/dir"},
	} {
		env := func(k string) string {
			if k == e[0] {
				return e[1]
			}
			return env(k)
		}
		if code := cli(context.Background(), installArgs(bin, corral, tg, name), env, &recRunner{}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Errorf("%s: want exit 2, got %d", label, code)
		}
	}
	for label, args := range cases {
		if code := cli(context.Background(), args, env, &recRunner{}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Errorf("%s: want exit 2, got %d", label, code)
		}
	}
	if es, _ := os.ReadDir(xdg); len(es) != 0 {
		t.Error("a refused install wrote files")
	}
}

type recRunner struct{ argvs [][]string }

func (r *recRunner) Run(_ context.Context, _ string, argv ...string) (string, error) {
	r.argvs = append(r.argvs, argv)
	return "", nil
}

func TestInstallTimerRequiresPathAndOnCalendar(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	copyTargets(t, tg)
	name := firstTarget(t, tg)
	xdg := t.TempDir()
	env := func(k string) string {
		switch k {
		case "CORRAL_CAMPAIGN_DIR":
			return dir
		case "XDG_CONFIG_HOME":
			return xdg
		}
		return ""
	}
	base := []string{"-targets", tg, "install-timer", name, "--bin", bin, "--corral", corral}
	cases := map[string][]string{
		"no --path":          append(append([]string{}, base...), "--on-calendar", testCal),
		"no --on-calendar":   append(append([]string{}, base...), "--path", testPath),
		"relative component": append(append([]string{}, base...), "--path", "/usr/bin:bin", "--on-calendar", testCal),
		"empty component":    append(append([]string{}, base...), "--path", "/usr/bin::/bin", "--on-calendar", testCal),
		"specifier in path":  append(append([]string{}, base...), "--path", "/usr/%h/bin", "--on-calendar", testCal),
		"space in path":      append(append([]string{}, base...), "--path", "/usr/my bin", "--on-calendar", testCal),
		"newline in cal":     append(append([]string{}, base...), "--path", testPath, "--on-calendar", "daily\nExecStart=/x"),
		"specifier in cal":   append(append([]string{}, base...), "--path", testPath, "--on-calendar", "%h"),
	}
	for label, args := range cases {
		if code := cli(context.Background(), args, env, &recRunner{}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Errorf("%s: want exit 2, got %d", label, code)
		}
	}
	if es, _ := os.ReadDir(xdg); len(es) != 0 {
		t.Error("a refused install wrote files")
	}
}

func TestInstallTimerWritesPrivateUnits(t *testing.T) {
	bin, corral, dir, tg := unitInputs(t)
	d := filepath.Join(t.TempDir(), "systemd", "user")
	var out bytes.Buffer
	if err := InstallTimer(d, bin, corral, dir, tg, testPath, testCal, "starlette", &out); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("unit dir: %v %v", fi, err)
	}
	fi, err := os.Stat(filepath.Join(d, "corral-campaign-starlette.service"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("unit file: %v %v", fi, err)
	}
}
