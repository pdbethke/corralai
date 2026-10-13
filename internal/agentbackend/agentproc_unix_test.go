// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package agentbackend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// goneWithin polls until every pid has exited, or d passes.
func goneWithin(d time.Duration, pids ...int) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		any := false
		for _, p := range pids {
			any = any || alive(p)
		}
		if !any {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// An agent runs in its own process group (so a timeout can kill what it
// started), which takes it out of the terminal's foreground group: Ctrl-C no
// longer reaches it. corral must forward the interrupt itself — at every
// verb, including one with no signal handling of its own — killing every
// live seat's group at once, and start no new call afterwards. Otherwise an
// interrupted run leaves a CLI spending until its timeout, and a second
// Ctrl-C orphans it outright.
func TestAnInterruptKillsEveryLiveAgentProcessGroup(t *testing.T) {
	t.Cleanup(resetInterruptForTest)
	pids := filepath.Join(t.TempDir(), "pids")
	t.Setenv("CORRALAI_AGENT_SLEEPER", `sh -c 'echo $$ > `+pids+`; sleep 60 & echo $! >> `+pids+`; wait'`)
	b, err := ForSeat("sleeper", SeatOptions{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil)
		done <- err
	}()
	var got []int
	for deadline := time.Now().Add(5 * time.Second); len(got) < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("the fake agent never started: %v", got)
		}
		time.Sleep(20 * time.Millisecond)
		got = got[:0]
		b, _ := os.ReadFile(pids)
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				got = append(got, n)
			}
		}
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Skipf("cannot signal self: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Errorf("the call ended with %v, want ErrInterrupted", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the call did not end within a second of the interrupt")
	}
	if !goneWithin(time.Second, got...) {
		t.Fatalf("agent processes %v survived the interrupt (orphaned)", got)
	}
	// Nothing new is started once the run is interrupted.
	if _, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("a call after the interrupt must be refused, got %v", err)
	}
}

// The handler exists only while a seat is live: between calls, and in a
// process that never runs an agent, Ctrl-C keeps its default meaning.
func TestNoSignalHandlerOutlivesTheLastSeat(t *testing.T) {
	t.Setenv("CORRALAI_AGENT_QUICK", "/bin/echo hi")
	b, err := ForSeat("quick", SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat([]Message{{Role: "user", Content: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if signalHandlerActive() {
		t.Fatal("a signal handler is still registered with no live seat")
	}
}

// A group that has already exited is not an error to kill: a timeout firing
// as the agent finishes must not turn a clean exit into a failure.
func TestKillingAnExitedGroupIsNotAnError(t *testing.T) {
	cmd := agentCommand(context.Background(), []string{"true"})
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel on an exited group = %v, want nil (ESRCH ignored)", err)
	}
}
