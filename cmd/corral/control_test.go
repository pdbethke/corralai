// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/controlspec"
)

func TestControlSeed(t *testing.T) {
	dir := t.TempDir()
	specDB := filepath.Join(dir, "cs.db")
	testFile := filepath.Join(dir, "login_control_test.go")
	if err := os.WriteFile(testFile, []byte("package control\n// vetted test"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runControl([]string{"seed",
		"--spec-db", specDB, "--owner", "lead@x", "--goal", "asvs-v2.1.1",
		"--target", "internal/auth/login.go", "--code-path", "login.go",
		"--test-path", "login_control_test.go", "--test-file", testFile,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := controlspec.OpenStore(specDB)
	defer s.Close()
	v, _ := s.ListVetted("lead@x")
	if len(v) != 1 || v[0].Target != "internal/auth/login.go" || v[0].CodePath != "login.go" || v[0].Test == "" {
		t.Fatalf("seeded vetted control wrong: %+v", v)
	}
}
