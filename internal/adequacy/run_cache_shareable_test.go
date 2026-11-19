// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"path/filepath"
	"testing"
)

// RunCacheShareable answers what a run-cache jail on this backend would
// answer, through the one decision the jail makes (makeSharedCacheDir), and
// leaves nothing behind: the scan header reads it before any jail exists, and
// a header that disagreed with every file's own line was a bug.
func TestRunCacheShareableIsTheJailsOwnAnswer(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if RunCacheShareable(&captureIsolator{}) {
		t.Error("a backend that cannot overlay: RunCacheShareable = true")
	}
	if RunCacheShareable(nil) {
		t.Error("no backend: RunCacheShareable = true")
	}

	iso := overlayJailOrSkip(t)
	j := NewJail(iso, 0, WithRunCache("CORRAL_TEST_CACHE")).(RunCacheJail)
	defer j.Close()
	want := j.RunCache().Unshared == ""
	if got := RunCacheShareable(iso); got != want {
		t.Errorf("RunCacheShareable = %v, the jail's own RunCache says shared = %v", got, want)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "corral-cache-*"))
	if len(left) != 0 {
		t.Errorf("RunCacheShareable left %d directory(ies) behind: %v", len(left), left)
	}
}
