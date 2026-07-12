// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func TestJailAdapterExitMapping(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — adapter exit-mapping needs the real jail")
	}
	j := NewJail(backend, 30*time.Second)
	// a command that exits 0 => passed; one that exits nonzero => not passed.
	pass, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"true"})
	if err != nil || !pass {
		t.Fatalf("exit-0 command: passed=%v err=%v, want passed=true", pass, err)
	}
	fail, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"false"})
	if err != nil || fail {
		t.Fatalf("exit-1 command: passed=%v err=%v, want passed=false", fail, err)
	}
}

func TestJailAdapterNilBackendErrors(t *testing.T) {
	j := NewJail(nil, time.Second)
	if _, err := j.RunTest(context.Background(), map[string]string{}, []string{"true"}); err == nil {
		t.Fatal("nil backend must error, never run unsandboxed")
	}
}

func TestJailAdapterWritesFilesIntoWorkspace(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise workspace writes")
	}
	j := NewJail(backend, 30*time.Second)
	// A nested path key must have its parent dir created, and the file content
	// must actually be readable by the command run in the jail.
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/dir/marker.txt": "hello",
	}, []string{"cat", "sub/dir/marker.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatalf("expected pass=true reading a written nested file, got false")
	}
}

func TestJailAdapterTimeoutNeverReadsAsPassed(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise a timeout")
	}
	j := NewJail(backend, 200*time.Millisecond)
	pass, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"sleep", "5"})
	if pass {
		t.Fatalf("a timed-out run must never read as passed")
	}
	if err == nil {
		t.Fatalf("a timed-out run should surface an error")
	}
}
