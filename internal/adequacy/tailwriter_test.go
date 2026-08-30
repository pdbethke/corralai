// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestTailWriterHoldsOnlyTheTail: the detailed path exists to hand a failure
// parser the LAST 64 KiB of a run. Buffering the whole run first and then
// slicing the tail off it holds every byte the suite printed, once per
// mutant per tree — a stack-trace-heavy suite is megabytes, mutant counts
// are in the dozens, and the trees run in parallel. The bound has to be on
// what is HELD, not on what is returned.
func TestTailWriterHoldsOnlyTheTail(t *testing.T) {
	w := newTailWriter(maxDetailedOutput)

	// 10 MB, in chunks an OS pipe would plausibly deliver, each numbered so
	// the tail can be identified exactly.
	const chunk = 4096
	const total = 10 << 20
	var last bytes.Buffer
	for written := 0; written < total; written += chunk {
		line := fmt.Sprintf("%08d ", written/chunk) + strings.Repeat("x", chunk-9) + "\n"
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		last.WriteString(line)
		if last.Len() > 4*maxDetailedOutput {
			// Keep only enough to compute the expected tail, so the TEST
			// does not do the very thing it is asserting the writer avoids.
			b := last.Bytes()
			keep := append([]byte(nil), b[len(b)-2*maxDetailedOutput:]...)
			last.Reset()
			last.Write(keep)
		}
	}

	got := w.Bytes()
	if len(got) != maxDetailedOutput {
		t.Fatalf("held %d bytes, want exactly the %d-byte cap", len(got), maxDetailedOutput)
	}
	want := last.Bytes()
	want = want[len(want)-maxDetailedOutput:]
	if !bytes.Equal(got, want) {
		t.Errorf("the retained bytes are not the LAST %d of the run", maxDetailedOutput)
	}

	// The bound is structural, not a post-hoc trim: nothing in the writer
	// ever grows past the cap.
	if c := cap(w.buf); c != maxDetailedOutput {
		t.Errorf("the writer's buffer capacity is %d, want a fixed %d — a growing buffer is the bug this replaces", c, maxDetailedOutput)
	}
}

// A run shorter than the cap must come back whole, and an empty one empty —
// the tail of 12 bytes is 12 bytes, not 64 KiB of padding.
func TestTailWriterShortRunsAreWholeAndUnpadded(t *testing.T) {
	w := newTailWriter(maxDetailedOutput)
	if got := w.Bytes(); len(got) != 0 {
		t.Errorf("an unwritten tail = %q, want empty", got)
	}
	w.Write([]byte("FAIL a_test.py")) //nolint:errcheck
	if got := string(w.Bytes()); got != "FAIL a_test.py" {
		t.Errorf("short run = %q, want it whole", got)
	}
}

// A single Write LARGER than the cap must leave the cap's worth of ITS tail
// — the case a suite that prints one enormous line produces.
func TestTailWriterSingleOversizeWrite(t *testing.T) {
	const in = "0123456789abcdefghijklmnop"
	w := newTailWriter(16)
	w.Write([]byte(in)) //nolint:errcheck
	want := in[len(in)-16:]
	if got := string(w.Bytes()); got != want {
		t.Errorf("oversize single write = %q, want %q", got, want)
	}
}

// TestRunTestDetailedIsBoundedAtTheSource walks the real runner: a command
// that prints far more than the cap comes back capped, and the bytes are the
// END of the run (where every runner puts its failure summary).
func TestRunTestDetailedIsBoundedAtTheSource(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	// 2 MB of noise, then the line a parser would actually want.
	script := `head -c 2000000 /dev/zero | tr '\0' 'x'; echo; echo "FAILED a_test.py::test_x"`
	ok, out, err := w.RunTestDetailed(context.Background(), nil, []string{"sh", "-c", script})
	if err != nil {
		t.Fatalf("RunTestDetailed: %v", err)
	}
	if !ok {
		t.Fatalf("the script exited non-zero; output tail: %q", tailString(out))
	}
	if len(out) > maxDetailedOutput {
		t.Errorf("RunTestDetailed returned %d bytes, want at most the %d-byte cap", len(out), maxDetailedOutput)
	}
	if !strings.Contains(string(out), "FAILED a_test.py::test_x") {
		t.Error("the TAIL was dropped — the summary a parser reads lives at the END of a run")
	}
}

func tailString(b []byte) string {
	if len(b) > 200 {
		b = b[len(b)-200:]
	}
	return string(b)
}
