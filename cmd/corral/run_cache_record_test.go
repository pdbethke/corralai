// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The RunCache a file's row records is what its jail DID, read when the
// file's run ends: shared only when a warming run ran on the cache.

// A Go file graded normally: its dev pass warmed the cache, so it records shared.
func TestAGradedGoFileRecordsSharedOnlyAfterTheCacheWasWarmed(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	repoDir, seed := goCacheModule(t)
	var sink string
	w := wireGoSink(t, iso, repoDir, &seed, []string{"go", "test", "./..."}, "go", &sink)

	if _, err := w.scorer.ScoreDevReport(context.Background(), w.codeKey, seed.files["p/p.go"], "", oneMutant, "go test ./...", nil); err != nil {
		t.Fatal(err)
	}
	w.cleanup()
	if sink != "shared" {
		t.Errorf("a normally graded Go file records %q, want %q", sink, "shared")
	}
}

// A Go file whose dev pass runs a command the rewrite cannot give the flag
// that forbids a cached result: the cache is mounted but never warmed, and the
// record says so rather than "shared".
func TestAGoFileWhoseDevCommandFailsTheWarmCheckRecordsNeverWarmed(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	repoDir, seed := goCacheModule(t)
	var sink string
	w := wireGoSink(t, iso, repoDir, &seed, []string{"go", "test", "./..."}, "go", &sink)
	if w.runCache != "shared" {
		t.Fatalf("the wiring decided %q, want shared (the decision, before any run)", w.runCache)
	}

	if _, err := w.scorer.ScoreDevReport(context.Background(), w.codeKey, seed.files["p/p.go"], "", oneMutant, "go vet ./...", nil); err != nil {
		t.Fatal(err)
	}
	w.cleanup()
	const want = "unshared: never warmed (the dev suite's command could not be given the flag that forbids a cached result)"
	if sink != want {
		t.Errorf("record = %q, want %q", sink, want)
	}
}

// No dev pass at all (the jail is built and closed with no ScoreDevReport):
// the cache was never warmed, and the record says no dev pass ran.
func TestAGoFileWithNoDevPassRecordsNeverWarmed(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	repoDir, seed := goCacheModule(t)
	var sink string
	w := wireGoSink(t, iso, repoDir, &seed, []string{"go", "test", "./..."}, "go", &sink)
	w.cleanup()
	const want = "unshared: never warmed (no dev pass ran)"
	if sink != want {
		t.Errorf("record = %q, want %q", sink, want)
	}
}

// A runner with no run cache records "": nothing was ever decided.
func TestARunnerWithNoRunCacheRecordsNothingAtTheEnd(t *testing.T) {
	repoDir, seed := goCacheModule(t)
	sink := "stale"
	w := wireGoSink(t, &wrapRecordingIsolator{}, repoDir, &seed, []string{"python3", "-m", "pytest"}, "python", &sink)
	w.cleanup()
	if sink != "" {
		t.Errorf("record = %q, want empty", sink)
	}
}

// A file whose baseline failed records "" whatever its jail did.
func TestAFileWhoseBaselineFailedStillRecordsNothing(t *testing.T) {
	if got := fileRunCache(reposcan.FileResult{Gradable: false, RunCache: "unshared: never warmed (no dev pass ran)"}); got != "" {
		t.Errorf("fileRunCache = %q, want empty", got)
	}
}

// failingCloseJail is a RunCacheJail whose Close fails, as a directory that
// cannot be removed would.
type failingCloseJail struct{ adequacy.RunCacheJail }

func (failingCloseJail) Close() error {
	return errors.New("adequacy: remove the run cache /tmp/corral-cache-x: busy")
}

// A cache directory that cannot be removed is said on stderr, naming it, and
// closeGradingJail still returns normally: no exit code depends on it.
func TestCloseGradingJailSaysWhenTheCacheDirectoryCannotBeRemoved(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	closeGradingJail(failingCloseJail{})
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "/tmp/corral-cache-x") {
		t.Errorf("stderr = %q, want a line naming the directory", out)
	}
}
