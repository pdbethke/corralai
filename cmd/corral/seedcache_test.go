package main

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSeedCacheBuildsOncePerLanguage(t *testing.T) {
	var calls atomic.Int32
	c := newSeedCache(func(lang string) (repoSeed, error) {
		calls.Add(1)
		return repoSeed{seedDir: "/seed/" + lang, cleanup: func() {}}, nil
	})
	defer c.close()

	for i := 0; i < 10; i++ {
		s, err := c.get("go")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if s.seedDir != "/seed/go" {
			t.Fatalf("seedDir = %q", s.seedDir)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("builder called %d times for 10 gets of one language, want 1", got)
	}
}

func TestSeedCacheBuildsPerDistinctLanguage(t *testing.T) {
	var calls atomic.Int32
	c := newSeedCache(func(lang string) (repoSeed, error) {
		calls.Add(1)
		return repoSeed{seedDir: lang, cleanup: func() {}}, nil
	})
	defer c.close()

	for _, l := range []string{"go", "python", "go", "python", "ruby"} {
		if _, err := c.get(l); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("builder called %d times for 3 distinct languages, want 3", got)
	}
}

// The bug this exists to prevent: a failing `go mod vendor` re-running once
// per job instead of once per language.
func TestSeedCacheCachesFailures(t *testing.T) {
	var calls atomic.Int32
	boom := errors.New("go mod vendor failed")
	c := newSeedCache(func(lang string) (repoSeed, error) {
		calls.Add(1)
		return repoSeed{cleanup: func() {}}, boom
	})
	defer c.close()

	for i := 0; i < 50; i++ {
		if _, err := c.get("go"); !errors.Is(err, boom) {
			t.Fatalf("get %d: err = %v, want the cached build error", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("builder called %d times after a failure, want 1 — failures MUST be cached", got)
	}
}

func TestSeedCacheIsConcurrencySafe(t *testing.T) {
	var calls atomic.Int32
	c := newSeedCache(func(lang string) (repoSeed, error) {
		calls.Add(1)
		return repoSeed{seedDir: lang, cleanup: func() {}}, nil
	})
	defer c.close()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lang := []string{"go", "python"}[i%2]
			if _, err := c.get(lang); err != nil {
				t.Errorf("get: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("builder called %d times under concurrency, want 2", got)
	}
}

func TestSeedCacheCloseRunsEveryCleanupOnce(t *testing.T) {
	var cleaned atomic.Int32
	c := newSeedCache(func(lang string) (repoSeed, error) {
		return repoSeed{seedDir: lang, cleanup: func() { cleaned.Add(1) }}, nil
	})
	if _, err := c.get("go"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get("python"); err != nil {
		t.Fatal(err)
	}
	c.close()
	c.close() // idempotent
	if got := cleaned.Load(); got != 2 {
		t.Fatalf("cleanup ran %d times, want exactly 2 (one per built seed, close idempotent)", got)
	}
}

// close() must WAIT for an in-flight build and then run its cleanup. The bug
// this rules out: close() reads e.seed.cleanup while the build is still
// running, sees the zero value's nil, and leaks the staging dir.
//
// The interleaving is FORCED, not hoped for. The build is released only after
// the test observes c.closed == true, which close() sets under the lock before
// it enters the drain loop. So at the moment close() starts draining, the build
// is provably still blocked and its cleanup is still the zero value's nil —
// exactly the window the earlier version of this test only visited by luck.
func TestSeedCacheCloseWaitsForInflightBuilds(t *testing.T) {
	var cleaned atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})

	c := newSeedCache(func(lang string) (repoSeed, error) {
		close(buildStarted) // the build is now in flight
		<-releaseBuild      // hold here until close() is past its `closed` flag
		return repoSeed{seedDir: lang, cleanup: func() { cleaned.Add(1) }}, nil
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = c.get("go")
	}()
	<-buildStarted // the builder is blocked inside once.Do

	go func() {
		defer wg.Done()
		c.close()
	}()
	// Spin until close() has taken the lock, set closed, and released it: it is
	// now committed to the drain loop with the build still unfinished.
	for {
		c.mu.Lock()
		entered := c.closed
		c.mu.Unlock()
		if entered {
			break
		}
		runtime.Gosched()
	}
	close(releaseBuild)
	wg.Wait()

	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup ran %d times, want 1 (close must not leak in-flight builds)", got)
	}
}

// THE INVARIANT: get() must NEVER return a zero seed with a nil error. A zero
// seed has files == nil, so workspaceFromSeed would build a jail workspace
// holding only the job's own code + test — no go.mod, no siblings, no binds —
// and a self-contained Python/Ruby suite can still PASS there. That would be a
// kill rate measured against a repo that wasn't present: a fabricated
// measurement. The window: get() passes the `closed` check and releases the
// lock, close() then wins the entry's Once.
func TestSeedCacheGetNeverReturnsAZeroSeedWithNilError(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := newSeedCache(func(lang string) (repoSeed, error) {
			return repoSeed{
				seedDir: "/seed/" + lang,
				files:   map[string]string{"go.mod": "module x\n"},
				cleanup: func() {},
			}, nil
		})

		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		var seed repoSeed
		var err error
		go func() {
			defer wg.Done()
			<-start
			seed, err = c.get("go")
		}()
		go func() {
			defer wg.Done()
			<-start
			c.close()
		}()
		close(start)
		wg.Wait()

		if err == nil && seed.files == nil {
			t.Fatalf("iteration %d: get returned a ZERO seed with a nil error — "+
				"a job would audit a workspace missing the whole repo and report a real-looking kill rate", i)
		}
	}
}

// The invariant asserted directly at the seam, without depending on the
// scheduler visiting the window: drive the exact interleaving by hand.
// get() creates the entry and releases the lock; close() then runs its drain
// before get() reaches its own once.Do.
func TestSeedCacheGetLosingTheCloseRaceErrors(t *testing.T) {
	c := newSeedCache(func(lang string) (repoSeed, error) {
		t.Fatalf("build must not run for %q after close()", lang)
		return repoSeed{}, nil
	})

	// Step 1: create the entry exactly as get() does before it unlocks.
	c.mu.Lock()
	e := &seedEntry{}
	c.entries["go"] = e
	c.mu.Unlock()

	// Step 2: close() runs, and its drain wins the entry's Once.
	c.close()

	// Step 3: the racing get() now reaches e.once.Do — which is already spent.
	e.once.Do(func() { e.seed, e.err = c.build("go") })
	if e.err == nil {
		t.Fatal("entry drained by close() carries a nil error: get would return a zero seed as a valid one")
	}
	if e.seed.files != nil {
		t.Fatalf("drained entry has files %v, want nil", e.seed.files)
	}
}

// Test that get() after close() returns an error and does not build.
func TestSeedCacheGetAfterCloseReturnsError(t *testing.T) {
	var calls atomic.Int32
	c := newSeedCache(func(lang string) (repoSeed, error) {
		calls.Add(1)
		return repoSeed{seedDir: lang, cleanup: func() {}}, nil
	})

	// Build once before closing
	if _, err := c.get("go"); err != nil {
		t.Fatal(err)
	}

	// Close the cache
	c.close()

	// Try to get after close — should return an error and not build
	_, err := c.get("python")
	if err == nil {
		t.Fatal("get after close: expected error, got nil")
	}

	// Verify no new build was triggered
	if got := calls.Load(); got != 1 {
		t.Fatalf("builder called %d times after close, want 1 (no new build)", got)
	}
}
