// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/clip"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// hangModule is a Go module whose one test hangs with 600 goroutines parked,
// so `go test -timeout 1s` prints `panic: test timed out`, the running test's
// name, and then every goroutine's stack: far more than any door keeps.
var hangModule = map[string]string{
	"go.mod": "module hang\n\ngo 1.21\n",
	"hang_test.go": `package hang

import "testing"

func TestHang(t *testing.T) {
	block := make(chan struct{})
	for i := 0; i < 600; i++ {
		go func() { <-block }()
	}
	<-block
}
`,
}

// A hung Go test names itself FIRST and prints goroutine stacks after: the
// run a review measured at 264,781 bytes, whose last 64 KiB named no test.
// The clean-code door keeps both ends of it, on the workspace substrate and
// in the real jail, and the string the test writer is shown names the hung
// test, still holds the end of the run, and is bounded.
func TestAHungGoTestIsNamedInWhatTheWriterIsShown(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("SKIP: go is not on PATH: %v", err)
	}
	cmd := []string{"go", "test", "-timeout", "1s", "./..."}
	jails := map[string]func(t *testing.T) adequacy.Jail{
		"workspace": func(t *testing.T) adequacy.Jail {
			return adequacy.NewWorkspaceRunner(t.TempDir(), 5*time.Minute)
		},
		"jail": func(t *testing.T) adequacy.Jail {
			backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
			if err != nil {
				t.Skipf("SKIP: no bwrap backend: %v", err)
			}
			return adequacy.NewJail(backend, 5*time.Minute)
		},
	}
	for name, mk := range jails {
		t.Run(name, func(t *testing.T) {
			j := mk(t)
			ej, ok := j.(adequacy.EndsJail)
			if !ok {
				t.Fatalf("%T does not keep both ends of a run", j)
			}
			pass, out, err := ej.RunTestEnds(context.Background(), hangModule, cmd)
			if err != nil {
				t.Fatal(err)
			}
			if pass {
				t.Fatalf("the hanging test passed:\n%s", out)
			}
			if !strings.Contains(out, "panic: test timed out") {
				if name == "jail" {
					t.Skipf("SKIP: go test did not run in the jail (toolchain unavailable there): %s", clip.Tail(out, 400))
				}
				t.Fatalf("the run did not time out as expected: %s", clip.Tail(out, 400))
			}
			lines := strings.Split(out, "\n")
			elided := 0
			for _, l := range lines {
				if clip.IsElision(l) {
					elided++
				}
			}
			if elided != 1 || len(out) < 80<<10 || len(out) > (16<<10)+(64<<10)+128 {
				t.Fatalf("the capture is %d bytes with %d elision lines; want the first 16 KiB, one elision line and the last 64 KiB of a run that printed more than 80 KiB", len(out), elided)
			}
			tail := out[len(out)-(64<<10):]
			if strings.Contains(tail, "TestHang (") || strings.Contains(tail, "panic: test timed out") {
				t.Fatalf("control: the last 64 KiB alone name the hung test; this fixture no longer shows why both ends are kept")
			}
			if !strings.Contains(tail, "FAIL") {
				t.Errorf("the end of the run is not in the capture: %s", clip.Tail(out, 200))
			}

			shown := writerFailureDetail(out)
			if len(shown) > 2*writerFailureEnd+128 {
				t.Errorf("the writer is shown %d bytes, more than twice %d and one line", len(shown), writerFailureEnd)
			}
			for _, want := range []string{"panic: test timed out", "TestHang"} {
				if !strings.Contains(shown[:writerFailureEnd], want) {
					t.Errorf("what the writer is shown does not say %q in its opening", want)
				}
			}
			if !strings.HasSuffix(strings.TrimSpace(shown), strings.TrimSpace(out[len(out)-200:])) {
				t.Errorf("what the writer is shown does not end with the end of the run")
			}
			if n := strings.Count(shown, "bytes of this run's output are not shown here]"); n != 1 {
				t.Errorf("%d elision lines in what the writer is shown, want 1", n)
			}
		})
	}
}
