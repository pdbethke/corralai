// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"fmt"
	"sort"
)

// DeriveFormat names the shape Derive returns, so a page reading it can refuse
// a shape it does not know.
const DeriveFormat = "corral-derive-1"

// RawEntry is one ledger file as bytes: what a browser fetches, or a CLI reads.
type RawEntry struct {
	Name  string `json:"name"`
	Bytes []byte `json:"bytes"`
}

// EntryStatus is what Derive concluded about one entry. Only "verified"
// entries contribute rows to a Derived.
type EntryStatus struct {
	File  string `json:"file"`
	Hash  string `json:"hash"`
	Kind  string `json:"kind"`
	KeyID string `json:"keyid"`
	// Status is "verified" (hash, link and signature all check under the
	// trusted root), "unsigned" (hash and link check and the entry carries no
	// signature) or "failed" (anything else; Problem says what).
	Status  string `json:"status"`
	Problem string `json:"problem,omitempty"`
}

// Derived is a ledger's verified, live rows as one document. Every slice is
// non-nil so it marshals as [] and never null.
type Derived struct {
	Format        string                  `json:"format"`
	Entries       []EntryStatus           `json:"entries"`
	Scans         []ScanRow               `json:"scans"`
	Files         []Row                   `json:"files"`
	Mutants       []MutantRow             `json:"mutants"`
	Calls         []ModelCallRow          `json:"calls"`
	Reviews       int                     `json:"reviews"`
	Adjudications map[string]Adjudication `json:"adjudications"`
}

const (
	StatusVerified = "verified"
	StatusUnsigned = "unsigned"
	StatusFailed   = "failed"
)

const problemOffChain = "not on the ledger's chain"

// Derive reads a ledger from its bytes and returns the rows a reader may
// trust. It runs Verify and the record rules (Retracted, LiveEntries,
// Adjudications) and adds no rule of its own: an entry contributes rows only
// if Verify passed it with a signature that checked under t.RootPub, and a
// retraction voids what it names exactly as it does for the CLI.
//
// Entries are ordered by following Prev links back from the head, not by any
// timestamp. Entries off that single chain (forks, orphans, a second head) and
// files that do not parse are listed after it as failed: never dropped, never
// counted. An error is returned only when two inputs share a name.
func Derive(raw []RawEntry, t Trust) (Derived, error) {
	d := Derived{
		Format:        DeriveFormat,
		Entries:       []EntryStatus{},
		Scans:         []ScanRow{},
		Files:         []Row{},
		Mutants:       []MutantRow{},
		Calls:         []ModelCallRow{},
		Adjudications: map[string]Adjudication{},
	}
	names := map[string]bool{}
	for _, r := range raw {
		if names[r.Name] {
			return Derived{}, fmt.Errorf("ledger: two entries are named %q", r.Name)
		}
		names[r.Name] = true
	}
	sorted := append([]RawEntry(nil), raw...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var parsed []Entry
	var unparsed []EntryStatus
	for _, r := range sorted {
		e, err := ParseEntry(r.Name, r.Bytes)
		if err != nil {
			unparsed = append(unparsed, EntryStatus{File: r.Name, Status: StatusFailed, Problem: err.Error()})
			continue
		}
		parsed = append(parsed, e)
	}
	chain, off := orderByPrev(parsed)

	checks := Verify(chain, t)
	var verified []Entry
	for i, c := range checks {
		st := statusOf(c, t)
		d.Entries = append(d.Entries, st)
		if st.Status == StatusVerified {
			verified = append(verified, chain[i])
		}
	}
	for _, e := range off {
		d.Entries = append(d.Entries, EntryStatus{File: e.File, Hash: e.Hash, Kind: e.Kind, KeyID: e.KeyID, Status: StatusFailed, Problem: problemOffChain})
	}
	d.Entries = append(d.Entries, unparsed...)

	live := LiveEntries(verified)
	for _, e := range live {
		if e.Kind == KindReview {
			d.Reviews++
		}
	}
	for _, e := range ScanEntries(verified) {
		d.Scans = append(d.Scans, e.Bundle.Scan)
		d.Files = append(d.Files, e.Bundle.Files...)
		d.Mutants = append(d.Mutants, e.Bundle.Mutants...)
		d.Calls = append(d.Calls, e.Bundle.Calls...)
	}
	d.Adjudications = Adjudications(live)
	return d, nil
}

// statusOf turns Verify's check into a status. A signed entry cannot be called
// verified without a root key to check it against: no pass by default.
func statusOf(c ChainCheck, t Trust) EntryStatus {
	st := EntryStatus{File: c.File, Hash: c.Hash, Kind: c.Kind, KeyID: c.KeyID}
	switch {
	case c.Problem != "":
		st.Status, st.Problem = StatusFailed, c.Problem
	case !c.HashOK || !c.LinkOK:
		st.Status, st.Problem = StatusFailed, "hash or link does not check"
	case !c.Signed:
		st.Status = StatusUnsigned
	case len(t.RootPub) == 0:
		st.Status, st.Problem = StatusFailed, "signed, but no root key was given to check the signature against"
	case !c.SigOK:
		st.Status, st.Problem = StatusFailed, "signature was not verified"
	default:
		st.Status = StatusVerified
	}
	return st
}

// orderByPrev returns the entries on the single chain ending at the head,
// oldest first, and the rest. The head is the entry no other names as Prev;
// with several, the one with the longest chain (ties: latest Pushed, then
// name), so the choice does not depend on input order. The walk stops at an
// entry whose Prev is empty (a genesis or a checkpoint) or names nothing
// present, which Verify then reports as a broken link.
func orderByPrev(es []Entry) (chain, off []Entry) {
	byHash := map[string]int{}
	dup := map[int]bool{}
	for i, e := range es {
		if _, ok := byHash[e.Hash]; ok {
			// Two entries claiming one hash: the earlier-sorted keeps the
			// slot, the other cannot be on the chain.
			dup[i] = true
			continue
		}
		byHash[e.Hash] = i
	}
	named := map[string]bool{}
	for _, e := range es {
		if e.Prev != "" {
			named[e.Prev] = true
		}
	}
	walk := func(head int) []int {
		var rev []int
		seen := map[int]bool{}
		for i := head; ; {
			seen[i] = true
			rev = append(rev, i)
			p, ok := byHash[es[i].Prev]
			if es[i].Prev == "" || !ok || seen[p] {
				break
			}
			i = p
		}
		return rev
	}
	best := -1
	var bestWalk []int
	for i, e := range es {
		if dup[i] || named[e.Hash] {
			continue
		}
		w := walk(i)
		if best < 0 || len(w) > len(bestWalk) ||
			(len(w) == len(bestWalk) && (e.Pushed.After(es[best].Pushed) || (e.Pushed.Equal(es[best].Pushed) && e.File > es[best].File))) {
			best, bestWalk = i, w
		}
	}
	on := map[int]bool{}
	for k := len(bestWalk) - 1; k >= 0; k-- {
		chain = append(chain, es[bestWalk[k]])
		on[bestWalk[k]] = true
	}
	var rest []int
	for i := range es {
		if !on[i] {
			rest = append(rest, i)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return es[rest[a]].Pushed.Before(es[rest[b]].Pushed) })
	for _, i := range rest {
		off = append(off, es[i])
	}
	return chain, off
}
