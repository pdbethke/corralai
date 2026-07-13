// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

func TestSandboxExecDeniesReadsByDefault(t *testing.T) {
	iso, err := newSandboxExecIsolator()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := iso.Wrap("echo hi", Options{Workspace: "/tmp/ws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile := argv[2]
	if strings.Contains(profile, "(allow file-read*)\n") || strings.Contains(profile, "(allow file-read* )") {
		t.Fatalf("profile still grants blanket file-read*:\n%s", profile)
	}
	if !strings.Contains(profile, "(deny default)") {
		t.Fatalf("profile must default-deny:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* (subpath "/tmp/ws"))`) {
		t.Fatalf("workspace must be readable:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* (subpath "/usr"))`) {
		t.Fatalf("toolchain /usr must be readable:\n%s", profile)
	}
}

func TestSandboxExecAllowsUsrLocal(t *testing.T) {
	iso, err := newSandboxExecIsolator()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := iso.Wrap("echo hi", Options{Workspace: "/tmp/ws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile := argv[2]
	if !strings.Contains(profile, `(allow file-read* (subpath "/usr/local"))`) {
		t.Fatalf("Intel Homebrew / manual toolchains under /usr/local must be readable:\n%s", profile)
	}
}

func TestSandboxExecPrependsUlimitPrelude(t *testing.T) {
	iso, err := newSandboxExecIsolator()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := iso.Wrap("echo hi", Options{Workspace: "/tmp/ws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	shCommand := argv[len(argv)-1]
	if !strings.Contains(shCommand, "ulimit -u") || !strings.Contains(shCommand, "ulimit -f") {
		t.Fatalf("sh -c command must be prefixed with a ulimit ceiling (fork-bomb + disk-write guard):\n%s", shCommand)
	}
	if !strings.HasSuffix(shCommand, "echo hi") {
		t.Fatalf("original command must still run after the prelude:\n%s", shCommand)
	}
}

func TestSandboxExecDeniesSharedTmpWrite(t *testing.T) {
	iso, err := newSandboxExecIsolator()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := iso.Wrap("echo hi", Options{Workspace: "/tmp/ws"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile := argv[2]
	for _, p := range []string{"/tmp", "/var/tmp", "/private/tmp"} {
		if strings.Contains(profile, fmt.Sprintf(`(allow file-write* (subpath %q))`, p)) {
			t.Fatalf("shared host %s must not be writable inside the jail:\n%s", p, profile)
		}
	}
	shCommand := argv[len(argv)-1]
	if !strings.Contains(shCommand, "TMPDIR=") || !strings.Contains(shCommand, ".corral-tmp") {
		t.Fatalf("command must redirect TMPDIR to a per-run dir under the workspace:\n%s", shCommand)
	}
}
