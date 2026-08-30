// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
	"time"
)

// A run that fits comes back whole, unpadded, and with no truncation marker —
// the tail of twelve bytes is twelve bytes, not a ring's worth of zeros.
func TestTailWriterKeepsAShortRunWhole(t *testing.T) {
	w := NewTailWriter(1024)
	if _, err := w.Write([]byte("hello\nworld\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := w.String(); got != "hello\nworld" {
		t.Errorf("String() = %q, want %q", got, "hello\nworld")
	}
}

// The point of the type: what survives an over-long run is the END, because
// that is where a test runner prints the summary a failure parser reads.
func TestTailWriterKeepsTheEndNotTheBeginning(t *testing.T) {
	w := NewTailWriter(256)
	_, _ = w.Write([]byte(strings.Repeat("x", 200000)))
	_, _ = w.Write([]byte("\nFAILED a.py::test_x\n"))
	got := w.String()
	if !strings.Contains(got, "FAILED a.py::test_x") {
		t.Errorf("the trailing summary was dropped: %q", got)
	}
	if !strings.Contains(got, TruncationMarker) {
		t.Errorf("a truncated capture did not disclose it: %q", got)
	}
	if !strings.HasPrefix(got, TruncationMarker) {
		t.Errorf("the marker names what is MISSING, which is the front: %q", got)
	}
}

// The marker's room is reserved out of Max, not added to it: a caller that
// bounded its capture at N gets at most N back, so it never has to trim — and
// trimming is what would throw the marker away again.
func TestTailWriterNeverExceedsItsMax(t *testing.T) {
	const max = 512
	w := NewTailWriter(max)
	_, _ = w.Write([]byte(strings.Repeat("y", 100000)))
	if got := len(w.String()); got > max {
		t.Errorf("String() is %d bytes, want at most %d", got, max)
	}
}

// Run must hand back the tail when asked for it, and the head otherwise —
// same command, same exit code, only the capture differs.
func TestRunKeepTailPicksTheTailWriter(t *testing.T) {
	opts := Options{
		Backend:   passthroughIsolator{},
		MaxOutput: 512,
		Timeout:   30 * time.Second,
	}
	script := `head -c 100000 /dev/zero | tr '\0' 'z'; echo; echo LAST-LINE`
	tail := Run(t.Context(), script, func() Options { o := opts; o.KeepTail = true; return o }())
	if !strings.Contains(tail.Output, "LAST-LINE") {
		t.Errorf("KeepTail run lost the end of the output: %q", tail.Output)
	}
	head := Run(t.Context(), script, opts)
	if strings.Contains(head.Output, "LAST-LINE") {
		t.Errorf("the default run kept the tail — KeepTail would then mean nothing: %q", head.Output)
	}
	if tail.ExitCode != head.ExitCode {
		t.Errorf("the capture policy changed the exit code: %d vs %d", tail.ExitCode, head.ExitCode)
	}
}

// passthroughIsolator runs the command through sh with no isolation at all —
// the standing fake for exercising Run's own plumbing (capture, guards, exit
// codes) without needing bwrap on the host.
type passthroughIsolator struct{}

func (passthroughIsolator) Wrap(command string, _ Options, _ []string) ([]string, error) {
	return []string{"sh", "-c", command}, nil
}
func (passthroughIsolator) Preflight() error { return nil }
func (passthroughIsolator) Name() string     { return "passthrough" }
