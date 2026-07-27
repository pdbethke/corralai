package main

import (
	"errors"
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
