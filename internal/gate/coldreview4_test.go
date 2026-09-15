// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Round four of the cold review, 2026-09-13 (reviewer codex, verifier
// claude-code). Three findings: R1 and R2 (both high) were the `;` guard's
// third defeat and the newline-eating strings.Fields split, and both were
// retired by CHANGING THE FORMAT rather than writing a fourth guard — see
// ParsePolicy and TestACommandMayContainASemicolon /
// TestACommandKeepsItsNewlinesAndQuoting. R3 (medium) is here.

// TestPollerDedupesUnderTheContextTheRunnerSaved is R3 — a defect in round
// two's fix.
//
// THE DEFECT: the poller looked up the dedupe row under the policy's Context
// AS WRITTEN, while the runner normalized it (whitespace → "corral/gate")
// before saving. A programmatic Policy with Context "   " was saved under
// corral/gate and looked up under "   ", so every tick missed, re-ran the
// jail and re-certified an already delivered head, forever. Three doors held
// three ideas of the default: the runner normalized, the store defaulted
// only the empty string, the poller did neither. One normalizer now, and
// every door calls it.
func TestPollerDedupesUnderTheContextTheRunnerSaved(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var runs int
	p := &Poller{
		Policies: []Policy{{Repo: "o/r", Base: []string{"main"}, Context: "   ", CheckCmd: "true"}},
		List:     &fakeLister{prs: []PRRef{{Number: 1, HeadSHA: "abc", Base: "main"}}},
		Store:    store,
		// Saves under the context the REAL runner saves under: Runner.Run
		// normalizes the policy first (p.normalized()), so a whitespace
		// Context reaches the store as the default.
		Run: func(ctx context.Context, repoURL string, pol Policy, pr PRRef) error {
			runs++
			return store.Save(Run{Repo: pol.Repo, HeadSHA: pr.HeadSHA, PR: pr.Number,
				Context: pol.normalized().Context, StatusPosted: true, RanAt: time.Unix(0, 0)})
		},
	}
	_ = p.Tick(context.Background())
	_ = p.Tick(context.Background())
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 — the poller looked the head up under a context the runner never saved under", runs)
	}
}

// TestStoreDoorsShareOneContextNormalizer is the sibling check for R3 at the
// store: Save, GetByHead and MarkPosted must resolve a blank-ish context the
// same way the runner does, or a row written by one door is invisible to the
// next.
func TestStoreDoorsShareOneContextNormalizer(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 1, Context: "  ", RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"", "   ", DefaultStatusContext, " corral/gate "} {
		if _, ok, err := store.GetByHead("o/r", "abc", c); err != nil || !ok {
			t.Errorf("GetByHead under %q missed the row Save wrote under \"  \" (ok=%v err=%v)", c, ok, err)
		}
	}
	if err := store.MarkPosted("o/r", "abc", "\t"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.GetByHead("o/r", "abc", "")
	if !got.StatusPosted {
		t.Error("MarkPosted under a whitespace context did not reach the row")
	}
}
