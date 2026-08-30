#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# verify-mutantset-bytes.sh — prove the hunk representation grades the SAME
# BYTES the whole-file one did, against sets recorded by real paid runs.
#
# NOT a CI gate, and deliberately so: it needs a recorded corral-mutants-1
# document plus the exact source it was cut from, neither of which lives in
# this repo. It is the one-off an operator runs after the representation
# change lands, on the sets they actually have.
#
# WHAT IT DOES, per mutant in the set:
#
#   1. Re-derives the SEARCH/REPLACE hunk the v1 document threw away, by
#      diffing the stored whole mutated file against the original — the
#      smallest changed line range, widened until the anchor is UNIQUE, which
#      is the same uniqueness rule Apply enforces.
#   2. Applies that hunk through adequacy.Mutant.Apply — the real function, in
#      this build, not a re-implementation.
#   3. Compares the result to the whole file v1 stored, byte for byte.
#
# Any mutant that comes back "DIFFERS" means the change moved something
# measured, and the representation change is wrong. "no unique anchor" is a
# property of that v1 mutant (its edit is not a single contiguous unique
# region), not a failure of Apply — it is reported apart from the differences.
#
# Usage:
#   scripts/verify-mutantset-bytes.sh <mutant-set.json> <path-in-set> <original-file>
#   scripts/verify-mutantset-bytes.sh <mutant-set.json> --repo-dir <dir>
#
# The second form resolves every path key in the set against <dir>.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
	sed -n '5,32p' "${BASH_SOURCE[0]}" >&2
	exit 2
}

[ $# -ge 2 ] || usage
set_json="$1"
shift
[ -f "$set_json" ] || {
	echo "verify-mutantset-bytes: no such mutant set: $set_json" >&2
	exit 2
}

if [ "$1" = "--repo-dir" ]; then
	[ $# -eq 2 ] || usage
	mode_args=(--repo-dir "$2")
else
	[ $# -eq 2 ] || usage
	mode_args=(--path "$1" --original "$2")
fi

# The helper must live INSIDE the module to import internal/adequacy at all.
tmp="$(mktemp -d "${repo_root}/.verify-mutantset-XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

cat >"${tmp}/main.go" <<'GO'
// SPDX-License-Identifier: Elastic-2.0

// Command verify-mutantset-bytes replays a corral-mutants-1 document through
// adequacy.Mutant.Apply and reports, per mutant, whether the bytes match the
// whole mutated file v1 recorded.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
)

type v1Mutant struct {
	ID   string `json:"id"`
	File string `json:"code"`
}

type v1Entry struct {
	ParentSHA256 string     `json:"parent_sha256"`
	Mutants      []v1Mutant `json:"mutants"`
}

type v1Doc struct {
	Format string             `json:"format"`
	Files  map[string]v1Entry `json:"files"`
}

// deriveHunk recovers the SEARCH/REPLACE the v1 document did not keep: the
// smallest run of ORIGINAL lines that has to change to become mutated,
// widened by one line at a time until that run occurs exactly once in the
// original. Widening is the whole trick — the minimal changed range is often
// something like "    return 1", which appears all over a file; Apply refuses
// an ambiguous anchor, and so must the anchor we hand it.
func deriveHunk(original, mutated string) (search, replace string, ok bool) {
	o := strings.SplitAfter(original, "\n")
	m := strings.SplitAfter(mutated, "\n")
	pre := 0
	for pre < len(o) && pre < len(m) && o[pre] == m[pre] {
		pre++
	}
	suf := 0
	for suf < len(o)-pre && suf < len(m)-pre && o[len(o)-1-suf] == m[len(m)-1-suf] {
		suf++
	}
	lo, hi := pre, len(o)-suf // original lines [lo,hi) changed
	mlo, mhi := pre, len(m)-suf
	for {
		search = strings.Join(o[lo:hi], "")
		replace = strings.Join(m[mlo:mhi], "")
		if search != "" && search != replace && strings.Count(original, search) == 1 {
			return search, replace, true
		}
		widened := false
		if lo > 0 {
			lo--
			mlo--
			widened = true
		}
		if hi < len(o) {
			hi++
			mhi++
			widened = true
		}
		if !widened {
			return "", "", false
		}
	}
}

func main() {
	setPath := flag.String("set", "", "corral-mutants-1 document")
	pathKey := flag.String("path", "", "one path key inside the set")
	origPath := flag.String("original", "", "the source file that key was recorded from")
	repoDir := flag.String("repo-dir", "", "resolve every path key in the set against this directory")
	flag.Parse()

	raw, err := os.ReadFile(*setPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var doc v1Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("set %s (format %q, %d file(s))\n", *setPath, doc.Format, len(doc.Files))

	type job struct{ key, file string }
	var jobs []job
	if *repoDir != "" {
		keys := make([]string, 0, len(doc.Files))
		for k := range doc.Files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			jobs = append(jobs, job{k, filepath.Join(*repoDir, filepath.FromSlash(k))})
		}
	} else {
		jobs = []job{{*pathKey, *origPath}}
	}

	var identical, differing, unanchored, skipped int
	for _, j := range jobs {
		entry, ok := doc.Files[j.key]
		if !ok {
			fmt.Printf("  %s: NOT IN SET\n", j.key)
			skipped++
			continue
		}
		src, err := os.ReadFile(j.file)
		if err != nil {
			fmt.Printf("  %s: cannot read %s: %v\n", j.key, j.file, err)
			skipped++
			continue
		}
		original := string(src)
		sum := sha256.Sum256(src)
		have := hex.EncodeToString(sum[:])
		fmt.Printf("  %s (%d mutant(s))\n", j.key, len(entry.Mutants))
		if have != entry.ParentSHA256 {
			// Every comparison below is meaningless against the wrong bytes.
			fmt.Printf("    PARENT MISMATCH: %s on disk, %s in the set — check out the recorded commit\n", have, entry.ParentSHA256)
			skipped += len(entry.Mutants)
			continue
		}
		for _, rm := range entry.Mutants {
			search, replace, ok := deriveHunk(original, rm.File)
			if !ok {
				fmt.Printf("    %-6s NO UNIQUE ANCHOR (this v1 mutant is not one contiguous unique region)\n", rm.ID)
				unanchored++
				continue
			}
			got, aerr := adequacy.Mutant{ID: rm.ID, Search: search, Replace: replace}.Apply(original)
			switch {
			case aerr != nil:
				fmt.Printf("    %-6s APPLY REFUSED: %v\n", rm.ID, aerr)
				differing++
			case got == rm.File:
				fmt.Printf("    %-6s identical (%d bytes)\n", rm.ID, len(got))
				identical++
			default:
				fmt.Printf("    %-6s DIFFERS: Apply produced %d bytes, the set stored %d\n", rm.ID, len(got), len(rm.File))
				differing++
			}
		}
	}
	fmt.Printf("\n%d identical, %d DIFFERING, %d without a unique anchor, %d skipped\n",
		identical, differing, unanchored, skipped)
	if differing > 0 {
		os.Exit(1)
	}
}
GO

cd "$repo_root"
go run "./$(basename "$tmp")" --set "$set_json" "${mode_args[@]}"
