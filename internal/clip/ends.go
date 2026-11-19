// SPDX-License-Identifier: Elastic-2.0

package clip

import (
	"regexp"
	"strconv"
)

// An elision line stands where the middle of a run's output was dropped and
// says how many bytes that was. It is always on a line of its own. Its
// wording is fixed here, in the one leaf package, because three kinds of
// reader must agree on it: the writers that put it there (EndsWriter, Ends),
// the failure parsers that must never read it as a test id or as one of
// their own markers (internal/lang), and a second clip of an already clipped
// string, which adds the bytes the first one dropped to its own count.
const (
	elisionOpen  = "[corral: "
	elisionClose = " bytes of this run's output are not shown here]"
)

var elisionLine = regexp.MustCompile(`\n\[corral: ([0-9]+) bytes of this run's output are not shown here\]\n`)

// Elision is the elision line for n dropped bytes, without its newlines.
func Elision(n int64) string {
	return elisionOpen + strconv.FormatInt(n, 10) + elisionClose
}

// IsElision reports whether line (one line, no newline) is an elision line.
func IsElision(line string) bool {
	return elisionLine.MatchString("\n" + line + "\n")
}

// Ends keeps the first head and the last tail bytes of s with one elision
// line between them saying how many bytes of the run are not shown; s comes
// back unchanged when nothing has to be dropped. The start of a run's output
// is where some failures are named (a Go test that hangs prints the panic and
// the name of the running test first, then every goroutine's stack) and the
// end is where the others are (a runner's summary), so a reader with room
// for only part of it is given both.
//
// s may already hold one elision line (a capture that kept two ends): the
// bytes that one stands for are counted in the new line rather than lost, and
// what is kept is taken from the real output on either side of it.
func Ends(s string, head, tail int) string {
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	first, last, hidden := s, "", int64(0)
	if loc := elisionLine.FindStringSubmatchIndex(s); loc != nil {
		n, err := strconv.ParseInt(s[loc[2]:loc[3]], 10, 64)
		if err == nil {
			first, last, hidden = s[:loc[0]], s[loc[1]:], n
		}
	}
	if hidden == 0 {
		if len(s) <= head+tail {
			return s
		}
		return s[:head] + "\n" + Elision(int64(len(s)-head-tail)) + "\n" + s[len(s)-tail:]
	}
	keepFirst, keepLast := min(head, len(first)), min(tail, len(last))
	dropped := hidden + int64(len(first)-keepFirst) + int64(len(last)-keepLast)
	return first[:keepFirst] + "\n" + Elision(dropped) + "\n" + last[len(last)-keepLast:]
}

// EndsWriter is an io.Writer that keeps the FIRST head bytes and the LAST
// tail bytes written to it, in memory allocated once, and counts the rest.
// It is the capture for a run whose readers want both ends (see Ends): it
// bounds what is HELD as the bytes arrive, so a run that prints megabytes
// costs head+tail bytes, not megabytes trimmed afterwards.
//
// Not safe for concurrent use, and it does not need to be: os/exec gives a
// command's Stdout and Stderr a single pipe and a single copying goroutine
// when they are the same writer value.
type EndsWriter struct {
	head    []byte // filled first, to cap(head)
	ring    []byte // everything after the head, last len(ring) bytes
	end     int
	full    bool
	written int64
}

// NewEndsWriter keeps the first head and the last tail bytes. A value below
// 1 for either keeps nothing of that end.
func NewEndsWriter(head, tail int) *EndsWriter {
	return &EndsWriter{head: make([]byte, 0, max(head, 0)), ring: make([]byte, max(tail, 0))}
}

// Write always reports the full length as written and never errors: this is
// a SINK for a command's output, and reporting a short write to os/exec
// would kill a test run over a capture policy.
func (w *EndsWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.written += int64(n)
	if room := cap(w.head) - len(w.head); room > 0 {
		c := min(room, len(p))
		w.head = append(w.head, p[:c]...)
		p = p[c:]
	}
	if len(w.ring) == 0 || len(p) == 0 {
		return n, nil
	}
	if len(p) >= len(w.ring) {
		copy(w.ring, p[len(p)-len(w.ring):])
		w.end, w.full = 0, true
		return n, nil
	}
	for len(p) > 0 {
		c := copy(w.ring[w.end:], p)
		p = p[c:]
		w.end += c
		if w.end == len(w.ring) {
			w.end, w.full = 0, true
		}
	}
	return n, nil
}

// String is what was kept, in write order: the whole output, byte for byte,
// when it fit in head+tail, and otherwise the head, an elision line on a
// line of its own counting the dropped bytes, and the tail.
func (w *EndsWriter) String() string {
	tail := w.ring[:w.end]
	if w.full {
		tail = append(append(make([]byte, 0, len(w.ring)), w.ring[w.end:]...), w.ring[:w.end]...)
	}
	dropped := w.written - int64(len(w.head)) - int64(len(tail))
	if dropped == 0 {
		return string(w.head) + string(tail)
	}
	return string(w.head) + "\n" + Elision(dropped) + "\n" + string(tail)
}
