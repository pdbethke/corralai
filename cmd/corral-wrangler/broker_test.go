// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// Two sessions on one box, one store: the second session's claim on a path
// the first holds is REFUSED with exit 1 and the holder named, so a shell
// `claim … && edit` stops before the edit. Release, and it is granted.
func TestBrokerClaimsAreExclusiveAcrossSessionsOnOneStore(t *testing.T) {
	t.Setenv("CORRALAI_DB", filepath.Join(t.TempDir(), "coord.sqlite3"))
	run := func(verb string, args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := runBroker(verb, args, &out, &errb)
		return code, out.String() + errb.String()
	}
	if code, out := run("claim", "--as", "a", "--reason", "editing the parser", "internal/lang/python.go"); code != 0 || !strings.Contains(out, "claimed internal/lang/python.go") {
		t.Fatalf("a's claim: exit %d\n%s", code, out)
	}
	code, out := run("claim", "--as", "b", "internal/lang/python.go")
	if code != 1 || !strings.Contains(out, "REFUSED internal/lang/python.go — held by a: editing the parser") {
		t.Fatalf("b's claim on a's path: exit %d\n%s", code, out)
	}
	if code, out := run("who", "a"); code != 0 || !strings.Contains(out, "holds internal/lang/python.go") {
		t.Fatalf("who a: exit %d\n%s", code, out)
	}
	if code, out := run("list"); code != 0 || !strings.Contains(out, "a holds internal/lang/python.go") || !strings.Contains(out, "b —") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}
	if code, out := run("release", "--as", "a"); code != 0 || !strings.Contains(out, "released 1 claim(s)") {
		t.Fatalf("release: exit %d\n%s", code, out)
	}
	if code, _ := run("claim", "--as", "b", "internal/lang/python.go"); code != 0 {
		t.Fatalf("b's claim after a released: exit %d", code)
	}
	if code, out := run("done", "--as", "b", "--task", "parser fixed", "internal/lang/python.go"); code != 0 || !strings.Contains(out, "done: parser fixed — released 1 claim(s)") {
		t.Fatalf("done: exit %d\n%s", code, out)
	}
	if code, out := run("list", "--json"); code != 0 || !strings.Contains(out, `"summary": "parser fixed"`) {
		t.Fatalf("list --json after done: exit %d\n%s", code, out)
	}
	// The handoff note is what a later session reads.
	if code, out := run("heartbeat", "--as", "a", "--task", "moving to the scorer"); code != 0 || !strings.Contains(out, "registered as a") {
		t.Fatalf("heartbeat: exit %d\n%s", code, out)
	}
	if _, out := run("who", "a"); !strings.Contains(out, "moving to the scorer") {
		t.Fatalf("who a after heartbeat --task:\n%s", out)
	}
}
