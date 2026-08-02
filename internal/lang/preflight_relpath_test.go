// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A RELATIVE tool path is resolved against the calling process's working
// directory, but the command it belongs to is meant to run in the repo (and,
// in the jail, in the workspace copy of it). So `-- ./.venv/bin/python -m
// pytest` is refused unless the operator happens to have cd'd into the repo
// first — and the refusal says "not found on PATH", which is actively
// misleading: PATH was never consulted for a name with a separator in it.
//
// This cost a real audit an afternoon. Until the plugin interface can carry the
// repo root, the error must at least say WHERE it looked and what to do.
func TestToolOnPath_RelativePathErrorNamesTheDirectoryAndTheFix(t *testing.T) {
	err := toolOnPath("./.venv/bin/python")
	if err == nil {
		t.Fatal("expected an error for a relative path that does not exist here")
	}
	msg := err.Error()

	wd, _ := os.Getwd()
	if !strings.Contains(msg, wd) {
		t.Errorf("error does not name the directory it searched (%s): %s", wd, msg)
	}
	// It must not claim PATH was consulted — it wasn't, for a name with a
	// separator, and that phrasing sends the reader to fix the wrong thing.
	if strings.Contains(msg, "not found on PATH") {
		t.Errorf("relative path error still blames PATH, which was never consulted: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "relative") {
		t.Errorf("error does not explain that the path is relative: %s", msg)
	}
}

// A BARE name genuinely is a PATH lookup, and its error should keep saying so.
func TestToolOnPath_BareNameStillBlamesPath(t *testing.T) {
	err := toolOnPath("corral-no-such-tool-xyz")
	if err == nil {
		t.Fatal("expected an error for a missing bare tool name")
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("bare-name error should still name PATH: %s", err)
	}
}

// And a relative path that DOES resolve must still pass — the check is only
// being made more informative, not stricter.
func TestToolOnPath_RelativePathThatExistsStillPasses(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	if err := toolOnPath("./tool"); err != nil {
		t.Fatalf("a relative path that exists must pass: %v", err)
	}
}
