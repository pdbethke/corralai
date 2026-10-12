// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"
)

// TestStartRunRefusesAFileWhoseTestWouldOverwriteIt: for a Rust source,
// pairing.Primary — the path advPoolTestPath writes the pool's synthetic and
// authored tests to — is the source itself, and its test landing is not
// built yet. StartRun refuses before anything is enqueued (the queue is the
// first store a run writes), so no seat is ever claimed; a Go run on the same
// driver still starts.
func TestStartRunRefusesAFileWhoseTestWouldOverwriteIt(t *testing.T) {
	q := newTestQueue(t)
	d, err := NewDriver(q, &fakeScorer{}, &fakeValidator{}, decorrelatedAssign(), 0.8)
	if err != nil {
		t.Fatal(err)
	}
	rs := testRunSpec()
	rs.CodePath, rs.DevTestPath, rs.Lang, rs.TestCmd = "src/lib.rs", "src/lib.rs", "rust", "cargo test"
	err = d.StartRun(41, rs, nil)
	if err == nil || !strings.Contains(err.Error(), "not built yet") {
		t.Fatalf("StartRun(src/lib.rs) err = %v, want the landing refusal", err)
	}
	if tasks, lerr := q.List(41); lerr != nil || len(tasks) != 0 {
		t.Fatalf("a refused run enqueued %d task(s) (err %v)", len(tasks), lerr)
	}
	if advPoolTestPath("src/lib.rs") != "src/lib.rs" {
		t.Fatal("precondition: the refusal exists because the test path IS the source")
	}
	if err := d.StartRun(42, testRunSpec(), nil); err != nil {
		t.Fatalf("a Go run was refused: %v", err)
	}
}
