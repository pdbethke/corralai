// SPDX-License-Identifier: Elastic-2.0

package adequacy

// tailWriter is an io.Writer that keeps only the LAST n bytes written to it,
// in a fixed ring buffer allocated once.
//
// WHY IT IS NOT A bytes.Buffer PLUS tailBytes. The detailed path
// (DetailedJail.RunTestDetailed) exists to hand the language plugin's failure
// parser the end of a run, and the end is all it ever reads. Buffering the
// whole run and trimming afterwards still HOLDS every byte the suite printed
// — and that happens once per mutant, in every tree, concurrently. A
// stack-trace-heavy suite prints megabytes per mutant; mutant counts are in
// the dozens; the workspace substrate runs several trees at once. The bound
// has to be on what is held, not on what is returned, or the cap is
// decoration.
//
// Not safe for concurrent use, and it does not need to be: os/exec gives a
// command's Stdout and Stderr a single pipe (and a single copying goroutine)
// when they are the same writer value, which is how applyRunRestore wires
// it.
type tailWriter struct {
	buf []byte
	// end is where the next byte goes. full flips once the ring has wrapped,
	// which is the only thing that distinguishes "512 bytes written" from
	// "the ring is full and end happens to be 512".
	end  int
	full bool
}

// newTailWriter allocates a tail of exactly n bytes. n < 1 yields a writer
// that keeps nothing, which is a legitimate "do not capture" rather than an
// error.
func newTailWriter(n int) *tailWriter {
	if n < 1 {
		return &tailWriter{}
	}
	return &tailWriter{buf: make([]byte, n)}
}

// Write always reports the full length as written and never errors: this is
// a SINK for a command's output, and reporting a short write to os/exec
// would kill a test run over a capture policy.
func (w *tailWriter) Write(p []byte) (int, error) {
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

// Bytes returns the retained tail in write order, as a fresh slice — the ring
// keeps being written to by the next mutant's run, and a caller holding an
// alias of it would watch its own output change.
//
// Short runs come back WHOLE and unpadded: the tail of twelve bytes is twelve
// bytes, not the cap's worth of zeros.
func (w *tailWriter) Bytes() []byte {
	if !w.full {
		out := make([]byte, w.end)
		copy(out, w.buf[:w.end])
		return out
	}
	out := make([]byte, len(w.buf))
	n := copy(out, w.buf[w.end:])
	copy(out[n:], w.buf[:w.end])
	return out
}
