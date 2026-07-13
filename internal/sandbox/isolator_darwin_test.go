// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import (
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
