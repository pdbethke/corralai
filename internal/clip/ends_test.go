// SPDX-License-Identifier: Elastic-2.0

package clip

import (
	"strings"
	"testing"
)

func written(head, tail int, chunks ...string) string {
	w := NewEndsWriter(head, tail)
	for _, c := range chunks {
		if n, err := w.Write([]byte(c)); n != len(c) || err != nil {
			panic("EndsWriter.Write reported a short write or an error")
		}
	}
	return w.String()
}

// The capture is the output itself when it fits, exactly at the boundary
// included; one byte over, the one byte in the middle is elided and counted;
// and an output shorter than head+tail is never duplicated or padded.
func TestEndsWriterAtTheBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		head, tail int
		chunks     []string
		want       string
	}{
		{"empty", 4, 4, nil, ""},
		{"shorter than the head", 4, 4, []string{"ab"}, "ab"},
		{"head and tail would overlap", 4, 4, []string{"abcde"}, "abcde"},
		{"fits exactly", 4, 4, []string{"abcd", "efgh"}, "abcdefgh"},
		{"one byte over", 4, 4, []string{"abcdefghi"}, "abcd\n" + Elision(1) + "\nfghi"},
		{"one byte over, written a byte at a time", 4, 4, strings.Split("abcdefghi", ""), "abcd\n" + Elision(1) + "\nfghi"},
		{"far over, one write bigger than the tail", 2, 3, []string{"0123456789"}, "01\n" + Elision(5) + "\n789"},
		{"far over, wrapped several times", 2, 3, []string{"01", "234", "5", "6789"}, "01\n" + Elision(5) + "\n789"},
		{"no head", 0, 3, []string{"abcdef"}, "\n" + Elision(3) + "\ndef"},
		{"no tail", 3, 0, []string{"abcdef"}, "abc\n" + Elision(3) + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := written(c.head, c.tail, c.chunks...); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Ends on a string follows the same rule, and a string that already carries
// an elision line is clipped around it: the bytes the first clip dropped are
// in the second one's count, and no second elision line appears.
func TestEndsClipsAStringAndCountsAnEarlierElision(t *testing.T) {
	if got := Ends("abcdefgh", 4, 4); got != "abcdefgh" {
		t.Errorf("fits exactly: %q", got)
	}
	if got, want := Ends("abcdefghi", 4, 4), "abcd\n"+Elision(1)+"\nfghi"; got != want {
		t.Errorf("one over: got %q, want %q", got, want)
	}
	captured := written(4, 8, "HEADmiddle-of-the-run-TAILTAIL") // drops 18
	if want := "HEAD\n" + Elision(18) + "\nTAILTAIL"; captured != want {
		t.Fatalf("capture = %q, want %q", captured, want)
	}
	if got, want := Ends(captured, 4, 4), "HEAD\n"+Elision(22)+"\nTAIL"; got != want {
		t.Errorf("second clip = %q, want %q", got, want)
	}
	if got, want := Ends(captured, 2, 100), "HE\n"+Elision(20)+"\nTAILTAIL"; got != want {
		t.Errorf("second clip, shorter head = %q, want %q", got, want)
	}
	if got := Ends(captured, 100, 100); got != captured {
		t.Errorf("a clip with room for everything changed the string: %q", got)
	}
	if strings.Count(Ends(captured, 1, 1), elisionOpen) != 1 {
		t.Errorf("two elision lines in %q", Ends(captured, 1, 1))
	}
}

func TestIsElision(t *testing.T) {
	if !IsElision(Elision(12345)) {
		t.Error("an elision line is not recognised")
	}
	for _, line := range []string{"", "[corral: x bytes of this run's output are not shown here]", Elision(3) + " ", "FAILED " + Elision(3)} {
		if IsElision(line) {
			t.Errorf("%q read as an elision line", line)
		}
	}
}
