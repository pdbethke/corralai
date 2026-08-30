// SPDX-License-Identifier: Elastic-2.0

package sandbox

import "strings"

// TailWriter is CappedWriter's mirror image: it keeps the LAST Max bytes
// written to it instead of the first, in a ring buffer allocated once.
//
// WHICH END TO KEEP IS A CORRECTNESS QUESTION, not a preference. A test
// runner puts its failure SUMMARY last — pytest's "short test summary info",
// `go test`'s FAIL lines — so the caller that has to answer "which test
// killed this mutant" needs the end of the run. Keeping the head of a verbose
// suite hands that caller the run's banner and leaves the column it exists to
// fill empty, with no error anywhere: the run passes, the verdict is right,
// the answer is silently missing. See Options.KeepTail for who asks for this.
//
// Bounded AS THE BYTES ARRIVE, like CappedWriter and for the same reason: a
// buffer-everything-then-trim would hold every byte a stack-trace-heavy suite
// printed, once per mutant, to return 64 KiB of it.
//
// (internal/adequacy has its OWN tail writer, for the workspace substrate,
// which runs commands directly and never passes through Run at all — see
// adequacy.tailWriter. It returns raw bytes with no marker; this one returns
// a Result.Output string, marker and all, because that is what Run's contract
// is. sandbox cannot import adequacy, and the shapes differ, so they are
// deliberately two small types rather than one awkward one.)
//
// Not safe for concurrent use, and it does not need to be: os/exec gives a
// command's Stdout and Stderr a single pipe and a single copying goroutine
// when they are the same writer value, which is how Run wires it.
type TailWriter struct {
	buf  []byte
	end  int
	full bool
}

// NewTailWriter returns a TailWriter whose String() is at most max bytes,
// INCLUDING the TruncationMarker it prepends once anything has been dropped.
// The marker's room is reserved out of max rather than added on top, so a
// caller that bounded its capture at max gets at most max back and never has
// to trim — trimming is what would throw the marker away again.
//
// max <= 0 falls back to 16 KiB, the same default Options.MaxOutput
// documents; never "unbounded", for the reason NewCappedWriter gives.
func NewTailWriter(max int) *TailWriter {
	if max <= 0 {
		max = 16 << 10
	}
	n := max - len(TruncationMarker) - 1
	if n < 1 {
		n = 1
	}
	return &TailWriter{buf: make([]byte, n)}
}

// Write always reports the full length as written and never errors: this is a
// SINK for a command's output, and reporting a short write to os/exec would
// kill a test run over a capture policy.
func (w *TailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.buf) == 0 {
		return n, nil
	}
	// One write bigger than the whole ring: only its own tail can survive, so
	// skip the wrap arithmetic and overwrite outright.
	if len(p) >= len(w.buf) {
		copy(w.buf, p[len(p)-len(w.buf):])
		w.end, w.full = 0, true
		return n, nil
	}
	for len(p) > 0 {
		c := copy(w.buf[w.end:], p)
		p = p[c:]
		w.end += c
		if w.end == len(w.buf) {
			w.end, w.full = 0, true
		}
	}
	return n, nil
}

// String returns the retained tail in write order, with TruncationMarker on
// the FRONT when anything was dropped — the marker names what is missing, and
// what is missing here is the beginning. A caller detects truncation the same
// structural way it does for CappedWriter: by searching for the marker.
//
// A short run comes back whole and unpadded, with no marker: the tail of
// twelve bytes is twelve bytes.
func (w *TailWriter) String() string {
	if !w.full {
		return strings.TrimRight(string(w.buf[:w.end]), "\n")
	}
	out := make([]byte, 0, len(w.buf))
	out = append(out, w.buf[w.end:]...)
	out = append(out, w.buf[:w.end]...)
	return TruncationMarker + "\n" + strings.TrimRight(string(out), "\n")
}
