package main

import (
	"errors"
	"sync"
)

// seedCache memoizes repoSeed construction per language for the lifetime of one
// scan. Without it, prep runs twice per audited file — a 189-file Go repo does
// 378 tree copies and 378 `go mod vendor` runs, up to NumCPU concurrently,
// which exhausts TMPDIR rather than merely running slowly.
//
// Failures are cached alongside successes. A repo whose vendoring fails would
// otherwise retry it once per job.
type seedCache struct {
	build func(lang string) (repoSeed, error)

	mu      sync.Mutex
	entries map[string]*seedEntry
	closed  bool
}

type seedEntry struct {
	once sync.Once
	seed repoSeed
	err  error
}

func newSeedCache(build func(lang string) (repoSeed, error)) *seedCache {
	return &seedCache{build: build, entries: map[string]*seedEntry{}}
}

// get returns the language's seed, building it at most once. Concurrent
// callers for the SAME language block on one build; different languages
// build concurrently. Returns an error if the cache has been closed.
func (c *seedCache) get(lang string) (repoSeed, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return repoSeed{}, errors.New("seedCache: cache is closed")
	}
	e := c.entries[lang]
	if e == nil {
		e = &seedEntry{}
		c.entries[lang] = e
	}
	c.mu.Unlock()

	// Outside the lock: a slow build must not block other languages.
	e.once.Do(func() { e.seed, e.err = c.build(lang) })
	return e.seed, e.err
}

// close releases every staging dir the cache created. Idempotent: safe to
// defer and to call again. It waits for any in-flight builds to complete
// before running cleanup functions.
func (c *seedCache) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	// Take a snapshot of entries under the lock, then release it.
	entries := make([]*seedEntry, 0, len(c.entries))
	for _, e := range c.entries {
		entries = append(entries, e)
	}
	c.mu.Unlock()

	// Outside the lock: drain each entry's build (no-op if already done, blocks
	// if in flight) and call its cleanup. sync.Once ensures this is safe to
	// call even if already running.
	//
	// The drain records an ERROR rather than running a no-op, because it can WIN
	// the Once: a get() that passed the `closed` check and released the lock just
	// before close() took it has created its entry but not yet reached its own
	// once.Do. If this drain wrote nothing, that get() would return a ZERO seed
	// with a NIL error — a workspace holding only the job's own two files, no
	// go.mod, no siblings, no binds. A self-contained Python/Ruby suite can still
	// PASS in such a workspace, so the scan would report a real-looking kill rate
	// measured against a repo that wasn't there. Fail closed instead: a get that
	// loses this race gets an error and its file is reported ungradable.
	for _, e := range entries {
		e.once.Do(func() { e.err = errors.New("seedCache: closed before build") })
		if e.seed.cleanup != nil {
			e.seed.cleanup()
		}
	}
}
