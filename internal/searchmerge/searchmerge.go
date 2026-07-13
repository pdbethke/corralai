// SPDX-License-Identifier: Elastic-2.0

// Package searchmerge fuses keyword + semantic hit lists: each arm is
// max-normalized to [0,1], the two are unioned by a caller-supplied key,
// collisions keep the higher score and are tagged "both", and the result is
// sorted by score descending and truncated. It is the shared home for the
// hybrid-merge logic previously copy-pasted in memory and repoindex.
package searchmerge

import "sort"

// Accessors lets Merge operate on any hit type T without T needing to
// implement an interface: the caller supplies field access as functions.
type Accessors[T any] struct {
	Key      func(T) string
	Score    func(*T) float64
	SetScore func(*T, float64)
	SetVia   func(*T, string)
}

// Merge max-normalizes kw and sem independently to [0,1] (defensive-copied,
// so the caller's slices are never mutated), unions them by a.Key, keeps the
// higher score and tags "both" on collision, sorts by score descending, and
// truncates to limit (0 or negative means no truncation).
func Merge[T any](kw, sem []T, a Accessors[T], limit int) []T {
	norm := func(hs []T) []T {
		cp := make([]T, len(hs))
		copy(cp, hs) // defensive: never mutate the caller's slice
		var max float64
		for i := range cp {
			if s := a.Score(&cp[i]); s > max {
				max = s
			}
		}
		if max > 0 {
			for i := range cp {
				a.SetScore(&cp[i], a.Score(&cp[i])/max)
			}
		}
		return cp
	}
	nk, ns := norm(kw), norm(sem)
	idx := map[string]int{}
	var out []T
	add := func(h T) {
		k := a.Key(h)
		if j, ok := idx[k]; ok {
			if a.Score(&h) > a.Score(&out[j]) {
				a.SetScore(&out[j], a.Score(&h))
			}
			a.SetVia(&out[j], "both")
			return
		}
		idx[k] = len(out)
		out = append(out, h)
	}
	for _, h := range nk {
		add(h)
	}
	for _, h := range ns {
		add(h)
	}
	sort.SliceStable(out, func(i, j int) bool { return a.Score(&out[i]) > a.Score(&out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
